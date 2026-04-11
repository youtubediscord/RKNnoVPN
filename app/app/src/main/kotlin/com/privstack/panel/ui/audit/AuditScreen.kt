package com.privstack.panel.ui.audit

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.PlayArrow
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ElevatedCard
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ScrollableTabRow
import androidx.compose.material3.Surface
import androidx.compose.material3.Tab
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import kotlinx.coroutines.launch

// ───────────────────────────────────────────────────────────────────── //
//  Color palette
// ───────────────────────────────────────────────────────────────────── //

private val GreenPass = Color(0xFF4CAF50)
private val YellowWarn = Color(0xFFFFC107)
private val RedFail = Color(0xFFE53935)
private val GreenBg = Color(0xFFE8F5E9)
private val YellowBg = Color(0xFFFFF8E1)
private val RedBg = Color(0xFFFFEBEE)

private fun severityColor(severity: Severity): Color = when (severity) {
    Severity.CRITICAL -> RedFail
    Severity.HIGH -> Color(0xFFFF7043)
    Severity.MEDIUM -> YellowWarn
    Severity.LOW -> Color(0xFF42A5F5)
    Severity.PASS -> GreenPass
}

private fun riskColor(risk: RiskLevel): Color = when (risk) {
    RiskLevel.GREEN -> GreenPass
    RiskLevel.YELLOW -> YellowWarn
    RiskLevel.RED -> RedFail
}

private fun riskBgColor(risk: RiskLevel): Color = when (risk) {
    RiskLevel.GREEN -> GreenBg
    RiskLevel.YELLOW -> YellowBg
    RiskLevel.RED -> RedBg
}

// ───────────────────────────────────────────────────────────────────── //
//  Root screen: two-tab pager (Audit | Advisor)
// ───────────────────────────────────────────────────────────────────── //

@OptIn(androidx.compose.foundation.ExperimentalFoundationApi::class)
@Composable
fun AuditScreen(
    viewModel: AuditViewModel = hiltViewModel(),
) {
    val tabs = listOf("Audit", "Advisor")
    val pagerState = rememberPagerState(pageCount = { tabs.size })
    val scope = rememberCoroutineScope()

    Column(modifier = Modifier.fillMaxSize()) {
        ScrollableTabRow(
            selectedTabIndex = pagerState.currentPage,
            edgePadding = 16.dp,
        ) {
            tabs.forEachIndexed { index, title ->
                Tab(
                    selected = pagerState.currentPage == index,
                    onClick = { scope.launch { pagerState.animateScrollToPage(index) } },
                    text = { Text(title) },
                )
            }
        }

        HorizontalPager(
            state = pagerState,
            modifier = Modifier.fillMaxSize(),
        ) { page ->
            when (page) {
                0 -> AuditTab(viewModel)
                1 -> AdvisorTab(viewModel)
            }
        }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Audit tab
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun AuditTab(viewModel: AuditViewModel) {
    val state by viewModel.audit.collectAsStateWithLifecycle()

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item { Spacer(modifier = Modifier.height(8.dp)) }

        // Risk indicator card
        item {
            RiskCard(
                state = state,
                onRunAudit = { viewModel.runAudit() },
            )
        }

        // Error banner
        if (state.errorMessage != null) {
            item {
                Card(
                    colors = CardDefaults.cardColors(containerColor = RedBg),
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text(
                        text = state.errorMessage ?: "",
                        color = RedFail,
                        modifier = Modifier.padding(16.dp),
                        style = MaterialTheme.typography.bodyMedium,
                    )
                }
            }
        }

        // Findings grouped by category
        state.findingsByCategory.forEach { (category, findings) ->
            item {
                Text(
                    text = category,
                    style = MaterialTheme.typography.titleMedium,
                    fontWeight = FontWeight.SemiBold,
                    modifier = Modifier.padding(top = 8.dp),
                )
            }
            items(findings, key = { it.checkId.name }) { finding ->
                FindingCard(finding = finding)
            }
        }

        item { Spacer(modifier = Modifier.height(16.dp)) }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Risk overview card
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun RiskCard(
    state: AuditUiState,
    onRunAudit: () -> Unit,
) {
    val bgColor by animateColorAsState(
        targetValue = if (state.hasRun) riskBgColor(state.riskLevel) else MaterialTheme.colorScheme.surfaceVariant,
        label = "risk_bg",
    )

    ElevatedCard(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.elevatedCardColors(containerColor = bgColor),
    ) {
        Column(
            modifier = Modifier.padding(20.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            if (state.isRunning) {
                LoadingIndicator()
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = "Running audit checks...",
                    style = MaterialTheme.typography.bodyLarge,
                )
            } else if (state.hasRun) {
                Icon(
                    imageVector = when (state.riskLevel) {
                        RiskLevel.GREEN -> Icons.Filled.CheckCircle
                        RiskLevel.YELLOW -> Icons.Filled.Warning
                        RiskLevel.RED -> Icons.Filled.Error
                    },
                    contentDescription = state.riskLevel.label,
                    tint = riskColor(state.riskLevel),
                    modifier = Modifier.size(48.dp),
                )
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = state.riskLevel.label,
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.Bold,
                    color = riskColor(state.riskLevel),
                )
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = "${state.passedCount} passed / ${state.failedCount} failed of ${state.findings.size} checks",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            } else {
                Icon(
                    imageVector = Icons.Filled.Shield,
                    contentDescription = "Audit",
                    tint = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.size(48.dp),
                )
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = "Security Audit",
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.Bold,
                )
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = "Run 14 checks against the daemon to detect leaks and misconfigurations.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }

            Spacer(modifier = Modifier.height(16.dp))

            Button(
                onClick = onRunAudit,
                enabled = !state.isRunning,
                colors = ButtonDefaults.buttonColors(
                    containerColor = MaterialTheme.colorScheme.primary,
                ),
            ) {
                if (state.isRunning) {
                    CircularProgressIndicator(
                        modifier = Modifier.size(18.dp),
                        strokeWidth = 2.dp,
                        color = MaterialTheme.colorScheme.onPrimary,
                    )
                } else {
                    Icon(
                        imageVector = Icons.Filled.PlayArrow,
                        contentDescription = null,
                        modifier = Modifier.size(18.dp),
                    )
                }
                Spacer(modifier = Modifier.width(8.dp))
                Text(if (state.hasRun) "Re-run Audit" else "Run Audit")
            }
        }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Spinning shield loading indicator
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun LoadingIndicator() {
    val infiniteTransition = rememberInfiniteTransition(label = "audit_loading")
    val rotation by infiniteTransition.animateFloat(
        initialValue = 0f,
        targetValue = 360f,
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 1_200, easing = LinearEasing),
            repeatMode = RepeatMode.Restart,
        ),
        label = "rotation",
    )

    Icon(
        imageVector = Icons.Filled.Shield,
        contentDescription = "Scanning",
        tint = MaterialTheme.colorScheme.primary,
        modifier = Modifier
            .size(48.dp)
            .graphicsLayer { rotationZ = rotation },
    )
}

// ───────────────────────────────────────────────────────────────────── //
//  Individual finding card (expandable)
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun FindingCard(finding: AuditFinding) {
    var expanded by remember { mutableStateOf(false) }

    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { expanded = !expanded },
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surface,
        ),
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            // Header row: dot + title + severity badge + chevron
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.fillMaxWidth(),
            ) {
                TrafficLightDot(severity = finding.severity)
                Spacer(modifier = Modifier.width(12.dp))
                Text(
                    text = finding.title,
                    style = MaterialTheme.typography.bodyLarge,
                    fontWeight = FontWeight.Medium,
                    modifier = Modifier.weight(1f),
                )
                SeverityBadge(severity = finding.severity)
                Spacer(modifier = Modifier.width(4.dp))
                Icon(
                    imageVector = if (expanded) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore,
                    contentDescription = if (expanded) "Collapse" else "Expand",
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.size(20.dp),
                )
            }

            // Expandable detail section
            AnimatedVisibility(
                visible = expanded,
                enter = expandVertically(),
                exit = shrinkVertically(),
            ) {
                Column(modifier = Modifier.padding(top = 12.dp)) {
                    Text(
                        text = finding.detail,
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    if (!finding.passed) {
                        Spacer(modifier = Modifier.height(8.dp))
                        Surface(
                            color = MaterialTheme.colorScheme.secondaryContainer,
                            shape = MaterialTheme.shapes.small,
                        ) {
                            Text(
                                text = "Remediation: ${finding.remediation}",
                                style = MaterialTheme.typography.bodySmall,
                                modifier = Modifier.padding(horizontal = 10.dp, vertical = 6.dp),
                                color = MaterialTheme.colorScheme.onSecondaryContainer,
                            )
                        }
                    }
                }
            }
        }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Small composable helpers
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun TrafficLightDot(severity: Severity) {
    val color = severityColor(severity)
    Canvas(modifier = Modifier.size(10.dp)) {
        drawCircle(color = color)
    }
}

@Composable
private fun SeverityBadge(severity: Severity) {
    val color = severityColor(severity)
    Surface(
        color = color.copy(alpha = 0.15f),
        shape = CircleShape,
    ) {
        Text(
            text = severity.label,
            color = color,
            style = MaterialTheme.typography.labelSmall,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp),
        )
    }
}
