package com.privstack.panel.i18n

import android.content.Context
import androidx.annotation.StringRes
import com.privstack.panel.R
import com.privstack.panel.ipc.ConfigMutationInfo
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.model.RuntimeStageReport
import dagger.hilt.android.qualifiers.ApplicationContext
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class UserMessageFormatter @Inject constructor(
    @ApplicationContext private val context: Context,
) {
    private companion object {
        const val COMPATIBILITY_ERROR_CODE = -32090
        const val RUNTIME_BUSY_CODE = -32004
    }

    fun get(@StringRes resId: Int, vararg args: Any): String = context.getString(resId, *args)

    fun defaultGroupName(): String = get(R.string.default_group_name)

    fun isDefaultGroupName(value: String): Boolean {
        val normalized = value.trim()
        return normalized.equals("Default", ignoreCase = true) ||
            normalized.equals(defaultGroupName(), ignoreCase = true)
    }

    fun formatDaemonFailure(result: DaemonClientResult<*>): String = when (result) {
        is DaemonClientResult.DaemonError ->
            when (result.code) {
                COMPATIBILITY_ERROR_CODE -> result.message
                else -> if (result.configWasSaved()) {
                    get(R.string.error_config_saved_not_applied, result.message)
                } else if (result.code == RUNTIME_BUSY_CODE) {
                    formatRuntimeBusy(result)
                } else {
                    get(
                        R.string.error_daemon_with_code,
                        result.code,
                        result.message,
                    )
                }
            }
        is DaemonClientResult.RootDenied -> get(R.string.error_root_access_denied)
        is DaemonClientResult.Timeout -> get(R.string.error_request_timed_out_with_method, result.method)
        is DaemonClientResult.DaemonNotFound -> get(R.string.error_daemon_not_installed)
        is DaemonClientResult.ParseError -> get(R.string.error_invalid_daemon_response)
        is DaemonClientResult.Failure -> formatControlPlaneFailure(
            result.throwable.message,
            R.string.error_unexpected_with_reason,
        )
        is DaemonClientResult.Ok -> get(R.string.dns_ok)
    }

    fun formatControlPlaneFailure(message: String?, @StringRes fallbackResId: Int): String {
        val raw = message?.trim().orEmpty()
        if (raw.isBlank()) {
            return if (fallbackResId == R.string.error_unexpected_with_reason) {
                get(fallbackResId, get(R.string.daemon_status_unknown_text))
            } else {
                get(fallbackResId)
            }
        }
        return when {
            raw.startsWith("HTTP ", ignoreCase = true) ->
                get(R.string.error_http_status, raw.removePrefix("HTTP ").trim())
            raw.startsWith("GitHub API returned HTTP ", ignoreCase = true) ->
                get(R.string.error_github_http_status, raw.substringAfterLast(' ').trim())
            raw.equals("SHA256 checksum verification failed", ignoreCase = true) ->
                get(R.string.error_checksum_failed)
            raw.startsWith("Download failed with HTTP ", ignoreCase = true) ->
                get(R.string.error_download_http_status, raw.substringAfterLast(' ').trim())
            else -> raw
        }
    }

    fun formatOperationFailure(@StringRes operationResId: Int, reason: String): String =
        get(R.string.error_operation_failed_with_reason, get(operationResId), reason)

    fun formatConfigMutationNotice(info: ConfigMutationInfo): String? {
        val base = when (info.runtimeApply) {
            "accepted" -> get(R.string.operation_accepted)
            "skipped_runtime_stopped" -> get(R.string.operation_saved_runtime_stopped)
            "failed" -> get(R.string.operation_saved_runtime_not_applied)
            else -> null
        } ?: return null
        val rollback = runCatching {
            info.operation
                ?.jsonObject
                ?.get("rollback")
                ?.jsonPrimitive
                ?.contentOrNull
                .orEmpty()
        }.getOrDefault("")
        val suffix = when (rollback) {
            "cleanup_succeeded" -> get(R.string.operation_cleanup_succeeded)
            "cleanup_incomplete", "unknown" -> get(R.string.operation_cleanup_incomplete)
            else -> ""
        }
        return listOf(base, suffix).filter { it.isNotBlank() }.joinToString(" ")
    }

    fun formatSubscriptionRefresh(importedNodes: Int, parseFailures: Int): String =
        if (parseFailures > 0) {
            get(R.string.subscription_refresh_summary_with_errors, importedNodes, parseFailures)
        } else {
            get(R.string.subscription_refresh_summary, importedNodes)
        }

    private fun formatRuntimeBusy(result: DaemonClientResult.DaemonError): String {
        val active = runCatching {
            result.detailObject()
                ?.get("activeOperation")
                ?.jsonObject
                ?.get("kind")
                ?.jsonPrimitive
                ?.contentOrNull
                .orEmpty()
        }.getOrDefault("")
        return when (active) {
            "reset" -> get(R.string.error_reset_in_progress)
            "start" -> get(R.string.error_operation_busy_named, get(R.string.operation_start))
            "stop" -> get(R.string.error_operation_busy_named, get(R.string.operation_stop))
            "restart", "reload" -> get(R.string.error_operation_busy_named, get(R.string.operation_reload))
            else -> get(R.string.error_runtime_busy)
        }
    }

    private fun DaemonClientResult.DaemonError.configWasSaved(): Boolean {
        val details = detailObject() ?: return false
        return details["config_saved"]?.jsonPrimitive?.booleanOrNull == true ||
            details["configSaved"]?.jsonPrimitive?.booleanOrNull == true
    }

    private fun DaemonClientResult.DaemonError.detailObject() = runCatching {
        details?.jsonObject
            ?: envelope?.jsonObject
                ?.get("error")?.jsonObject
                ?.get("details")?.jsonObject
    }.getOrNull()

    fun formatHealthIssue(
        code: String?,
        detail: String?,
        userMessage: String? = null,
        stageReport: RuntimeStageReport? = null,
    ): String {
        val normalized = code?.trim().orEmpty()
        val mapped = when (normalized) {
            "CORE_PID_MISSING",
            "CORE_PID_LOOKUP_FAILED",
            "CORE_PROCESS_DEAD" -> get(R.string.health_issue_core_crashed)
            "TPROXY_PORT_DOWN" -> get(R.string.health_issue_tproxy_port_down)
            "CORE_LOG_OPEN_FAILED",
            "CORE_SPAWN_FAILED",
            "CONFIG_RENDER_FAILED",
            "CONFIG_CHECK_FAILED" -> get(R.string.health_issue_readiness_failed)
            "API_PORT_DOWN" -> get(R.string.health_issue_api_port_down)
            "RULES_NOT_APPLIED" -> get(R.string.health_issue_rules_not_applied)
            "NETSTACK_VERIFY_FAILED" -> get(R.string.health_issue_netstack_verify_failed)
            "NETSTACK_CLEANUP_FAILED" -> get(R.string.health_issue_netstack_cleanup_failed)
            "ROUTING_CHECK_FAILED",
            "ROUTING_V4_MISSING",
            "ROUTING_V6_MISSING",
            "ROUTING_NOT_APPLIED" -> get(R.string.health_issue_routing_not_applied)
            "DNS_LISTENER_DOWN",
            "DNS_APPLY_FAILED" -> get(R.string.health_issue_dns_listener_down)
            "DNS_LOOKUP_TIMEOUT" -> get(R.string.health_issue_dns_lookup_timeout)
            "DNS_EMPTY_RESPONSE",
            "DNS_LOOKUP_FAILED",
            "PROXY_DNS_UNAVAILABLE" -> get(R.string.health_issue_proxy_dns_unavailable)
            "OUTBOUND_URL_FAILED" -> get(R.string.health_issue_outbound_url_failed)
            "READINESS_GATE_FAILED" -> get(R.string.health_issue_readiness_failed)
            "OPERATIONAL_DEGRADED" -> get(R.string.health_issue_operational_degraded)
            else -> ""
        }
        val base = mapped.ifBlank { userMessage?.trim().orEmpty() }.ifBlank { detail?.trim().orEmpty() }.ifBlank {
            get(R.string.runtime_operational_degraded)
        }
        val stage = formatRuntimeStage(stageReport)
        return if (stage.isBlank()) base else "$base ($stage)"
    }

    private fun formatRuntimeStage(report: RuntimeStageReport?): String {
        val stage = report?.failedStage?.trim()?.ifBlank { null }
            ?: report?.failedStageOrLast?.name?.trim()?.ifBlank { null }
            ?: return ""
        return when (stage) {
            "render-config" -> "рендер конфигурации"
            "config-check" -> "проверка sing-box config"
            "open-core-log" -> "открытие core log"
            "spawn-core" -> "запуск sing-box"
            "wait-tproxy" -> "ожидание TPROXY"
            "wait-dns" -> "ожидание DNS listener"
            "wait-api" -> "ожидание API listener"
            "netstack-apply" -> "применение netstack"
            "netstack-verify" -> "проверка netstack"
            "commit-state" -> "фиксация состояния"
            "stop-subsystems" -> "остановка watchers"
            "hot-swap" -> "hot-swap core"
            "netstack-reapply" -> "переустановка netstack"
            "rescue-reset" -> "сброс rescue state"
            "start-subsystems" -> "запуск watchers"
            "health-refresh" -> "проверка health"
            else -> stage
        }
    }

    fun formatNodeTestIssue(code: String?): String {
        val normalized = code?.trim().orEmpty()
        return when (normalized) {
            "tcp_direct_failed" -> get(R.string.node_test_reason_tcp_direct_failed)
            "tunnel_delay_failed" -> get(R.string.node_test_reason_tunnel_delay_failed)
            "tunnel_unavailable" -> get(R.string.node_test_reason_tunnel_unavailable)
            "dns_bootstrap_failed" -> get(R.string.node_test_reason_dns_bootstrap_failed)
            "runtime_not_ready" -> get(R.string.node_test_reason_runtime_not_ready)
            "runtime_degraded" -> get(R.string.node_test_reason_runtime_degraded)
            "proxy_dns_unavailable" -> get(R.string.node_test_reason_proxy_dns_unavailable)
            "outbound_url_failed" -> get(R.string.node_test_reason_outbound_url_failed)
            "http_helper_unavailable" -> get(R.string.node_test_reason_http_helper_unavailable)
            "api_disabled" -> get(R.string.node_test_reason_api_disabled)
            "api_unavailable" -> get(R.string.node_test_reason_api_unavailable)
            "outbound_missing" -> get(R.string.node_test_reason_outbound_missing)
            "tls_handshake_failed" -> get(R.string.node_test_reason_tls_handshake_failed)
            "", "ok" -> get(R.string.node_test_reason_unknown)
            else -> normalized
        }
    }

    fun mapLogLevelLabel(name: String): String = when (name) {
        "DEBUG" -> get(R.string.log_level_debug)
        "INFO" -> get(R.string.log_level_info)
        "WARNING" -> get(R.string.log_level_warning)
        "ERROR" -> get(R.string.log_level_error)
        "NONE" -> get(R.string.log_level_none)
        else -> name
    }
}
