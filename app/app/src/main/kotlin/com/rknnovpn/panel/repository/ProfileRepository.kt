package com.rknnovpn.panel.repository

import android.util.Log
import com.rknnovpn.panel.`import`.LinkParser
import com.rknnovpn.panel.i18n.UserMessageFormatter
import com.rknnovpn.panel.ipc.DaemonClient
import com.rknnovpn.panel.ipc.DaemonClientResult
import com.rknnovpn.panel.ipc.ConfigMutationInfo
import com.rknnovpn.panel.ipc.PollingStatusSource
import com.rknnovpn.panel.model.Node
import com.rknnovpn.panel.model.ProfileConfig
import com.rknnovpn.panel.model.SubscriptionSource
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

data class SubscriptionImportPreview(
    val url: String,
    val source: SubscriptionSource,
    val nodes: List<Node>,
    val parseFailures: Int,
    val added: Int,
    val updated: Int,
    val stale: Int,
) {
    val addedCount: Int get() = added
    val updatedCount: Int get() = updated
    val removedCount: Int get() = stale
}

/**
 * Cache-backed profile CRUD via [DaemonClient].
 *
 * **Single source of truth**: the daemon-owned `profile.json`.
 * This repository keeps an in-memory cache that is refreshed:
 * - On first access after construction
 * - On explicit [refresh] (typically called from Activity.onResume)
 * - After every mutating operation (add/remove/update node, change routing, etc.)
 *
 * All writes go through the daemon profile RPCs and then re-read to keep
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

    private val _notice = MutableStateFlow<String?>(null)
    /** Human-readable status from the last successful partial or informational operation. */
    val notice: StateFlow<String?> = _notice.asStateFlow()

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
        _notice.value = null
        try {
            when (val result = client.profileGet()) {
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
    suspend fun setActiveNode(nodeId: String): Boolean = mutex.withLock {
        _loading.value = true
        _error.value = null
        _notice.value = null
        try {
            val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                if (_error.value.isNullOrBlank()) {
                    _error.value = messages.get(com.rknnovpn.panel.R.string.error_no_profile_loaded)
                }
                return@withLock false
            }
            if (current.nodes.none { it.id == nodeId && !it.stale }) {
                _error.value = messages.get(com.rknnovpn.panel.R.string.node_not_found)
                return@withLock false
            }
            if (!ensureRuntimeIdle("setActiveNode")) {
                return@withLock false
            }
            when (val result = client.profileSetActiveNode(nodeId)) {
                is DaemonClientResult.Ok -> {
                    publishRuntimeStatus(result.data)
                    refreshUnlockedWithStatus("setActiveNode")
                }
                else -> {
                    val msg = describeFailure(result)
                    Log.w(TAG, "setActiveNode profile update failed: $msg")
                    if (result.configWasSaved()) {
                        return@withLock refreshAfterSavedFailure("setActiveNode", msg)
                    }
                    _error.value = msg
                    false
                }
            }
        } finally {
            _loading.value = false
        }
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
            _notice.value = null
            try {
                val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                    if (_error.value.isNullOrBlank()) {
                        _error.value = messages.get(com.rknnovpn.panel.R.string.error_no_profile_loaded)
                    }
                    return@withLock emptyList()
                }

                if (LinkParser.isSubscriptionUrl(input.trim())) {
                    importSubscriptionUnlocked(input.trim())
                } else {
                    importDirectLinksUnlocked(input)
                }
            } finally {
                _loading.value = false
            }
        }
    }

    suspend fun previewSubscription(url: String): SubscriptionImportPreview? = withContext(Dispatchers.IO) {
        mutex.withLock {
            _loading.value = true
            _error.value = null
            _notice.value = null
            try {
                val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                    if (_error.value.isNullOrBlank()) {
                        _error.value = messages.get(com.rknnovpn.panel.R.string.error_no_profile_loaded)
                    }
                    return@withLock null
                }
                buildSubscriptionPreviewUnlocked(url.trim())
            } finally {
                _loading.value = false
            }
        }
    }

    suspend fun applySubscriptionPreview(preview: SubscriptionImportPreview): List<Node> =
        withContext(Dispatchers.IO) {
            mutex.withLock {
                _loading.value = true
                _error.value = null
                _notice.value = null
                try {
                    val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                        if (_error.value.isNullOrBlank()) {
                            _error.value = messages.get(com.rknnovpn.panel.R.string.error_no_profile_loaded)
                        }
                        return@withLock emptyList()
                    }
                    applySubscriptionPreviewUnlocked(preview)
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
        _notice.value = null
        try {
            if (!ensureRuntimeIdle("setProfile")) {
                return@withLock false
            }
            when (val result = client.profileApply(config, reload = true)) {
                is DaemonClientResult.Ok -> {
                    publishRuntimeStatus(result.data)
                    refreshUnlockedWithStatus("setProfile")
                }
                else -> {
                    val msg = describeFailure(result)
                    Log.w(TAG, "setProfile failed: $msg")
                    if (result.configWasSaved()) {
                        return@withLock refreshAfterSavedFailure("setProfile", msg)
                    }
                    _error.value = msg
                    false
                }
            }
        } finally {
            _loading.value = false
        }
    }

    /** Update routing, DNS, reserved profile network fields, or inbound settings. */
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
        _notice.value = null
        try {
            val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                if (_error.value.isNullOrBlank()) {
                    _error.value = messages.get(com.rknnovpn.panel.R.string.error_no_profile_loaded)
                }
                return@withLock false
            }
            if (!ensureRuntimeIdle(tag)) {
                return@withLock false
            }
            val updated = transform(current)
            when (val result = client.profileApply(updated)) {
                is DaemonClientResult.Ok -> {
                    publishRuntimeStatus(result.data)
                    refreshUnlockedWithStatus(tag)
                }
                else -> {
                    val msg = describeFailure(result)
                    Log.w(TAG, "$tag failed: $msg")
                    if (result.configWasSaved()) {
                        return refreshAfterSavedFailure(tag, msg)
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
        _notice.value = null
        try {
            val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                if (_error.value.isNullOrBlank()) {
                    _error.value = messages.get(com.rknnovpn.panel.R.string.error_no_profile_loaded)
                }
                return@withLock false
            }
            if (!ensureRuntimeIdle(tag)) {
                return@withLock false
            }
            val updated = transform(current)
            when (val result = client.profileApply(updated)) {
                is DaemonClientResult.Ok -> {
                    publishRuntimeStatus(result.data)
                    refreshUnlockedWithStatus(tag)
                }
                else -> {
                    val msg = describeFailure(result)
                Log.w(TAG, "$tag profile update failed: $msg")
                    if (result.configWasSaved()) {
                        return refreshAfterSavedFailure(tag, msg)
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
        when (val result = client.profileGet()) {
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
        return when (val result = client.profileGet()) {
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

    private suspend fun refreshAfterSavedFailure(tag: String, message: String): Boolean {
        _error.value = message
        refreshUnlockedWithStatus("$tag saved-failure")
        return false
    }

    private suspend fun refreshUnlockedOrNull(): ProfileConfig? {
        return when (val result = client.profileGet()) {
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
        rawInput: String
    ): List<Node> {
        val detectedUris = LinkParser.detectUris(rawInput)
        if (detectedUris.isEmpty()) {
            _error.value = messages.get(com.rknnovpn.panel.R.string.node_no_valid_proxy_links_detected)
            return emptyList()
        }

        val parsedNodes = detectedUris.mapNotNull(LinkParser::parse)
        if (parsedNodes.isEmpty()) {
            _error.value = messages.get(com.rknnovpn.panel.R.string.node_no_supported_proxy_links_parsed)
            return emptyList()
        }

        return when (val result = client.profileImportNodes(parsedNodes)) {
            is DaemonClientResult.Ok -> {
                publishRuntimeStatus(result.data)
                refreshUnlockedWithStatus("importDirectLinks")
                parsedNodes
            }
            else -> {
                val msg = describeFailure(result)
                Log.w(TAG, "importDirectLinks failed: $msg")
                if (result.configWasSaved()) {
                    refreshAfterSavedFailure("importDirectLinks", msg)
                    return emptyList()
                }
                _error.value = msg
                emptyList()
            }
        }
    }

    private suspend fun importSubscriptionUnlocked(
        url: String
    ): List<Node> {
        val preview = buildSubscriptionPreviewUnlocked(url) ?: return emptyList()
        return applySubscriptionPreviewUnlocked(preview)
    }

    private suspend fun buildSubscriptionPreviewUnlocked(
        url: String,
    ): SubscriptionImportPreview? {
        val preview = when (val result = client.subscriptionPreview(url)) {
            is DaemonClientResult.Ok -> result.data
            else -> {
                val msg = messages.formatDaemonFailure(result)
                Log.w(TAG, "daemon subscription preview failed: $msg")
                _error.value = msg
                return null
            }
        }
        if (preview.nodes.isEmpty() && preview.stale == 0) {
            _error.value = if (preview.parseFailures > 0) {
                messages.get(com.rknnovpn.panel.R.string.subscription_no_supported_links)
            } else {
                messages.get(com.rknnovpn.panel.R.string.subscription_empty)
            }
            return null
        }

        return SubscriptionImportPreview(
            url = preview.source.url.ifBlank { url },
            source = preview.source,
            nodes = preview.nodes,
            parseFailures = preview.parseFailures,
            added = preview.added,
            updated = preview.updated,
            stale = preview.stale,
        )
    }

    private suspend fun applySubscriptionPreviewUnlocked(
        preview: SubscriptionImportPreview,
    ): List<Node> {
        return when (val result = client.subscriptionRefresh(preview.url)) {
            is DaemonClientResult.Ok -> {
                publishRuntimeStatus(result.data)
                refreshUnlockedWithStatus("subscriptionRefresh")
                _notice.value = messages.formatSubscriptionRefresh(
                    importedNodes = result.data.imported ?: preview.nodes.size,
                    parseFailures = result.data.parseFailures ?: preview.parseFailures,
                )
                preview.nodes
            }
            else -> {
                val msg = describeFailure(result)
                Log.w(TAG, "subscriptionRefresh failed: $msg")
                if (result.configWasSaved()) {
                    refreshAfterSavedFailure("subscriptionRefresh", msg)
                    return emptyList()
                }
                _error.value = msg
                emptyList()
            }
        }
    }

    private fun <T> describeFailure(result: DaemonClientResult<T>): String =
        messages.formatDaemonFailure(result)

    private fun publishRuntimeStatus(info: ConfigMutationInfo) {
        info.runtimeStatus?.let(poller::publishBackendStatus)
        messages.formatConfigMutationNotice(info)?.let { _notice.value = it }
    }

    private suspend fun ensureRuntimeIdle(tag: String): Boolean {
        return when (val result = client.backendStatus()) {
            is DaemonClientResult.Ok -> {
                poller.publishBackendStatus(result.data)
                if (result.data.activeOperation != null) {
                    _error.value = messages.get(com.rknnovpn.panel.R.string.error_runtime_busy)
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
        val details = runCatching {
            error.details?.jsonObject
                ?: error.envelope?.jsonObject
                    ?.get("error")?.jsonObject
                    ?.get("details")?.jsonObject
        }.getOrNull() ?: return false
        return details["config_saved"]?.jsonPrimitive?.booleanOrNull == true ||
            details["configSaved"]?.jsonPrimitive?.booleanOrNull == true
    }
}
