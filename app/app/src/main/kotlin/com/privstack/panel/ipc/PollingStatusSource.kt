package com.privstack.panel.ipc

import android.util.Log
import com.privstack.panel.model.ConnectionState
import com.privstack.panel.model.DaemonConnectionState
import com.privstack.panel.model.DaemonStatus
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import javax.inject.Inject
import javax.inject.Singleton

/**
 * Coroutine-based daemon status poller.
 *
 * Polling intervals adapt to the connection state:
 * - **Connected / Connecting**: every [FAST_INTERVAL_MS] (2 s) for responsive traffic stats.
 * - **Disconnected / Error / Unknown**: every [SLOW_INTERVAL_MS] (10 s) to conserve resources.
 *
 * The poller is explicitly started/stopped by the UI layer (typically in
 * Activity.onResume / onPause) so we never poll in the background.
 */
@Singleton
class PollingStatusSource @Inject constructor(
    private val client: DaemonClient
) {
    companion object {
        private const val TAG = "PollingStatusSource"
        private const val FAST_INTERVAL_MS = 2_000L
        private const val SLOW_INTERVAL_MS = 10_000L
        /** After this many consecutive failures we switch to slow polling. */
        private const val FAILURE_THRESHOLD = 3
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var pollingJob: Job? = null

    // ---- Exposed state ----

    private val _status = MutableStateFlow<DaemonStatus?>(null)
    /** Latest daemon status snapshot, or null if never polled / unreachable. */
    val status: StateFlow<DaemonStatus?> = _status.asStateFlow()

    private val _connectionState = MutableStateFlow(DaemonConnectionState.IDLE)
    /** Connectivity state between this app and the daemon process. */
    val connectionState: StateFlow<DaemonConnectionState> = _connectionState.asStateFlow()

    private var consecutiveFailures = 0

    // ---- Lifecycle ----

    /**
     * Start the polling loop. Safe to call multiple times; subsequent calls
     * are no-ops if already running.
     */
    fun startPolling() {
        if (pollingJob?.isActive == true) return
        Log.d(TAG, "Starting status polling")
        consecutiveFailures = 0
        pollingJob = scope.launch { pollLoop() }
    }

    /**
     * Stop the polling loop. The last emitted status remains available
     * in [status] for instant display on next resume.
     */
    fun stopPolling() {
        Log.d(TAG, "Stopping status polling")
        pollingJob?.cancel()
        pollingJob = null
        _connectionState.value = DaemonConnectionState.IDLE
    }

    /**
     * Force a single immediate status poll outside the regular cadence.
     * Useful after the user triggers start/stop so the UI updates instantly.
     */
    fun pollNow() {
        scope.launch { pollOnce() }
    }

    // ---- Internal ----

    private suspend fun pollLoop() {
        while (scope.isActive) {
            pollOnce()
            val interval = computeInterval()
            delay(interval)
        }
    }

    private suspend fun pollOnce() {
        _connectionState.value = DaemonConnectionState.POLLING

        when (val result = client.status()) {
            is DaemonClientResult.Ok -> {
                consecutiveFailures = 0
                _status.value = result.data
                _connectionState.value = DaemonConnectionState.REACHABLE
            }
            is DaemonClientResult.RootDenied -> {
                consecutiveFailures++
                _connectionState.value = DaemonConnectionState.UNREACHABLE
                Log.w(TAG, "Root denied during status poll")
            }
            is DaemonClientResult.Timeout -> {
                consecutiveFailures++
                _connectionState.value = DaemonConnectionState.UNREACHABLE
                Log.w(TAG, "Status poll timed out")
            }
            is DaemonClientResult.DaemonNotFound -> {
                consecutiveFailures++
                _connectionState.value = DaemonConnectionState.UNREACHABLE
                Log.w(TAG, "Daemon binary not found")
            }
            is DaemonClientResult.DaemonError -> {
                consecutiveFailures++
                _connectionState.value = DaemonConnectionState.UNREACHABLE
                Log.w(TAG, "Daemon error: ${result.code} ${result.message}")
            }
            is DaemonClientResult.ParseError -> {
                consecutiveFailures++
                _connectionState.value = DaemonConnectionState.REACHABLE
                Log.w(TAG, "Failed to parse status response", result.cause)
            }
            is DaemonClientResult.Failure -> {
                consecutiveFailures++
                _connectionState.value = DaemonConnectionState.UNREACHABLE
                Log.e(TAG, "Unexpected failure during poll", result.throwable)
            }
        }
    }

    private fun computeInterval(): Long {
        // If daemon is unreachable for several polls, slow down
        if (consecutiveFailures >= FAILURE_THRESHOLD) return SLOW_INTERVAL_MS

        // Adapt based on daemon's connection state
        val daemonState = _status.value?.state
        return when (daemonState) {
            ConnectionState.CONNECTED,
            ConnectionState.CONNECTING -> FAST_INTERVAL_MS
            else -> SLOW_INTERVAL_MS
        }
    }
}
