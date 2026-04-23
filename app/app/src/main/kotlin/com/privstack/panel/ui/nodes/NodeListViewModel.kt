package com.privstack.panel.ui.nodes

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.`import`.ClipboardWatcher
import com.privstack.panel.`import`.LinkParser
import com.privstack.panel.i18n.UserMessageFormatter
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.model.Node
import com.privstack.panel.repository.ProfileRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

private const val TAG = "NodeListViewModel"

enum class NodeSortMode { NAME, LATENCY, COUNTRY }
enum class ImportSheetTab { PASTE_URI, SCAN_QR, SUBSCRIPTION }

data class NodeListUiState(
    val groups: List<String> = emptyList(),
    val selectedGroup: String = "",
    val nodes: List<Node> = emptyList(),
    val activeNodeId: String? = null,
    val sortMode: NodeSortMode = NodeSortMode.NAME,
    val showImportSheet: Boolean = false,
    val importSheetTab: ImportSheetTab = ImportSheetTab.PASTE_URI,
    val importInitialText: String = "",
    /** Nodes parsed from import input, waiting for user selection. */
    val importCandidates: List<ImportCandidate> = emptyList(),
    val isLoading: Boolean = false,
    val isTestingNodes: Boolean = false,
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
    private val messages: UserMessageFormatter,
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
            val previousNodeId = _uiState.value.activeNodeId
            _uiState.update { it.copy(activeNodeId = nodeId, errorMessage = null) }
            val ok = profileRepository.setActiveNode(nodeId)
            if (!ok) {
                val err = profileRepository.error.value
                profileRepository.refresh()
                _uiState.update {
                    it.copy(
                        activeNodeId = previousNodeId,
                        errorMessage = err ?: messages.get(com.privstack.panel.R.string.node_set_active_failed),
                    )
                }
            } else {
                Log.d(TAG, "Active node set to $nodeId via panel-set")
                profileRepository.error.value?.let { persistedWarning ->
                    _uiState.update { it.copy(errorMessage = persistedWarning) }
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
                    it.copy(errorMessage = err ?: messages.get(com.privstack.panel.R.string.node_delete_failed))
                }
            }
            // UI updates via the profile observer
        }
    }

    fun updateNodeMetadata(nodeId: String, name: String, group: String) {
        val cleanName = name.trim()
        val cleanGroup = group.trim().ifBlank { messages.defaultGroupName() }
        if (cleanName.isBlank()) {
            _uiState.update {
                it.copy(errorMessage = messages.get(com.privstack.panel.R.string.node_name_required))
            }
            return
        }

        viewModelScope.launch {
            _uiState.update { it.copy(errorMessage = null) }
            val current = _uiState.value.nodes.firstOrNull { it.id == nodeId }
            if (current == null) {
                _uiState.update {
                    it.copy(errorMessage = messages.get(com.privstack.panel.R.string.node_not_found))
                }
                return@launch
            }

            val ok = profileRepository.updateNode(
                current.copy(
                    name = cleanName,
                    group = cleanGroup,
                )
            )
            if (!ok) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(errorMessage = err ?: messages.get(com.privstack.panel.R.string.node_update_failed))
                }
            } else {
                _uiState.update { state ->
                    state.copy(selectedGroup = cleanGroup)
                }
            }
        }
    }

    fun testLatency(nodeId: String) {
        viewModelScope.launch {
            runNodeTests(listOf(nodeId))
        }
    }

    fun testAllNodes() {
        viewModelScope.launch {
            val ids = _uiState.value.nodes.map { it.id }
            runNodeTests(ids)
        }
    }

    private suspend fun runNodeTests(nodeIds: List<String>) {
        if (nodeIds.isEmpty() || _uiState.value.isTestingNodes) return
        _uiState.update { it.copy(isTestingNodes = true, errorMessage = null) }
        try {
            when (val result = daemonClient.nodeTest(nodeIds)) {
                is DaemonClientResult.Ok -> {
                    val byId = result.data.results.associateBy { it.id }
                    _uiState.update { state ->
                        state.copy(
                            nodes = state.nodes.map { node ->
                                val test = byId[node.id] ?: return@map node
                                node.copy(
                                    latencyMs = test.tcpMs ?: -1,
                                    responseMs = test.urlMs,
                                    testStatus = when {
                                        test.tcpError != null -> messages.get(
                                            com.privstack.panel.R.string.node_test_status_tcp_error,
                                            messages.formatNodeTestIssue(test.tcpError),
                                        )
                                        test.urlError != null -> messages.get(
                                            com.privstack.panel.R.string.node_test_status_url_error,
                                            messages.formatNodeTestIssue(test.urlError),
                                        )
                                        test.verdict == "unusable" ->
                                            messages.get(com.privstack.panel.R.string.node_test_status_unusable)
                                        test.urlMs != null ->
                                            messages.get(com.privstack.panel.R.string.node_test_status_ok)
                                        else ->
                                            messages.get(com.privstack.panel.R.string.node_test_status_tcp_ok)
                                    },
                                )
                            },
                        )
                    }
                }
                else -> {
                    val msg = describeError(result)
                    Log.w(TAG, "Node test failed: $msg")
                    _uiState.update { it.copy(errorMessage = msg) }
                }
            }
        } finally {
            _uiState.update { it.copy(isTestingNodes = false) }
        }
    }

    fun showImportSheet() {
        _uiState.update {
            it.copy(
                showImportSheet = true,
                importSheetTab = ImportSheetTab.PASTE_URI,
                importInitialText = "",
                importCandidates = emptyList(),
                errorMessage = null,
            )
        }
    }

    fun hideImportSheet() {
        _uiState.update {
            it.copy(
                showImportSheet = false,
                importInitialText = "",
                importCandidates = emptyList(),
                errorMessage = null,
            )
        }
    }

    fun clearError() {
        _uiState.update { it.copy(errorMessage = null) }
    }

    fun showClipboardImport(preview: ClipboardWatcher.ImportPreview) {
        if (preview.isSubscription) {
            _uiState.update {
                it.copy(
                    showImportSheet = true,
                    importSheetTab = ImportSheetTab.SUBSCRIPTION,
                    importInitialText = preview.rawText.trim(),
                    importCandidates = emptyList(),
                    errorMessage = null,
                )
            }
            return
        }

        val candidates = preview.parsedNodes.map { ImportCandidate(node = it, selected = true) }
        if (candidates.isEmpty()) return

        _uiState.update {
            it.copy(
                showImportSheet = true,
                importSheetTab = ImportSheetTab.PASTE_URI,
                importInitialText = preview.rawText,
                importCandidates = candidates,
                errorMessage = null,
            )
        }
    }

    /**
     * Parse URIs from pasted text and populate import candidates.
     *
     * Uses [LinkParser] for full protocol-aware parsing so that the preview
     * shows correct names, servers, ports, and the outbound JSON is populated.
     * Malformed or unsupported URIs are skipped with an error instead of being
     * shown as importable, because persistence goes through the same parser.
     */
    fun detectUris(text: String) {
        val detectedUris = LinkParser.detectUris(text)
        if (detectedUris.isEmpty()) {
            _uiState.update {
                it.copy(
                    importCandidates = emptyList(),
                    errorMessage = messages.get(
                        com.privstack.panel.R.string.node_no_valid_proxy_uris_detected
                    ),
                )
            }
            return
        }

        val parsedNodes = detectedUris.mapNotNull(LinkParser::parse)
        if (parsedNodes.isEmpty()) {
            _uiState.update {
                it.copy(
                    importCandidates = emptyList(),
                    errorMessage = messages.get(
                        com.privstack.panel.R.string.node_detected_uris_unparsed
                    ),
                )
            }
            return
        }
        val candidates = parsedNodes.map { ImportCandidate(node = it, selected = true) }

        _uiState.update { it.copy(importCandidates = candidates, errorMessage = null) }
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

            // Build a single multi-line string of share links for the daemon.
            val links = selected.mapNotNull { it.link.ifBlank { null } }.joinToString("\n")

            if (links.isNotBlank()) {
                val imported = profileRepository.importNodes(links)
                if (imported.isEmpty()) {
                    val err = profileRepository.error.value
                    _uiState.update {
                        it.copy(
                            errorMessage = err ?: messages.get(
                                com.privstack.panel.R.string.node_import_failed
                            ),
                            isLoading = false,
                        )
                    }
                } else {
                    Log.d(TAG, "Imported ${imported.size} nodes via daemon")
                    // Auto-select the group of the first imported node so the user
                    // can see the results immediately.
                    val firstGroup = imported.firstOrNull()?.group
                    val persistedWarning = profileRepository.error.value
                    _uiState.update {
                        it.copy(
                            showImportSheet = persistedWarning != null,
                            importCandidates = if (persistedWarning != null) it.importCandidates else emptyList(),
                            isLoading = false,
                            errorMessage = persistedWarning,
                            selectedGroup = firstGroup ?: it.selectedGroup,
                        )
                    }
                }
            } else {
                _uiState.update {
                    it.copy(
                        errorMessage = messages.get(
                            com.privstack.panel.R.string.node_no_valid_links_to_import
                        ),
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

            // The repository asks the daemon to fetch the URL, then merges the
            // parsed nodes into the stored profile locally.
            val imported = profileRepository.importNodes(url)
            if (imported.isEmpty()) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(
                        errorMessage = err ?: messages.get(
                            com.privstack.panel.R.string.subscription_fetch_failed
                        ),
                        isLoading = false,
                    )
                }
            } else {
                val firstGroup = imported.firstOrNull()?.group
                val persistedWarning = profileRepository.error.value
                _uiState.update {
                    it.copy(
                        showImportSheet = persistedWarning != null,
                        importCandidates = if (persistedWarning != null) it.importCandidates else emptyList(),
                        isLoading = false,
                        errorMessage = persistedWarning,
                        selectedGroup = firstGroup ?: it.selectedGroup,
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
                    val nodes = config.nodes.map(::normalizeNode)
                    val groups = nodes.map { it.group }.distinct()
                        .ifEmpty { listOf(messages.defaultGroupName()) }
                    _uiState.update { state ->
                        val selectedGroup = state.selectedGroup.takeIf { it in groups } ?: groups.first()
                        state.copy(
                            nodes = sortNodes(nodes, state.sortMode),
                            groups = groups,
                            selectedGroup = selectedGroup,
                            activeNodeId = config.activeNodeId,
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

    private fun normalizeNode(node: Node): Node = if (messages.isDefaultGroupName(node.group)) {
        node.copy(group = messages.defaultGroupName())
    } else {
        node
    }

    private fun extractCountryFromName(name: String): String {
        // Simple heuristic: first word before dash/space
        return name.split(Regex("[\\s-]")).firstOrNull()?.lowercase() ?: ""
    }
    private fun <T> describeError(result: DaemonClientResult<T>): String = when (result) {
        is DaemonClientResult.Ok -> messages.get(com.privstack.panel.R.string.node_test_status_ok)
        else -> messages.formatDaemonFailure(result)
    }
}
