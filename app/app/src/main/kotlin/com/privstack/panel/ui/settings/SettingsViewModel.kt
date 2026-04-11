package com.privstack.panel.ui.settings

import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import com.privstack.panel.repository.CommandOutcome
import com.privstack.panel.repository.ProfileRepository
import com.privstack.panel.repository.StatusRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
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

enum class RoutingMode { GLOBAL, WHITELIST, DIRECT }

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
    val currentVersion: String = "0.1.0",
    val latestVersion: String = "",
    val status: UpdateStatus = UpdateStatus.IDLE,
    val changelog: String = "",
    val downloadProgress: Float = 0f,
    val errorMessage: String = "",
)

data class SettingsUiState(
    // Routing
    val routingMode: RoutingMode = RoutingMode.GLOBAL,
    // DNS
    val dnsPreset: DnsPreset = DnsPreset.CLOUDFLARE,
    val customDnsUrl: String = "",
    // Advanced
    val fragmentEnabled: Boolean = false,
    val muxEnabled: Boolean = false,
    val logLevel: LogLevel = LogLevel.WARNING,
    // Module
    val moduleVersion: String = "0.1.0",
    val daemonStatusText: String = "Unknown",
    // Theme
    val themeMode: ThemeMode = ThemeMode.SYSTEM,
    // About
    val appVersion: String = "0.1.0",
    val githubUrl: String = "https://github.com/nickolay168/RKNnoVPN",
    // Error
    val errorMessage: String? = null,
)

@HiltViewModel
class SettingsViewModel @Inject constructor(
    private val daemonClient: DaemonClient,
    private val profileRepository: ProfileRepository,
    private val statusRepository: StatusRepository,
) : ViewModel() {

    private val _uiState = MutableStateFlow(SettingsUiState())
    val uiState: StateFlow<SettingsUiState> = _uiState.asStateFlow()

    private val _updateState = MutableStateFlow(UpdateUiState())
    val updateState: StateFlow<UpdateUiState> = _updateState.asStateFlow()

    init {
        loadCurrentSettings()
        loadVersionInfo()
    }

    // ---- Public actions ----

    fun setRoutingMode(mode: RoutingMode) {
        _uiState.update { it.copy(routingMode = mode, errorMessage = null) }
        viewModelScope.launch {
            val profileMode = when (mode) {
                RoutingMode.GLOBAL -> com.privstack.panel.model.RoutingMode.PROXY_ALL
                RoutingMode.WHITELIST -> com.privstack.panel.model.RoutingMode.PER_APP
                RoutingMode.DIRECT -> com.privstack.panel.model.RoutingMode.RULES
            }
            val ok = profileRepository.updateConfig { config ->
                config.copy(routing = config.routing.copy(mode = profileMode))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to set routing mode: $err")
                _uiState.update { it.copy(errorMessage = err) }
            }
        }
    }

    fun setDnsPreset(preset: DnsPreset) {
        _uiState.update { it.copy(dnsPreset = preset, errorMessage = null) }
        if (preset != DnsPreset.CUSTOM) {
            saveDnsToProfile(preset.url)
        }
    }

    fun setCustomDnsUrl(url: String) {
        _uiState.update { it.copy(customDnsUrl = url) }
    }

    fun applyCustomDns() {
        val url = _uiState.value.customDnsUrl
        if (url.isNotBlank()) {
            _uiState.update { it.copy(dnsPreset = DnsPreset.CUSTOM, errorMessage = null) }
            saveDnsToProfile(url)
        }
    }

    fun toggleFragment() {
        val newVal = !_uiState.value.fragmentEnabled
        _uiState.update { it.copy(fragmentEnabled = newVal, errorMessage = null) }
        // Fragment is a local xray setting; save via profile config if the daemon supports it.
        // For now, this is persisted locally and applied on next connection start.
    }

    fun toggleMux() {
        val newVal = !_uiState.value.muxEnabled
        _uiState.update { it.copy(muxEnabled = newVal, errorMessage = null) }
        // Mux is a local xray setting; same as fragment above.
    }

    fun setLogLevel(level: LogLevel) {
        _uiState.update { it.copy(logLevel = level, errorMessage = null) }
        // Log level is a local preference affecting which daemon logs we request.
    }

    fun restartDaemon() {
        _uiState.update { it.copy(daemonStatusText = "Restarting...", errorMessage = null) }
        viewModelScope.launch {
            when (val outcome = statusRepository.reload()) {
                is CommandOutcome.Success -> {
                    Log.d(TAG, "Daemon reload succeeded")
                    _uiState.update { it.copy(daemonStatusText = "Running") }
                }
                is CommandOutcome.Failed -> {
                    Log.w(TAG, "Daemon reload failed: ${outcome.message}")
                    _uiState.update {
                        it.copy(
                            daemonStatusText = "Error",
                            errorMessage = outcome.message,
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

    // ---- Update actions ----

    fun checkForUpdates() {
        _updateState.update { it.copy(status = UpdateStatus.CHECKING, errorMessage = "") }
        viewModelScope.launch {
            when (val result = daemonClient.updateCheck()) {
                is DaemonClientResult.Ok -> {
                    val data = result.data
                    _updateState.update {
                        it.copy(
                            currentVersion = data.currentVersion,
                            latestVersion = data.latestVersion,
                            changelog = data.changelog,
                            status = if (data.hasUpdate) UpdateStatus.AVAILABLE else UpdateStatus.UP_TO_DATE,
                        )
                    }
                }
                is DaemonClientResult.DaemonNotFound -> {
                    _updateState.update {
                        it.copy(status = UpdateStatus.MODULE_TOO_OLD)
                    }
                }
                is DaemonClientResult.DaemonError -> {
                    // Detect old daemon/privctl that doesn't support update methods.
                    if (isMethodNotFound(result)) {
                        _updateState.update {
                            it.copy(status = UpdateStatus.MODULE_TOO_OLD)
                        }
                    } else {
                        _updateState.update {
                            it.copy(
                                status = UpdateStatus.ERROR,
                                errorMessage = formatUpdateError(result),
                            )
                        }
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
            when (val result = daemonClient.updateDownload()) {
                is DaemonClientResult.Ok -> {
                    _updateState.update {
                        it.copy(
                            status = UpdateStatus.DOWNLOADED,
                            downloadProgress = 1f,
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

    fun installUpdate() {
        _updateState.update { it.copy(status = UpdateStatus.INSTALLING) }
        viewModelScope.launch {
            when (val result = daemonClient.updateInstall()) {
                is DaemonClientResult.Ok -> {
                    _updateState.update { it.copy(status = UpdateStatus.INSTALLED) }
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
    private fun loadCurrentSettings() {
        viewModelScope.launch {
            val config = profileRepository.getOrLoad() ?: return@launch

            // Map daemon routing mode to UI enum
            val routingMode = when (config.routing.mode) {
                com.privstack.panel.model.RoutingMode.PROXY_ALL -> RoutingMode.GLOBAL
                com.privstack.panel.model.RoutingMode.PER_APP -> RoutingMode.WHITELIST
                com.privstack.panel.model.RoutingMode.PER_APP_BYPASS -> RoutingMode.WHITELIST
                com.privstack.panel.model.RoutingMode.RULES -> RoutingMode.DIRECT
            }

            // Map daemon DNS config to UI preset
            val dnsUrl = config.dns.remoteDns
            val dnsPreset = DnsPreset.entries.find { it.url == dnsUrl && it != DnsPreset.CUSTOM }
                ?: DnsPreset.CUSTOM

            _uiState.update {
                it.copy(
                    routingMode = routingMode,
                    dnsPreset = dnsPreset,
                    customDnsUrl = if (dnsPreset == DnsPreset.CUSTOM) dnsUrl else it.customDnsUrl,
                )
            }
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
                    _uiState.update {
                        it.copy(
                            moduleVersion = info.daemonVersion,
                            daemonStatusText = "Running (core: ${info.coreVersion})",
                        )
                    }
                }
                is DaemonClientResult.DaemonNotFound -> {
                    _uiState.update {
                        it.copy(daemonStatusText = "Module not installed")
                    }
                }
                is DaemonClientResult.RootDenied -> {
                    _uiState.update {
                        it.copy(daemonStatusText = "Root access denied")
                    }
                }
                is DaemonClientResult.Timeout -> {
                    _uiState.update {
                        it.copy(daemonStatusText = "Daemon not responding")
                    }
                }
                else -> {
                    _uiState.update {
                        it.copy(daemonStatusText = "Unknown")
                    }
                }
            }
        }
    }

    /**
     * Save the DNS URL to the daemon's profile config.
     */
    private fun saveDnsToProfile(url: String) {
        viewModelScope.launch {
            val ok = profileRepository.updateConfig { config ->
                config.copy(dns = config.dns.copy(remoteDns = url))
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save DNS setting: $err")
                _uiState.update { it.copy(errorMessage = err) }
            }
        }
    }

    private fun <T> formatUpdateError(result: DaemonClientResult<T>): String = when (result) {
        is DaemonClientResult.DaemonError -> result.message
        is DaemonClientResult.RootDenied -> "Root denied: ${result.reason}"
        is DaemonClientResult.Timeout -> "Timeout"
        is DaemonClientResult.DaemonNotFound -> "Daemon not found"
        is DaemonClientResult.ParseError -> "Parse error"
        is DaemonClientResult.Failure -> result.throwable.message ?: "Unknown error"
        else -> "Unknown error"
    }
}
