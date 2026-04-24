package com.privstack.panel.ui.audit

import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import androidx.annotation.StringRes
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.R
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

enum class Severity(val weight: Int) {
    CRITICAL(4),
    HIGH(3),
    MEDIUM(2),
    LOW(1),
    PASS(0),
}

enum class AuditCategoryKey {
    API,
    NETWORK,
    DNS,
    PRIVACY,
    FIREWALL,
    SYSTEM,
    ROUTING,
    ENCRYPTION,
    CONFIG,
}

enum class AuditCheckId(
    val codes: Set<String>,
    val category: AuditCategoryKey,
    val defaultSeverity: Severity,
) {
    VPN_API_SURFACE(setOf("VPN_API_SURFACE"), AuditCategoryKey.API, Severity.CRITICAL),
    TUN_INTERFACE(setOf("TUN_INTERFACE"), AuditCategoryKey.NETWORK, Severity.HIGH),
    NOT_VPN_CAPABILITY(setOf("NOT_VPN_CAPABILITY"), AuditCategoryKey.API, Severity.CRITICAL),
    API_PORT_PROTECTED(setOf("API_PORT_PROTECTED"), AuditCategoryKey.API, Severity.MEDIUM),
    DNS_LEAK(setOf("DNS_LEAK"), AuditCategoryKey.DNS, Severity.MEDIUM),
    PACKAGE_VISIBILITY(setOf("PACKAGE_VISIBILITY"), AuditCategoryKey.PRIVACY, Severity.HIGH),
    IPTABLES_LOOP_SAFE(setOf("IPTABLES_LOOP_SAFE"), AuditCategoryKey.FIREWALL, Severity.HIGH),
    PROC_NET_LEAK(setOf("PROC_NET_LEAK"), AuditCategoryKey.PRIVACY, Severity.HIGH),
    IPV6_RULES_MIRROR(setOf("IPV6_RULES_MIRROR"), AuditCategoryKey.FIREWALL, Severity.HIGH),
    ICMP_HANDLING(setOf("ICMP_HANDLING"), AuditCategoryKey.NETWORK, Severity.MEDIUM),
    SELINUX_STATUS(setOf("SELINUX_STATUS"), AuditCategoryKey.SYSTEM, Severity.HIGH),
    MODULE_INTEGRITY(setOf("MODULE_INTEGRITY"), AuditCategoryKey.SYSTEM, Severity.MEDIUM),
    PRIVATE_DNS_UNCHANGED(setOf("PRIVATE_DNS_UNCHANGED"), AuditCategoryKey.DNS, Severity.MEDIUM),
    NETWORK_CHANGE_HANDLER(setOf("NETWORK_CHANGE_HANDLER"), AuditCategoryKey.NETWORK, Severity.MEDIUM),
    NODE_NOT_CONFIGURED(setOf("NODE_NOT_CONFIGURED"), AuditCategoryKey.CONFIG, Severity.CRITICAL),
    PROXY_DNS_EMPTY(setOf("PROXY_DNS_EMPTY"), AuditCategoryKey.DNS, Severity.HIGH),
    DIRECT_DNS_EMPTY(setOf("DIRECT_DNS_EMPTY"), AuditCategoryKey.DNS, Severity.MEDIUM),
    LOOPBACK_DNS_VISIBLE(setOf("LOOPBACK_DNS_VISIBLE"), AuditCategoryKey.DNS, Severity.HIGH),
    TRANSPORT_NOT_ENCRYPTED(setOf("TRANSPORT_NOT_ENCRYPTED"), AuditCategoryKey.ENCRYPTION, Severity.MEDIUM),
    PER_APP_ROUTING_DISABLED(setOf("PER_APP_ROUTING_DISABLED"), AuditCategoryKey.ROUTING, Severity.LOW),
    DIRECT_MODE_NOT_HARD_BYPASS(setOf("DIRECT_MODE_NOT_HARD_BYPASS"), AuditCategoryKey.ROUTING, Severity.HIGH),
    SENSITIVE_FILE_PERMISSIONS(setOf("SENSITIVE_FILE_PERMISSIONS"), AuditCategoryKey.CONFIG, Severity.MEDIUM),
    LOCAL_PORT_PROTECTION_MISSING(setOf("LOCAL_PORT_PROTECTION_MISSING"), AuditCategoryKey.PRIVACY, Severity.HIGH),
    LOCALHOST_PROXY_PORT_VISIBLE(setOf("LOCALHOST_PROXY_PORT_VISIBLE"), AuditCategoryKey.PRIVACY, Severity.HIGH),
    LOCAL_HELPER_INBOUND_ENABLED(setOf("LOCAL_HELPER_INBOUND_ENABLED", "LOCAL_HELPER_EXPOSED_ON_LAN"), AuditCategoryKey.PRIVACY, Severity.HIGH),
    HEALTH_DNS(setOf("HEALTH_DNS"), AuditCategoryKey.DNS, Severity.HIGH),
    HEALTH_IPTABLES(setOf("HEALTH_IPTABLES"), AuditCategoryKey.ROUTING, Severity.HIGH),
    HEALTH_ROUTING(setOf("HEALTH_ROUTING"), AuditCategoryKey.ROUTING, Severity.HIGH),
    HEALTH_TPROXY_PORT(setOf("HEALTH_TPROXY_PORT"), AuditCategoryKey.SYSTEM, Severity.CRITICAL),
    HEALTH_SINGBOX_ALIVE(setOf("HEALTH_SINGBOX_ALIVE"), AuditCategoryKey.SYSTEM, Severity.CRITICAL),
    HEALTH_GENERIC(setOf("HEALTH_GENERIC"), AuditCategoryKey.SYSTEM, Severity.HIGH),
}

data class AuditFinding(
    val checkId: AuditCheckId,
    val passed: Boolean,
    val title: String,
    val detail: String,
    val remediation: String,
    val category: String,
    val severity: Severity,
)

// ───────────────────────────────────────────────────────────────────── //
//  Overall risk level
// ───────────────────────────────────────────────────────────────────── //

enum class RiskLevel {
    GREEN,
    YELLOW,
    RED,
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

    private fun string(@StringRes resId: Int, vararg args: Any): String =
        context.getString(resId, *args)

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
                            throw IllegalStateException(
                                string(R.string.audit_error_daemon_failed, result.message)
                            )
                        }
                    }
                    is DaemonClientResult.RootDenied -> {
                        throw IllegalStateException(string(R.string.audit_error_root_denied))
                    }
                    is DaemonClientResult.Timeout -> {
                        throw IllegalStateException(string(R.string.audit_error_timed_out))
                    }
                    is DaemonClientResult.DaemonNotFound -> {
                        throw IllegalStateException(string(R.string.audit_error_daemon_not_installed))
                    }
                    is DaemonClientResult.ParseError -> {
                        throw IllegalStateException(string(R.string.audit_error_invalid_response))
                    }
                    is DaemonClientResult.Failure -> {
                        throw IllegalStateException(
                            result.throwable.message ?: string(R.string.audit_error_failed)
                        )
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
                        errorMessage = e.message ?: string(R.string.audit_error_failed),
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

                val recommendations = advisor.advise(installedApps).map(::localizeRecommendation)
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
                        errorMessage = e.message ?: string(R.string.advisor_error_load_failed),
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
        val checkId = mapDaemonFindingToCheckId(finding)
        return AuditFinding(
            checkId = checkId,
            passed = passed,
            title = finding.title,
            detail = finding.description,
            remediation = finding.recommendation ?: string(R.string.audit_no_remediation),
            category = localizedAuditCategory(checkId.category),
            severity = severity,
        )
    }

    private fun mapDaemonFindingToCheckId(finding: DaemonAuditFinding): AuditCheckId {
        val severity = finding.severity.toUiSeverity()
        AuditCheckId.entries.firstOrNull { finding.code in it.codes }?.let { return it }
        if (finding.code.startsWith("HEALTH_", ignoreCase = true)) {
            return when (finding.code) {
                "HEALTH_DNS" -> AuditCheckId.HEALTH_DNS
                "HEALTH_IPTABLES" -> AuditCheckId.HEALTH_IPTABLES
                "HEALTH_ROUTING" -> AuditCheckId.HEALTH_ROUTING
                "HEALTH_TPROXY_PORT" -> AuditCheckId.HEALTH_TPROXY_PORT
                "HEALTH_SINGBOX_ALIVE" -> AuditCheckId.HEALTH_SINGBOX_ALIVE
                else -> AuditCheckId.HEALTH_GENERIC
            }
        }
        return when (finding.category) {
            DaemonAuditCategory.DNS -> if (severity.weight >= Severity.HIGH.weight) {
                AuditCheckId.DNS_LEAK
            } else {
                AuditCheckId.PRIVATE_DNS_UNCHANGED
            }
            DaemonAuditCategory.ROUTING -> AuditCheckId.HEALTH_ROUTING
            DaemonAuditCategory.ENCRYPTION -> AuditCheckId.TRANSPORT_NOT_ENCRYPTED
            DaemonAuditCategory.LEAK -> AuditCheckId.PROC_NET_LEAK
            DaemonAuditCategory.CONFIG -> AuditCheckId.MODULE_INTEGRITY
            DaemonAuditCategory.SYSTEM -> AuditCheckId.HEALTH_GENERIC
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
        simulatedFinding(
            AuditCheckId.VPN_API_SURFACE,
            true,
            R.string.audit_check_vpn_api_surface_title,
            R.string.audit_check_vpn_api_surface_detail,
            R.string.audit_check_vpn_api_surface_remediation,
        ),
        simulatedFinding(
            AuditCheckId.TUN_INTERFACE,
            false,
            R.string.audit_check_tun_interface_title,
            R.string.audit_check_tun_interface_detail,
            R.string.audit_check_tun_interface_remediation,
        ),
        simulatedFinding(
            AuditCheckId.NOT_VPN_CAPABILITY,
            true,
            R.string.audit_check_not_vpn_capability_title,
            R.string.audit_check_not_vpn_capability_detail,
            R.string.audit_check_not_vpn_capability_remediation,
        ),
        simulatedFinding(
            AuditCheckId.API_PORT_PROTECTED,
            true,
            R.string.audit_check_api_port_protected_title,
            R.string.audit_check_api_port_protected_detail,
            R.string.audit_check_api_port_protected_remediation,
        ),
        simulatedFinding(
            AuditCheckId.DNS_LEAK,
            false,
            R.string.audit_check_dns_leak_title,
            R.string.audit_check_dns_leak_detail,
            R.string.audit_check_dns_leak_remediation,
        ),
        simulatedFinding(
            AuditCheckId.PACKAGE_VISIBILITY,
            false,
            R.string.audit_check_package_visibility_title,
            R.string.audit_check_package_visibility_detail,
            R.string.audit_check_package_visibility_remediation,
        ),
        simulatedFinding(
            AuditCheckId.IPTABLES_LOOP_SAFE,
            true,
            R.string.audit_check_iptables_loop_safe_title,
            R.string.audit_check_iptables_loop_safe_detail,
            R.string.audit_check_iptables_loop_safe_remediation,
        ),
        simulatedFinding(
            AuditCheckId.PROC_NET_LEAK,
            false,
            R.string.audit_check_proc_net_leak_title,
            R.string.audit_check_proc_net_leak_detail,
            R.string.audit_check_proc_net_leak_remediation,
        ),
        simulatedFinding(
            AuditCheckId.IPV6_RULES_MIRROR,
            true,
            R.string.audit_check_ipv6_rules_mirror_title,
            R.string.audit_check_ipv6_rules_mirror_detail,
            R.string.audit_check_ipv6_rules_mirror_remediation,
        ),
        simulatedFinding(
            AuditCheckId.ICMP_HANDLING,
            true,
            R.string.audit_check_icmp_handling_title,
            R.string.audit_check_icmp_handling_detail,
            R.string.audit_check_icmp_handling_remediation,
        ),
        simulatedFinding(
            AuditCheckId.SELINUX_STATUS,
            true,
            R.string.audit_check_selinux_status_title,
            R.string.audit_check_selinux_status_detail,
            R.string.audit_check_selinux_status_remediation,
        ),
        simulatedFinding(
            AuditCheckId.MODULE_INTEGRITY,
            true,
            R.string.audit_check_module_integrity_title,
            R.string.audit_check_module_integrity_detail,
            R.string.audit_check_module_integrity_remediation,
        ),
        simulatedFinding(
            AuditCheckId.PRIVATE_DNS_UNCHANGED,
            false,
            R.string.audit_check_private_dns_unchanged_title,
            R.string.audit_check_private_dns_unchanged_detail,
            R.string.audit_check_private_dns_unchanged_remediation,
        ),
        simulatedFinding(
            AuditCheckId.NETWORK_CHANGE_HANDLER,
            true,
            R.string.audit_check_network_change_handler_title,
            R.string.audit_check_network_change_handler_detail,
            R.string.audit_check_network_change_handler_remediation,
        ),
    )

    private fun computeRisk(findings: List<AuditFinding>): RiskLevel {
        val maxSeverity = findings
            .filter { !it.passed }
            .maxOfOrNull { it.severity.weight }
            ?: return RiskLevel.GREEN

        return when {
            maxSeverity >= Severity.HIGH.weight -> RiskLevel.RED
            maxSeverity >= Severity.MEDIUM.weight -> RiskLevel.YELLOW
            else -> RiskLevel.GREEN
        }
    }

    private fun simulatedFinding(
        checkId: AuditCheckId,
        passed: Boolean,
        @StringRes titleRes: Int,
        @StringRes detailRes: Int,
        @StringRes remediationRes: Int,
    ): AuditFinding = AuditFinding(
        checkId = checkId,
        passed = passed,
        title = string(titleRes),
        detail = string(detailRes),
        remediation = string(remediationRes),
        category = localizedAuditCategory(checkId.category),
        severity = if (passed) Severity.PASS else checkId.defaultSeverity,
    )

    private fun localizedAuditCategory(category: AuditCategoryKey): String = when (category) {
        AuditCategoryKey.API -> string(R.string.audit_category_api)
        AuditCategoryKey.NETWORK -> string(R.string.audit_category_network)
        AuditCategoryKey.DNS -> string(R.string.audit_category_dns)
        AuditCategoryKey.PRIVACY -> string(R.string.audit_category_privacy)
        AuditCategoryKey.FIREWALL -> string(R.string.audit_category_firewall)
        AuditCategoryKey.SYSTEM -> string(R.string.audit_category_system)
        AuditCategoryKey.ROUTING -> string(R.string.audit_category_routing)
        AuditCategoryKey.ENCRYPTION -> string(R.string.audit_category_encryption)
        AuditCategoryKey.CONFIG -> string(R.string.audit_category_config)
    }

    private fun localizedReason(category: AppCategory): String = when (category) {
        AppCategory.BANKING -> string(R.string.advisor_reason_banking)
        AppCategory.GOVERNMENT -> string(R.string.advisor_reason_government)
        AppCategory.TELECOM -> string(R.string.advisor_reason_telecom)
        AppCategory.BROWSER -> string(R.string.advisor_reason_browser)
        AppCategory.SOCIAL_MESSAGING -> string(R.string.advisor_reason_social)
        AppCategory.STREAMING -> string(R.string.advisor_reason_streaming)
        AppCategory.VPN_PROXY -> string(R.string.advisor_reason_vpn_proxy)
        AppCategory.SYSTEM -> string(R.string.advisor_reason_system)
        AppCategory.OTHER -> string(R.string.advisor_reason_other)
    }

    private fun localizeRecommendation(recommendation: PlacementRecommendation): PlacementRecommendation =
        recommendation.copy(reason = localizedReason(recommendation.app.category))
}
