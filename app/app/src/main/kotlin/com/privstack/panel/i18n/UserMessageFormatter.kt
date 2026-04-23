package com.privstack.panel.i18n

import android.content.Context
import androidx.annotation.StringRes
import com.privstack.panel.R
import com.privstack.panel.ipc.DaemonClientResult
import dagger.hilt.android.qualifiers.ApplicationContext
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class UserMessageFormatter @Inject constructor(
    @ApplicationContext private val context: Context,
) {
    fun get(@StringRes resId: Int, vararg args: Any): String = context.getString(resId, *args)

    fun defaultGroupName(): String = get(R.string.default_group_name)

    fun isDefaultGroupName(value: String): Boolean {
        val normalized = value.trim()
        return normalized.equals("Default", ignoreCase = true) ||
            normalized.equals(defaultGroupName(), ignoreCase = true)
    }

    fun formatDaemonFailure(result: DaemonClientResult<*>): String = when (result) {
        is DaemonClientResult.DaemonError -> get(
            R.string.error_daemon_with_code,
            result.code,
            result.message,
        )
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

    fun formatNodeTestIssue(code: String?): String {
        val normalized = code?.trim().orEmpty()
        return when (normalized) {
            "tcp_direct_failed" -> get(R.string.node_test_reason_tcp_direct_failed)
            "tunnel_delay_failed" -> get(R.string.node_test_reason_tunnel_delay_failed)
            "tunnel_unavailable" -> get(R.string.node_test_reason_tunnel_unavailable)
            "dns_bootstrap_failed" -> get(R.string.node_test_reason_dns_bootstrap_failed)
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
