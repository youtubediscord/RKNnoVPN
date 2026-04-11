package com.privstack.panel.ui.dashboard

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.model.ConnectionState
import com.privstack.panel.model.DaemonConnectionState
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.TrafficStats
import com.privstack.panel.repository.CommandOutcome
import com.privstack.panel.repository.StatusRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.delay
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
    /** Error message from the last daemon operation, or null. */
    val errorMessage: String? = null,
    /** True when the daemon process is not reachable at all. */
    val daemonUnreachable: Boolean = false,
)

private const val TRAFFIC_HISTORY_SIZE = 120
private const val TAG = "DashboardViewModel"

/** Peak rate used to normalize sparkline samples. */
private const val PEAK_RATE_FOR_NORMALIZATION = 10_000_000f // 10 MB/s

@HiltViewModel
class DashboardViewModel @Inject constructor(
    private val statusRepository: StatusRepository,
) : ViewModel() {

    private val _uiState = MutableStateFlow(DashboardUiState())
    val uiState: StateFlow<DashboardUiState> = _uiState.asStateFlow()

    /** Mutable ring buffer backing the sparkline. */
    private val _trafficRing = ArrayDeque<Float>(TRAFFIC_HISTORY_SIZE)

    init {
        observeDaemonStatus()
        observeDaemonConnectionState()
        statusRepository.startPolling()
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
            _uiState.update { it.copy(isRefreshing = true, errorMessage = null) }
            statusRepository.pollNow()
            // Give the poller a moment to complete, then clear refresh flag.
            // The actual data update comes via the status flow observer.
            delay(500)
            _uiState.update { it.copy(isRefreshing = false) }
        }
    }

    // ---- Internal ----

    private fun connect() {
        viewModelScope.launch {
            _uiState.update {
                it.copy(connectionState = ConnectionState.CONNECTING, errorMessage = null)
            }
            when (val outcome = statusRepository.start()) {
                is CommandOutcome.Success -> {
                    Log.d(TAG, "Start command succeeded; waiting for status poll")
                    // Status will be updated via the observer when the poll fires
                }
                is CommandOutcome.Failed -> {
                    Log.w(TAG, "Start failed: ${outcome.message}")
                    _uiState.update {
                        it.copy(
                            connectionState = ConnectionState.ERROR,
                            errorMessage = outcome.message,
                        )
                    }
                }
            }
        }
    }

    private fun disconnect() {
        viewModelScope.launch {
            _uiState.update { it.copy(errorMessage = null) }
            when (val outcome = statusRepository.stop()) {
                is CommandOutcome.Success -> {
                    Log.d(TAG, "Stop command succeeded; waiting for status poll")
                    // Clear traffic history immediately for snappy feel
                    _trafficRing.clear()
                    _uiState.update {
                        it.copy(
                            trafficHistory = emptyList(),
                        )
                    }
                }
                is CommandOutcome.Failed -> {
                    Log.w(TAG, "Stop failed: ${outcome.message}")
                    _uiState.update {
                        it.copy(errorMessage = outcome.message)
                    }
                }
            }
        }
    }

    /**
     * Observe daemon status snapshots from [StatusRepository] and project
     * them into [DashboardUiState].
     */
    private fun observeDaemonStatus() {
        viewModelScope.launch {
            statusRepository.status.collect { status ->
                if (status != null) {
                    applyDaemonStatus(status)
                }
            }
        }
    }

    /**
     * Observe the Panel-to-daemon connectivity state so we can show
     * "daemon unreachable" in the UI.
     */
    private fun observeDaemonConnectionState() {
        viewModelScope.launch {
            statusRepository.connectionState.collect { connState ->
                val unreachable = connState == DaemonConnectionState.UNREACHABLE
                _uiState.update { it.copy(daemonUnreachable = unreachable) }

                // If daemon becomes unreachable and we were connected,
                // show UNKNOWN so the UI reflects the loss of contact.
                if (unreachable && _uiState.value.connectionState == ConnectionState.CONNECTED) {
                    _uiState.update { it.copy(connectionState = ConnectionState.UNKNOWN) }
                }
            }
        }
    }

    fun applyDaemonStatus(status: DaemonStatus) {
        // Push a normalized RX rate sample into the sparkline ring buffer
        val rxRate = status.traffic.rxRate
        if (status.state == ConnectionState.CONNECTED && rxRate > 0) {
            val normalized = (rxRate / PEAK_RATE_FOR_NORMALIZATION).coerceIn(0f, 1f)
            if (_trafficRing.size >= TRAFFIC_HISTORY_SIZE) {
                _trafficRing.removeFirst()
            }
            _trafficRing.addLast(normalized)
        } else if (status.state == ConnectionState.DISCONNECTED) {
            _trafficRing.clear()
        }

        _uiState.update {
            it.copy(
                connectionState = status.state,
                activeNodeName = status.activeNodeName,
                activeNodeProtocol = status.activeNodeProtocol,
                traffic = status.traffic,
                trafficHistory = _trafficRing.toList(),
                dnsOperational = status.health.dnsOperational,
                uptimeSeconds = status.uptime,
                // Clear error when we get a successful status with a healthy state
                errorMessage = if (status.state == ConnectionState.ERROR) {
                    status.health.lastError ?: it.errorMessage
                } else {
                    null
                },
            )
        }
    }

    override fun onCleared() {
        super.onCleared()
        statusRepository.stopPolling()
    }
}
