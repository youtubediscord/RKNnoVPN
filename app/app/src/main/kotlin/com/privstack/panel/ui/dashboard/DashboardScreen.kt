package com.privstack.panel.ui.dashboard

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.AccessTime
import androidx.compose.material.icons.filled.ArrowDownward
import androidx.compose.material.icons.filled.ArrowUpward
import androidx.compose.material.icons.filled.Dns
import androidx.compose.material.icons.filled.Speed
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
// ExperimentalMaterial3Api removed — using standard Column instead of PullToRefreshBox
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.privstack.panel.R
import com.privstack.panel.model.ConnectionState
import com.privstack.panel.ui.common.ConnectionIndicator
import com.privstack.panel.ui.common.TrafficSparkline

@Composable
fun DashboardScreen(
    viewModel: DashboardViewModel = hiltViewModel(),
) {
    val state by viewModel.uiState.collectAsStateWithLifecycle()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState()),
    ) {
        Column(
            horizontalAlignment = Alignment.CenterHorizontally,
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 24.dp, vertical = 16.dp),
        ) {
            Spacer(modifier = Modifier.height(16.dp))

            // -- Connection indicator --
            ConnectionIndicator(
                state = state.connectionState,
                size = 160.dp,
            )

            Spacer(modifier = Modifier.height(8.dp))

            // -- State label --
            Text(
                text = connectionStateLabel(state.connectionState),
                style = MaterialTheme.typography.titleMedium,
                color = connectionStateColor(state.connectionState),
            )

            Spacer(modifier = Modifier.height(20.dp))

            // -- Connect / Disconnect button --
            ConnectButton(
                connectionState = state.connectionState,
                onClick = viewModel::toggleConnection,
            )

            Spacer(modifier = Modifier.height(24.dp))

            // -- Active node --
            ActiveNodeCard(
                nodeName = state.activeNodeName,
                protocol = state.activeNodeProtocol,
            )

            Spacer(modifier = Modifier.height(16.dp))

            // -- Traffic sparkline --
            AnimatedVisibility(
                visible = state.trafficHistory.size >= 2,
                enter = fadeIn(),
                exit = fadeOut(),
            ) {
                Card(
                    modifier = Modifier.fillMaxWidth(),
                    colors = CardDefaults.cardColors(
                        containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
                    ),
                ) {
                    Column(modifier = Modifier.padding(16.dp)) {
                        Text(
                            text = stringResource(R.string.traffic),
                            style = MaterialTheme.typography.labelMedium,
                        )
                        Spacer(modifier = Modifier.height(8.dp))
                        TrafficSparkline(
                            points = state.trafficHistory,
                            height = 80.dp,
                        )
                    }
                }
            }

            Spacer(modifier = Modifier.height(16.dp))

            // -- Metrics row --
            MetricsRow(state)

            Spacer(modifier = Modifier.height(16.dp))

            // -- Traffic counters --
            TrafficCountersRow(state)

            Spacer(modifier = Modifier.height(24.dp))
        }
    }
}

// ---- Sub-composables ----

@Composable
private fun ConnectButton(
    connectionState: ConnectionState,
    onClick: () -> Unit,
) {
    val isConnected = connectionState == ConnectionState.CONNECTED
    val isConnecting = connectionState == ConnectionState.CONNECTING

    val containerColor = when {
        isConnected -> MaterialTheme.colorScheme.error
        isConnecting -> MaterialTheme.colorScheme.tertiaryContainer
        else -> MaterialTheme.colorScheme.primaryContainer
    }
    val contentColor = when {
        isConnected -> MaterialTheme.colorScheme.onError
        isConnecting -> MaterialTheme.colorScheme.onTertiaryContainer
        else -> MaterialTheme.colorScheme.onPrimaryContainer
    }

    val label = when {
        isConnected -> stringResource(R.string.disconnect)
        isConnecting -> stringResource(R.string.state_connecting)
        else -> stringResource(R.string.connect)
    }

    FilledTonalButton(
        onClick = onClick,
        enabled = !isConnecting,
        colors = ButtonDefaults.filledTonalButtonColors(
            containerColor = containerColor,
            contentColor = contentColor,
        ),
        modifier = Modifier
            .fillMaxWidth()
            .height(52.dp),
    ) {
        Text(
            text = label,
            style = MaterialTheme.typography.titleSmall,
        )
    }
}

@Composable
private fun ActiveNodeCard(
    nodeName: String?,
    protocol: String?,
) {
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        ),
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(
                text = stringResource(R.string.active_node),
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = nodeName ?: stringResource(R.string.no_node_selected),
                style = MaterialTheme.typography.bodyLarge,
                fontWeight = FontWeight.Medium,
            )
            if (protocol != null) {
                Text(
                    text = protocol,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

@Composable
private fun MetricsRow(state: DashboardUiState) {
    Row(
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        // Egress IP
        MetricCard(
            label = stringResource(R.string.egress_ip),
            value = buildString {
                state.countryFlag?.let { append(it); append(" ") }
                append(state.egressIp ?: stringResource(R.string.unknown_ip))
            },
            modifier = Modifier.weight(1f),
        )

        // Latency
        MetricCard(
            label = stringResource(R.string.latency),
            value = state.latencyMs?.let {
                stringResource(R.string.ms_format, it)
            } ?: stringResource(R.string.unknown_ip),
            icon = { Icon(Icons.Filled.Speed, contentDescription = null, modifier = Modifier.size(16.dp)) },
            modifier = Modifier.weight(1f),
        )
    }

    Spacer(modifier = Modifier.height(12.dp))

    Row(
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        // DNS
        MetricCard(
            label = stringResource(R.string.dns_status),
            value = if (state.dnsOperational) stringResource(R.string.dns_ok)
            else stringResource(R.string.dns_fail),
            icon = { Icon(Icons.Filled.Dns, contentDescription = null, modifier = Modifier.size(16.dp)) },
            valueColor = if (state.dnsOperational) MaterialTheme.colorScheme.primary
            else MaterialTheme.colorScheme.error,
            modifier = Modifier.weight(1f),
        )

        // Uptime
        MetricCard(
            label = stringResource(R.string.uptime),
            value = formatUptime(state.uptimeSeconds),
            icon = { Icon(Icons.Filled.AccessTime, contentDescription = null, modifier = Modifier.size(16.dp)) },
            modifier = Modifier.weight(1f),
        )
    }
}

@Composable
private fun TrafficCountersRow(state: DashboardUiState) {
    Row(
        horizontalArrangement = Arrangement.spacedBy(12.dp),
        modifier = Modifier.fillMaxWidth(),
    ) {
        TrafficCard(
            label = stringResource(R.string.download_short),
            total = formatBytes(state.traffic.rxBytes),
            rate = stringResource(R.string.speed_format, formatBytes(state.traffic.rxRate)),
            icon = Icons.Filled.ArrowDownward,
            modifier = Modifier.weight(1f),
        )
        TrafficCard(
            label = stringResource(R.string.upload_short),
            total = formatBytes(state.traffic.txBytes),
            rate = stringResource(R.string.speed_format, formatBytes(state.traffic.txRate)),
            icon = Icons.Filled.ArrowUpward,
            modifier = Modifier.weight(1f),
        )
    }
}

@Composable
private fun MetricCard(
    label: String,
    value: String,
    modifier: Modifier = Modifier,
    icon: (@Composable () -> Unit)? = null,
    valueColor: androidx.compose.ui.graphics.Color = MaterialTheme.colorScheme.onSurface,
) {
    Card(
        modifier = modifier,
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        ),
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                icon?.invoke()
                if (icon != null) Spacer(modifier = Modifier.width(4.dp))
                Text(
                    text = label,
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = value,
                style = MaterialTheme.typography.bodyMedium,
                fontWeight = FontWeight.Medium,
                color = valueColor,
            )
        }
    }
}

@Composable
private fun TrafficCard(
    label: String,
    total: String,
    rate: String,
    icon: androidx.compose.ui.graphics.vector.ImageVector,
    modifier: Modifier = Modifier,
) {
    Card(
        modifier = modifier,
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.5f),
        ),
    ) {
        Column(modifier = Modifier.padding(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(icon, contentDescription = null, modifier = Modifier.size(16.dp))
                Spacer(modifier = Modifier.width(4.dp))
                Text(
                    text = label,
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Spacer(modifier = Modifier.height(4.dp))
            Text(
                text = total,
                style = MaterialTheme.typography.bodyMedium,
                fontWeight = FontWeight.Medium,
            )
            Text(
                text = rate,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

// ---- Helpers ----

@Composable
private fun connectionStateLabel(state: ConnectionState): String = when (state) {
    ConnectionState.DISCONNECTED -> stringResource(R.string.state_disconnected)
    ConnectionState.CONNECTING -> stringResource(R.string.state_connecting)
    ConnectionState.CONNECTED -> stringResource(R.string.state_connected)
    ConnectionState.ERROR -> stringResource(R.string.state_error)
    ConnectionState.UNKNOWN -> stringResource(R.string.state_unknown)
}

@Composable
private fun connectionStateColor(state: ConnectionState) = when (state) {
    ConnectionState.CONNECTED -> MaterialTheme.colorScheme.primary
    ConnectionState.CONNECTING -> MaterialTheme.colorScheme.tertiary
    ConnectionState.ERROR -> MaterialTheme.colorScheme.error
    ConnectionState.DISCONNECTED -> MaterialTheme.colorScheme.outline
    ConnectionState.UNKNOWN -> MaterialTheme.colorScheme.outlineVariant
}

private fun formatUptime(seconds: Long): String {
    if (seconds <= 0) return "00:00"
    val h = seconds / 3600
    val m = (seconds % 3600) / 60
    val s = seconds % 60
    return if (h > 0) "%d:%02d:%02d".format(h, m, s)
    else "%02d:%02d".format(m, s)
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
