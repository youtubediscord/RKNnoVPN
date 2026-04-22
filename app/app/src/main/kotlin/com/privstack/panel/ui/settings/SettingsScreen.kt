package com.privstack.panel.ui.settings

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Code
import androidx.compose.material.icons.filled.DarkMode
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.Info
import androidx.compose.material.icons.filled.Memory
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.filled.RestartAlt
import androidx.compose.material.icons.filled.Route
import androidx.compose.material.icons.filled.Tune
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.SegmentedButton
import androidx.compose.material3.SegmentedButtonDefaults
import androidx.compose.material3.SingleChoiceSegmentedButtonRow
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.privstack.panel.R

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(
    onNavigateToAudit: () -> Unit = {},
    viewModel: SettingsViewModel = hiltViewModel(),
) {
    val state by viewModel.uiState.collectAsStateWithLifecycle()
    val updateState by viewModel.updateState.collectAsStateWithLifecycle()
    val context = LocalContext.current

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(horizontal = 16.dp, vertical = 8.dp),
    ) {
        if (state.errorMessage != null) {
            SettingsCard {
                ListItem(
                    headlineContent = {
                        Text(
                            text = state.errorMessage ?: "",
                            color = MaterialTheme.colorScheme.error,
                        )
                    },
                    colors = transparentListItemColors(),
                )
            }
            Spacer(modifier = Modifier.height(12.dp))
        }

        // ===== ROUTING =====
        SectionHeader(
            title = stringResource(R.string.settings_routing),
            icon = { Icon(Icons.Filled.Route, contentDescription = null) },
        )

        RoutingModeSelector(
            currentMode = state.routingMode,
            onModeChange = viewModel::setRoutingMode,
        )

        Spacer(modifier = Modifier.height(20.dp))

        // ===== DNS =====
        SectionHeader(
            title = stringResource(R.string.settings_dns),
            icon = { Icon(Icons.Filled.Dns, contentDescription = null) },
        )

        DnsPresetSelector(
            currentPreset = state.dnsPreset,
            onPresetChange = viewModel::setDnsPreset,
        )

        if (state.dnsPreset == DnsPreset.CUSTOM) {
            Spacer(modifier = Modifier.height(8.dp))
            OutlinedTextField(
                value = state.customDnsUrl,
                onValueChange = viewModel::setCustomDnsUrl,
                label = { Text(stringResource(R.string.dns_custom_hint)) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            Spacer(modifier = Modifier.height(4.dp))
            TextButton(onClick = viewModel::applyCustomDns) {
                Text(stringResource(R.string.apply))
            }
        }

        Spacer(modifier = Modifier.height(20.dp))

        // ===== ADVANCED =====
        SectionHeader(
            title = stringResource(R.string.settings_advanced),
            icon = { Icon(Icons.Filled.Tune, contentDescription = null) },
        )

        SettingsCard {
            // Log level picker
            LogLevelPicker(
                currentLevel = state.logLevel,
                onLevelChange = viewModel::setLogLevel,
            )

            Spacer(modifier = Modifier.height(8.dp))

            OutlinedTextField(
                value = state.urlTestUrl,
                onValueChange = viewModel::setUrlTestUrl,
                label = { Text(stringResource(R.string.urltest_url)) },
                singleLine = true,
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp),
            )

            TextButton(
                onClick = viewModel::applyUrlTestUrl,
                modifier = Modifier.padding(horizontal = 8.dp),
            ) {
                Text(stringResource(R.string.apply))
            }

            OutlinedTextField(
                value = state.alwaysDirectPackagesText,
                onValueChange = viewModel::setAlwaysDirectPackagesText,
                label = { Text(stringResource(R.string.always_direct_apps)) },
                supportingText = { Text(stringResource(R.string.always_direct_apps_desc)) },
                minLines = 3,
                maxLines = 6,
                modifier = Modifier
                    .fillMaxWidth()
                    .padding(horizontal = 16.dp),
            )

            TextButton(
                onClick = viewModel::applyAlwaysDirectPackages,
                modifier = Modifier.padding(horizontal = 8.dp),
            ) {
                Text(stringResource(R.string.apply))
            }
        }

        Spacer(modifier = Modifier.height(20.dp))

        // ===== MODULE =====
        SectionHeader(
            title = stringResource(R.string.settings_module),
            icon = { Icon(Icons.Filled.Memory, contentDescription = null) },
        )

        SettingsCard {
            ListItem(
                headlineContent = { Text(stringResource(R.string.module_version)) },
                supportingContent = { Text(state.moduleVersion) },
                colors = transparentListItemColors(),
            )

            ListItem(
                headlineContent = { Text(stringResource(R.string.daemon_status)) },
                supportingContent = { Text(state.daemonStatusText) },
                colors = transparentListItemColors(),
            )

            ListItem(
                headlineContent = {
                    FilledTonalButton(onClick = viewModel::restartDaemon) {
                        Icon(
                            Icons.Filled.RestartAlt,
                            contentDescription = null,
                            modifier = Modifier.padding(end = 4.dp),
                        )
                        Text(stringResource(R.string.restart_daemon))
                    }
                },
                colors = transparentListItemColors(),
            )

            ListItem(
                headlineContent = {
                    FilledTonalButton(
                        onClick = viewModel::resetNetworkRules,
                        colors = ButtonDefaults.filledTonalButtonColors(
                            containerColor = MaterialTheme.colorScheme.errorContainer,
                            contentColor = MaterialTheme.colorScheme.onErrorContainer,
                        ),
                    ) {
                        Icon(
                            Icons.Filled.RestartAlt,
                            contentDescription = null,
                            modifier = Modifier.padding(end = 4.dp),
                        )
                        Text(stringResource(R.string.reset_network_rules))
                    }
                },
                supportingContent = { Text(stringResource(R.string.reset_network_rules_desc)) },
                colors = transparentListItemColors(),
            )

            ListItem(
                headlineContent = {
                    FilledTonalButton(onClick = onNavigateToAudit) {
                        Icon(
                            Icons.Filled.Shield,
                            contentDescription = null,
                            modifier = Modifier.padding(end = 4.dp),
                        )
                        Text("Security Audit")
                    }
                },
                colors = transparentListItemColors(),
            )
        }

        Spacer(modifier = Modifier.height(20.dp))

        // ===== UPDATE =====
        UpdateSection(
            state = updateState,
            onCheckForUpdates = viewModel::checkForUpdates,
            onDownloadUpdate = viewModel::downloadUpdate,
            onInstallUpdate = viewModel::installUpdate,
        )

        Spacer(modifier = Modifier.height(20.dp))

        // ===== THEME =====
        SectionHeader(
            title = stringResource(R.string.settings_theme),
            icon = { Icon(Icons.Filled.DarkMode, contentDescription = null) },
        )

        ThemeModeSelector(
            currentMode = state.themeMode,
            onModeChange = viewModel::setThemeMode,
        )

        Spacer(modifier = Modifier.height(20.dp))

        // ===== ABOUT =====
        SectionHeader(
            title = stringResource(R.string.settings_about),
            icon = { Icon(Icons.Filled.Info, contentDescription = null) },
        )

        SettingsCard {
            ListItem(
                headlineContent = { Text(stringResource(R.string.about_version)) },
                supportingContent = { Text(state.appVersion) },
                colors = transparentListItemColors(),
            )

            ListItem(
                headlineContent = { Text(stringResource(R.string.about_github)) },
                supportingContent = { Text(state.githubUrl) },
                leadingContent = {
                    Icon(Icons.Filled.Code, contentDescription = null)
                },
                modifier = Modifier.clickable {
                    val intent = Intent(Intent.ACTION_VIEW, Uri.parse(state.githubUrl))
                    context.startActivity(intent)
                },
                colors = transparentListItemColors(),
            )
        }

        Spacer(modifier = Modifier.height(24.dp))
    }
}

// ---- Section helper composables ----

@Composable
private fun SectionHeader(
    title: String,
    icon: @Composable () -> Unit,
) {
    ListItem(
        headlineContent = {
            Text(
                text = title,
                style = MaterialTheme.typography.titleSmall,
                color = MaterialTheme.colorScheme.primary,
            )
        },
        leadingContent = icon,
        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
    )
}

@Composable
private fun SettingsCard(
    content: @Composable () -> Unit,
) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        ),
    ) {
        Column {
            content()
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun RoutingModeSelector(
    currentMode: RoutingMode,
    onModeChange: (RoutingMode) -> Unit,
) {
    val options = listOf(
        RoutingMode.GLOBAL to stringResource(R.string.routing_global),
        RoutingMode.WHITELIST to stringResource(R.string.routing_whitelist),
        RoutingMode.BYPASS to stringResource(R.string.routing_bypass),
        RoutingMode.DIRECT to stringResource(R.string.routing_direct),
    )

    SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
        options.forEachIndexed { index, (mode, label) ->
            SegmentedButton(
                selected = currentMode == mode,
                onClick = { onModeChange(mode) },
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
private fun DnsPresetSelector(
    currentPreset: DnsPreset,
    onPresetChange: (DnsPreset) -> Unit,
) {
    val presets = listOf(
        DnsPreset.CLOUDFLARE to stringResource(R.string.dns_cloudflare),
        DnsPreset.GOOGLE to stringResource(R.string.dns_google),
        DnsPreset.ADGUARD to stringResource(R.string.dns_adguard),
        DnsPreset.CUSTOM to stringResource(R.string.dns_custom),
    )

    SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
        presets.forEachIndexed { index, (preset, label) ->
            SegmentedButton(
                selected = currentPreset == preset,
                onClick = { onPresetChange(preset) },
                shape = SegmentedButtonDefaults.itemShape(
                    index = index,
                    count = presets.size,
                ),
            ) {
                Text(label, style = MaterialTheme.typography.labelSmall)
            }
        }
    }
}

@Composable
private fun LogLevelPicker(
    currentLevel: LogLevel,
    onLevelChange: (LogLevel) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }

    ListItem(
        headlineContent = { Text(stringResource(R.string.log_level)) },
        supportingContent = { Text(currentLevel.name) },
        trailingContent = {
            TextButton(onClick = { expanded = true }) {
                Text(currentLevel.name)
            }
            DropdownMenu(
                expanded = expanded,
                onDismissRequest = { expanded = false },
            ) {
                LogLevel.entries.forEach { level ->
                    DropdownMenuItem(
                        text = { Text(level.name) },
                        onClick = {
                            onLevelChange(level)
                            expanded = false
                        },
                    )
                }
            }
        },
        colors = transparentListItemColors(),
    )
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun ThemeModeSelector(
    currentMode: ThemeMode,
    onModeChange: (ThemeMode) -> Unit,
) {
    val options = listOf(
        ThemeMode.LIGHT to stringResource(R.string.theme_light),
        ThemeMode.DARK to stringResource(R.string.theme_dark),
        ThemeMode.SYSTEM to stringResource(R.string.theme_system),
    )

    SingleChoiceSegmentedButtonRow(modifier = Modifier.fillMaxWidth()) {
        options.forEachIndexed { index, (mode, label) ->
            SegmentedButton(
                selected = currentMode == mode,
                onClick = { onModeChange(mode) },
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

@Composable
private fun transparentListItemColors() = ListItemDefaults.colors(
    containerColor = Color.Transparent,
)
