package com.privstack.panel.ui.settings

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.BuildConfig
import com.privstack.panel.controlplane.ControlPlaneClient
import com.privstack.panel.controlplane.DownloadedUpdate
import com.privstack.panel.controlplane.ReleaseInfo
import com.privstack.panel.i18n.UserMessageFormatter
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.model.ConnectionState
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.FallbackPolicy
import com.privstack.panel.model.ProfileConfig
import com.privstack.panel.repository.CommandOutcome
import com.privstack.panel.repository.ProfileRepository
import com.privstack.panel.repository.StatusRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

private const val TAG = "SettingsViewModel"

/** JSON-RPC standard error code for "method not found" (-32601). */
private const val METHOD_NOT_FOUND_CODE = -32601

/**
 * Returns true if the daemon error indicates the method doesn't exist.
 * This covers two cases:
 *  - New privctl + old daemon: daemon returns JSON-RPC -32601 "method not found"
 *  - Old privctl (doesn't know command): privctl exits with code 1 and
 *    stderr contains "unknown command" -- parsed as DaemonError(code=1, ...)
 */
private fun isMethodNotFound(result: DaemonClientResult.DaemonError): Boolean =
    result.code == METHOD_NOT_FOUND_CODE ||
    result.message.contains("unknown command", ignoreCase = true) ||
    result.message.contains("method not found", ignoreCase = true)

enum class RoutingMode { GLOBAL, WHITELIST, BYPASS, DIRECT }

enum class DnsPreset(val label: String, val url: String) {
    CLOUDFLARE("Cloudflare", "https://1.1.1.1/dns-query"),
    GOOGLE("Google", "https://dns.google/dns-query"),
    ADGUARD("AdGuard", "https://dns.adguard-dns.com/dns-query"),
    CUSTOM("Custom", ""),
}

enum class ThemeMode { LIGHT, DARK, SYSTEM }

enum class LogLevel { DEBUG, INFO, WARNING, ERROR, NONE }

enum class UpdateStatus {
    IDLE, CHECKING, UP_TO_DATE, AVAILABLE,
    DOWNLOADING, DOWNLOADED, INSTALLING, INSTALLED,
    MODULE_TOO_OLD, ERROR
}

data class UpdateUiState(
    val currentVersion: String = BuildConfig.VERSION_NAME,
    val latestVersion: String = "",
    val status: UpdateStatus = UpdateStatus.IDLE,
    val changelog: String = "",
    val downloadProgress: Float = 0f,
    val errorMessage: String = "",
    val modulePath: String = "",
    val apkPath: String = "",
)

data class SettingsUiState(
    val fallbackPolicy: FallbackPolicy = FallbackPolicy.OFFER_RESET,
    // Routing
    val routingMode: RoutingMode = RoutingMode.GLOBAL,
    // DNS
    val dnsPreset: DnsPreset = DnsPreset.CLOUDFLARE,
    val customDnsUrl: String = "",
    val urlTestUrl: String = "https://www.gstatic.com/generate_204",
    val alwaysDirectPackagesText: String = "",
    val logLevel: LogLevel = LogLevel.WARNING,
    // Module
    val moduleVersion: String = BuildConfig.VERSION_NAME,
    val daemonStatusText: String = "",
    // Theme
    val themeMode: ThemeMode = ThemeMode.SYSTEM,
    // About
    val appVersion: String = BuildConfig.VERSION_NAME,
    val githubUrl: String = "https://github.com/youtubediscord/RKNnoVPN",
    // Error
    val errorMessage: String? = null,
    val lastResetSummary: String? = null,
    val logsText: String = "",
    val isLoadingLogs: Boolean = false,
    val shareLogsText: String? = null,
)

@HiltViewModel
class SettingsViewModel @Inject constructor(
    private val daemonClient: DaemonClient,
    private val controlPlaneClient: ControlPlaneClient,
    private val profileRepository: ProfileRepository,
    private val statusRepository: StatusRepository,
    private val messages: UserMessageFormatter,
) : ViewModel() {

    private val _uiState = MutableStateFlow(SettingsUiState())
    val uiState: StateFlow<SettingsUiState> = _uiState.asStateFlow()

    private val _updateState = MutableStateFlow(UpdateUiState())
    val updateState: StateFlow<UpdateUiState> = _updateState.asStateFlow()

    private var lastReleaseInfo: ReleaseInfo? = null
    private var lastDownloadedUpdate: DownloadedUpdate? = null

    init {
        observeProfile()
        observeRuntimeStatus()
        loadVersionInfo()
    }

    // ---- Public actions ----

    fun setFallbackPolicy(policy: FallbackPolicy) {
        val previousPolicy = _uiState.value.fallbackPolicy
        _uiState.update { it.copy(fallbackPolicy = policy, errorMessage = null) }
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(runtime = config.runtime.copy(fallbackPolicy = policy))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to set fallback policy: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(fallbackPolicy = previousPolicy, errorMessage = err) }
            }
        }
    }

    fun setRoutingMode(mode: RoutingMode) {
        val previousMode = _uiState.value.routingMode
        _uiState.update { it.copy(routingMode = mode, errorMessage = null) }
        viewModelScope.launch {
            val profileMode = when (mode) {
                RoutingMode.GLOBAL -> com.privstack.panel.model.RoutingMode.PROXY_ALL
                RoutingMode.WHITELIST -> com.privstack.panel.model.RoutingMode.PER_APP
                RoutingMode.BYPASS -> com.privstack.panel.model.RoutingMode.PER_APP_BYPASS
                RoutingMode.DIRECT -> com.privstack.panel.model.RoutingMode.DIRECT
            }
            val ok = profileRepository.updateConfig { config ->
                config.copy(routing = config.routing.copy(mode = profileMode))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to set routing mode: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(routingMode = previousMode, errorMessage = err) }
            }
        }
    }

    fun setDnsPreset(preset: DnsPreset) {
        val previousPreset = _uiState.value.dnsPreset
        _uiState.update { it.copy(dnsPreset = preset, errorMessage = null) }
        if (preset != DnsPreset.CUSTOM) {
            saveDnsToProfile(preset.url, previousPreset)
        }
    }

    fun setCustomDnsUrl(url: String) {
        _uiState.update { it.copy(customDnsUrl = url) }
    }

    fun applyCustomDns() {
        val url = _uiState.value.customDnsUrl
        if (url.isNotBlank()) {
            val previousPreset = _uiState.value.dnsPreset
            _uiState.update { it.copy(dnsPreset = DnsPreset.CUSTOM, errorMessage = null) }
            saveDnsToProfile(url, previousPreset)
        }
    }

    fun setUrlTestUrl(url: String) {
        _uiState.update { it.copy(urlTestUrl = url, errorMessage = null) }
    }

    fun setAlwaysDirectPackagesText(value: String) {
        _uiState.update { it.copy(alwaysDirectPackagesText = value, errorMessage = null) }
    }

    fun applyUrlTestUrl() {
        val url = _uiState.value.urlTestUrl.trim()
        if (url.isBlank()) {
            _uiState.update {
                it.copy(errorMessage = messages.get(com.privstack.panel.R.string.url_test_endpoint_required))
            }
            return
        }
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(health = config.health.copy(checkUrl = url))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save URL test setting: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(errorMessage = err) }
            }
        }
    }

    fun applyAlwaysDirectPackages() {
        val packages = parsePackageList(_uiState.value.alwaysDirectPackagesText)
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(
                    routing = config.routing.copy(alwaysDirectAppList = packages),
                )
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save always-direct apps: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(errorMessage = err) }
            }
        }
    }

    fun setLogLevel(level: LogLevel) {
        _uiState.update { it.copy(logLevel = level, errorMessage = null) }
        // Log level is a local preference affecting which daemon logs we request.
    }

    fun restartDaemon() {
        _uiState.update {
            it.copy(
                daemonStatusText = messages.get(com.privstack.panel.R.string.daemon_status_restarting),
                errorMessage = null,
                lastResetSummary = null,
            )
        }
        viewModelScope.launch {
            when (val outcome = statusRepository.reload()) {
                is CommandOutcome.Success -> {
                    Log.d(TAG, "Backend restart succeeded")
                    _uiState.update {
                        it.copy(daemonStatusText = messages.get(com.privstack.panel.R.string.daemon_status_restarted))
                    }
                }
                is CommandOutcome.Failed -> {
                    Log.w(TAG, "Backend restart failed: ${outcome.message}")
                    _uiState.update {
                        it.copy(
                            daemonStatusText = messages.get(com.privstack.panel.R.string.state_error),
                            errorMessage = outcome.message,
                        )
                    }
                }
            }
        }
    }

    fun resetNetworkRules() {
        _uiState.update {
            it.copy(
                daemonStatusText = messages.get(com.privstack.panel.R.string.daemon_status_resetting),
                errorMessage = null,
                lastResetSummary = null,
            )
        }
        viewModelScope.launch {
            when (val result = statusRepository.networkReset()) {
                is DaemonClientResult.Ok -> {
                    val report = result.data
                    Log.d(TAG, "Backend reset finished with status=${report.status}")
                    _uiState.update {
                        it.copy(
                            daemonStatusText = if (report.status == "ok") {
                                messages.get(com.privstack.panel.R.string.daemon_status_stopped)
                            } else {
                                messages.get(com.privstack.panel.R.string.daemon_status_partial_reset)
                            },
                            errorMessage = report.errors.takeIf { it.isNotEmpty() }?.joinToString("\n"),
                            lastResetSummary = summarizeResetReport(report),
                        )
                    }
                }
                else -> {
                    val message = formatUpdateError(result)
                    Log.w(TAG, "Backend reset failed: $message")
                    _uiState.update {
                        it.copy(
                            daemonStatusText = messages.get(com.privstack.panel.R.string.state_error),
                            errorMessage = message,
                        )
                    }
                }
            }
        }
    }

    fun setThemeMode(mode: ThemeMode) {
        _uiState.update { it.copy(themeMode = mode) }
        // Theme is a pure UI preference, stored locally (SharedPreferences/DataStore).
        // Not sent to the daemon.
    }

    fun refreshRuntimeLogs() {
        _uiState.update { it.copy(isLoadingLogs = true, errorMessage = null) }
        viewModelScope.launch {
            when (val result = daemonClient.runtimeLogs(lines = 160)) {
                is DaemonClientResult.Ok -> {
                    _uiState.update {
                        it.copy(
                            logsText = result.data.text,
                            isLoadingLogs = false,
                        )
                    }
                }
                else -> {
                    val message = formatUpdateError(result)
                    Log.w(TAG, "Failed to load runtime logs: $message")
                    _uiState.update {
                        it.copy(
                            isLoadingLogs = false,
                            errorMessage = message,
                        )
                    }
                }
            }
        }
    }

    fun shareRuntimeLogs() {
        val current = _uiState.value.logsText
        if (current.isNotBlank()) {
            _uiState.update { it.copy(shareLogsText = current) }
            return
        }
        _uiState.update { it.copy(isLoadingLogs = true, errorMessage = null) }
        viewModelScope.launch {
            when (val result = daemonClient.diagnosticBundle(lines = 220)) {
                is DaemonClientResult.Ok -> {
                    _uiState.update {
                        it.copy(
                            logsText = result.data,
                            shareLogsText = result.data,
                            isLoadingLogs = false,
                        )
                    }
                }
                else -> {
                    val message = formatUpdateError(result)
                    Log.w(TAG, "Failed to prepare runtime logs: $message")
                    _uiState.update {
                        it.copy(
                            isLoadingLogs = false,
                            errorMessage = message,
                        )
                    }
                }
            }
        }
    }

    fun clearSharedLogs() {
        _uiState.update { it.copy(shareLogsText = null) }
    }

    // ---- Update actions ----

    fun checkForUpdates() {
        _updateState.update { it.copy(status = UpdateStatus.CHECKING, errorMessage = "") }
        viewModelScope.launch {
            runCatching {
                controlPlaneClient.checkForUpdates(_updateState.value.currentVersion)
            }.onSuccess { release ->
                lastReleaseInfo = release
                lastDownloadedUpdate = null
                _updateState.update {
                    it.copy(
                        currentVersion = release.currentVersion,
                        latestVersion = release.latestVersion,
                        changelog = release.changelog,
                        status = if (release.hasUpdate) UpdateStatus.AVAILABLE else UpdateStatus.UP_TO_DATE,
                        modulePath = "",
                        apkPath = "",
                    )
                }
            }.onFailure { throwable ->
                _updateState.update {
                    it.copy(
                        status = UpdateStatus.ERROR,
                        errorMessage = messages.formatControlPlaneFailure(
                            throwable.message,
                            com.privstack.panel.R.string.update_error_check_failed,
                        ),
                    )
                }
            }
        }
    }

    fun downloadUpdate() {
        _updateState.update { it.copy(status = UpdateStatus.DOWNLOADING, downloadProgress = 0f) }
        viewModelScope.launch {
            val release = lastReleaseInfo
            if (release == null || !release.hasUpdate) {
                _updateState.update {
                    it.copy(
                        status = UpdateStatus.ERROR,
                        errorMessage = messages.get(com.privstack.panel.R.string.update_error_no_checked_update),
                    )
                }
                return@launch
            }

            runCatching {
                controlPlaneClient.downloadUpdate(release) { progress ->
                    _updateState.update { current -> current.copy(downloadProgress = progress) }
                }
            }.onSuccess { downloaded ->
                lastDownloadedUpdate = downloaded
                val hasArtifacts = downloaded.modulePath.isNotBlank() && downloaded.apkPath.isNotBlank()
                _updateState.update {
                    it.copy(
                        status = if (hasArtifacts) UpdateStatus.DOWNLOADED else UpdateStatus.ERROR,
                        downloadProgress = if (hasArtifacts) 1f else 0f,
                        errorMessage = if (hasArtifacts) {
                            ""
                        } else {
                            messages.get(com.privstack.panel.R.string.update_error_pair_required)
                        },
                        modulePath = downloaded.modulePath,
                        apkPath = downloaded.apkPath,
                    )
                }
            }.onFailure { throwable ->
                _updateState.update {
                    it.copy(
                        status = UpdateStatus.ERROR,
                        errorMessage = messages.formatControlPlaneFailure(
                            throwable.message,
                            com.privstack.panel.R.string.update_error_download_failed,
                        ),
                    )
                }
            }
        }
    }

    fun installUpdate() {
        _updateState.update { it.copy(status = UpdateStatus.INSTALLING) }
        viewModelScope.launch {
            val downloaded = lastDownloadedUpdate
            if (downloaded == null) {
                _updateState.update {
                    it.copy(
                        status = UpdateStatus.ERROR,
                        errorMessage = messages.get(com.privstack.panel.R.string.update_error_no_downloaded_update),
                    )
                }
                return@launch
            }

            when (val result = daemonClient.updateInstall(
                modulePath = downloaded.modulePath,
                apkPath = downloaded.apkPath,
            )) {
                is DaemonClientResult.Ok -> {
                    val installedSomething = result.data.moduleInstalled || result.data.apkInstalled
                    _updateState.update {
                        it.copy(
                            status = if (installedSomething) UpdateStatus.INSTALLED else UpdateStatus.ERROR,
                            errorMessage = if (installedSomething) {
                                ""
                            } else {
                                result.data.apkError ?: messages.get(
                                    com.privstack.panel.R.string.update_error_install_without_artifacts
                                )
                            },
                        )
                    }
                }
                is DaemonClientResult.DaemonError -> {
                    if (isMethodNotFound(result)) {
                        _updateState.update { it.copy(status = UpdateStatus.MODULE_TOO_OLD) }
                    } else {
                        _updateState.update {
                            it.copy(
                                status = UpdateStatus.ERROR,
                                errorMessage = formatUpdateError(result),
                            )
                        }
                    }
                }
                is DaemonClientResult.DaemonNotFound -> {
                    _updateState.update { it.copy(status = UpdateStatus.MODULE_TOO_OLD) }
                }
                else -> {
                    _updateState.update {
                        it.copy(
                            status = UpdateStatus.ERROR,
                            errorMessage = formatUpdateError(result),
                        )
                    }
                }
            }
        }
    }

    // ---- Internal ----

    /**
     * Load the current daemon profile config and project relevant settings
     * into [SettingsUiState].
     */
    private fun observeProfile() {
        viewModelScope.launch {
            profileRepository.profile
                .filterNotNull()
                .collect(::applyProfileConfig)
        }
        viewModelScope.launch {
            profileRepository.getOrLoad()
        }
    }

    private fun observeRuntimeStatus() {
        viewModelScope.launch {
            statusRepository.status
                .filterNotNull()
                .collect { status ->
                    _uiState.update {
                        it.copy(daemonStatusText = formatRuntimeStatus(status, it.daemonStatusText))
                    }
                }
        }
    }

    private fun applyProfileConfig(config: ProfileConfig) {
        val routingMode = when (config.routing.mode) {
            com.privstack.panel.model.RoutingMode.PROXY_ALL -> RoutingMode.GLOBAL
            com.privstack.panel.model.RoutingMode.PER_APP -> RoutingMode.WHITELIST
            com.privstack.panel.model.RoutingMode.PER_APP_BYPASS -> RoutingMode.BYPASS
            com.privstack.panel.model.RoutingMode.DIRECT -> RoutingMode.DIRECT
            com.privstack.panel.model.RoutingMode.RULES -> RoutingMode.DIRECT
        }

        val dnsUrl = config.dns.remoteDns
        val dnsPreset = DnsPreset.entries.find { it.url == dnsUrl && it != DnsPreset.CUSTOM }
            ?: DnsPreset.CUSTOM

        _uiState.update {
            it.copy(
                fallbackPolicy = config.runtime.fallbackPolicy,
                routingMode = routingMode,
                dnsPreset = dnsPreset,
                customDnsUrl = if (dnsPreset == DnsPreset.CUSTOM) dnsUrl else it.customDnsUrl,
                urlTestUrl = config.health.checkUrl,
                alwaysDirectPackagesText = config.routing.alwaysDirectAppList.joinToString("\n"),
            )
        }
    }

    /**
     * Fetch daemon and core version strings.
     */
    private fun loadVersionInfo() {
        viewModelScope.launch {
            when (val result = daemonClient.version()) {
                is DaemonClientResult.Ok -> {
                    val info = result.data
                    val missingMethods = info.missingRequiredMethods(DaemonClient.REQUIRED_METHODS)
                    val compatibilityWarning = when {
                        info.controlProtocolVersion in 1 until DaemonClient.MIN_CONTROL_PROTOCOL_VERSION ->
                            messages.get(
                                com.privstack.panel.R.string.daemon_status_incompatible_protocol,
                                info.controlProtocolVersion,
                                DaemonClient.MIN_CONTROL_PROTOCOL_VERSION,
                            )
                        missingMethods.isNotEmpty() ->
                            messages.get(
                                com.privstack.panel.R.string.daemon_status_missing_methods,
                                missingMethods.joinToString(", "),
                            )
                        else -> null
                    }
                    _uiState.update {
                        it.copy(
                            moduleVersion = info.daemonVersion,
                            daemonStatusText = compatibilityWarning
                                ?: messages.get(
                                    com.privstack.panel.R.string.daemon_status_running_with_core,
                                    info.coreVersion,
                                ),
                            errorMessage = compatibilityWarning,
                        )
                    }
                    _updateState.update {
                        it.copy(currentVersion = info.daemonVersion)
                    }
                    when (val statusResult = daemonClient.status()) {
                        is DaemonClientResult.Ok -> {
                            _uiState.update {
                                it.copy(
                                    daemonStatusText = formatRuntimeStatus(
                                        statusResult.data,
                                        it.daemonStatusText,
                                    ),
                                )
                            }
                        }
                        else -> Unit
                    }
                }
                is DaemonClientResult.DaemonNotFound -> {
                    _uiState.update {
                        it.copy(
                            daemonStatusText = messages.get(
                                com.privstack.panel.R.string.daemon_status_module_not_installed
                            )
                        )
                    }
                }
                is DaemonClientResult.RootDenied -> {
                    _uiState.update {
                        it.copy(
                            daemonStatusText = messages.get(
                                com.privstack.panel.R.string.daemon_status_root_denied
                            )
                        )
                    }
                }
                is DaemonClientResult.Timeout -> {
                    _uiState.update {
                        it.copy(
                            daemonStatusText = messages.get(
                                com.privstack.panel.R.string.daemon_status_not_responding
                            )
                        )
                    }
                }
                else -> {
                    _uiState.update {
                        it.copy(
                            daemonStatusText = messages.get(
                                com.privstack.panel.R.string.daemon_status_unknown_text
                            )
                        )
                    }
                }
            }
        }
    }

    /**
     * Save the DNS URL to the daemon's profile config.
     */
    private fun saveDnsToProfile(url: String, previousPreset: DnsPreset) {
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(dns = config.dns.copy(remoteDns = url))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save DNS setting: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(dnsPreset = previousPreset, errorMessage = err) }
            }
        }
    }

    private fun formatRuntimeStatus(status: DaemonStatus, fallback: String): String {
        return when (status.state) {
            ConnectionState.CONNECTED -> {
                if (status.health.healthy && !status.health.operationalHealthy) {
                    messages.get(
                        com.privstack.panel.R.string.daemon_status_running_degraded,
                        status.health.lastError
                            ?: messages.get(com.privstack.panel.R.string.runtime_operational_degraded),
                    )
                } else {
                    fallback.ifBlank { messages.get(com.privstack.panel.R.string.state_connected) }
                }
            }
            ConnectionState.CONNECTING ->
                messages.get(com.privstack.panel.R.string.state_connecting)
            ConnectionState.DISCONNECTED ->
                messages.get(com.privstack.panel.R.string.daemon_status_stopped)
            ConnectionState.ERROR ->
                messages.get(
                    com.privstack.panel.R.string.daemon_status_error_with_reason,
                    status.health.lastError
                        ?: messages.get(com.privstack.panel.R.string.daemon_status_unknown_text),
                )
            ConnectionState.UNKNOWN ->
                messages.get(com.privstack.panel.R.string.daemon_status_unknown_text)
        }
    }

    private fun <T> formatUpdateError(result: DaemonClientResult<T>): String =
        messages.formatDaemonFailure(result)

    private fun parsePackageList(raw: String): List<String> =
        raw.split('\n', ',', ';', ' ', '\t')
            .map(String::trim)
            .filter(String::isNotBlank)
            .distinct()

    private fun summarizeResetReport(report: com.privstack.panel.model.ResetReport): String {
        if (report.errors.isEmpty() && report.leftovers.isEmpty()) {
            return messages.get(com.privstack.panel.R.string.reset_summary_all_done)
        }
        return buildString {
            append(messages.get(com.privstack.panel.R.string.reset_summary_partial_prefix))
            append('\n')
            append(report.steps.filter { it.status != "ok" }.joinToString("\n") { step ->
                val stepValue = if (step.detail.isBlank()) {
                    when (step.status.lowercase()) {
                        "error" -> messages.get(com.privstack.panel.R.string.state_error)
                        "ok" -> messages.get(com.privstack.panel.R.string.node_test_status_ok)
                        else -> step.status
                    }
                } else {
                    step.detail
                }
                messages.get(
                    com.privstack.panel.R.string.reset_summary_step,
                    step.name,
                    stepValue,
                )
            })
            if (report.leftovers.isNotEmpty()) {
                append('\n')
                append(messages.get(com.privstack.panel.R.string.reset_summary_leftovers_prefix))
                append('\n')
                append(report.leftovers.joinToString("\n"))
            }
            if (report.rebootRequired) {
                append('\n')
                append(messages.get(com.privstack.panel.R.string.reset_summary_reboot_required))
            }
        }
    }
}
