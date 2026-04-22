package com.privstack.panel.model

import kotlinx.serialization.Serializable
import kotlinx.serialization.SerialName

/**
 * Snapshot of the daemon's runtime state, returned by `privctl status`.
 */
@Serializable
data class DaemonStatus(
    val state: ConnectionState,
    @SerialName("active_node_id")
    val activeNodeId: String? = null,
    @SerialName("active_node_name")
    val activeNodeName: String? = null,
    @SerialName("active_node_protocol")
    val activeNodeProtocol: String? = null,
    @SerialName("egress_ip")
    val egressIp: String? = null,
    @SerialName("country_flag")
    val countryFlag: String? = null,
    @SerialName("latency_ms")
    val latencyMs: Int? = null,
    val uptime: Long = 0L,
    val traffic: TrafficStats = TrafficStats(),
    val health: HealthResult = HealthResult()
)

/**
 * Connection lifecycle states reported by the daemon.
 */
@Serializable
enum class ConnectionState {
    /** Daemon is running but no proxy connection is active. */
    DISCONNECTED,
    /** Proxy core is starting up. */
    CONNECTING,
    /** Proxy core is running and traffic is flowing. */
    CONNECTED,
    /** An error occurred; see DaemonStatus.health for details. */
    ERROR,
    /** Daemon process is not reachable at all. */
    UNKNOWN;

    val isActive: Boolean
        get() = this == CONNECTED || this == CONNECTING
}

/**
 * Cumulative traffic counters since the last connection start.
 */
@Serializable
data class TrafficStats(
    /** Total bytes uploaded since connection start. */
    val txBytes: Long = 0L,
    /** Total bytes downloaded since connection start. */
    val rxBytes: Long = 0L,
    /** Current upload speed in bytes/sec. */
    val txRate: Long = 0L,
    /** Current download speed in bytes/sec. */
    val rxRate: Long = 0L
) {
    /** Total transferred bytes in both directions. */
    val totalBytes: Long get() = txBytes + rxBytes
}

/**
 * Result of a daemon health check (`privctl health`).
 */
@Serializable
data class HealthResult(
    val healthy: Boolean = false,
    val coreRunning: Boolean = false,
    val tunActive: Boolean = false,
    val dnsOperational: Boolean = false,
    val lastError: String? = null,
    val checkedAt: Long = 0L
)

/**
 * Connectivity state between the Panel app and the daemon process.
 * This is distinct from [ConnectionState], which is the daemon's own
 * proxy-connection state.
 */
enum class DaemonConnectionState {
    /** We have not attempted to reach the daemon yet. */
    IDLE,
    /** A status poll is in flight. */
    POLLING,
    /** The most recent poll succeeded. */
    REACHABLE,
    /** The daemon did not respond (timeout, root denied, not installed). */
    UNREACHABLE
}
