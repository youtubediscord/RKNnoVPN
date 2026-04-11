package com.privstack.panel.ui.apps

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import dagger.hilt.android.lifecycle.HiltViewModel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import javax.inject.Inject

/**
 * Lightweight representation of an installed Android app.
 */
data class AppInfo(
    val packageName: String,
    val label: String,
    val isSystemApp: Boolean,
    val isProxied: Boolean = false,
)

data class AppPickerUiState(
    val apps: List<AppInfo> = emptyList(),
    val searchQuery: String = "",
    val showSystemApps: Boolean = false,
    val isLoading: Boolean = true,
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
class AppPickerViewModel @Inject constructor() : ViewModel() {

    private val _uiState = MutableStateFlow(AppPickerUiState())
    val uiState: StateFlow<AppPickerUiState> = _uiState.asStateFlow()

    init {
        loadApps()
    }

    fun setSearchQuery(query: String) {
        _uiState.update { it.copy(searchQuery = query) }
    }

    fun toggleShowSystemApps() {
        _uiState.update { it.copy(showSystemApps = !it.showSystemApps) }
    }

    fun toggleApp(packageName: String) {
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
        val proxiedPackages = _uiState.value.apps
            .filter { it.isProxied }
            .map { it.packageName }
        // TODO: send to DaemonRepository.setPerAppProxy(proxiedPackages)
    }

    // ---- Internal ----

    private fun loadApps() {
        viewModelScope.launch {
            _uiState.update { it.copy(isLoading = true) }

            // TODO: real PackageManager query via a Repository/UseCase
            delay(600)

            val demoApps = listOf(
                AppInfo("com.android.chrome", "Chrome", false),
                AppInfo("org.mozilla.firefox", "Firefox", false),
                AppInfo("org.telegram.messenger", "Telegram", false),
                AppInfo("com.whatsapp", "WhatsApp", false),
                AppInfo("com.discord", "Discord", false),
                AppInfo("com.instagram.android", "Instagram", false),
                AppInfo("com.twitter.android", "X (Twitter)", false),
                AppInfo("com.google.android.youtube", "YouTube", false),
                AppInfo("com.spotify.music", "Spotify", false),
                AppInfo("com.netflix.mediaclient", "Netflix", false),
                AppInfo("ru.sberbankmobile", "Sberbank", false),
                AppInfo("com.android.vending", "Play Store", true),
                AppInfo("com.google.android.gms", "Google Play Services", true),
                AppInfo("com.android.settings", "Settings", true),
            )

            _uiState.update {
                it.copy(
                    apps = demoApps.sortedBy { app -> app.label.lowercase() },
                    isLoading = false,
                )
            }
        }
    }
}

enum class AppTemplate {
    BROWSERS,
    SOCIAL,
    STREAMING,
    ALL_EXCEPT_BANKS,
}
