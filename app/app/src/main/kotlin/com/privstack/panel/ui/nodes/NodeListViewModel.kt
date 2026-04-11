package com.privstack.panel.ui.nodes

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.model.Node
import com.privstack.panel.model.Protocol
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.serialization.json.buildJsonObject
import javax.inject.Inject

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
)

data class ImportCandidate(
    val node: Node,
    val selected: Boolean = true,
)

@HiltViewModel
class NodeListViewModel @Inject constructor() : ViewModel() {

    private val _uiState = MutableStateFlow(NodeListUiState())
    val uiState: StateFlow<NodeListUiState> = _uiState.asStateFlow()

    init {
        // TODO: load from NodeRepository
        loadDemoNodes()
    }

    // ---- Public actions ----

    fun selectGroup(group: String) {
        _uiState.update { it.copy(selectedGroup = group) }
    }

    fun selectNode(nodeId: String) {
        _uiState.update { it.copy(activeNodeId = nodeId) }
        // TODO: DaemonRepository.connect(nodeId)
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
        _uiState.update { state ->
            val updated = state.nodes.filter { it.id != nodeId }
            val groups = updated.map { it.group }.distinct().ifEmpty { listOf("Default") }
            state.copy(
                nodes = updated,
                groups = groups,
                activeNodeId = if (state.activeNodeId == nodeId) null else state.activeNodeId,
            )
        }
    }

    fun testLatency(nodeId: String) {
        viewModelScope.launch {
            // TODO: call DaemonRepository.pingNode(nodeId)
            delay(500)
            val fakeLatency = (30..800).random()
            _uiState.update { state ->
                state.copy(
                    nodes = state.nodes.map { node ->
                        if (node.id == nodeId) node.copy(latencyMs = fakeLatency) else node
                    },
                )
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

        _uiState.update { state ->
            val allNodes = state.nodes + selected
            val groups = allNodes.map { it.group }.distinct()
            state.copy(
                nodes = sortNodes(allNodes, state.sortMode),
                groups = groups,
                showImportSheet = false,
                importCandidates = emptyList(),
            )
        }
    }

    /**
     * Parse subscription URL and add nodes.
     */
    fun fetchSubscription(url: String) {
        viewModelScope.launch {
            // TODO: real HTTP fetch + base64 decode
            delay(1000)
            // For now, generate a demo node from the URL
            val node = Node(
                id = "sub_${System.currentTimeMillis()}",
                name = "Sub node",
                protocol = Protocol.VLESS,
                server = "sub.example.com",
                port = 443,
                link = "vless://fake@sub.example.com:443",
                outbound = buildJsonObject {},
                group = "Subscription",
            )
            _uiState.update { state ->
                state.copy(
                    importCandidates = listOf(ImportCandidate(node, selected = true)),
                )
            }
        }
    }

    // ---- Internal ----

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

    private fun loadDemoNodes() {
        val demoNodes = listOf(
            createDemoNode("1", "Frankfurt-1", Protocol.VLESS, "de-1.example.com", 443, 42),
            createDemoNode("2", "Amsterdam-2", Protocol.TROJAN, "nl-2.example.com", 443, 68),
            createDemoNode("3", "Helsinki-3", Protocol.SHADOWSOCKS, "fi-3.example.com", 8388, 120),
            createDemoNode("4", "Tokyo-4", Protocol.VMESS, "jp-4.example.com", 443, 210),
            createDemoNode("5", "US-West-5", Protocol.HYSTERIA2, "us-5.example.com", 443, 480),
        )

        _uiState.update {
            it.copy(
                nodes = demoNodes,
                groups = demoNodes.map { n -> n.group }.distinct(),
            )
        }
    }

    private fun createDemoNode(
        id: String, name: String, protocol: Protocol,
        server: String, port: Int, latency: Int,
    ): Node = Node(
        id = id,
        name = name,
        protocol = protocol,
        server = server,
        port = port,
        link = "${protocol.name.lowercase()}://demo@$server:$port",
        outbound = buildJsonObject {},
        latencyMs = latency,
    )
}
