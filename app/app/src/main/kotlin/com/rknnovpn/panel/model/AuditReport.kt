package com.rknnovpn.panel.model

import kotlinx.serialization.Serializable

/**
 * Result of a daemon privacy/security audit (`daemonctl audit`).
 *
 * The daemon inspects the running configuration, routing rules, DNS settings,
 * and detected traffic patterns to produce a list of findings.
 */
@Serializable
data class AuditReport(
    /** Unique run identifier. */
    val auditId: String,
    /** Epoch millis when the audit was performed. */
    val timestamp: Long,
    /** Overall score 0-100 (100 = no issues found). */
    val score: Int,
    /** Individual findings, sorted by severity descending. */
    val findings: List<AuditFinding> = emptyList(),
    /** Summary counts per severity level. */
    val summary: AuditSummary = AuditSummary()
) {
    /** True when no CRITICAL or HIGH findings exist. */
    val isPassing: Boolean
        get() = findings.none { it.severity == Severity.CRITICAL || it.severity == Severity.HIGH }
}

@Serializable
data class AuditFinding(
    /** Machine-readable finding code, e.g. "DNS_LEAK_DETECTED". */
    val code: String,
    /** Human-readable title. */
    val title: String,
    /** Detailed description of what was found. */
    val description: String,
    val severity: Severity,
    val category: AuditCategory,
    /** Actionable recommendation to resolve this finding. */
    val recommendation: String? = null,
    /**
     * Affected resource identifier (package name, domain, IP, config key).
     * Null for systemic findings.
     */
    val affectedResource: String? = null
)

@Serializable
enum class Severity {
    /** Immediate privacy/security risk requiring action. */
    CRITICAL,
    /** Significant risk that should be addressed soon. */
    HIGH,
    /** Moderate risk or sub-optimal configuration. */
    MEDIUM,
    /** Minor issue or informational note. */
    LOW,
    /** Purely informational, no action needed. */
    INFO;

    /** True for severities that should block a "healthy" status. */
    val isActionRequired: Boolean
        get() = this == CRITICAL || this == HIGH
}

@Serializable
enum class AuditCategory {
    /** DNS configuration and leak detection. */
    DNS,
    /** Traffic routing and split-tunnel correctness. */
    ROUTING,
    /** TLS/certificate and encryption issues. */
    ENCRYPTION,
    /** App-level leak detection (WebRTC, captive portal, etc.). */
    LEAK,
    /** Configuration hygiene (unused nodes, weak ciphers, etc.). */
    CONFIG,
    /** System-level issues (SELinux, permissions, etc.). */
    SYSTEM
}

@Serializable
data class AuditSummary(
    val critical: Int = 0,
    val high: Int = 0,
    val medium: Int = 0,
    val low: Int = 0,
    val info: Int = 0
) {
    val total: Int get() = critical + high + medium + low + info
}
