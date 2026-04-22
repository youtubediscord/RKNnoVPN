package com.privstack.panel.repository

import android.util.Log
import com.privstack.panel.`import`.LinkParser
import com.privstack.panel.`import`.SubscriptionHandler
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
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
    private val client: DaemonClient
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
    suspend fun addNode(node: Node): Boolean = mutate("addNode") { config ->
        config.copy(nodes = config.nodes + node)
    }

    /** Remove a node by ID. */
    suspend fun removeNode(nodeId: String): Boolean = mutate("removeNode") { config ->
        config.copy(
            nodes = config.nodes.filter { it.id != nodeId },
            activeNodeId = if (config.activeNodeId == nodeId) null else config.activeNodeId
        )
    }

    /** Replace a node, matching by ID. */
    suspend fun updateNode(updated: Node): Boolean = mutate("updateNode") { config ->
        config.copy(nodes = config.nodes.map { if (it.id == updated.id) updated else it })
    }

    /** Set the active node for this profile. */
    suspend fun setActiveNode(nodeId: String): Boolean = mutate("setActiveNode") { config ->
        config.copy(activeNodeId = nodeId)
    }

    /** Import nodes from share links or refresh a subscription URL. */
    suspend fun importNodes(input: String): List<Node> = withContext(Dispatchers.IO) {
        mutex.withLock {
            _loading.value = true
            _error.value = null
            try {
                val current = _profile.value ?: refreshUnlockedOrNull() ?: run {
                    _error.value = "No profile loaded"
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
            when (val result = client.configSet(config)) {
                is DaemonClientResult.Ok -> {
                    // Re-read to confirm daemon accepted the config
                    refreshUnlockedWithStatus("setProfile")
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
                _error.value = "No profile loaded"
                return@withLock false
            }
            val updated = transform(current)
            when (val result = client.configSet(updated)) {
                is DaemonClientResult.Ok -> {
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
            _error.value = "No valid proxy links detected"
            return emptyList()
        }

        val parsedNodes = detectedUris.mapNotNull(LinkParser::parse)
        if (parsedNodes.isEmpty()) {
            _error.value = "No supported proxy links could be parsed"
            return emptyList()
        }

        val merged = mergeNodes(current, parsedNodes)
        return if (persistProfileUnlocked(merged.updatedConfig)) {
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
                val msg = describeFailure(result)
                Log.w(TAG, "subscriptionFetch failed: $msg")
                _error.value = msg
                return emptyList()
            }
        }

        val parsed = SubscriptionHandler.parseResponse(fetched.body, fetched.headers)
        if (parsed.nodes.isEmpty()) {
            _error.value = if (parsed.parseFailures > 0) {
                "Subscription did not contain any supported proxy links"
            } else {
                "Subscription was empty"
            }
            return emptyList()
        }

        val merged = mergeNodes(current, parsed.nodes, dropRemoved = true)
        return if (persistProfileUnlocked(merged.updatedConfig)) {
            parsed.nodes
        } else {
            emptyList()
        }
    }

    private suspend fun persistProfileUnlocked(config: ProfileConfig): Boolean {
        return when (val result = client.configSet(config)) {
            is DaemonClientResult.Ok -> {
                refreshUnlockedWithStatus("persistProfileUnlocked")
            }
            else -> {
                val msg = describeFailure(result)
                Log.w(TAG, "persistProfileUnlocked failed: $msg")
                if (result.configWasSaved()) {
                    _error.value = msg
                    return refreshUnlockedWithStatus("persistProfileUnlocked")
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

    private fun <T> describeFailure(result: DaemonClientResult<T>): String = when (result) {
        is DaemonClientResult.DaemonError -> "Daemon error ${result.code}: ${result.message}"
        is DaemonClientResult.RootDenied -> "Root access denied"
        is DaemonClientResult.Timeout -> "Request timed out (${result.method})"
        is DaemonClientResult.DaemonNotFound -> "Daemon not installed"
        is DaemonClientResult.ParseError -> "Invalid response from daemon"
        is DaemonClientResult.Failure -> "Unexpected error: ${result.throwable.message}"
        is DaemonClientResult.Ok -> "OK" // unreachable in failure context
    }

    private fun <T> DaemonClientResult<T>.configWasSaved(): Boolean {
        val error = this as? DaemonClientResult.DaemonError ?: return false
        val details = error.details?.jsonObject ?: return false
        return details["config_saved"]?.jsonPrimitive?.booleanOrNull == true
    }
}
