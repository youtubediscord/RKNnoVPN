package com.privstack.panel.ui.settings

import android.content.Intent
import android.net.Uri
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.expandVertically
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.OpenInBrowser
import androidx.compose.material.icons.filled.SystemUpdate
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.ListItemDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.privstack.panel.R

private const val GITHUB_RELEASES_URL =
    "https://github.com/youtubediscord/RKNnoVPN/releases/latest"

/**
 * Self-contained Compose section for in-app updates.
 *
 * Shows current version, a "Check for updates" button, and -- when an
 * update is available -- the changelog, download progress, and install
 * button.  All network activity goes through the root daemon via IPC
 * because the APK has NO INTERNET permission.
 */
@Composable
fun UpdateSection(
    state: UpdateUiState,
    onCheckForUpdates: () -> Unit,
    onDownloadUpdate: () -> Unit,
    onInstallUpdate: () -> Unit,
) {
    val context = LocalContext.current

    // Section header (matches existing SettingsScreen style)
    ListItem(
        headlineContent = {
            Text(
                text = stringResource(R.string.settings_update),
                style = MaterialTheme.typography.titleSmall,
                color = MaterialTheme.colorScheme.primary,
            )
        },
        leadingContent = {
            Icon(Icons.Filled.SystemUpdate, contentDescription = null)
        },
        colors = ListItemDefaults.colors(containerColor = Color.Transparent),
    )

    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        ),
    ) {
        Column {
            // Current version
            ListItem(
                headlineContent = { Text(stringResource(R.string.update_current_version)) },
                supportingContent = { Text(state.currentVersion) },
                colors = ListItemDefaults.colors(containerColor = Color.Transparent),
            )

            // Status line
            ListItem(
                headlineContent = { Text(stringResource(R.string.update_status)) },
                supportingContent = {
                    Text(
                        text = when (state.status) {
                            UpdateStatus.IDLE -> stringResource(R.string.update_status_idle)
                            UpdateStatus.CHECKING -> stringResource(R.string.update_status_checking)
                            UpdateStatus.UP_TO_DATE -> stringResource(R.string.update_status_up_to_date)
                            UpdateStatus.AVAILABLE -> stringResource(
                                R.string.update_status_available,
                                state.latestVersion
                            )
                            UpdateStatus.DOWNLOADING -> stringResource(R.string.update_status_downloading)
                            UpdateStatus.DOWNLOADED -> stringResource(R.string.update_status_downloaded)
                            UpdateStatus.INSTALLING -> stringResource(R.string.update_status_installing)
                            UpdateStatus.INSTALLED -> stringResource(R.string.update_status_installed)
                            UpdateStatus.MODULE_TOO_OLD -> stringResource(R.string.update_module_too_old)
                            UpdateStatus.ERROR -> state.errorMessage
                        },
                        color = when (state.status) {
                            UpdateStatus.ERROR -> MaterialTheme.colorScheme.error
                            UpdateStatus.MODULE_TOO_OLD -> MaterialTheme.colorScheme.error
                            UpdateStatus.UP_TO_DATE,
                            UpdateStatus.INSTALLED -> MaterialTheme.colorScheme.primary
                            else -> MaterialTheme.colorScheme.onSurface
                        },
                    )
                },
                trailingContent = {
                    if (state.status == UpdateStatus.CHECKING) {
                        CircularProgressIndicator()
                    }
                },
                colors = ListItemDefaults.colors(containerColor = Color.Transparent),
            )

            // Download progress bar
            AnimatedVisibility(
                visible = state.status == UpdateStatus.DOWNLOADING,
                enter = expandVertically(),
                exit = shrinkVertically(),
            ) {
                LinearProgressIndicator(
                    progress = { state.downloadProgress },
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(horizontal = 16.dp, vertical = 8.dp),
                )
            }

            // Changelog (visible when update is available or downloaded)
            AnimatedVisibility(
                visible = state.changelog.isNotBlank() &&
                        state.status in listOf(
                            UpdateStatus.AVAILABLE,
                            UpdateStatus.DOWNLOADED,
                            UpdateStatus.DOWNLOADING,
                        ),
                enter = expandVertically(),
                exit = shrinkVertically(),
            ) {
                Column(modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp)) {
                    Text(
                        text = stringResource(R.string.update_changelog),
                        style = MaterialTheme.typography.labelMedium,
                        fontWeight = FontWeight.Bold,
                    )
                    Spacer(modifier = Modifier.height(4.dp))
                    Text(
                        text = state.changelog,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            }

            // Action buttons
            ListItem(
                headlineContent = {
                    when (state.status) {
                        UpdateStatus.IDLE,
                        UpdateStatus.UP_TO_DATE,
                        UpdateStatus.ERROR -> {
                            FilledTonalButton(onClick = onCheckForUpdates) {
                                Text(stringResource(R.string.update_check_button))
                            }
                        }
                        UpdateStatus.AVAILABLE -> {
                            FilledTonalButton(onClick = onDownloadUpdate) {
                                Text(stringResource(R.string.update_download_button))
                            }
                        }
                        UpdateStatus.DOWNLOADED -> {
                            FilledTonalButton(onClick = onInstallUpdate) {
                                Text(stringResource(R.string.update_install_button))
                            }
                        }
                        UpdateStatus.MODULE_TOO_OLD -> {
                            // Daemon doesn't support updates -- offer GitHub link.
                            // ACTION_VIEW opens the default browser; no INTERNET
                            // permission needed by the APK itself.
                            Column {
                                Text(
                                    text = stringResource(R.string.update_module_too_old_hint),
                                    style = MaterialTheme.typography.bodySmall,
                                    modifier = Modifier.padding(bottom = 8.dp),
                                )
                                OutlinedButton(
                                    onClick = {
                                        val intent = Intent(
                                            Intent.ACTION_VIEW,
                                            Uri.parse(GITHUB_RELEASES_URL),
                                        )
                                        context.startActivity(intent)
                                    },
                                ) {
                                    Icon(
                                        Icons.Filled.OpenInBrowser,
                                        contentDescription = null,
                                        modifier = Modifier.padding(end = 4.dp),
                                    )
                                    Text(stringResource(R.string.update_open_github))
                                }
                            }
                        }
                        UpdateStatus.CHECKING,
                        UpdateStatus.DOWNLOADING,
                        UpdateStatus.INSTALLING -> {
                            // No action during these states
                        }
                        UpdateStatus.INSTALLED -> {
                            Text(
                                text = stringResource(R.string.update_status_installed),
                                color = MaterialTheme.colorScheme.primary,
                                style = MaterialTheme.typography.bodyMedium,
                            )
                        }
                    }
                },
                colors = ListItemDefaults.colors(containerColor = Color.Transparent),
            )
        }
    }
}
