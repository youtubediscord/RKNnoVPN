package com.rknnovpn.panel.repository

import com.rknnovpn.panel.ipc.DaemonClient
import com.rknnovpn.panel.ipc.DaemonClientResult
import com.rknnovpn.panel.ipc.PollingStatusSource
import com.rknnovpn.panel.ipc.SelfCheckSummary
import com.rknnovpn.panel.i18n.UserMessageFormatter
import com.rknnovpn.panel.R
import com.rknnovpn.panel.model.AuditReport
import com.rknnovpn.panel.model.BackendStatusV2
import com.rknnovpn.panel.model.DaemonConnectionState
import com.rknnovpn.panel.model.DaemonStatus
import com.rknnovpn.panel.model.HealthResult
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
    private val client: DaemonClient,
    private val messages: UserMessageFormatter,
) {
    // ---- Exposed state (delegates to PollingStatusSource) ----

    /** Latest daemon status, or null if never polled / daemon unreachable. */
    val status: StateFlow<DaemonStatus?> = poller.status

    /** Connectivity state between Panel and the daemon process. */
    val connectionState: StateFlow<DaemonConnectionState> = poller.connectionState

    /** Last human-readable poll failure from the daemon status loop. */
    val lastPollError: StateFlow<String?> = poller.lastError

    // ---- Polling lifecycle ----

    /** Start adaptive polling. Safe to call repeatedly. */
    fun startPolling() = poller.startPolling()

    /** Stop polling. Last status remains cached for instant display. */
    fun stopPolling() = poller.stopPolling()

    /** Force a single immediate status poll (e.g. after user triggers start/stop). */
    fun pollNow() = poller.pollNow()

    fun publishBackendStatus(status: BackendStatusV2) = poller.publishBackendStatus(status)

    // ---- One-shot daemon commands ----

    /**
     * Start the proxy connection and immediately poll for updated status.
     * @return Success message or human-readable error.
     */
    suspend fun start(): CommandOutcome {
        val result = client.start()
        publishOrPoll(result)
        return toOutcome(result, R.string.operation_start)
    }

    /**
     * Stop the proxy connection and immediately poll for updated status.
     */
    suspend fun stop(): CommandOutcome {
        val result = client.stop()
        publishOrPoll(result)
        return toOutcome(result, R.string.operation_stop)
    }

    /**
     * Reload daemon configuration without full restart.
     */
    suspend fun reload(): CommandOutcome {
        val result = client.reload()
        publishOrPoll(result)
        return toOutcome(result, R.string.operation_reload)
    }

    suspend fun networkReset(): DaemonClientResult<BackendStatusV2> {
        val result = client.networkReset()
        publishOrPoll(result)
        return result
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
     * Fetch the daemon's concise repair summary without the full diagnostics bundle.
     */
    suspend fun selfCheck(): DaemonClientResult<SelfCheckSummary> = client.selfCheck()

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
                status.value?.state == com.rknnovpn.panel.model.ConnectionState.CONNECTED

    private fun <T> toOutcome(result: DaemonClientResult<T>, operationResId: Int): CommandOutcome = when (result) {
        is DaemonClientResult.Ok -> CommandOutcome.Success
        else -> CommandOutcome.Failed(
            messages.formatOperationFailure(
                operationResId,
                messages.formatDaemonFailure(result),
            )
        )
    }

    private fun publishOrPoll(result: DaemonClientResult<BackendStatusV2>) {
        when (result) {
            is DaemonClientResult.Ok -> poller.publishBackendStatus(result.data)
            else -> poller.pollNow()
        }
    }
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
