package com.privstack.panel.ui.dashboard

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.i18n.UserMessageFormatter
import com.privstack.panel.model.BackendPhase
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
    val runtimePhase: BackendPhase = BackendPhase.STOPPED,
    val activeNodeName: String? = null,
    val activeNodeProtocol: String? = null,
    val traffic: TrafficStats = TrafficStats(),
    /** Ring buffer of normalized RX rate samples for the sparkline (0..1). */
    val trafficHistory: List<Float> = emptyList(),
    val egressIp: String? = null,
    val countryFlag: String? = null,
    val latencyMs: Int? = null,
    val dnsChecked: Boolean = false,
    val dnsOperational: Boolean = false,
    val operationalDegraded: Boolean = false,
    val operationalIssueMessage: String? = null,
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
    private val messages: UserMessageFormatter,
) : ViewModel() {

    private val _uiState = MutableStateFlow(DashboardUiState())
    val uiState: StateFlow<DashboardUiState> = _uiState.asStateFlow()

    /** Mutable ring buffer backing the sparkline. */
    private val _trafficRing = ArrayDeque<Float>(TRAFFIC_HISTORY_SIZE)

    init {
        observeDaemonStatus()
        observeDaemonConnectionState()
        observePollErrors()
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

                // If daemon becomes unreachable while we are waiting for status
                // or already connected, stop showing a stale spinner/state.
                if (unreachable &&
                    (_uiState.value.connectionState == ConnectionState.CONNECTED ||
                        _uiState.value.connectionState == ConnectionState.CONNECTING)
                ) {
                    _uiState.update { it.copy(connectionState = ConnectionState.UNKNOWN) }
                }
            }
        }
    }

    private fun observePollErrors() {
        viewModelScope.launch {
            statusRepository.lastPollError.collect { pollError ->
                if (pollError != null) {
                    _uiState.update { it.copy(errorMessage = pollError) }
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
            val showRuntimeHealth = status.shouldShowRuntimeHealth()
            val operationalDegraded = showRuntimeHealth &&
                status.health.healthy &&
                !status.health.operationalHealthy &&
                status.health.checkedAt > 0L
            val healthIssueMessage = messages.formatHealthIssue(
                status.health.lastCode,
                status.health.lastError,
                status.health.lastUserMessage,
                status.health.stageReport,
            )
            val lastOperationFailure = status.lastOperation
                ?.takeIf { operation -> !operation.succeeded && status.activeOperation == null }
                ?.let { operation ->
                    messages.formatOperationFailure(
                        operation.kind.operationNameRes(),
                        operation.errorMessage.ifBlank { operation.errorCode },
                    )
                }
            it.copy(
                connectionState = status.state,
                runtimePhase = status.health.phase,
                activeNodeName = status.activeNodeName,
                activeNodeProtocol = formatActiveNodeSubtitle(status),
                egressIp = status.egressIp,
                countryFlag = status.countryFlag,
                latencyMs = status.latencyMs,
                traffic = status.traffic,
                trafficHistory = _trafficRing.toList(),
                dnsChecked = showRuntimeHealth && status.health.checkedAt > 0L,
                dnsOperational = showRuntimeHealth && status.health.dnsOperational,
                operationalDegraded = operationalDegraded,
                operationalIssueMessage = if (operationalDegraded) {
                    healthIssueMessage
                } else {
                    null
                },
                uptimeSeconds = status.uptime,
                // Clear error when we get a successful status with a healthy state
                errorMessage = when {
                    operationalDegraded -> null
                    lastOperationFailure != null -> lastOperationFailure
                    status.state == ConnectionState.ERROR -> healthIssueMessage
                    else -> null
                },
            )
        }
    }

    private fun formatActiveNodeSubtitle(status: DaemonStatus): String? {
        return when (status.activeNodeMode) {
            "auto_selector" -> messages.get(com.privstack.panel.R.string.active_node_mode_auto)
            "manual" -> status.activeNodeProtocol?.let {
                messages.get(com.privstack.panel.R.string.active_node_mode_manual, it)
            }
            "manual_missing" -> messages.get(com.privstack.panel.R.string.active_node_mode_missing)
            "single_node" -> status.activeNodeProtocol
            else -> status.activeNodeProtocol
        }
    }

    private fun DaemonStatus.shouldShowRuntimeHealth(): Boolean {
        val runtimeSettled = health.phase !in TRANSIENT_OR_STOPPED_PHASES
        return runtimeSettled &&
            (state == ConnectionState.CONNECTED || state == ConnectionState.ERROR)
    }

    override fun onCleared() {
        super.onCleared()
        statusRepository.stopPolling()
    }
}

private fun String.operationNameRes(): Int = when (this) {
    "start" -> com.privstack.panel.R.string.operation_start
    "stop" -> com.privstack.panel.R.string.operation_stop
    "restart", "reload" -> com.privstack.panel.R.string.operation_reload
    "reset" -> com.privstack.panel.R.string.reset_network_rules
    else -> com.privstack.panel.R.string.operation_runtime
}

private val TRANSIENT_OR_STOPPED_PHASES = setOf(
    BackendPhase.STOPPED,
    BackendPhase.APPLYING,
    BackendPhase.STARTING,
    BackendPhase.CONFIG_CHECKED,
    BackendPhase.CORE_SPAWNED,
    BackendPhase.CORE_LISTENING,
    BackendPhase.STOPPING,
    BackendPhase.RESETTING,
)
