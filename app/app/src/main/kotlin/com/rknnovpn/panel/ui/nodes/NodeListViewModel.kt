package com.rknnovpn.panel.ui.nodes

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.rknnovpn.panel.`import`.ClipboardWatcher
import com.rknnovpn.panel.`import`.LinkParser
import com.rknnovpn.panel.i18n.UserMessageFormatter
import com.rknnovpn.panel.ipc.DaemonClient
import com.rknnovpn.panel.ipc.DaemonClientResult
import com.rknnovpn.panel.model.Node
import com.rknnovpn.panel.model.NodeSourceType
import com.rknnovpn.panel.model.ProfileConfig
import com.rknnovpn.panel.repository.ProfileRepository
import com.rknnovpn.panel.repository.SubscriptionImportPreview
import dagger.hilt.android.lifecycle.HiltViewModel
import java.net.URI
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

private const val TAG = "NodeListViewModel"

enum class NodeSortMode { NAME, LATENCY, THROUGHPUT, COUNTRY }
enum class ImportSheetTab { PASTE_URI, SCAN_QR, SUBSCRIPTION }

data class NodeListUiState(
    val groups: List<String> = emptyList(),
    val selectedGroup: String = "",
    val nodes: List<Node> = emptyList(),
    val subscriptions: List<SubscriptionUiSummary> = emptyList(),
    val activeNodeId: String? = null,
    val sortMode: NodeSortMode = NodeSortMode.NAME,
    val showImportSheet: Boolean = false,
    val importSheetTab: ImportSheetTab = ImportSheetTab.PASTE_URI,
    val importInitialText: String = "",
    /** Nodes parsed from import input, waiting for user selection. */
    val importCandidates: List<ImportCandidate> = emptyList(),
    val pendingSubscriptionPreview: SubscriptionImportPreview? = null,
    val isLoading: Boolean = false,
    val isTestingNodes: Boolean = false,
    /** Error message from the last operation, or null. */
    val errorMessage: String? = null,
    /** Informational status from the last operation, or null. */
    val statusMessage: String? = null,
)

data class SubscriptionUiSummary(
    val providerKey: String,
    val displayName: String,
    val activeNodeCount: Int,
    val staleNodeCount: Int,
    val parseFailures: Int,
)

data class ImportCandidate(
    val node: Node,
    val selected: Boolean = true,
    val selectable: Boolean = true,
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
            _uiState.update { it.copy(activeNodeId = nodeId, errorMessage = null, statusMessage = null) }
            val ok = profileRepository.setActiveNode(nodeId)
            if (!ok) {
                val err = profileRepository.error.value
                profileRepository.refresh()
                _uiState.update {
                    it.copy(
                        activeNodeId = previousNodeId,
                        errorMessage = err ?: messages.get(com.rknnovpn.panel.R.string.node_set_active_failed),
                        statusMessage = null,
                    )
                }
            } else {
                Log.d(TAG, "Active node set to $nodeId via profile.setActiveNode")
                profileRepository.error.value?.let { persistedWarning ->
                    _uiState.update { it.copy(errorMessage = persistedWarning, statusMessage = null) }
                } ?: profileRepository.notice.value?.let { notice ->
                    _uiState.update { it.copy(statusMessage = notice) }
                }
            }
        }
    }

    fun selectAuto() {
        viewModelScope.launch {
            val previousNodeId = _uiState.value.activeNodeId
            _uiState.update { it.copy(activeNodeId = null, errorMessage = null, statusMessage = null) }
            val ok = profileRepository.clearActiveNode()
            if (!ok) {
                val err = profileRepository.error.value
                profileRepository.refresh()
                _uiState.update {
                    it.copy(
                        activeNodeId = previousNodeId,
                        errorMessage = err ?: messages.get(com.rknnovpn.panel.R.string.node_set_active_failed),
                        statusMessage = null,
                    )
                }
            } else {
                Log.d(TAG, "Active node cleared; selector will use auto mode")
                profileRepository.error.value?.let { persistedWarning ->
                    _uiState.update { it.copy(errorMessage = persistedWarning, statusMessage = null) }
                } ?: profileRepository.notice.value?.let { notice ->
                    _uiState.update { it.copy(statusMessage = notice) }
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
            _uiState.update { it.copy(errorMessage = null, statusMessage = null) }
            val ok = profileRepository.removeNode(nodeId)
            if (!ok) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(
                        errorMessage = err ?: messages.get(com.rknnovpn.panel.R.string.node_delete_failed),
                        statusMessage = null,
                    )
                }
            }
            // UI updates via the profile observer
        }
    }

    fun updateNodeMetadata(nodeId: String, name: String, group: String, ownerPackage: String) {
        val cleanName = name.trim()
        val cleanGroup = group.trim().ifBlank { messages.defaultGroupName() }
        val cleanOwnerPackage = ownerPackage.trim()
        if (cleanName.isBlank()) {
            _uiState.update {
                it.copy(
                    errorMessage = messages.get(com.rknnovpn.panel.R.string.node_name_required),
                    statusMessage = null,
                )
            }
            return
        }

        viewModelScope.launch {
            _uiState.update { it.copy(errorMessage = null, statusMessage = null) }
            val current = _uiState.value.nodes.firstOrNull { it.id == nodeId }
            if (current == null) {
                _uiState.update {
                    it.copy(
                        errorMessage = messages.get(com.rknnovpn.panel.R.string.node_not_found),
                        statusMessage = null,
                    )
                }
                return@launch
            }

            val ok = profileRepository.updateNode(
                current.copy(
                    name = cleanName,
                    group = cleanGroup,
                    ownerPackage = cleanOwnerPackage,
                )
            )
            if (!ok) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(
                        errorMessage = err ?: messages.get(com.rknnovpn.panel.R.string.node_update_failed),
                        statusMessage = null,
                    )
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
            val ids = _uiState.value.nodes.filterNot { it.stale }.map { it.id }
            runNodeTests(ids)
        }
    }

    private suspend fun runNodeTests(nodeIds: List<String>) {
        if (nodeIds.isEmpty() || _uiState.value.isTestingNodes) return
        _uiState.update { it.copy(isTestingNodes = true, errorMessage = null, statusMessage = null) }
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
                                    throughputBps = test.throughputBps,
                                    testStatus = when {
                                        test.tcpError != null -> messages.get(
                                            com.rknnovpn.panel.R.string.node_test_status_tcp_error,
                                            messages.formatNodeTestIssue(test.tcpError),
                                        )
                                        test.urlError != null -> messages.get(
                                            com.rknnovpn.panel.R.string.node_test_status_url_error,
                                            messages.formatNodeTestIssue(test.urlError),
                                        )
                                        test.verdict == "unusable" ->
                                            messages.get(com.rknnovpn.panel.R.string.node_test_status_unusable)
                                        test.urlMs != null ->
                                            messages.get(com.rknnovpn.panel.R.string.node_test_status_ok)
                                        else ->
                                            messages.get(com.rknnovpn.panel.R.string.node_test_status_tcp_ok)
                                    },
                                )
                            },
                        )
                    }
                }
                else -> {
                    val msg = describeError(result)
                    Log.w(TAG, "Node test failed: $msg")
                    _uiState.update { it.copy(errorMessage = msg, statusMessage = null) }
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
                pendingSubscriptionPreview = null,
                errorMessage = null,
                statusMessage = null,
            )
        }
    }

    fun hideImportSheet() {
        _uiState.update {
            it.copy(
                showImportSheet = false,
                importInitialText = "",
                importCandidates = emptyList(),
                pendingSubscriptionPreview = null,
                errorMessage = null,
                statusMessage = null,
            )
        }
    }

    fun clearError() {
        _uiState.update { it.copy(errorMessage = null, statusMessage = null) }
    }

    fun showClipboardImport(preview: ClipboardWatcher.ImportPreview) {
        if (preview.isSubscription) {
            _uiState.update {
                it.copy(
                    showImportSheet = true,
                    importSheetTab = ImportSheetTab.SUBSCRIPTION,
                    importInitialText = preview.rawText.trim(),
                    importCandidates = emptyList(),
                    pendingSubscriptionPreview = null,
                    errorMessage = null,
                    statusMessage = null,
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
                pendingSubscriptionPreview = null,
                errorMessage = null,
                statusMessage = null,
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
                    pendingSubscriptionPreview = null,
                    errorMessage = messages.get(
                        com.rknnovpn.panel.R.string.node_no_valid_proxy_uris_detected
                    ),
                    statusMessage = null,
                )
            }
            return
        }

        val parsedNodes = detectedUris.mapNotNull(LinkParser::parse)
        if (parsedNodes.isEmpty()) {
            _uiState.update {
                it.copy(
                    importCandidates = emptyList(),
                    pendingSubscriptionPreview = null,
                    errorMessage = messages.get(
                        com.rknnovpn.panel.R.string.node_detected_uris_unparsed
                    ),
                    statusMessage = null,
                )
            }
            return
        }
        val candidates = parsedNodes.map { ImportCandidate(node = it, selected = true) }

        _uiState.update {
            it.copy(
                importCandidates = candidates,
                pendingSubscriptionPreview = null,
                errorMessage = null,
                statusMessage = null,
            )
        }
    }

    fun toggleImportCandidate(index: Int) {
        _uiState.update { state ->
            val updated = state.importCandidates.toMutableList()
            if (index in updated.indices && updated[index].selectable) {
                updated[index] = updated[index].copy(selected = !updated[index].selected)
            }
            state.copy(importCandidates = updated)
        }
    }

    fun importSelected() {
        _uiState.value.pendingSubscriptionPreview?.let { preview ->
            applySubscriptionPreview(preview)
            return
        }

        val selected = _uiState.value.importCandidates
            .filter { it.selected }
            .map { it.node }

        if (selected.isEmpty()) return

        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true, errorMessage = null, statusMessage = null) }

            // Build a single multi-line string of share links for the daemon.
            val links = selected.mapNotNull { it.link.ifBlank { null } }.joinToString("\n")

            if (links.isNotBlank()) {
                val imported = profileRepository.importNodes(links)
                if (imported.isEmpty()) {
                    val err = profileRepository.error.value
                    _uiState.update {
                        it.copy(
                            errorMessage = err ?: messages.get(
                                com.rknnovpn.panel.R.string.node_import_failed
                            ),
                            statusMessage = null,
                            isLoading = false,
                        )
                    }
                } else {
                    Log.d(TAG, "Imported ${imported.size} nodes via daemon")
                    // Auto-select the group of the first imported node so the user
                    // can see the results immediately.
                    val firstGroup = imported.firstOrNull()?.group
                    val persistedWarning = profileRepository.error.value
                    val notice = profileRepository.notice.value.takeIf { persistedWarning == null }
                    _uiState.update {
                        it.copy(
                            showImportSheet = persistedWarning != null,
                            importCandidates = if (persistedWarning != null) it.importCandidates else emptyList(),
                            pendingSubscriptionPreview = null,
                            isLoading = false,
                            errorMessage = persistedWarning,
                            statusMessage = notice,
                            selectedGroup = firstGroup ?: it.selectedGroup,
                        )
                    }
                }
            } else {
                _uiState.update {
                    it.copy(
                        errorMessage = messages.get(
                            com.rknnovpn.panel.R.string.node_no_valid_links_to_import
                        ),
                        statusMessage = null,
                        isLoading = false,
                    )
                }
            }
            // Node list updates via the profile observer
        }
    }

    /**
     * Fetch subscription URL and show a provider-scoped merge preview.
     */
    fun fetchSubscription(url: String) {
        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true, errorMessage = null, statusMessage = null) }

            val preview = profileRepository.previewSubscription(url)
            if (preview == null) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(
                        errorMessage = err ?: messages.get(
                            com.rknnovpn.panel.R.string.subscription_fetch_failed
                        ),
                        statusMessage = null,
                        isLoading = false,
                    )
                }
            } else {
                val candidates = preview.nodes.map { node ->
                    ImportCandidate(node = node, selected = true, selectable = false)
                }
                _uiState.update {
                    it.copy(
                        showImportSheet = true,
                        importSheetTab = ImportSheetTab.SUBSCRIPTION,
                        importInitialText = preview.url,
                        importCandidates = candidates,
                        pendingSubscriptionPreview = preview,
                        isLoading = false,
                        errorMessage = null,
                        statusMessage = messages.formatSubscriptionPreview(
                            preview.addedCount,
                            preview.updatedCount,
                            preview.removedCount,
                            preview.parseFailures,
                            preview.rejectedNodes,
                        ),
                    )
                }
            }
        }
    }

    private fun applySubscriptionPreview(preview: SubscriptionImportPreview) {
        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true, errorMessage = null, statusMessage = null) }
            val imported = profileRepository.applySubscriptionPreview(preview)
            if (imported.isEmpty() && preview.removedCount == 0) {
                val err = profileRepository.error.value
                _uiState.update {
                    it.copy(
                        errorMessage = err ?: messages.get(
                            com.rknnovpn.panel.R.string.subscription_fetch_failed
                        ),
                        statusMessage = null,
                        isLoading = false,
                    )
                }
                return@launch
            }

            val firstGroup = imported.firstOrNull()?.group
            val persistedWarning = profileRepository.error.value
            val notice = profileRepository.notice.value.takeIf { persistedWarning == null }
            _uiState.update {
                it.copy(
                    showImportSheet = persistedWarning != null,
                    importCandidates = if (persistedWarning != null) it.importCandidates else emptyList(),
                    pendingSubscriptionPreview = if (persistedWarning != null) preview else null,
                    isLoading = false,
                    errorMessage = persistedWarning,
                    statusMessage = notice,
                    selectedGroup = firstGroup ?: it.selectedGroup,
                )
            }
        }
    }

    // ---- Internal ----

    /**
     * Observe the profile repository for changes and project nodes into the UI state.
     */
    private fun observeProfile() {
        viewModelScope.launch {
            profileRepository.notice.collect { notice ->
                _uiState.update { it.copy(statusMessage = notice) }
            }
        }
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
                            subscriptions = subscriptionSummaries(config, nodes),
                            groups = groups,
                            selectedGroup = selectedGroup,
                            activeNodeId = config.activeNodeId?.takeIf { activeId ->
                                nodes.any { it.id == activeId && !it.stale }
                            },
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
            _uiState.update { it.copy(isLoading = true, errorMessage = null, statusMessage = null) }
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
        NodeSortMode.LATENCY -> nodes.sortedWith(
            compareBy<Node> { it.responseMs == null }
                .thenBy { it.responseMs ?: Int.MAX_VALUE }
                .thenBy { it.latencyMs ?: Int.MAX_VALUE },
        )
        NodeSortMode.THROUGHPUT -> nodes.sortedWith(
            compareByDescending<Node> { it.throughputBps ?: -1L }
                .thenBy { it.responseMs ?: Int.MAX_VALUE }
                .thenBy { it.latencyMs ?: Int.MAX_VALUE },
        )
        NodeSortMode.COUNTRY -> nodes.sortedBy { extractCountryFromName(it.name) }
    }

    private fun normalizeNode(node: Node): Node = if (messages.isDefaultGroupName(node.group)) {
        node.copy(group = messages.defaultGroupName())
    } else {
        node
    }

    private fun subscriptionSummaries(config: ProfileConfig, nodes: List<Node>): List<SubscriptionUiSummary> =
        config.subscriptions.map { subscription ->
            val providerNodes = nodes.filter {
                it.source.type == NodeSourceType.SUBSCRIPTION &&
                    it.source.providerKey == subscription.providerKey
            }
            SubscriptionUiSummary(
                providerKey = subscription.providerKey,
                displayName = subscription.name.ifBlank {
                    hostLabel(subscription.url).ifBlank {
                        subscription.providerKey.take(8).ifBlank { messages.get(com.rknnovpn.panel.R.string.subscription_provider_fallback) }
                    }
                },
                activeNodeCount = providerNodes.count { !it.stale },
                staleNodeCount = providerNodes.count { it.stale },
                parseFailures = subscription.parseFailures,
            )
        }.sortedBy { it.displayName.lowercase() }

    private fun hostLabel(url: String): String =
        runCatching { URI(url).host.orEmpty().removePrefix("www.") }.getOrDefault("")

    private fun extractCountryFromName(name: String): String {
        // Simple heuristic: first word before dash/space
        return name.split(Regex("[\\s-]")).firstOrNull()?.lowercase() ?: ""
    }
    private fun <T> describeError(result: DaemonClientResult<T>): String = when (result) {
        is DaemonClientResult.Ok -> messages.get(com.rknnovpn.panel.R.string.node_test_status_ok)
        else -> messages.formatDaemonFailure(result)
    }
}
