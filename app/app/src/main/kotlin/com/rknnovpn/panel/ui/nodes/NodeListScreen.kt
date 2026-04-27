package com.rknnovpn.panel.ui.nodes

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Apps
import androidx.compose.material.icons.filled.ContentCopy
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.Edit
import androidx.compose.material.icons.filled.Speed
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FloatingActionButton
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.ScrollableTabRow
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Surface
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.DpOffset
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.rknnovpn.panel.R
import com.rknnovpn.panel.`import`.ClipboardWatcher
import com.rknnovpn.panel.model.Node
import com.rknnovpn.panel.model.NodeSourceType
import com.rknnovpn.panel.ui.common.AppPackagePickerDialog

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun NodeListScreen(
    viewModel: NodeListViewModel = hiltViewModel(),
) {
    val state by viewModel.uiState.collectAsStateWithLifecycle()
    val context = LocalContext.current
    val lifecycleOwner = LocalLifecycleOwner.current
    var nodeToDelete by remember { mutableStateOf<Node?>(null) }
    var nodeToEdit by remember { mutableStateOf<Node?>(null) }

    fun checkClipboardForImport() {
        ClipboardWatcher.check(context)?.let(viewModel::showClipboardImport)
    }

    LaunchedEffect(context) {
        checkClipboardForImport()
    }

    DisposableEffect(lifecycleOwner, context) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_RESUME) {
                checkClipboardForImport()
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose {
            lifecycleOwner.lifecycle.removeObserver(observer)
        }
    }

    Scaffold(
        floatingActionButton = {
            FloatingActionButton(onClick = viewModel::showImportSheet) {
                Icon(Icons.Filled.Add, contentDescription = stringResource(R.string.add_node))
            }
        },
    ) { innerPadding ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(innerPadding),
        ) {
            if (state.groups.size > 1) {
                ScrollableTabRow(
                    selectedTabIndex = state.groups.indexOf(state.selectedGroup).coerceAtLeast(0),
                    edgePadding = 16.dp,
                ) {
                    state.groups.forEach { group ->
                        Tab(
                            selected = group == state.selectedGroup,
                            onClick = { viewModel.selectGroup(group) },
                            text = { Text(group) },
                        )
                    }
                }
            }

            val bannerMessage = state.errorMessage ?: state.statusMessage
            if (!bannerMessage.isNullOrBlank()) {
                StatusBanner(
                    text = bannerMessage,
                    isError = state.errorMessage != null,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                )
            }

            if (state.subscriptions.isNotEmpty()) {
                SubscriptionSummary(
                    subscriptions = state.subscriptions,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                )
            }

            SortRow(
                currentSort = state.sortMode,
                onSortChange = viewModel::setSortMode,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
            )
            val filteredNodes = state.nodes.filter { it.group == state.selectedGroup }
            val selectableNodes = state.nodes.filterNot { it.stale }
            val selectableFilteredNodes = filteredNodes.filterNot { it.stale }
            SelectionModeRow(
                isAuto = state.activeNodeId.isNullOrBlank(),
                hasNodes = selectableNodes.isNotEmpty(),
                onAutoSelect = viewModel::selectAuto,
                onManualSelect = {
                    (selectableFilteredNodes.firstOrNull() ?: selectableNodes.firstOrNull())
                        ?.let { viewModel.selectNode(it.id) }
                },
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            Row(
                horizontalArrangement = Arrangement.End,
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp),
            ) {
                TextButton(
                    onClick = viewModel::testAllNodes,
                    enabled = selectableNodes.isNotEmpty() && !state.isTestingNodes,
                ) {
                    Icon(Icons.Filled.Speed, contentDescription = null)
                    Spacer(modifier = Modifier.width(8.dp))
                    Text(
                        text = stringResource(
                            if (state.isTestingNodes) R.string.testing_nodes else R.string.test_all_nodes
                        ),
                    )
                }
            }

            if (filteredNodes.isEmpty()) {
                Box(
                    contentAlignment = Alignment.Center,
                    modifier = Modifier
                        .fillMaxSize()
                        .padding(32.dp),
                ) {
                    Text(
                        text = stringResource(R.string.no_nodes),
                        style = MaterialTheme.typography.bodyLarge,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            } else {
                LazyColumn(
                    contentPadding = PaddingValues(horizontal = 16.dp, vertical = 8.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    items(filteredNodes, key = { it.id }) { node ->
                        NodeCard(
                            node = node,
                            isActive = node.id == state.activeNodeId,
                            onSelect = {
                                if (!node.stale) {
                                    viewModel.selectNode(node.id)
                                }
                            },
                            onEdit = { nodeToEdit = node },
                            onTestLatency = { viewModel.testLatency(node.id) },
                            onDelete = { nodeToDelete = node },
                        )
                    }
                }
            }
        }
    }

    if (state.showImportSheet) {
        ImportSheet(
            initialTab = state.importSheetTab,
            initialText = state.importInitialText,
            candidates = state.importCandidates,
            canApplyEmptySubscriptionPreview = state.pendingSubscriptionPreview != null &&
                state.importCandidates.isEmpty(),
            isLoading = state.isLoading,
            errorMessage = state.errorMessage,
            statusMessage = state.statusMessage,
            onDetectUris = viewModel::detectUris,
            onToggleCandidate = viewModel::toggleImportCandidate,
            onImportSelected = viewModel::importSelected,
            onFetchSubscription = viewModel::fetchSubscription,
            onDismiss = viewModel::hideImportSheet,
        )
    }

    nodeToDelete?.let { node ->
        AlertDialog(
            onDismissRequest = { nodeToDelete = null },
            title = { Text(stringResource(R.string.delete)) },
            text = { Text(stringResource(R.string.delete_node_confirm, node.name)) },
            confirmButton = {
                TextButton(onClick = {
                    viewModel.deleteNode(node.id)
                    nodeToDelete = null
                }) {
                    Text(stringResource(R.string.confirm))
                }
            },
            dismissButton = {
                TextButton(onClick = { nodeToDelete = null }) {
                    Text(stringResource(R.string.cancel))
                }
            },
        )
    }

    nodeToEdit?.let { node ->
        EditNodeDialog(
            node = node,
            onDismiss = { nodeToEdit = null },
            onSave = { name, group, ownerPackage ->
                viewModel.updateNodeMetadata(node.id, name, group, ownerPackage)
                nodeToEdit = null
            },
        )
    }
}

@Composable
private fun SubscriptionSummary(
    subscriptions: List<SubscriptionUiSummary>,
    modifier: Modifier = Modifier,
) {
    Surface(
        color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.55f),
        contentColor = MaterialTheme.colorScheme.onSurfaceVariant,
        shape = MaterialTheme.shapes.small,
        modifier = modifier.fillMaxWidth(),
    ) {
        Column(
            verticalArrangement = Arrangement.spacedBy(6.dp),
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 10.dp),
        ) {
            Text(
                text = stringResource(R.string.subscriptions_summary_title),
                style = MaterialTheme.typography.labelLarge,
                color = MaterialTheme.colorScheme.onSurface,
            )
            subscriptions.forEach { subscription ->
                Column {
                    Text(
                        text = stringResource(
                            R.string.subscription_summary_line,
                            subscription.displayName,
                            subscription.activeNodeCount,
                            subscription.staleNodeCount,
                        ),
                        style = MaterialTheme.typography.bodySmall,
                    )
                    if (subscription.parseFailures > 0) {
                        Text(
                            text = stringResource(
                                R.string.subscription_summary_parse_errors,
                                subscription.parseFailures,
                            ),
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.error,
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun StatusBanner(
    text: String,
    isError: Boolean,
    modifier: Modifier = Modifier,
) {
    val container = if (isError) {
        MaterialTheme.colorScheme.errorContainer
    } else {
        MaterialTheme.colorScheme.secondaryContainer
    }
    val content = if (isError) {
        MaterialTheme.colorScheme.onErrorContainer
    } else {
        MaterialTheme.colorScheme.onSecondaryContainer
    }
    Surface(
        color = container,
        contentColor = content,
        shape = MaterialTheme.shapes.small,
        modifier = modifier.fillMaxWidth(),
    ) {
        Text(
            text = text,
            style = MaterialTheme.typography.bodyMedium,
            modifier = Modifier.padding(horizontal = 12.dp, vertical = 10.dp),
        )
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun SelectionModeRow(
    isAuto: Boolean,
    hasNodes: Boolean,
    onAutoSelect: () -> Unit,
    onManualSelect: () -> Unit,
    modifier: Modifier = Modifier,
) {
    SingleChoiceSegmentedButtonRow(modifier = modifier.fillMaxWidth()) {
        val options = listOf(
            true to stringResource(R.string.node_selector_auto),
            false to stringResource(R.string.node_selector_manual),
        )
        options.forEachIndexed { index, (autoMode, label) ->
            SegmentedButton(
                selected = isAuto == autoMode,
                enabled = hasNodes,
                onClick = {
                    if (autoMode) {
                        onAutoSelect()
                    } else {
                        onManualSelect()
                    }
                },
                shape = SegmentedButtonDefaults.itemShape(
                    index = index,
                    count = options.size,
                ),
            ) {
                Text(label)
            }
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun SortRow(
    currentSort: NodeSortMode,
    onSortChange: (NodeSortMode) -> Unit,
    modifier: Modifier = Modifier,
) {
    SingleChoiceSegmentedButtonRow(modifier = modifier.fillMaxWidth()) {
        val options = listOf(
            NodeSortMode.NAME to stringResource(R.string.sort_by_name),
            NodeSortMode.LATENCY to stringResource(R.string.sort_by_latency),
            NodeSortMode.THROUGHPUT to stringResource(R.string.sort_by_throughput),
            NodeSortMode.COUNTRY to stringResource(R.string.sort_by_country),
        )
        options.forEachIndexed { index, (mode, label) ->
            SegmentedButton(
                selected = currentSort == mode,
                onClick = { onSortChange(mode) },
                shape = SegmentedButtonDefaults.itemShape(
                    index = index,
                    count = options.size,
                ),
            ) {
                Text(label)
            }
        }
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun NodeCard(
    node: Node,
    isActive: Boolean,
    onSelect: () -> Unit,
    onEdit: () -> Unit,
    onTestLatency: () -> Unit,
    onDelete: () -> Unit,
) {
    var showContextMenu by remember { mutableStateOf(false) }
    val clipboardManager = LocalClipboardManager.current

    val borderColor by animateColorAsState(
        targetValue = if (isActive) MaterialTheme.colorScheme.primary
        else MaterialTheme.colorScheme.outlineVariant,
        animationSpec = tween(300),
        label = "node_border",
    )

    Card(
        modifier = Modifier
            .fillMaxWidth()
            .combinedClickable(
                onClick = onSelect,
                onLongClick = { showContextMenu = true },
            ),
        border = CardDefaults.outlinedCardBorder().let {
            androidx.compose.foundation.BorderStroke(
                width = if (isActive) 2.dp else 1.dp,
                color = borderColor,
            )
        },
        colors = CardDefaults.cardColors(
            containerColor = if (isActive)
                MaterialTheme.colorScheme.primaryContainer.copy(alpha = 0.3f)
            else MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        ),
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.padding(16.dp),
        ) {
            // Country flag
            Text(
                text = countryFlagForNode(node.name),
                style = MaterialTheme.typography.headlineMedium,
            )

            Spacer(modifier = Modifier.width(12.dp))

            val okStatus = stringResource(R.string.node_test_status_ok)
            val tcpOkStatus = stringResource(R.string.node_test_status_tcp_ok)
            val hasFailedDataPlane = node.testStatus != null &&
                node.testStatus != okStatus &&
                node.testStatus != tcpOkStatus

            // Name + protocol + server
            Column(modifier = Modifier.weight(1f)) {
                val testSummary = listOfNotNull(
                    node.latencyMs?.takeIf { it >= 0 }?.let { stringResource(R.string.node_test_tcp_ms, it) },
                    node.responseMs?.let { stringResource(R.string.node_test_url_ms, it) },
                    node.throughputBps?.takeIf { it > 0 }?.let {
                        stringResource(R.string.node_test_speed, formatBytes(it))
                    },
                    node.testStatus?.takeIf { it != okStatus && it != tcpOkStatus },
                ).joinToString(" | ")
                Text(
                    text = node.name,
                    style = MaterialTheme.typography.bodyLarge,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(
                    text = "${node.protocol.name} | ${node.server}:${node.port}",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                val sourceText = node.sourceLabel()
                if (sourceText.isNotBlank()) {
                    Text(
                        text = sourceText,
                        style = MaterialTheme.typography.labelSmall,
                        color = if (node.stale) {
                            MaterialTheme.colorScheme.error
                        } else {
                            MaterialTheme.colorScheme.onSurfaceVariant
                        },
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                if (node.responseMs != null || node.testStatus != null) {
                    Text(
                        text = testSummary,
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }

            Spacer(modifier = Modifier.width(8.dp))

            // Latency chip
            val chipMs = when {
                node.responseMs != null -> node.responseMs
                hasFailedDataPlane -> -1
                else -> node.latencyMs
            }
            chipMs?.let { ms ->
                LatencyChip(ms)
            }
        }

        // Context menu
        Box {
            DropdownMenu(
                expanded = showContextMenu,
                onDismissRequest = { showContextMenu = false },
                offset = DpOffset(16.dp, 0.dp),
            ) {
                DropdownMenuItem(
                    text = { Text(stringResource(R.string.edit)) },
                    leadingIcon = { Icon(Icons.Filled.Edit, contentDescription = null) },
                    onClick = {
                        showContextMenu = false
                        onEdit()
                    },
                )
                DropdownMenuItem(
                    text = { Text(stringResource(R.string.copy_link)) },
                    leadingIcon = { Icon(Icons.Filled.ContentCopy, contentDescription = null) },
                    onClick = {
                        showContextMenu = false
                        clipboardManager.setText(AnnotatedString(node.link))
                    },
                )
                DropdownMenuItem(
                    text = { Text(stringResource(R.string.test_latency)) },
                    leadingIcon = { Icon(Icons.Filled.Speed, contentDescription = null) },
                    enabled = !node.stale,
                    onClick = {
                        showContextMenu = false
                        onTestLatency()
                    },
                )
                DropdownMenuItem(
                    text = {
                        Text(
                            stringResource(R.string.delete),
                            color = MaterialTheme.colorScheme.error,
                        )
                    },
                    leadingIcon = {
                        Icon(
                            Icons.Filled.Delete,
                            contentDescription = null,
                            tint = MaterialTheme.colorScheme.error,
                        )
                    },
                    onClick = {
                        showContextMenu = false
                        onDelete()
                    },
                )
            }
        }
    }
}

@Composable
private fun Node.sourceLabel(): String = when {
    stale -> stringResource(R.string.node_source_subscription_stale)
    source.type == NodeSourceType.SUBSCRIPTION -> stringResource(R.string.node_source_subscription)
    else -> ""
}

@Composable
private fun EditNodeDialog(
    node: Node,
    onDismiss: () -> Unit,
    onSave: (String, String, String) -> Unit,
) {
    var name by remember(node.id) { mutableStateOf(node.name) }
    var group by remember(node.id) { mutableStateOf(node.group) }
    var ownerPackage by remember(node.id) { mutableStateOf(node.ownerPackage) }
    var showOwnerPicker by remember(node.id) { mutableStateOf(false) }

    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(stringResource(R.string.edit_node_title)) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
                OutlinedTextField(
                    value = name,
                    onValueChange = { name = it },
                    label = { Text(stringResource(R.string.node_name_label)) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                OutlinedTextField(
                    value = group,
                    onValueChange = { group = it },
                    label = { Text(stringResource(R.string.node_group_label)) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                if (node.isLoopbackNode()) {
                    OutlinedTextField(
                        value = ownerPackage,
                        onValueChange = { ownerPackage = it },
                        label = { Text(stringResource(R.string.node_owner_package_label)) },
                        placeholder = { Text("com.example.proxy") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    TextButton(onClick = { showOwnerPicker = true }) {
                        Icon(Icons.Filled.Apps, contentDescription = null)
                        Spacer(modifier = Modifier.width(8.dp))
                        Text(stringResource(R.string.choose_app))
                    }
                }
            }
        },
        confirmButton = {
            TextButton(
                onClick = { onSave(name, group, ownerPackage) },
                enabled = name.isNotBlank(),
            ) {
                Text(stringResource(R.string.save))
            }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) {
                Text(stringResource(R.string.cancel))
            }
        },
    )
    if (showOwnerPicker) {
        AppPackagePickerDialog(
            title = stringResource(R.string.choose_app),
            onDismiss = { showOwnerPicker = false },
            onSelect = { ownerPackage = it },
        )
    }
}

@Composable
private fun LatencyChip(ms: Int) {
    val color = when {
        ms < 0 -> MaterialTheme.colorScheme.outline
        ms < 200 -> MaterialTheme.colorScheme.primary // green/teal
        ms < 500 -> MaterialTheme.colorScheme.tertiary // yellow/amber
        else -> MaterialTheme.colorScheme.error // red
    }
    val text = if (ms < 0) {
        stringResource(R.string.node_latency_error)
    } else {
        stringResource(R.string.ms_format, ms)
    }

    Card(
        colors = CardDefaults.cardColors(containerColor = color.copy(alpha = 0.15f)),
    ) {
        Text(
            text = text,
            style = MaterialTheme.typography.labelSmall,
            color = color,
            fontWeight = FontWeight.Bold,
            modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp),
        )
    }
}

/**
 * Derive a country flag emoji from the node name heuristic.
 */
private fun countryFlagForNode(name: String): String {
    val lower = name.lowercase()
    return when {
        lower.startsWith("frankfurt") || lower.startsWith("berlin") ||
            lower.contains("-de") || lower.startsWith("de-") -> "\uD83C\uDDE9\uD83C\uDDEA"
        lower.startsWith("amsterdam") || lower.contains("-nl") ||
            lower.startsWith("nl-") -> "\uD83C\uDDF3\uD83C\uDDF1"
        lower.startsWith("helsinki") || lower.contains("-fi") ||
            lower.startsWith("fi-") -> "\uD83C\uDDEB\uD83C\uDDEE"
        lower.startsWith("tokyo") || lower.contains("-jp") ||
            lower.startsWith("jp-") -> "\uD83C\uDDEF\uD83C\uDDF5"
        lower.startsWith("us-") || lower.contains("-us") ||
            lower.startsWith("new york") || lower.startsWith("la-") -> "\uD83C\uDDFA\uD83C\uDDF8"
        lower.startsWith("london") || lower.contains("-uk") ||
            lower.contains("-gb") -> "\uD83C\uDDEC\uD83C\uDDE7"
        lower.startsWith("paris") || lower.contains("-fr") ||
            lower.startsWith("fr-") -> "\uD83C\uDDEB\uD83C\uDDF7"
        lower.startsWith("moscow") || lower.startsWith("ru-") ||
            lower.contains("-ru") -> "\uD83C\uDDF7\uD83C\uDDFA"
        lower.startsWith("singapore") || lower.contains("-sg") ||
            lower.startsWith("sg-") -> "\uD83C\uDDF8\uD83C\uDDEC"
        else -> "\uD83C\uDF10" // globe
    }
}

private fun Node.isLoopbackNode(): Boolean {
    val host = server.trim().removePrefix("[").removeSuffix("]").lowercase()
    return host == "localhost" ||
        host == "ip6-localhost" ||
        host == "127.0.0.1" ||
        host == "::1" ||
        host == "0.0.0.0" ||
        host == "::"
}

private fun formatBytes(bytes: Long): String {
    if (bytes <= 0) return "0 B"
    val units = arrayOf("B", "KB", "MB", "GB", "TB")
    var value = bytes.toDouble()
    var idx = 0
    while (value >= 1024 && idx < units.lastIndex) {
        value /= 1024
        idx++
    }
    return if (value == value.toLong().toDouble()) "${value.toLong()} ${units[idx]}"
    else "%.1f ${units[idx]}".format(value)
}
