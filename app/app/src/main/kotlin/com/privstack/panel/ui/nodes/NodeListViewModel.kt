package com.privstack.panel.ui.nodes

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.model.Node
import com.privstack.panel.model.Protocol
import com.privstack.panel.repository.ProfileRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import javax.inject.Inject

private const val TAG = "NodeListViewModel"

enum class NodeSortMode { NAME, LATENCY, COUNTRY }

data class NodeListUiState(
    val groups: List<String> = listOf("Default"),
    val selectedGroup: String = "Default",
    val nodes: List<Node> = emptyList(),
    val activeNodeId: String? = null,
    val sortMode: NodeSortMode = NodeSortMode.NAME,
    val showImportSheet: Boolean = false,
    /** Nodes parsed from import input, waiting for user selection. */
    val importCandidates: List<ImportCandidate> = emptyList(),
    val isLoading: Boolean = false,
    /** Error message from the last operation, or null. */
    val errorMessage: String? = null,
)

data class ImportCandidate(
    val node: Node,
    val selected: Boolean = true,
)

@HiltViewModel
class NodeListViewModel @Inject constructor(
    private val profileRepository: ProfileRepository,
    private val daemonClient: DaemonClient,
) : ViewModel() {

    private val _uiState = MutableStateFlow(NodeListUiState())
    val uiState: StateFlow<NodeListUiState> = _uiState.asStateFlow()

    init {
        observeProfile()
        loadNodes()
    }

    // ---- Public actions ----

    fun selectGroup(group: String) {
        _uiState.update { it.copy(selectedGroup = group) }
    }

    fun selectNode(nodeId: String) {
        viewModelScope.launch {
            _uiState.update { it.copy(activeNodeId = nodeId, errorMessage = null) }
            val ok = profileRepository.setActiveNode(nodeId)
            if (ok) {
                Log.d(TAG, "Active node set to $nodeId, starting connection")
                when (val result = daemonClient.start()) {
                    is DaemonClientResult.Ok -> {
                        Log.d(TAG, "Start succeeded for node $nodeId")
                    }
                    else -> {
                        val msg = describeError(result)
                        Log.w(TAG, "Start failed for node $nodeId: $msg")
                        _uiState.update { it.copy(errorMessage = msg) }
                    }
                }
            } else {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(errorMessage = err ?: "Failed to set active node")
                }
            }
        }
    }

    fun setSortMode(mode: NodeSortMode) {
        _uiState.update { state ->
            state.copy(
                sortMode = mode,
                nodes = sortNodes(state.nodes, mode),
            )
        }
    }

    fun deleteNode(nodeId: String) {
        viewModelScope.launch {
            _uiState.update { it.copy(errorMessage = null) }
            val ok = profileRepository.removeNode(nodeId)
            if (!ok) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(errorMessage = err ?: "Failed to delete node")
                }
            }
            // UI updates via the profile observer
        }
    }

    fun testLatency(nodeId: String) {
        viewModelScope.launch {
            // Use daemon health/status to measure latency for the specific node.
            // The daemon does not expose a per-node ping command yet, so we use
            // a simple timing wrapper around a status call as a proxy.
            val startMs = System.currentTimeMillis()
            when (val result = daemonClient.status()) {
                is DaemonClientResult.Ok -> {
                    val elapsed = (System.currentTimeMillis() - startMs).toInt()
                    _uiState.update { state ->
                        state.copy(
                            nodes = state.nodes.map { node ->
                                if (node.id == nodeId) node.copy(latencyMs = elapsed) else node
                            },
                        )
                    }
                }
                else -> {
                    Log.w(TAG, "Latency test failed for $nodeId: ${describeError(result)}")
                    _uiState.update { state ->
                        state.copy(
                            nodes = state.nodes.map { node ->
                                if (node.id == nodeId) node.copy(latencyMs = -1) else node
                            },
                        )
                    }
                }
            }
        }
    }

    fun showImportSheet() {
        _uiState.update { it.copy(showImportSheet = true, importCandidates = emptyList()) }
    }

    fun hideImportSheet() {
        _uiState.update { it.copy(showImportSheet = false, importCandidates = emptyList()) }
    }

    /**
     * Parse URIs from pasted text and populate import candidates.
     */
    fun detectUris(text: String) {
        val uriPattern = Regex("(vless|vmess|trojan|ss|hysteria2|hy2|tuic)://[^\\s]+")
        val matches = uriPattern.findAll(text).toList()

        val candidates = matches.mapIndexed { i, match ->
            val uri = match.value
            val scheme = uri.substringBefore("://")
            val protocol = Protocol.fromString(scheme) ?: Protocol.VLESS
            val name = "Imported-${i + 1}"

            ImportCandidate(
                node = Node(
                    id = "import_${System.currentTimeMillis()}_$i",
                    name = name,
                    protocol = protocol,
                    server = extractServerFromUri(uri),
                    port = extractPortFromUri(uri),
                    link = uri,
                    outbound = buildJsonObject {},
                    group = "Imported",
                ),
                selected = true,
            )
        }

        _uiState.update { it.copy(importCandidates = candidates) }
    }

    fun toggleImportCandidate(index: Int) {
        _uiState.update { state ->
            val updated = state.importCandidates.toMutableList()
            if (index in updated.indices) {
                updated[index] = updated[index].copy(selected = !updated[index].selected)
            }
            state.copy(importCandidates = updated)
        }
    }

    fun importSelected() {
        val selected = _uiState.value.importCandidates
            .filter { it.selected }
            .map { it.node }

        if (selected.isEmpty()) return

        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true, errorMessage = null) }

            // Build a single multi-line string of share links for the daemon
            val links = selected.mapNotNull { it.link.ifBlank { null } }.joinToString("\n")

            if (links.isNotBlank()) {
                val imported = profileRepository.importNodes(links)
                if (imported.isEmpty()) {
                    val err = profileRepository.error.value
                    _uiState.update {
                        it.copy(
                            errorMessage = err ?: "Import failed",
                            isLoading = false,
                        )
                    }
                } else {
                    Log.d(TAG, "Imported ${imported.size} nodes via daemon")
                    _uiState.update {
                        it.copy(
                            showImportSheet = false,
                            importCandidates = emptyList(),
                            isLoading = false,
                        )
                    }
                }
            } else {
                _uiState.update {
                    it.copy(
                        errorMessage = "No valid links to import",
                        isLoading = false,
                    )
                }
            }
            // Node list updates via the profile observer
        }
    }

    /**
     * Parse subscription URL and add nodes.
     */
    fun fetchSubscription(url: String) {
        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true, errorMessage = null) }

            // The daemon handles the HTTP fetch (APK has no INTERNET permission).
            // We pass the subscription URL as a link to config.import.
            val imported = profileRepository.importNodes(url)
            if (imported.isEmpty()) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(
                        errorMessage = err ?: "Subscription fetch failed",
                        isLoading = false,
                    )
                }
            } else {
                // Show imported nodes as candidates for review
                _uiState.update {
                    it.copy(
                        importCandidates = imported.map { ImportCandidate(it, selected = true) },
                        isLoading = false,
                    )
                }
            }
        }
    }

    // ---- Internal ----

    /**
     * Observe the profile repository for changes and project nodes into the UI state.
     */
    private fun observeProfile() {
        viewModelScope.launch {
            profileRepository.profile.collect { config ->
                if (config != null) {
                    val nodes = config.nodes
                    val groups = nodes.map { it.group }.distinct().ifEmpty { listOf("Default") }
                    _uiState.update { state ->
                        state.copy(
                            nodes = sortNodes(nodes, state.sortMode),
                            groups = groups,
                            activeNodeId = config.activeNodeId ?: state.activeNodeId,
                        )
                    }
                }
            }
        }
    }

    /**
     * Initial load from the daemon.
     */
    private fun loadNodes() {
        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true, errorMessage = null) }
            val config = profileRepository.getOrLoad()
            if (config == null) {
                val err = profileRepository.error.value
                Log.w(TAG, "Initial node load failed: $err")
                _uiState.update {
                    it.copy(
                        isLoading = false,
                        errorMessage = err,
                    )
                }
            } else {
                _uiState.update { it.copy(isLoading = false) }
            }
            // Actual nodes are applied by the profile observer
        }
    }

    private fun sortNodes(nodes: List<Node>, mode: NodeSortMode): List<Node> = when (mode) {
        NodeSortMode.NAME -> nodes.sortedBy { it.name.lowercase() }
        NodeSortMode.LATENCY -> nodes.sortedBy { it.latencyMs ?: Int.MAX_VALUE }
        NodeSortMode.COUNTRY -> nodes.sortedBy { extractCountryFromName(it.name) }
    }

    private fun extractCountryFromName(name: String): String {
        // Simple heuristic: first word before dash/space
        return name.split(Regex("[\\s-]")).firstOrNull()?.lowercase() ?: ""
    }

    private fun extractServerFromUri(uri: String): String {
        return try {
            val afterScheme = uri.substringAfter("://")
            val hostPart = afterScheme.substringAfter("@").substringBefore(":")
                .substringBefore("/").substringBefore("?")
            hostPart.ifBlank { "unknown" }
        } catch (_: Exception) { "unknown" }
    }

    private fun extractPortFromUri(uri: String): Int {
        return try {
            val afterScheme = uri.substringAfter("://")
            val afterHost = afterScheme.substringAfter("@").substringAfter(":")
            val portStr = afterHost.substringBefore("/").substringBefore("?")
                .substringBefore("#")
            portStr.toIntOrNull() ?: 443
        } catch (_: Exception) { 443 }
    }

    private fun <T> describeError(result: DaemonClientResult<T>): String = when (result) {
        is DaemonClientResult.DaemonError -> "Daemon error ${result.code}: ${result.message}"
        is DaemonClientResult.RootDenied -> "Root access denied"
        is DaemonClientResult.Timeout -> "Request timed out"
        is DaemonClientResult.DaemonNotFound -> "Daemon not installed"
        is DaemonClientResult.ParseError -> "Invalid daemon response"
        is DaemonClientResult.Failure -> "Error: ${result.throwable.message}"
        is DaemonClientResult.Ok -> "OK"
    }
}
