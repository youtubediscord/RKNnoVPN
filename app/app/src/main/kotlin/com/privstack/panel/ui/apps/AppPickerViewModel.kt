package com.privstack.panel.ui.apps

import android.content.Context
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.util.Log
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.privstack.panel.model.ProfileConfig
import com.privstack.panel.model.RoutingMode
import com.privstack.panel.repository.ProfileRepository
import dagger.hilt.android.lifecycle.HiltViewModel
import dagger.hilt.android.qualifiers.ApplicationContext
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import javax.inject.Inject

private const val TAG = "AppPickerViewModel"

private data class RoutingSelection(
    val routingMode: RoutingMode,
    val selectedPackages: Set<String>,
)

/**
 * Lightweight representation of an installed Android app for the UI layer.
 */
data class AppInfo(
    val packageName: String,
    val label: String,
    val isSystemApp: Boolean,
    val isProxied: Boolean = false,
)

data class AppPickerUiState(
    val apps: List<AppInfo> = emptyList(),
    val routingMode: RoutingMode = RoutingMode.PER_APP,
    val searchQuery: String = "",
    val showSystemApps: Boolean = false,
    val isLoading: Boolean = true,
    /** Error message from the last operation, or null. */
    val errorMessage: String? = null,
) {
    val filteredApps: List<AppInfo>
        get() {
            var list = apps
            if (!showSystemApps) {
                list = list.filter { !it.isSystemApp }
            }
            if (searchQuery.isNotBlank()) {
                val q = searchQuery.lowercase()
                list = list.filter {
                    it.label.lowercase().contains(q) ||
                        it.packageName.lowercase().contains(q)
                }
            }
            return list
        }

    val proxiedCount: Int
        get() = apps.count { it.isProxied }

    val supportsPerAppSelection: Boolean
        get() = routingMode == RoutingMode.PER_APP || routingMode == RoutingMode.PER_APP_BYPASS
}

/** Well-known package names for quick-select templates. */
private val BROWSER_PACKAGES = setOf(
    "com.android.chrome", "org.mozilla.firefox", "com.brave.browser",
    "com.opera.browser", "com.opera.mini.native", "com.vivaldi.browser",
    "com.microsoft.emmx", "org.chromium.chrome",
)
private val SOCIAL_PACKAGES = setOf(
    "com.twitter.android", "com.instagram.android", "com.facebook.katana",
    "org.telegram.messenger", "com.whatsapp", "com.discord",
    "com.snapchat.android", "com.reddit.frontpage",
)
private val STREAMING_PACKAGES = setOf(
    "com.google.android.youtube", "com.netflix.mediaclient",
    "com.spotify.music", "tv.twitch.android.app",
    "com.amazon.avod", "com.disney.disneyplus",
)
private val BANK_PACKAGES = setOf(
    "ru.sberbankmobile", "ru.alfabank.mobile.android",
    "ru.tinkoff.investing", "ru.vtb24.mobilebanking.android",
    "com.idamob.tinkoff.android", "ru.raiffeisennews",
)

@HiltViewModel
class AppPickerViewModel @Inject constructor(
    @ApplicationContext private val context: Context,
    private val profileRepository: ProfileRepository,
) : ViewModel() {

    private val _uiState = MutableStateFlow(AppPickerUiState())
    val uiState: StateFlow<AppPickerUiState> = _uiState.asStateFlow()

    init {
        observeProfile()
        loadApps()
    }

    fun setSearchQuery(query: String) {
        _uiState.update { it.copy(searchQuery = query) }
    }

    fun toggleShowSystemApps() {
        _uiState.update { it.copy(showSystemApps = !it.showSystemApps) }
    }

    fun toggleApp(packageName: String) {
        if (!_uiState.value.supportsPerAppSelection) return
        _uiState.update { state ->
            state.copy(
                apps = state.apps.map {
                    if (it.packageName == packageName) it.copy(isProxied = !it.isProxied)
                    else it
                },
            )
        }
    }

    fun applyTemplate(template: AppTemplate) {
        if (!_uiState.value.supportsPerAppSelection) return
        _uiState.update { state ->
            val targetPackages = when (template) {
                AppTemplate.BROWSERS -> BROWSER_PACKAGES
                AppTemplate.SOCIAL -> SOCIAL_PACKAGES
                AppTemplate.STREAMING -> STREAMING_PACKAGES
                AppTemplate.ALL_EXCEPT_BANKS -> {
                    // Select everything except banks
                    state.apps
                        .filter { it.packageName !in BANK_PACKAGES }
                        .map { it.packageName }
                        .toSet()
                }
            }

            state.copy(
                apps = state.apps.map { app ->
                    if (template == AppTemplate.ALL_EXCEPT_BANKS) {
                        app.copy(isProxied = app.packageName in targetPackages)
                    } else {
                        // For specific templates, toggle ON those packages, leave others unchanged
                        if (app.packageName in targetPackages) app.copy(isProxied = true)
                        else app
                    }
                },
            )
        }
    }

    fun applySelection() {
        val selectedPackages = _uiState.value.apps
            .filter { it.isProxied }
            .map { it.packageName }
        val routingMode = _uiState.value.routingMode

        viewModelScope.launch {
            _uiState.update { it.copy(errorMessage = null) }

            if (!routingMode.usesPerAppSelection()) {
                _uiState.update {
                    it.copy(errorMessage = "Per-app selection is unavailable in the current routing mode")
                }
                return@launch
            }

            val ok = profileRepository.updateConfig { config ->
                config.copy(
                    routing = config.routing.copy(
                        appProxyList = if (routingMode == RoutingMode.PER_APP) selectedPackages else emptyList(),
                        appBypassList = if (routingMode == RoutingMode.PER_APP_BYPASS) selectedPackages else emptyList(),
                    ),
                )
            }
            if (!ok) {
                val err = profileRepository.error.value
                Log.w(TAG, "Failed to save app selection: $err")
                profileRepository.refresh()
                _uiState.update {
                    it.copy(errorMessage = err ?: "Failed to save app selection")
                }
            } else {
                Log.d(TAG, "Saved ${selectedPackages.size} apps for routing mode $routingMode")
            }
        }
    }

    // ---- Internal ----

    /**
     * Load the real list of installed apps from PackageManager, then merge
     * with the daemon's per-app proxy list to mark which apps are proxied.
     */
    private fun loadApps() {
        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true, errorMessage = null) }

            try {
                // 1. Get installed apps from PackageManager (runs on IO)
                val installedApps = withContext(Dispatchers.IO) {
                    queryInstalledApps()
                }

                // 2. Get the current per-app proxy list from daemon config
                val daemonSelection = loadRoutingSelectionFromDaemon()

                // 3. Merge into UI model
                val appInfoList = installedApps.map { (pkg, label, isSystem) ->
                    AppInfo(
                        packageName = pkg,
                        label = label,
                        isSystemApp = isSystem,
                        isProxied = pkg in daemonSelection.selectedPackages,
                    )
                }.sortedBy { it.label.lowercase() }

                _uiState.update {
                    it.copy(
                        apps = appInfoList,
                        routingMode = daemonSelection.routingMode,
                        isLoading = false,
                    )
                }
            } catch (e: Exception) {
                Log.e(TAG, "Failed to load apps", e)
                _uiState.update {
                    it.copy(
                        isLoading = false,
                        errorMessage = "Failed to load apps: ${e.message}",
                    )
                }
            }
        }
    }

    /**
     * Query all installed applications via Android PackageManager.
     * Returns a list of (packageName, label, isSystemApp) triples.
     */
    private fun queryInstalledApps(): List<Triple<String, String, Boolean>> {
        val pm = context.packageManager
        @Suppress("DEPRECATION")
        val applications = pm.getInstalledApplications(PackageManager.GET_META_DATA)

        return applications.mapNotNull { appInfo ->
            // Skip apps without a launcher intent (pure services, providers, etc.)
            // but keep system apps that the user might want to route
            val label = try {
                appInfo.loadLabel(pm).toString()
            } catch (_: Exception) {
                appInfo.packageName
            }

            val isSystem = (appInfo.flags and ApplicationInfo.FLAG_SYSTEM) != 0

            Triple(appInfo.packageName, label, isSystem)
        }
    }

    /**
     * Load the set of package names currently configured for per-app proxy
     * from the daemon's profile config.
     *
     * Returns an empty selection if the daemon is unreachable (graceful fallback).
     */
    private suspend fun loadRoutingSelectionFromDaemon(): RoutingSelection {
        val config = profileRepository.getOrLoad()
        if (config != null) {
            return config.toRoutingSelection()
        }

        // Fallback: daemon unreachable, return empty set
        val err = profileRepository.error.value
        if (err != null) {
            Log.w(TAG, "Could not load proxied apps from daemon: $err")
        }
        return RoutingSelection(RoutingMode.PROXY_ALL, emptySet())
    }

    private fun observeProfile() {
        viewModelScope.launch {
            profileRepository.profile
                .filterNotNull()
                .collect { config ->
                    val selection = config.toRoutingSelection()
                    _uiState.update { state ->
                        state.copy(
                            routingMode = selection.routingMode,
                            apps = state.apps.map { app ->
                                app.copy(isProxied = app.packageName in selection.selectedPackages)
                            },
                        )
                    }
                }
        }
    }

}

private fun RoutingMode.usesPerAppSelection(): Boolean =
    this == RoutingMode.PER_APP || this == RoutingMode.PER_APP_BYPASS

private fun ProfileConfig.toRoutingSelection(): RoutingSelection {
    val routingMode = routing.mode
    val selected = when (routingMode) {
        RoutingMode.PER_APP -> routing.appProxyList.toSet()
        RoutingMode.PER_APP_BYPASS -> routing.appBypassList.toSet()
        else -> emptySet()
    }
    return RoutingSelection(routingMode, selected)
}

enum class AppTemplate {
    BROWSERS,
    SOCIAL,
    STREAMING,
    ALL_EXCEPT_BANKS,
}
