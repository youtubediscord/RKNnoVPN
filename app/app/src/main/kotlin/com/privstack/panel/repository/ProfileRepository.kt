package com.privstack.panel.repository

import android.util.Log
import com.privstack.panel.`import`.LinkParser
import com.privstack.panel.`import`.SubscriptionHandler
import com.privstack.panel.i18n.UserMessageFormatter
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.ipc.ConfigMutationInfo
import com.privstack.panel.ipc.PollingStatusSource
import com.privstack.panel.model.Node
import com.privstack.panel.model.ProfileConfig
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import javax.inject.Inject
import javax.inject.Singleton

/**
 * Cache-backed profile CRUD via [DaemonClient].
 *
 * **Single source of truth**: the daemon's `config.json`.
 * This repository keeps an in-memory cache that is refreshed:
 * - On first access after construction
 * - On explicit [refresh] (typically called from Activity.onResume)
 * - After every mutating operation (add/remove/update node, change routing, etc.)
 *
 * All writes go through the daemon (`config-set`) and then re-read to keep
 * the cache consistent. If a write fails the cache is NOT updated, so the UI
 * always reflects the actual daemon state.
 */
@Singleton
class ProfileRepository @Inject constructor(
    private val client: DaemonClient,
    private val poller: PollingStatusSource,
    private val messages: UserMessageFormatter,
) {
    companion object {
        private const val TAG = "ProfileRepository"
    }

    private val mutex = Mutex()

    private val _profile = MutableStateFlow<ProfileConfig?>(null)
    /** Current cached profile, or null if not yet loaded / load failed. */
    val profile: StateFlow<ProfileConfig?> = _profile.asStateFlow()

    private val _loading = MutableStateFlow(false)
    /** True while a network/IPC operation is in flight. */
    val loading: StateFlow<Boolean> = _loading.asStateFlow()

    private val _error = MutableStateFlow<String?>(null)
    /** Human-readable error from the last failed operation, or null. */
    val error: StateFlow<String?> = _error.asStateFlow()

    // ---- Read ----

    /**
     * Refresh the cache from the daemon. Called from Activity.onResume
     * and after every mutation.
     *
     * @return The fresh profile or null on failure.
     */
    suspend fun refresh(): ProfileConfig? = mutex.withLock {
        _loading.value = true
        _error.value = null
        try {
            when (val result = client.configGet()) {
                is DaemonClientResult.Ok -> {
                    _profile.value = result.data
                    result.data
                }
                else -> {
                    val msg = describeFailure(result)
                    Log.w(TAG, "refresh failed: $msg")
                    _profile.value = null
                    _error.value = msg
                    null
                }
            }
        } finally {
            _loading.value = false
        }
    }

    /**
     * Returns the cached profile, loading it from the daemon if the cache is empty.
     */
    suspend fun getOrLoad(): ProfileConfig? {
        _profile.value?.let { return it }
        return refresh()
    }

    // ---- Node CRUD ----

    /** Add a node to the profile. */
    suspend fun addNode(node: Node): Boolean = mutatePanel("addNode") { config ->
        config.copy(nodes = config.nodes + node)
    }

    /** Remove a node by ID. */
    suspend fun removeNode(nodeId: String): Boolean = mutatePanel("removeNode") { config ->
        config.copy(
            nodes = config.nodes.filter { it.id != nodeId },
            activeNodeId = if (config.activeNodeId == nodeId) null else config.activeNodeId
        )
    }

    /** Replace a node, matching by ID. */
    suspend fun updateNode(updated: Node): Boolean = mutatePanel("updateNode") { config ->
        config.copy(nodes = config.nodes.map { if (it.id == updated.id) updated else it })
    }

    /** Set the active node for this profile. */
    suspend fun setActiveNode(nodeId: String): Boolean = mutatePanel("setActiveNode") { config ->
        config.copy(activeNodeId = nodeId)
    }

    /** Let the daemon selector use automatic node selection. */
    suspend fun clearActiveNode(): Boolean = mutatePanel("clearActiveNode") { config ->
        config.copy(activeNodeId = null)
    }

    /** Import nodes from share links or refresh a subscription URL. */
    suspend fun importNodes(input: String): List<Node> = withContext(Dispatchers.IO) {
        mutex.withLock {
            _loading.value = true
            _error.value = null
            try {
                val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                    if (_error.value.isNullOrBlank()) {
                        _error.value = messages.get(com.privstack.panel.R.string.error_no_profile_loaded)
                    }
                    return@withLock emptyList()
                }

                if (LinkParser.isSubscriptionUrl(input.trim())) {
                    importSubscriptionUnlocked(current, input.trim())
                } else {
                    importDirectLinksUnlocked(current, input)
                }
            } finally {
                _loading.value = false
            }
        }
    }

    // ---- Profile-level mutations ----

    /** Replace the full profile config. */
    suspend fun setProfile(config: ProfileConfig): Boolean = mutex.withLock {
        _loading.value = true
        _error.value = null
        try {
            if (!ensureRuntimeIdle("setProfile")) {
                return@withLock false
            }
            when (val result = client.configSet(config, reload = false)) {
                is DaemonClientResult.Ok -> {
                    publishRuntimeStatus(result.data)
                    when (val panelResult = client.panelSet(config, reload = true)) {
                        is DaemonClientResult.Ok -> {
                            publishRuntimeStatus(panelResult.data)
                            refreshUnlockedWithStatus("setProfile")
                        }
                        else -> {
                            val msg = describeFailure(panelResult)
                            Log.w(TAG, "setProfile panel update failed: $msg")
                            _error.value = msg
                            refreshUnlockedWithStatus("setProfile")
                            false
                        }
                    }
                }
                else -> {
                    val msg = describeFailure(result)
                    Log.w(TAG, "setProfile failed: $msg")
                    if (result.configWasSaved()) {
                        _error.value = msg
                        return@withLock refreshUnlockedWithStatus("setProfile")
                    }
                    _error.value = msg
                    false
                }
            }
        } finally {
            _loading.value = false
        }
    }

    /** Update routing, DNS, TUN or inbound settings. */
    suspend fun updateConfig(transform: (ProfileConfig) -> ProfileConfig): Boolean =
        mutate("updateConfig", transform)

    // ---- Internal helpers ----

    /**
     * Generic read-modify-write: read cache -> apply transform -> send to daemon -> re-read.
     */
    private suspend fun mutate(
        tag: String,
        transform: (ProfileConfig) -> ProfileConfig
    ): Boolean = mutex.withLock {
        _loading.value = true
        _error.value = null
        try {
            val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                if (_error.value.isNullOrBlank()) {
                    _error.value = messages.get(com.privstack.panel.R.string.error_no_profile_loaded)
                }
                return@withLock false
            }
            if (!ensureRuntimeIdle(tag)) {
                return@withLock false
            }
            val updated = transform(current)
            when (val result = client.configSet(updated)) {
                is DaemonClientResult.Ok -> {
                    publishRuntimeStatus(result.data)
                    refreshUnlockedWithStatus(tag)
                }
                else -> {
                    val msg = describeFailure(result)
                    Log.w(TAG, "$tag failed: $msg")
                    if (result.configWasSaved()) {
                        _error.value = msg
                        return refreshUnlockedWithStatus(tag)
                    }
                    _error.value = msg
                    false
                }
            }
        } finally {
            _loading.value = false
        }
    }

    private suspend fun mutatePanel(
        tag: String,
        transform: (ProfileConfig) -> ProfileConfig,
    ): Boolean = mutex.withLock {
        _loading.value = true
        _error.value = null
        try {
            val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                if (_error.value.isNullOrBlank()) {
                    _error.value = messages.get(com.privstack.panel.R.string.error_no_profile_loaded)
                }
                return@withLock false
            }
            if (!ensureRuntimeIdle(tag)) {
                return@withLock false
            }
            val updated = transform(current)
            when (val result = client.panelSet(updated)) {
                is DaemonClientResult.Ok -> {
                    publishRuntimeStatus(result.data)
                    refreshUnlockedWithStatus(tag)
                }
                else -> {
                    val msg = describeFailure(result)
                    Log.w(TAG, "$tag panel update failed: $msg")
                    if (result.configWasSaved()) {
                        _error.value = msg
                        return refreshUnlockedWithStatus(tag)
                    }
                    _error.value = msg
                    false
                }
            }
        } finally {
            _loading.value = false
        }
    }

    /** Refresh without acquiring the mutex (caller already holds it). */
    private suspend fun refreshUnlocked() {
        when (val result = client.configGet()) {
            is DaemonClientResult.Ok -> {
                _profile.value = result.data
            }
            else -> {
                _profile.value = null
                Log.w(TAG, "refreshUnlocked failed: ${describeFailure(result)}")
            }
        }
    }

    private suspend fun refreshUnlockedWithStatus(tag: String): Boolean {
        return when (val result = client.configGet()) {
            is DaemonClientResult.Ok -> {
                _profile.value = result.data
                true
            }
            else -> {
                val msg = describeFailure(result)
                _profile.value = null
                _error.value = msg
                Log.w(TAG, "$tag post-write refresh failed: $msg")
                false
            }
        }
    }

    private suspend fun refreshUnlockedOrNull(): ProfileConfig? {
        return when (val result = client.configGet()) {
            is DaemonClientResult.Ok -> {
                _profile.value = result.data
                result.data
            }
            else -> {
                val msg = describeFailure(result)
                _profile.value = null
                Log.w(TAG, "refreshUnlockedOrNull failed: $msg")
                _error.value = msg
                null
            }
        }
    }

    private suspend fun importDirectLinksUnlocked(
        current: ProfileConfig,
        rawInput: String
    ): List<Node> {
        val detectedUris = LinkParser.detectUris(rawInput)
        if (detectedUris.isEmpty()) {
            _error.value = messages.get(com.privstack.panel.R.string.node_no_valid_proxy_links_detected)
            return emptyList()
        }

        val parsedNodes = detectedUris.mapNotNull(LinkParser::parse)
        if (parsedNodes.isEmpty()) {
            _error.value = messages.get(com.privstack.panel.R.string.node_no_supported_proxy_links_parsed)
            return emptyList()
        }

        val merged = mergeNodes(current, parsedNodes)
        return if (persistPanelUnlocked(merged.updatedConfig)) {
            merged.importedNodes
        } else {
            emptyList()
        }
    }

    private suspend fun importSubscriptionUnlocked(
        current: ProfileConfig,
        url: String
    ): List<Node> {
        val fetched = when (val result = client.subscriptionFetch(url)) {
            is DaemonClientResult.Ok -> result.data
            else -> {
                val msg = messages.formatDaemonFailure(result)
                Log.w(TAG, "daemon subscription fetch failed: $msg")
                _error.value = msg
                return emptyList()
            }
        }

        val parsed = SubscriptionHandler.parseResponse(fetched.body, fetched.headers)
        if (parsed.nodes.isEmpty()) {
            _error.value = if (parsed.parseFailures > 0) {
                messages.get(com.privstack.panel.R.string.subscription_no_supported_links)
            } else {
                messages.get(com.privstack.panel.R.string.subscription_empty)
            }
            return emptyList()
        }

        val merged = mergeNodes(current, parsed.nodes, dropRemoved = true)
        return if (persistPanelUnlocked(merged.updatedConfig)) {
            parsed.nodes
        } else {
            emptyList()
        }
    }

    private suspend fun persistPanelUnlocked(config: ProfileConfig): Boolean {
        if (!ensureRuntimeIdle("persistPanelUnlocked")) {
            return false
        }
        return when (val result = client.panelSet(config)) {
            is DaemonClientResult.Ok -> {
                publishRuntimeStatus(result.data)
                refreshUnlockedWithStatus("persistPanelUnlocked")
            }
            else -> {
                val msg = describeFailure(result)
                Log.w(TAG, "persistPanelUnlocked failed: $msg")
                if (result.configWasSaved()) {
                    _error.value = msg
                    return refreshUnlockedWithStatus("persistPanelUnlocked")
                }
                _error.value = msg
                false
            }
        }
    }

    private fun mergeNodes(
        current: ProfileConfig,
        incoming: List<Node>,
        dropRemoved: Boolean = false,
    ): MergeResult {
        val preview = SubscriptionHandler.previewMerge(current.nodes, incoming)
        val mergedNodes = SubscriptionHandler.applyMerge(preview, dropRemoved = dropRemoved)
        val nextActiveId = current.activeNodeId
            ?.takeIf { activeId -> mergedNodes.any { it.id == activeId } }
            ?: preview.added.firstOrNull()?.id
            ?: preview.updated.firstOrNull()?.id
            ?: mergedNodes.firstOrNull()?.id

        return MergeResult(
            importedNodes = incoming,
            updatedConfig = current.copy(
                nodes = mergedNodes,
                activeNodeId = nextActiveId,
            )
        )
    }

    private data class MergeResult(
        val importedNodes: List<Node>,
        val updatedConfig: ProfileConfig,
    )

    private fun <T> describeFailure(result: DaemonClientResult<T>): String =
        messages.formatDaemonFailure(result)

    private fun publishRuntimeStatus(info: ConfigMutationInfo) {
        info.runtimeStatus?.let(poller::publishBackendStatus)
    }

    private suspend fun ensureRuntimeIdle(tag: String): Boolean {
        return when (val result = client.backendStatus()) {
            is DaemonClientResult.Ok -> {
                poller.publishBackendStatus(result.data)
                if (result.data.activeOperation != null) {
                    _error.value = messages.get(com.privstack.panel.R.string.error_runtime_busy)
                    Log.w(TAG, "$tag blocked: runtime operation is active (${result.data.activeOperation.kind})")
                    false
                } else {
                    true
                }
            }
            else -> {
                val msg = describeFailure(result)
                Log.w(TAG, "$tag runtime idle check failed: $msg")
                _error.value = msg
                false
            }
        }
    }

    private fun <T> DaemonClientResult<T>.configWasSaved(): Boolean {
        val error = this as? DaemonClientResult.DaemonError ?: return false
        val details = error.details?.jsonObject ?: return false
        return details["config_saved"]?.jsonPrimitive?.booleanOrNull == true
    }
}
