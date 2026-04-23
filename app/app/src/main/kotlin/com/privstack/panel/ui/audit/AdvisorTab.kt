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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privstack.panel.R
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

        if (state.errorMessage != null) {
            item {
                Card(
                    colors = CardDefaults.cardColors(containerColor = Color(0xFFFFEBEE)),
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text(
                        text = state.errorMessage ?: "",
                        color = Color(0xFFE53935),
                        modifier = Modifier.padding(16.dp),
                        style = MaterialTheme.typography.bodyMedium,
                    )
                }
            }
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
                    text = stringResource(R.string.advisor_loading),
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
                        stringResource(R.string.advisor_all_good)
                    else
                        stringResource(R.string.advisor_attention_count, state.attentionCount),
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.Bold,
                )
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = stringResource(R.string.advisor_analyzed_count, state.recommendations.size),
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
                    text = stringResource(R.string.advisor_title),
                    style = MaterialTheme.typography.headlineSmall,
                    fontWeight = FontWeight.Bold,
                )
                Spacer(modifier = Modifier.height(4.dp))
                Text(
                    text = stringResource(R.string.advisor_description),
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
                Text(
                    if (state.hasLoaded) {
                        stringResource(R.string.advisor_reanalyze_button)
                    } else {
                        stringResource(R.string.advisor_analyze_button)
                    }
                )
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
            text = advisorCategoryLabel(category),
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
                    contentDescription = profilePlacementLabel(recommendation.recommended),
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
                    contentDescription = if (expanded) {
                        stringResource(R.string.audit_collapse)
                    } else {
                        stringResource(R.string.audit_expand)
                    },
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
                            text = stringResource(
                                R.string.advisor_recommended,
                                profilePlacementLabel(recommendation.recommended),
                            ),
                            style = MaterialTheme.typography.bodyMedium,
                            fontWeight = FontWeight.Medium,
                        )
                    }

                    if (recommendation.current != null) {
                        val currentPlacement = recommendation.current
                        Spacer(modifier = Modifier.height(4.dp))
                        Text(
                            text = stringResource(
                                R.string.advisor_currently_in,
                                profilePlacementLabel(currentPlacement),
                            ),
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
                text = advisorUrgencyLabel(urgency),
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
                        text = stringResource(R.string.advisor_setup_guide_title),
                        style = MaterialTheme.typography.titleMedium,
                        fontWeight = FontWeight.Bold,
                        color = MaterialTheme.colorScheme.onTertiaryContainer,
                    )
                    Text(
                        text = stringResource(R.string.advisor_setup_guide_subtitle),
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
                Text(
                    if (expanded) {
                        stringResource(R.string.advisor_setup_guide_hide)
                    } else {
                        stringResource(R.string.advisor_setup_guide_show)
                    }
                )
            }

            AnimatedVisibility(
                visible = expanded,
                enter = expandVertically(),
                exit = shrinkVertically(),
            ) {
                Column(modifier = Modifier.padding(top = 16.dp)) {
                    val steps = listOf(
                        stringResource(R.string.advisor_setup_step_1),
                        stringResource(R.string.advisor_setup_step_2),
                        stringResource(R.string.advisor_setup_step_3),
                        stringResource(R.string.advisor_setup_step_4),
                        stringResource(R.string.advisor_setup_step_5),
                        stringResource(R.string.advisor_setup_step_6),
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
                            text = stringResource(R.string.advisor_setup_tip),
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

@Composable
private fun advisorUrgencyLabel(urgency: PlacementUrgency): String = when (urgency) {
    PlacementUrgency.CORRECT -> stringResource(R.string.advisor_urgency_ok)
    PlacementUrgency.CONSIDER -> stringResource(R.string.advisor_urgency_consider)
    PlacementUrgency.MISPLACED -> stringResource(R.string.advisor_urgency_misplaced)
}

@Composable
private fun advisorCategoryLabel(category: AppCategory): String = when (category) {
    AppCategory.BROWSER -> stringResource(R.string.advisor_category_browser)
    AppCategory.SOCIAL_MESSAGING -> stringResource(R.string.advisor_category_social)
    AppCategory.STREAMING -> stringResource(R.string.advisor_category_streaming)
    AppCategory.BANKING -> stringResource(R.string.advisor_category_banking)
    AppCategory.TELECOM -> stringResource(R.string.advisor_category_telecom)
    AppCategory.GOVERNMENT -> stringResource(R.string.advisor_category_government)
    AppCategory.VPN_PROXY -> stringResource(R.string.advisor_category_vpn_proxy)
    AppCategory.SYSTEM -> stringResource(R.string.advisor_category_system)
    AppCategory.OTHER -> stringResource(R.string.advisor_category_other)
}

@Composable
private fun profilePlacementLabel(placement: ProfilePlacement): String = when (placement) {
    ProfilePlacement.PERSONAL -> stringResource(R.string.advisor_profile_personal)
    ProfilePlacement.WORK -> stringResource(R.string.advisor_profile_work)
}
