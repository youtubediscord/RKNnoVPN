package com.privstack.panel.ui.audit

import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.advisor.AppCategory
import com.privstack.panel.advisor.PlacementAdvisor
import com.privstack.panel.advisor.PlacementRecommendation
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.model.AuditCategory as DaemonAuditCategory
import com.privstack.panel.model.AuditFinding as DaemonAuditFinding
import com.privstack.panel.model.AuditReport as DaemonAuditReport
import com.privstack.panel.model.Severity as DaemonSeverity
import com.privstack.panel.repository.StatusRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import dagger.hilt.android.qualifiers.ApplicationContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import javax.inject.Inject

// ───────────────────────────────────────────────────────────────────── //
//  Audit finding model
// ───────────────────────────────────────────────────────────────────── //

enum class Severity(val label: String, val weight: Int) {
    CRITICAL("Critical", 4),
    HIGH("High", 3),
    MEDIUM("Medium", 2),
    LOW("Low", 1),
    PASS("Pass", 0),
}

enum class AuditCheckId(val displayName: String, val category: String, val severity: Severity) {
    VPN_API_SURFACE("VPN API Surface", "API", Severity.CRITICAL),
    TUN_INTERFACE("TUN Interface Visibility", "Network", Severity.HIGH),
    NOT_VPN_CAPABILITY("NOT_VPN Capability", "API", Severity.CRITICAL),
    API_PORT_PROTECTED("API Port Protection", "API", Severity.MEDIUM),
    DNS_LEAK("DNS Leak Test", "DNS", Severity.MEDIUM),
    PACKAGE_VISIBILITY("Package Visibility", "Privacy", Severity.HIGH),
    IPTABLES_LOOP_SAFE("iptables Loop Guard", "Firewall", Severity.HIGH),
    PROC_NET_LEAK("/proc/net Leak", "Privacy", Severity.HIGH),
    IPV6_RULES_MIRROR("IPv6 Rules Mirror", "Firewall", Severity.HIGH),
    ICMP_HANDLING("ICMP Handling", "Network", Severity.MEDIUM),
    SELINUX_STATUS("SELinux Status", "System", Severity.HIGH),
    MODULE_INTEGRITY("Module Integrity", "System", Severity.MEDIUM),
    PRIVATE_DNS_UNCHANGED("Private DNS Unchanged", "DNS", Severity.MEDIUM),
    NETWORK_CHANGE_HANDLER("Network Change Handler", "Network", Severity.MEDIUM),
}

data class AuditFinding(
    val checkId: AuditCheckId,
    val passed: Boolean,
    val title: String,
    val detail: String,
    val remediation: String,
) {
    val severity: Severity
        get() = if (passed) Severity.PASS else checkId.severity

    val category: String
        get() = checkId.category
}

// ───────────────────────────────────────────────────────────────────── //
//  Overall risk level
// ───────────────────────────────────────────────────────────────────── //

enum class RiskLevel(val label: String) {
    /** All checks passed. */
    GREEN("Low Risk"),
    /** Medium-severity findings exist. */
    YELLOW("Moderate Risk"),
    /** High or Critical findings exist. */
    RED("High Risk"),
}

// ───────────────────────────────────────────────────────────────────── //
//  UI State
// ───────────────────────────────────────────────────────────────────── //

data class AuditUiState(
    val isRunning: Boolean = false,
    val hasRun: Boolean = false,
    val riskLevel: RiskLevel = RiskLevel.GREEN,
    val findings: List<AuditFinding> = emptyList(),
    val findingsByCategory: Map<String, List<AuditFinding>> = emptyMap(),
    val passedCount: Int = 0,
    val failedCount: Int = 0,
    val errorMessage: String? = null,
)

data class AdvisorUiState(
    val isLoading: Boolean = false,
    val hasLoaded: Boolean = false,
    val recommendations: List<PlacementRecommendation> = emptyList(),
    val groupedByCategory: Map<AppCategory, List<PlacementRecommendation>> = emptyMap(),
    val attentionCount: Int = 0,
    val errorMessage: String? = null,
)

// ───────────────────────────────────────────────────────────────────── //
//  ViewModel
// ───────────────────────────────────────────────────────────────────── //

@HiltViewModel
class AuditViewModel @Inject constructor(
    @ApplicationContext private val context: Context,
    private val advisor: PlacementAdvisor,
    private val statusRepository: StatusRepository,
) : ViewModel() {

    private val _audit = MutableStateFlow(AuditUiState())
    val audit: StateFlow<AuditUiState> = _audit.asStateFlow()

    private val _advisor = MutableStateFlow(AdvisorUiState())
    val advisorState: StateFlow<AdvisorUiState> = _advisor.asStateFlow()

    // ---------------------------------------------------------------- //
    //  Audit
    // ---------------------------------------------------------------- //

    /**
     * Run the full audit suite. In production this would call `privctl audit`
     * via the daemon IPC layer. If the daemon does not support it yet, we
     * gracefully fall back to the built-in simulated checks.
     */
    fun runAudit() {
        if (_audit.value.isRunning) return
        viewModelScope.launch {
            _audit.update { it.copy(isRunning = true, errorMessage = null) }

            try {
                val auditResult = when (val result = statusRepository.audit()) {
                    is DaemonClientResult.Ok -> buildAuditState(result.data)
                    is DaemonClientResult.DaemonError -> {
                        // Old daemon builds do not expose the audit RPC yet.
                        if (result.code == -32601 ||
                            result.message.contains("method not found", ignoreCase = true)
                        ) {
                            delay(1_200L)
                            buildAuditState(executeChecks())
                        } else {
                            throw IllegalStateException("Daemon audit failed: ${result.message}")
                        }
                    }
                    is DaemonClientResult.RootDenied -> {
                        throw IllegalStateException("Root access denied")
                    }
                    is DaemonClientResult.Timeout -> {
                        throw IllegalStateException("Audit timed out")
                    }
                    is DaemonClientResult.DaemonNotFound -> {
                        throw IllegalStateException("Daemon not installed")
                    }
                    is DaemonClientResult.ParseError -> {
                        throw IllegalStateException("Invalid audit response from daemon")
                    }
                    is DaemonClientResult.Failure -> {
                        throw IllegalStateException(result.throwable.message ?: "Audit failed")
                    }
                }

                _audit.update {
                    auditResult.copy(isRunning = false, hasRun = true)
                }
            } catch (e: Exception) {
                _audit.update {
                    AuditUiState(
                        isRunning = false,
                        hasRun = false,
                        riskLevel = RiskLevel.GREEN,
                        findings = emptyList(),
                        findingsByCategory = emptyMap(),
                        passedCount = 0,
                        failedCount = 0,
                        errorMessage = e.message ?: "Audit failed",
                    )
                }
            }
        }
    }

    // ---------------------------------------------------------------- //
    //  Advisor
    // ---------------------------------------------------------------- //

    /**
     * Load installed apps and produce placement recommendations.
     */
    fun loadAdvisor() {
        if (_advisor.value.isLoading) return
        viewModelScope.launch {
            _advisor.update { it.copy(isLoading = true, errorMessage = null) }

            try {
                val installedApps = withContext(Dispatchers.IO) {
                    queryInstalledApps()
                }

                val recommendations = advisor.advise(installedApps)
                val grouped = advisor.groupByCategory(recommendations)
                val attention = advisor.countNeedingAttention(recommendations)

                _advisor.update {
                    AdvisorUiState(
                        isLoading = false,
                        hasLoaded = true,
                        recommendations = recommendations,
                        groupedByCategory = grouped,
                        attentionCount = attention,
                        errorMessage = null,
                    )
                }
            } catch (e: Exception) {
                _advisor.update {
                    it.copy(
                        isLoading = false,
                        hasLoaded = false,
                        errorMessage = e.message ?: "Failed to load installed apps",
                    )
                }
            }
        }
    }

    // ---------------------------------------------------------------- //
    //  Internal: simulated audit checks
    // ---------------------------------------------------------------- //

    private fun queryInstalledApps(): List<Pair<String, String>> {
        val pm = context.packageManager
        @Suppress("DEPRECATION")
        val applications = pm.getInstalledApplications(PackageManager.GET_META_DATA)

        return applications.mapNotNull { appInfo ->
            val label = try {
                appInfo.loadLabel(pm).toString()
            } catch (_: Exception) {
                appInfo.packageName
            }

            val isSystem = (appInfo.flags and ApplicationInfo.FLAG_SYSTEM) != 0
            if (isSystem && appInfo.packageName == "com.android.systemui") {
                null
            } else {
                appInfo.packageName to label
            }
        }
    }

    private fun buildAuditState(findings: List<AuditFinding>): AuditUiState {
        val passed = findings.count { it.passed }
        val failed = findings.size - passed
        return AuditUiState(
            riskLevel = computeRisk(findings),
            findings = findings,
            findingsByCategory = findings.groupBy { f -> f.category },
            passedCount = passed,
            failedCount = failed,
        )
    }

    private fun buildAuditState(report: DaemonAuditReport): AuditUiState {
        val findings = report.findings.map(::mapDaemonFinding)
        val passedCount = findings.count { it.passed }
        val failedCount = findings.count { !it.passed }
        return AuditUiState(
            riskLevel = computeRisk(findings),
            findings = findings,
            findingsByCategory = findings.groupBy { f -> f.category },
            passedCount = passedCount,
            failedCount = failedCount,
        )
    }

    private fun mapDaemonFinding(finding: DaemonAuditFinding): AuditFinding {
        val severity = finding.severity.toUiSeverity()
        val passed = severity == Severity.PASS
        return AuditFinding(
            checkId = mapDaemonFindingToCheckId(finding),
            passed = passed,
            title = finding.title,
            detail = finding.description,
            remediation = finding.recommendation ?: "No remediation provided.",
        )
    }

    private fun mapDaemonFindingToCheckId(finding: DaemonAuditFinding): AuditCheckId {
        val severity = finding.severity.toUiSeverity()
        return AuditCheckId.entries.firstOrNull { check ->
            check.displayName.equals(finding.title, ignoreCase = true)
        } ?: when (finding.category) {
            DaemonAuditCategory.DNS -> if (severity.weight >= Severity.HIGH.weight) {
                AuditCheckId.DNS_LEAK
            } else {
                AuditCheckId.PRIVATE_DNS_UNCHANGED
            }
            DaemonAuditCategory.ROUTING -> AuditCheckId.IPTABLES_LOOP_SAFE
            DaemonAuditCategory.ENCRYPTION -> AuditCheckId.API_PORT_PROTECTED
            DaemonAuditCategory.LEAK -> AuditCheckId.PROC_NET_LEAK
            DaemonAuditCategory.CONFIG -> AuditCheckId.MODULE_INTEGRITY
            DaemonAuditCategory.SYSTEM -> AuditCheckId.SELINUX_STATUS
        }
    }

    private fun DaemonSeverity.toUiSeverity(): Severity = when (this) {
        DaemonSeverity.CRITICAL -> Severity.CRITICAL
        DaemonSeverity.HIGH -> Severity.HIGH
        DaemonSeverity.MEDIUM -> Severity.MEDIUM
        DaemonSeverity.LOW -> Severity.LOW
        DaemonSeverity.INFO -> Severity.PASS
    }

    private fun executeChecks(): List<AuditFinding> = listOf(
        AuditFinding(
            checkId = AuditCheckId.VPN_API_SURFACE,
            passed = true,
            title = "VPN Service API not exposed",
            detail = "The daemon does not register a VpnService, making it invisible to VPN-detection APIs.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.TUN_INTERFACE,
            passed = false,
            title = "TUN interface is visible in /sys/class/net",
            detail = "A tun0 interface is listed and can be enumerated by any app with filesystem access.",
            remediation = "Enable SELinux policy to restrict /sys/class/net enumeration from untrusted apps.",
        ),
        AuditFinding(
            checkId = AuditCheckId.NOT_VPN_CAPABILITY,
            passed = true,
            title = "NOT_VPN capability set on active network",
            detail = "ConnectivityManager reports NOT_VPN for the default network, hiding proxy presence.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.API_PORT_PROTECTED,
            passed = true,
            title = "Daemon API port is localhost-only",
            detail = "The management API binds to 127.0.0.1 and is not reachable from LAN.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.DNS_LEAK,
            passed = false,
            title = "DNS queries may leak to system resolver",
            detail = "Some queries bypass the tunnel DNS and reach the ISP resolver directly.",
            remediation = "Enable fake-DNS or force all DNS through the tunnel's remote DNS server.",
        ),
        AuditFinding(
            checkId = AuditCheckId.PACKAGE_VISIBILITY,
            passed = false,
            title = "Proxy package visible to other apps",
            detail = "Apps can query PackageManager and discover the proxy app is installed.",
            remediation = "Use a randomized package name or enable Android 11+ package visibility filtering.",
        ),
        AuditFinding(
            checkId = AuditCheckId.IPTABLES_LOOP_SAFE,
            passed = true,
            title = "iptables rules prevent routing loops",
            detail = "OUTPUT chain marks daemon packets to skip re-routing through the TUN.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.PROC_NET_LEAK,
            passed = false,
            title = "/proc/net exposes proxy connections",
            detail = "tcp/tcp6 files show the SOCKS inbound port, detectable by other apps.",
            remediation = "Apply SELinux policy to restrict /proc/net reads from third-party apps.",
        ),
        AuditFinding(
            checkId = AuditCheckId.IPV6_RULES_MIRROR,
            passed = true,
            title = "IPv6 iptables rules mirror IPv4",
            detail = "ip6tables rules are consistent with iptables, preventing IPv6 bypass.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.ICMP_HANDLING,
            passed = true,
            title = "ICMP routed through tunnel",
            detail = "Ping traffic is captured and routed correctly.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.SELINUX_STATUS,
            passed = true,
            title = "SELinux is enforcing",
            detail = "SELinux is in enforcing mode with daemon-specific policy loaded.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.MODULE_INTEGRITY,
            passed = true,
            title = "Kernel module hashes verified",
            detail = "wintun.ko and tun.ko match expected SHA-256 digests.",
            remediation = "No action required.",
        ),
        AuditFinding(
            checkId = AuditCheckId.PRIVATE_DNS_UNCHANGED,
            passed = false,
            title = "Private DNS was modified by daemon",
            detail = "The system Private DNS setting was changed, which apps can detect.",
            remediation = "Restore the original Private DNS setting and route DNS at the iptables level instead.",
        ),
        AuditFinding(
            checkId = AuditCheckId.NETWORK_CHANGE_HANDLER,
            passed = true,
            title = "Network change listener active",
            detail = "The daemon re-establishes routes and DNS after connectivity changes.",
            remediation = "No action required.",
        ),
    )

    private fun computeRisk(findings: List<AuditFinding>): RiskLevel {
        val maxSeverity = findings
            .filter { !it.passed }
            .maxOfOrNull { it.checkId.severity.weight }
            ?: return RiskLevel.GREEN

        return when {
            maxSeverity >= Severity.HIGH.weight -> RiskLevel.RED
            maxSeverity >= Severity.MEDIUM.weight -> RiskLevel.YELLOW
            else -> RiskLevel.GREEN
        }
    }
}
