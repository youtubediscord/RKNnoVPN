package com.privstack.panel.repository

import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.ipc.PollingStatusSource
import com.privstack.panel.model.AuditReport
import com.privstack.panel.model.DaemonConnectionState
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.HealthResult
import kotlinx.coroutines.flow.StateFlow
import javax.inject.Inject
import javax.inject.Singleton

/**
 * Repository wrapping [PollingStatusSource] and caching the last daemon status
 * for instant display.
 *
 * Provides:
 * - [status]: latest daemon status snapshot (StateFlow, survives Activity pause)
 * - [connectionState]: Panel-to-daemon connectivity (StateFlow)
 * - One-shot operations: [start], [stop], [reload], [health], [audit]
 *
 * The polling lifecycle is driven by the UI layer:
 * ```
 * onResume  -> statusRepository.startPolling()
 * onPause   -> statusRepository.stopPolling()
 * ```
 */
@Singleton
class StatusRepository @Inject constructor(
    private val poller: PollingStatusSource,
    private val client: DaemonClient
) {
    // ---- Exposed state (delegates to PollingStatusSource) ----

    /** Latest daemon status, or null if never polled / daemon unreachable. */
    val status: StateFlow<DaemonStatus?> = poller.status

    /** Connectivity state between Panel and the daemon process. */
    val connectionState: StateFlow<DaemonConnectionState> = poller.connectionState

    // ---- Polling lifecycle ----

    /** Start adaptive polling. Safe to call repeatedly. */
    fun startPolling() = poller.startPolling()

    /** Stop polling. Last status remains cached for instant display. */
    fun stopPolling() = poller.stopPolling()

    /** Force a single immediate status poll (e.g. after user triggers start/stop). */
    fun pollNow() = poller.pollNow()

    // ---- One-shot daemon commands ----

    /**
     * Start the proxy connection and immediately poll for updated status.
     * @return Success message or human-readable error.
     */
    suspend fun start(): CommandOutcome {
        val result = client.start()
        poller.pollNow()
        return result.toOutcome("Start")
    }

    /**
     * Stop the proxy connection and immediately poll for updated status.
     */
    suspend fun stop(): CommandOutcome {
        val result = client.stop()
        poller.pollNow()
        return result.toOutcome("Stop")
    }

    /**
     * Reload daemon configuration without full restart.
     */
    suspend fun reload(): CommandOutcome {
        val result = client.reload()
        poller.pollNow()
        return result.toOutcome("Reload")
    }

    suspend fun networkReset(): CommandOutcome {
        val result = client.networkReset()
        poller.pollNow()
        return result.toOutcome("Network reset")
    }

    /**
     * Run a daemon health check.
     * On success, also updates the cached status with the health info.
     */
    suspend fun health(): DaemonClientResult<DaemonStatus> {
        val result = client.health()
        if (result is DaemonClientResult.Ok) {
            // The health response is a full DaemonStatus; the poller will
            // pick it up on the next tick, but we can hint an immediate poll.
            poller.pollNow()
        }
        return result
    }

    /**
     * Run a privacy/security audit against the current configuration.
     */
    suspend fun audit(): DaemonClientResult<AuditReport> = client.audit()

    /**
     * Fetch recent log lines from the daemon.
     */
    suspend fun logs(
        lines: Int = 100,
        level: String = "info"
    ): DaemonClientResult<List<String>> = client.logs(lines, level)

    // ---- Convenience accessors ----

    /** Cached health result from the last status poll (may be stale). */
    val lastHealth: HealthResult?
        get() = status.value?.health

    /** True if the daemon is currently reachable and the proxy is connected. */
    val isConnected: Boolean
        get() = connectionState.value == DaemonConnectionState.REACHABLE &&
                status.value?.state == com.privstack.panel.model.ConnectionState.CONNECTED
}

// ---- Result types ----

/**
 * Simplified outcome for fire-and-forget commands (start, stop, reload).
 */
sealed class CommandOutcome {
    data object Success : CommandOutcome()
    data class Failed(val message: String) : CommandOutcome()

    val isSuccess: Boolean get() = this is Success
}

private fun <T> DaemonClientResult<T>.toOutcome(label: String): CommandOutcome = when (this) {
    is DaemonClientResult.Ok -> CommandOutcome.Success
    is DaemonClientResult.DaemonError -> CommandOutcome.Failed("$label failed: $message (code $code)")
    is DaemonClientResult.RootDenied -> CommandOutcome.Failed("Root access denied")
    is DaemonClientResult.Timeout -> CommandOutcome.Failed("$label timed out")
    is DaemonClientResult.DaemonNotFound -> CommandOutcome.Failed("Daemon not installed")
    is DaemonClientResult.ParseError -> CommandOutcome.Failed("Invalid daemon response")
    is DaemonClientResult.Failure -> CommandOutcome.Failed("Unexpected error: ${throwable.message}")
}
