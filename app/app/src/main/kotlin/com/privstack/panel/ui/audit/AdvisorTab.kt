package com.privstack.panel.ui.audit

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.expandVertically
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.CheckCircle
import androidx.compose.material.icons.filled.Error
import androidx.compose.material.icons.filled.ExpandLess
import androidx.compose.material.icons.filled.ExpandMore
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.PhoneAndroid
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material.icons.filled.Work
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ElevatedCard
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privstack.panel.advisor.AppCategory
import com.privstack.panel.advisor.PlacementRecommendation
import com.privstack.panel.advisor.PlacementUrgency
import com.privstack.panel.advisor.ProfilePlacement

// ───────────────────────────────────────────────────────────────────── //
//  Color helpers
// ───────────────────────────────────────────────────────────────────── //

private val GreenCorrect = Color(0xFF4CAF50)
private val YellowConsider = Color(0xFFFFC107)
private val RedMisplaced = Color(0xFFE53935)

private fun urgencyColor(urgency: PlacementUrgency): Color = when (urgency) {
    PlacementUrgency.CORRECT -> GreenCorrect
    PlacementUrgency.CONSIDER -> YellowConsider
    PlacementUrgency.MISPLACED -> RedMisplaced
}

private fun urgencyLabel(urgency: PlacementUrgency): String = when (urgency) {
    PlacementUrgency.CORRECT -> "OK"
    PlacementUrgency.CONSIDER -> "Consider"
    PlacementUrgency.MISPLACED -> "Misplaced"
}

// ───────────────────────────────────────────────────────────────────── //
//  Advisor tab (placed inside AuditScreen pager)
// ───────────────────────────────────────────────────────────────────── //

@Composable
fun AdvisorTab(viewModel: AuditViewModel) {
    val state by viewModel.advisorState.collectAsStateWithLifecycle()

    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item { Spacer(modifier = Modifier.height(8.dp)) }

        // Summary card
        item {
            AdvisorSummaryCard(
                state = state,
                onLoad = { viewModel.loadAdvisor() },
            )
        }

        if (state.hasLoaded) {
            // Category groups
            state.groupedByCategory.forEach { (category, recommendations) ->
                item {
                    CategoryHeader(category = category, count = recommendations.size)
                }
                items(recommendations, key = { it.app.packageName }) { rec ->
                    RecommendationCard(recommendation = rec)
                }
            }

            // Setup Guide button
            item {
                Spacer(modifier = Modifier.height(8.dp))
                SetupGuideCard()
            }
        }

        item { Spacer(modifier = Modifier.height(16.dp)) }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Summary card
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun AdvisorSummaryCard(
    state: AdvisorUiState,
    onLoad: () -> Unit,
) {
    val bgColor = when {
        !state.hasLoaded -> MaterialTheme.colorScheme.surfaceVariant
        state.attentionCount == 0 -> Color(0xFFE8F5E9)
        else -> Color(0xFFFFF8E1)
    }

    ElevatedCard(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.elevatedCardColors(containerColor = bgColor),
    ) {
        Column(
            modifier = Modifier.padding(20.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            if (state.isLoading) {
                CircularProgressIndicator(modifier = Modifier.size(40.dp))
                Spacer(modifier = Modifier.height(12.dp))
                Text(
                    text = "Analyzing installed apps...",
                    style = MaterialTheme.typography.bodyLarge,
                )
            } else if (state.hasLoaded) {
                Icon(
                    imageVector = if (state.attentionCount == 0) Icons.Filled.CheckCircle else Icons.Filled.Warning,
                    contentDescription = null,
                    tint = if (state.attentionCount == 0) GreenCorrect else YellowConsider,
                    modifier = Modifier.size(48.dp),
                )
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = if (state.attentionCount == 0)
                        "All apps are properly placed"
                    else
                        "${state.attentionCount} app${if (state.attentionCount > 1) "s" else ""} may need attention",
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.Bold,
                )
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = "${state.recommendations.size} apps analyzed",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            } else {
                Icon(
                    imageVector = Icons.Filled.PhoneAndroid,
                    contentDescription = null,
                    tint = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.size(48.dp),
                )
                Spacer(modifier = Modifier.height(8.dp))
                Text(
                    text = "App Placement Advisor",
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.Bold,
                )
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = "Analyze installed apps and get profile placement recommendations.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }

            Spacer(modifier = Modifier.height(16.dp))

            Button(
                onClick = onLoad,
                enabled = !state.isLoading,
            ) {
                if (state.isLoading) {
                    CircularProgressIndicator(
                        modifier = Modifier.size(18.dp),
                        strokeWidth = 2.dp,
                        color = MaterialTheme.colorScheme.onPrimary,
                    )
                } else {
                    Icon(
                        imageVector = Icons.Filled.PhoneAndroid,
                        contentDescription = null,
                        modifier = Modifier.size(18.dp),
                    )
                }
                Spacer(modifier = Modifier.width(8.dp))
                Text(if (state.hasLoaded) "Re-analyze" else "Analyze Apps")
            }
        }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Category header
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun CategoryHeader(category: AppCategory, count: Int) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .padding(top = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            text = category.displayName,
            style = MaterialTheme.typography.titleMedium,
            fontWeight = FontWeight.SemiBold,
        )
        Spacer(modifier = Modifier.width(8.dp))
        Surface(
            color = MaterialTheme.colorScheme.secondaryContainer,
            shape = CircleShape,
        ) {
            Text(
                text = count.toString(),
                style = MaterialTheme.typography.labelSmall,
                modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp),
                color = MaterialTheme.colorScheme.onSecondaryContainer,
            )
        }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Individual recommendation card (expandable)
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun RecommendationCard(recommendation: PlacementRecommendation) {
    var expanded by remember { mutableStateOf(false) }
    val color = urgencyColor(recommendation.urgency)

    Card(
        modifier = Modifier
            .fillMaxWidth()
            .clickable { expanded = !expanded },
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            // Header row
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.fillMaxWidth(),
            ) {
                // Profile icon
                Icon(
                    imageVector = when (recommendation.recommended) {
                        ProfilePlacement.WORK -> Icons.Filled.Work
                        ProfilePlacement.PERSONAL -> Icons.Filled.PhoneAndroid
                    },
                    contentDescription = recommendation.recommended.displayName,
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.size(24.dp),
                )
                Spacer(modifier = Modifier.width(12.dp))

                // App name and package
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        text = recommendation.app.label,
                        style = MaterialTheme.typography.bodyLarge,
                        fontWeight = FontWeight.Medium,
                    )
                    Text(
                        text = recommendation.app.packageName,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }

                // Urgency badge
                UrgencyBadge(urgency = recommendation.urgency)

                Spacer(modifier = Modifier.width(4.dp))
                Icon(
                    imageVector = if (expanded) Icons.Filled.ExpandLess else Icons.Filled.ExpandMore,
                    contentDescription = if (expanded) "Collapse" else "Expand",
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.size(20.dp),
                )
            }

            // Expanded detail
            AnimatedVisibility(
                visible = expanded,
                enter = expandVertically(),
                exit = shrinkVertically(),
            ) {
                Column(modifier = Modifier.padding(top = 12.dp)) {
                    // Placement info
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Icon(
                            imageVector = Icons.Filled.Info,
                            contentDescription = null,
                            tint = MaterialTheme.colorScheme.primary,
                            modifier = Modifier.size(16.dp),
                        )
                        Spacer(modifier = Modifier.width(6.dp))
                        Text(
                            text = "Recommended: ${recommendation.recommended.displayName}",
                            style = MaterialTheme.typography.bodyMedium,
                            fontWeight = FontWeight.Medium,
                        )
                    }

                    if (recommendation.current != null) {
                        Spacer(modifier = Modifier.height(4.dp))
                        Text(
                            text = "Currently in: ${recommendation.current.displayName}",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }

                    Spacer(modifier = Modifier.height(8.dp))
                    Text(
                        text = recommendation.reason,
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Urgency badge
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun UrgencyBadge(urgency: PlacementUrgency) {
    val color = urgencyColor(urgency)
    Surface(
        color = color.copy(alpha = 0.15f),
        shape = CircleShape,
    ) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp),
        ) {
            Icon(
                imageVector = when (urgency) {
                    PlacementUrgency.CORRECT -> Icons.Filled.CheckCircle
                    PlacementUrgency.CONSIDER -> Icons.Filled.Warning
                    PlacementUrgency.MISPLACED -> Icons.Filled.Error
                },
                contentDescription = null,
                tint = color,
                modifier = Modifier.size(12.dp),
            )
            Spacer(modifier = Modifier.width(4.dp))
            Text(
                text = urgencyLabel(urgency),
                color = color,
                style = MaterialTheme.typography.labelSmall,
                fontWeight = FontWeight.SemiBold,
            )
        }
    }
}

// ───────────────────────────────────────────────────────────────────── //
//  Work Profile setup guide card
// ───────────────────────────────────────────────────────────────────── //

@Composable
private fun SetupGuideCard() {
    var expanded by remember { mutableStateOf(false) }

    ElevatedCard(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.elevatedCardColors(
            containerColor = MaterialTheme.colorScheme.tertiaryContainer,
        ),
    ) {
        Column(modifier = Modifier.padding(20.dp)) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.fillMaxWidth(),
            ) {
                Icon(
                    imageVector = Icons.Filled.Work,
                    contentDescription = null,
                    tint = MaterialTheme.colorScheme.onTertiaryContainer,
                    modifier = Modifier.size(28.dp),
                )
                Spacer(modifier = Modifier.width(12.dp))
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        text = "Work Profile Setup Guide",
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                        color = MaterialTheme.colorScheme.onTertiaryContainer,
                    )
                    Text(
                        text = "Isolate sensitive apps from proxy detection",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onTertiaryContainer.copy(alpha = 0.7f),
                    )
                }
            }

            Spacer(modifier = Modifier.height(12.dp))

            OutlinedButton(
                onClick = { expanded = !expanded },
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text(if (expanded) "Hide Guide" else "Setup Guide")
            }

            AnimatedVisibility(
                visible = expanded,
                enter = expandVertically(),
                exit = shrinkVertically(),
            ) {
                Column(modifier = Modifier.padding(top = 16.dp)) {
                    val steps = listOf(
                        "1. Install Shelter or Island from F-Droid / Play Store.",
                        "2. Create a Work Profile when the app prompts you.",
                        "3. Clone banking and government apps into the Work Profile.",
                        "4. In the Work Profile, do NOT install the proxy/VPN app.",
                        "5. Work Profile apps will use the system's direct connection, " +
                            "completely isolated from the TUN interface in the Personal profile.",
                        "6. Verify by opening a banking app in the Work Profile -- " +
                            "it should no longer detect a VPN.",
                    )

                    steps.forEach { step ->
                        Text(
                            text = step,
                            style = MaterialTheme.typography.bodyMedium,
                            color = MaterialTheme.colorScheme.onTertiaryContainer,
                            modifier = Modifier.padding(bottom = 8.dp),
                        )
                    }

                    Spacer(modifier = Modifier.height(4.dp))
                    Surface(
                        color = MaterialTheme.colorScheme.onTertiaryContainer.copy(alpha = 0.08f),
                        shape = MaterialTheme.shapes.small,
                    ) {
                        Text(
                            text = "Tip: Shelter is open-source and lightweight. " +
                                "It creates a standard Android Work Profile without " +
                                "requiring device owner or root.",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onTertiaryContainer,
                            modifier = Modifier.padding(10.dp),
                        )
                    }
                }
            }
        }
    }
}
