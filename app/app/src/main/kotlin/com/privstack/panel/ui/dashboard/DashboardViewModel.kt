package com.privstack.panel.ui.dashboard

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.model.ConnectionState
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.HealthResult
import com.privstack.panel.model.TrafficStats
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

/**
 * UI state exposed to the Dashboard screen.
 */
data class DashboardUiState(
    val connectionState: ConnectionState = ConnectionState.DISCONNECTED,
    val activeNodeName: String? = null,
    val activeNodeProtocol: String? = null,
    val traffic: TrafficStats = TrafficStats(),
    /** Ring buffer of normalized RX rate samples for the sparkline (0..1). */
    val trafficHistory: List<Float> = emptyList(),
    val egressIp: String? = null,
    val countryFlag: String? = null,
    val latencyMs: Int? = null,
    val dnsOperational: Boolean = false,
    val uptimeSeconds: Long = 0L,
    val isRefreshing: Boolean = false,
)

private const val TRAFFIC_HISTORY_SIZE = 120

@HiltViewModel
class DashboardViewModel @Inject constructor() : ViewModel() {

    private val _uiState = MutableStateFlow(DashboardUiState())
    val uiState: StateFlow<DashboardUiState> = _uiState.asStateFlow()

    /** Mutable ring buffer backing the sparkline. */
    private val _trafficRing = ArrayDeque<Float>(TRAFFIC_HISTORY_SIZE)

    init {
        // TODO: wire to real DaemonRepository polling
        startFakePolling()
    }

    // ---- Public actions ----

    fun toggleConnection() {
        val current = _uiState.value.connectionState
        when (current) {
            ConnectionState.DISCONNECTED,
            ConnectionState.ERROR,
            ConnectionState.UNKNOWN -> connect()
            ConnectionState.CONNECTED -> disconnect()
            ConnectionState.CONNECTING -> { /* ignore while connecting */ }
        }
    }

    fun refresh() {
        viewModelScope.launch {
            _uiState.update { it.copy(isRefreshing = true) }
            // TODO: call DaemonRepository.fetchStatus()
            delay(800)
            _uiState.update { it.copy(isRefreshing = false) }
        }
    }

    // ---- Internal ----

    private fun connect() {
        viewModelScope.launch {
            _uiState.update { it.copy(connectionState = ConnectionState.CONNECTING) }
            // TODO: call DaemonRepository.connect(nodeId)
            delay(2000)
            _uiState.update {
                it.copy(
                    connectionState = ConnectionState.CONNECTED,
                    activeNodeName = "Frankfurt-1",
                    activeNodeProtocol = "VLESS",
                    egressIp = "185.12.64.1",
                    countryFlag = "\uD83C\uDDE9\uD83C\uDDEA",
                    latencyMs = 42,
                    dnsOperational = true,
                )
            }
        }
    }

    private fun disconnect() {
        viewModelScope.launch {
            // TODO: call DaemonRepository.disconnect()
            _uiState.update {
                it.copy(
                    connectionState = ConnectionState.DISCONNECTED,
                    egressIp = null,
                    countryFlag = null,
                    latencyMs = null,
                    dnsOperational = false,
                    uptimeSeconds = 0L,
                    traffic = TrafficStats(),
                )
            }
            _trafficRing.clear()
            _uiState.update { it.copy(trafficHistory = emptyList()) }
        }
    }

    /**
     * Temporary fake polling loop. Replace with real daemon IPC.
     */
    private fun startFakePolling() {
        viewModelScope.launch {
            var tick = 0L
            while (true) {
                delay(1000)
                val state = _uiState.value
                if (state.connectionState == ConnectionState.CONNECTED) {
                    tick++

                    // Fake traffic bump
                    val rxRate = (500_000L..5_000_000L).random()
                    val txRate = (50_000L..500_000L).random()
                    val newTraffic = state.traffic.copy(
                        rxBytes = state.traffic.rxBytes + rxRate,
                        txBytes = state.traffic.txBytes + txRate,
                        rxRate = rxRate,
                        txRate = txRate,
                    )

                    // Push normalized sample into ring buffer
                    val normalized = (rxRate / 5_000_000f).coerceIn(0f, 1f)
                    if (_trafficRing.size >= TRAFFIC_HISTORY_SIZE) {
                        _trafficRing.removeFirst()
                    }
                    _trafficRing.addLast(normalized)

                    _uiState.update {
                        it.copy(
                            traffic = newTraffic,
                            trafficHistory = _trafficRing.toList(),
                            uptimeSeconds = tick,
                        )
                    }
                }
            }
        }
    }

    fun applyDaemonStatus(status: DaemonStatus) {
        _uiState.update {
            it.copy(
                connectionState = status.state,
                activeNodeName = status.activeNodeName,
                traffic = status.traffic,
                dnsOperational = status.health.dnsOperational,
                uptimeSeconds = status.uptime,
            )
        }
    }
}
