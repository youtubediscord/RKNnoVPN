package com.privstack.panel.ui.settings

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.BuildConfig
import com.privstack.panel.i18n.UserMessageFormatter
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.model.ConnectionState
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.DnsIpv6Mode
import com.privstack.panel.model.FallbackPolicy
import com.privstack.panel.model.ProfileConfig
import com.privstack.panel.model.UpdateInstallState
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

enum class DnsPreset(
    val remoteUrl: String,
    val directUrl: String,
    val bootstrapIp: String,
) {
    CLOUDFLARE("https://1.1.1.1/dns-query", "https://1.1.1.1/dns-query", "1.1.1.1"),
    GOOGLE("https://dns.google/dns-query", "https://dns.google/dns-query", "8.8.8.8"),
    MULLVAD("https://adblock.dns.mullvad.net/dns-query", "https://adblock.dns.mullvad.net/dns-query", "194.242.2.3"),
    ADGUARD("https://dns.adguard-dns.com/dns-query", "https://dns.adguard-dns.com/dns-query", "94.140.14.14"),
    CUSTOM("", "", ""),
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
    val remoteDnsUrl: String = DnsPreset.CLOUDFLARE.remoteUrl,
    val directDnsUrl: String = DnsPreset.CLOUDFLARE.directUrl,
    val bootstrapDnsIp: String = DnsPreset.CLOUDFLARE.bootstrapIp,
    val dnsIpv6Mode: DnsIpv6Mode = DnsIpv6Mode.MIRROR,
    val blockQuicDns: Boolean = true,
    val fakeDns: Boolean = false,
    val urlTestUrl: String = "https://www.gstatic.com/generate_204",
    val alwaysDirectPackagesText: String = "",
    val sharingEnabled: Boolean = false,
    val sharingInterfacesText: String = "",
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
    val isResetting: Boolean = false,
    val runtimeActionActive: Boolean = false,
    val logsText: String = "",
    val isLoadingLogs: Boolean = false,
    val shareLogsText: String? = null,
    val shareLogsEventId: Long = 0L,
    val copyReportText: String? = null,
    val copyReportEventId: Long = 0L,
)

@HiltViewModel
class SettingsViewModel @Inject constructor(
    private val daemonClient: DaemonClient,
    private val profileRepository: ProfileRepository,
    private val statusRepository: StatusRepository,
    private val messages: UserMessageFormatter,
) : ViewModel() {

    private val _uiState = MutableStateFlow(SettingsUiState())
    val uiState: StateFlow<SettingsUiState> = _uiState.asStateFlow()

    private val _updateState = MutableStateFlow(UpdateUiState())
    val updateState: StateFlow<UpdateUiState> = _updateState.asStateFlow()

    private var lastCheckedHasUpdate: Boolean = false

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
        if (preset == DnsPreset.CUSTOM) {
            _uiState.update { it.copy(dnsPreset = preset, errorMessage = null) }
            return
        }
        _uiState.update {
            it.copy(
                dnsPreset = preset,
                remoteDnsUrl = preset.remoteUrl,
                directDnsUrl = preset.directUrl,
                bootstrapDnsIp = preset.bootstrapIp,
                errorMessage = null,
            )
        }
        saveDnsToProfile(previousPreset)
    }

    fun setRemoteDnsUrl(url: String) {
        _uiState.update {
            it.copy(
                dnsPreset = DnsPreset.CUSTOM,
                remoteDnsUrl = url,
                errorMessage = null,
            )
        }
    }

    fun setDirectDnsUrl(url: String) {
        _uiState.update {
            it.copy(
                dnsPreset = DnsPreset.CUSTOM,
                directDnsUrl = url,
                errorMessage = null,
            )
        }
    }

    fun setBootstrapDnsIp(value: String) {
        _uiState.update {
            it.copy(
                dnsPreset = DnsPreset.CUSTOM,
                bootstrapDnsIp = value,
                errorMessage = null,
            )
        }
    }

    fun setDnsIpv6Mode(mode: DnsIpv6Mode) {
        val previous = _uiState.value.dnsIpv6Mode
        _uiState.update { it.copy(dnsIpv6Mode = mode, errorMessage = null) }
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(dns = config.dns.copy(ipv6Mode = mode))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save DNS IPv6 mode: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(dnsIpv6Mode = previous, errorMessage = err) }
            }
        }
    }

    fun setBlockQuicDns(enabled: Boolean) {
        val previous = _uiState.value.blockQuicDns
        _uiState.update { it.copy(blockQuicDns = enabled, errorMessage = null) }
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(dns = config.dns.copy(blockQuic = enabled))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save DNS QUIC policy: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(blockQuicDns = previous, errorMessage = err) }
            }
        }
    }

    fun setFakeDns(enabled: Boolean) {
        val previous = _uiState.value.fakeDns
        _uiState.update { it.copy(fakeDns = enabled, errorMessage = null) }
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(dns = config.dns.copy(fakeDns = enabled))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save FakeDNS policy: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(fakeDns = previous, errorMessage = err) }
            }
        }
    }

    fun applyDnsSettings() {
        val state = _uiState.value
        if (state.remoteDnsUrl.isBlank() || state.directDnsUrl.isBlank() || state.bootstrapDnsIp.isBlank()) {
            _uiState.update {
                it.copy(errorMessage = messages.get(com.privstack.panel.R.string.dns_settings_required))
            }
            return
        }
        saveDnsToProfile(state.dnsPreset)
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

    fun setSharingEnabled(enabled: Boolean) {
        val previous = _uiState.value.sharingEnabled
        _uiState.update { it.copy(sharingEnabled = enabled, errorMessage = null) }
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(sharing = config.sharing.copy(enabled = enabled))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save sharing mode: $err")
                profileRepository.refresh()
                _uiState.update { it.copy(sharingEnabled = previous, errorMessage = err) }
            }
        }
    }

    fun setSharingInterfacesText(value: String) {
        _uiState.update { it.copy(sharingInterfacesText = value, errorMessage = null) }
    }

    fun applySharingInterfaces() {
        val interfaces = parsePackageList(_uiState.value.sharingInterfacesText)
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(sharing = config.sharing.copy(interfaces = interfaces))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save sharing interfaces: $err")
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
        if (_uiState.value.runtimeActionActive) return
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
        if (_uiState.value.runtimeActionActive || _uiState.value.isResetting) return
        _uiState.update {
            it.copy(
                daemonStatusText = messages.get(com.privstack.panel.R.string.daemon_status_resetting),
                errorMessage = null,
                lastResetSummary = null,
                isResetting = true,
            )
        }
        viewModelScope.launch {
            when (val result = statusRepository.networkReset()) {
                is DaemonClientResult.Ok -> {
                    Log.d(TAG, "Backend reset accepted; waiting for status result")
                    _uiState.update {
                        it.copy(
                            daemonStatusText = messages.get(com.privstack.panel.R.string.daemon_status_resetting),
                            errorMessage = null,
                            lastResetSummary = messages.get(com.privstack.panel.R.string.operation_accepted),
                            isResetting = true,
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
                            isResetting = false,
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
            _uiState.update { it.copy(shareLogsText = current, shareLogsEventId = it.shareLogsEventId + 1) }
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
                            shareLogsEventId = it.shareLogsEventId + 1,
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

    fun copyDiagnosticReport() {
        _uiState.update { it.copy(isLoadingLogs = true, errorMessage = null) }
        viewModelScope.launch {
            when (val result = daemonClient.diagnosticBundle(lines = 220)) {
                is DaemonClientResult.Ok -> {
                    _uiState.update {
                        it.copy(
                            logsText = result.data,
                            copyReportText = result.data,
                            copyReportEventId = it.copyReportEventId + 1,
                            isLoadingLogs = false,
                        )
                    }
                }
                else -> {
                    val message = formatUpdateError(result)
                    Log.w(TAG, "Failed to copy diagnostic report: $message")
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

    fun clearCopiedReport() {
        _uiState.update { it.copy(copyReportText = null) }
    }

    // ---- Update actions ----

    fun checkForUpdates() {
        _updateState.update { it.copy(status = UpdateStatus.CHECKING, errorMessage = "") }
        viewModelScope.launch {
            when (val result = daemonClient.updateCheck()) {
                is DaemonClientResult.Ok -> {
                    val info = result.data
                    lastCheckedHasUpdate = info.hasUpdate
                    _updateState.update {
                        it.copy(
                            currentVersion = info.currentVersion,
                            latestVersion = info.latestVersion,
                            changelog = info.changelog,
                            status = if (info.hasUpdate) UpdateStatus.AVAILABLE else UpdateStatus.UP_TO_DATE,
                            modulePath = "",
                            apkPath = "",
                        )
                    }
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

    fun downloadUpdate() {
        _updateState.update { it.copy(status = UpdateStatus.DOWNLOADING, downloadProgress = 0f) }
        viewModelScope.launch {
            if (!lastCheckedHasUpdate) {
                _updateState.update {
                    it.copy(
                        status = UpdateStatus.ERROR,
                        errorMessage = messages.get(com.privstack.panel.R.string.update_error_no_checked_update),
                    )
                }
                return@launch
            }

            when (val result = daemonClient.updateDownload()) {
                is DaemonClientResult.Ok -> {
                    val downloaded = result.data
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

    fun installUpdate() {
        if (_uiState.value.runtimeActionActive) return
        _updateState.update { it.copy(status = UpdateStatus.INSTALLING) }
        viewModelScope.launch {
            val current = _updateState.value
            if (current.modulePath.isBlank() || current.apkPath.isBlank()) {
                _updateState.update {
                    it.copy(
                        status = UpdateStatus.ERROR,
                        errorMessage = messages.get(com.privstack.panel.R.string.update_error_no_downloaded_update),
                    )
                }
                return@launch
            }

            when (val result = daemonClient.updateInstall(
                modulePath = current.modulePath,
                apkPath = current.apkPath,
            )) {
                is DaemonClientResult.Ok -> {
                    result.data.runtimeStatus?.let(statusRepository::publishBackendStatus)
                    _updateState.update {
                        it.copy(
                            status = if (result.data.accepted) UpdateStatus.INSTALLING else UpdateStatus.ERROR,
                            errorMessage = if (result.data.accepted) {
                                messages.get(com.privstack.panel.R.string.operation_accepted)
                            } else {
                                messages.get(com.privstack.panel.R.string.update_error_install_without_artifacts)
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
                    val completedUpdateInstall = status.lastOperation
                        ?.takeIf { operation -> operation.kind == "update-install" && status.activeOperation == null }
                    if (completedUpdateInstall != null) {
                        _updateState.update { current ->
                            if (current.status != UpdateStatus.INSTALLING) {
                                current
                            } else if (completedUpdateInstall.succeeded) {
                                current.copy(
                                    status = UpdateStatus.INSTALLED,
                                    errorMessage = "",
                                )
                            } else {
                                current.copy(
                                    status = UpdateStatus.ERROR,
                                    errorMessage = completedUpdateInstall.errorMessage.ifBlank {
                                        messages.get(com.privstack.panel.R.string.update_error_install_failed)
                                    },
                                )
                            }
                        }
                    }
                    status.updateInstall
                        ?.takeIf { install ->
                            status.activeOperation == null &&
                                install.status in setOf("running", "failed", "unknown")
                        }
                        ?.let { install ->
                            _updateState.update {
                                it.copy(
                                    status = UpdateStatus.ERROR,
                                    errorMessage = formatPersistedUpdateInstallState(install),
                                )
                            }
                        }
                    _uiState.update {
                        val resetResult = status.lastOperation
                            ?.takeIf { operation -> it.isResetting && operation.kind == "reset" && status.activeOperation == null }
                        if (resetResult != null) {
                            val report = resetResult.resetReport
                            it.copy(
                                daemonStatusText = if (resetResult.succeeded) {
                                    messages.get(com.privstack.panel.R.string.daemon_status_stopped)
                                } else {
                                    messages.get(com.privstack.panel.R.string.daemon_status_partial_reset)
                                },
                                errorMessage = resetResult.errorMessage.ifBlank { null },
                                lastResetSummary = report?.let(::summarizeResetReport)
                                    ?: resetResult.errorMessage.ifBlank { messages.get(com.privstack.panel.R.string.state_error) },
                                isResetting = false,
                                runtimeActionActive = false,
                            )
                        } else {
                            it.copy(
                                daemonStatusText = formatRuntimeStatus(status, it.daemonStatusText),
                                isResetting = status.activeOperation?.kind == "reset",
                                runtimeActionActive = status.activeOperation != null,
                            )
                        }
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

        val dnsPreset = DnsPreset.entries.find {
            it != DnsPreset.CUSTOM &&
                it.remoteUrl == config.dns.remoteDns &&
                it.directUrl == config.dns.directDns
        }
            ?: DnsPreset.CUSTOM

        _uiState.update {
            it.copy(
                fallbackPolicy = config.runtime.fallbackPolicy,
                routingMode = routingMode,
                dnsPreset = dnsPreset,
                remoteDnsUrl = config.dns.remoteDns,
                directDnsUrl = config.dns.directDns,
                bootstrapDnsIp = config.dns.bootstrapIp,
                dnsIpv6Mode = config.dns.ipv6Mode,
                blockQuicDns = config.dns.blockQuic,
                fakeDns = config.dns.fakeDns,
                urlTestUrl = config.health.checkUrl,
                alwaysDirectPackagesText = config.routing.alwaysDirectAppList.joinToString("\n"),
                sharingEnabled = config.sharing.enabled,
                sharingInterfacesText = config.sharing.interfaces.joinToString("\n"),
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
                        info.releaseMismatch(BuildConfig.VERSION_NAME) != null ->
                            info.releaseMismatch(BuildConfig.VERSION_NAME)
                        info.currentReleaseWarning() != null ->
                            info.currentReleaseWarning()
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
                        !info.singBoxAvailable ->
                            messages.get(
                                com.privstack.panel.R.string.daemon_status_sing_box_unavailable,
                                info.singBoxError.ifBlank { "unknown" },
                            )
                        else -> null
                    }
                    _uiState.update {
                        it.copy(
                            moduleVersion = info.moduleVersion.ifBlank { info.daemonVersion },
                            daemonStatusText = compatibilityWarning
                                ?: messages.get(
                                    com.privstack.panel.R.string.daemon_status_running_with_core,
                                    messages.get(
                                        com.privstack.panel.R.string.daemon_status_runtime_versions,
                                        info.daemonVersion,
                                        info.moduleVersion.ifBlank { "unknown" },
                                        info.coreVersion,
                                    ),
                                ),
                            errorMessage = compatibilityWarning,
                        )
                    }
                    _updateState.update {
                        it.copy(currentVersion = BuildConfig.VERSION_NAME)
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
     * Save DNS settings to the daemon profile config.
     */
    private fun saveDnsToProfile(previousPreset: DnsPreset) {
        val state = _uiState.value
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(
                    dns = config.dns.copy(
                        remoteDns = state.remoteDnsUrl.trim(),
                        directDns = state.directDnsUrl.trim(),
                        bootstrapIp = state.bootstrapDnsIp.trim(),
                        ipv6Mode = state.dnsIpv6Mode,
                        blockQuic = state.blockQuicDns,
                        fakeDns = state.fakeDns,
                    )
                )
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
        val healthIssue = messages.formatHealthIssue(
            status.health.lastCode,
            status.health.lastError,
            status.health.lastUserMessage,
            status.health.stageReport,
        )
        return when (status.state) {
            ConnectionState.CONNECTED -> {
                if (status.health.healthy && !status.health.operationalHealthy) {
                    messages.get(
                        com.privstack.panel.R.string.daemon_status_running_degraded,
                        healthIssue,
                    )
                } else {
                    fallback.ifBlank { messages.get(com.privstack.panel.R.string.state_connected) }
                }
            }
            ConnectionState.CONNECTING ->
                when {
                    status.activeOperation?.stuck == true ->
                        formatStuckOperation(
                            status.activeOperation.stepDetail,
                            status.activeOperation.step,
                        )
                    status.activeOperation?.kind == "reset" ->
                        messages.get(com.privstack.panel.R.string.daemon_status_resetting)
                    status.activeOperation?.kind == "restart" || status.activeOperation?.kind == "reload" ->
                        messages.get(com.privstack.panel.R.string.daemon_status_restarting)
                    else ->
                        messages.get(com.privstack.panel.R.string.state_connecting)
                }
            ConnectionState.DISCONNECTED ->
                messages.get(com.privstack.panel.R.string.daemon_status_stopped)
            ConnectionState.ERROR ->
                messages.get(
                    com.privstack.panel.R.string.daemon_status_error_with_reason,
                    healthIssue,
                )
            ConnectionState.UNKNOWN ->
                messages.get(com.privstack.panel.R.string.daemon_status_unknown_text)
        }
    }

    private fun formatStuckOperation(stepDetail: String, step: String): String {
        val currentStep = stepDetail.ifBlank { step }.trim()
        return if (currentStep.isBlank()) {
            messages.get(com.privstack.panel.R.string.daemon_status_operation_stuck)
        } else {
            messages.get(com.privstack.panel.R.string.daemon_status_operation_stuck_with_step, currentStep)
        }
    }

    private fun <T> formatUpdateError(result: DaemonClientResult<T>): String =
        messages.formatDaemonFailure(result)

    private fun formatPersistedUpdateInstallState(state: UpdateInstallState): String {
        val step = state.step.ifBlank { state.code }.ifBlank { state.status }
        val detail = state.detail.ifBlank { state.code }.ifBlank { state.status }
        return messages.get(com.privstack.panel.R.string.update_error_install_interrupted, step, detail)
    }

    private fun parsePackageList(raw: String): List<String> =
        raw.split('\n', ',', ';', ' ', '\t')
            .map(String::trim)
            .filter(String::isNotBlank)
            .distinct()

    private fun summarizeResetReport(report: com.privstack.panel.model.ResetReport): String {
        if (report.errors.isEmpty() && report.warnings.isEmpty() && report.leftovers.isEmpty()) {
            return messages.get(com.privstack.panel.R.string.reset_summary_all_done)
        }
        return buildString {
            append(
                messages.get(
                    if (report.errors.isEmpty()) {
                        com.privstack.panel.R.string.reset_summary_warning_prefix
                    } else {
                        com.privstack.panel.R.string.reset_summary_partial_prefix
                    },
                ),
            )
            append('\n')
            val stepLines = report.steps.filter { it.status != "ok" }.map { step ->
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
            }
            val warningLines = report.warnings.filter { it.isNotBlank() }
            append((stepLines + warningLines).joinToString("\n"))
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
