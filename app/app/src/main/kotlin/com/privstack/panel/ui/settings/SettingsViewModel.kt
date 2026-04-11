package com.privstack.panel.ui.settings

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.ipc.DaemonClient
import com.privstack.panel.ipc.DaemonClientResult
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

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
    DOWNLOADING, DOWNLOADED, INSTALLING, INSTALLED, ERROR
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
)

@HiltViewModel
class SettingsViewModel @Inject constructor(
    private val daemonClient: DaemonClient,
) : ViewModel() {

    private val _uiState = MutableStateFlow(SettingsUiState())
    val uiState: StateFlow<SettingsUiState> = _uiState.asStateFlow()

    private val _updateState = MutableStateFlow(UpdateUiState())
    val updateState: StateFlow<UpdateUiState> = _updateState.asStateFlow()

    // ---- Public actions ----

    fun setRoutingMode(mode: RoutingMode) {
        _uiState.update { it.copy(routingMode = mode) }
        // TODO: DaemonRepository.setRoutingMode(mode)
    }

    fun setDnsPreset(preset: DnsPreset) {
        _uiState.update { it.copy(dnsPreset = preset) }
        // TODO: DaemonRepository.setDns(preset.url)
    }

    fun setCustomDnsUrl(url: String) {
        _uiState.update { it.copy(customDnsUrl = url) }
    }

    fun applyCustomDns() {
        val url = _uiState.value.customDnsUrl
        if (url.isNotBlank()) {
            _uiState.update { it.copy(dnsPreset = DnsPreset.CUSTOM) }
            // TODO: DaemonRepository.setDns(url)
        }
    }

    fun toggleFragment() {
        _uiState.update { it.copy(fragmentEnabled = !it.fragmentEnabled) }
        // TODO: DaemonRepository.setFragment(state)
    }

    fun toggleMux() {
        _uiState.update { it.copy(muxEnabled = !it.muxEnabled) }
        // TODO: DaemonRepository.setMux(state)
    }

    fun setLogLevel(level: LogLevel) {
        _uiState.update { it.copy(logLevel = level) }
        // TODO: DaemonRepository.setLogLevel(level)
    }

    fun restartDaemon() {
        // TODO: DaemonRepository.restart()
        _uiState.update { it.copy(daemonStatusText = "Restarting...") }
    }

    fun setThemeMode(mode: ThemeMode) {
        _uiState.update { it.copy(themeMode = mode) }
        // TODO: persist to SharedPreferences / DataStore
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
