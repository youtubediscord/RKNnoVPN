package com.rknnovpn.panel.model

import kotlinx.serialization.Serializable

/**
 * Metadata about an installed Android application, used for per-app routing.
 *
 * Retrieved via `daemonctl app.list` (daemon reads package data from the system)
 * or resolved locally from PackageManager for faster access.
 */
@Serializable
data class AppInfo(
    /** Android package name, e.g. "com.google.android.youtube". */
    val packageName: String,
    /** Human-readable app label. */
    val appName: String,
    /** Linux UID assigned to this app. */
    val uid: Int,
    /** True for apps installed in /system or /vendor partitions. */
    val isSystemApp: Boolean = false,
    /**
     * Broad category for grouping in the UI.
     * Maps to PackageManager application category where available.
     */
    val category: AppCategory = AppCategory.OTHER,
    /** Path to the APK, useful for icon loading. */
    val apkPath: String? = null,
    /** Version name string, e.g. "18.45.41". */
    val versionName: String? = null,
    /** Whether the app is currently enabled. */
    val enabled: Boolean = true
) {
    /** Stable identifier for per-app routing rules. */
    val routingKey: String get() = packageName
}

@Serializable
enum class AppCategory {
    SOCIAL,
    MESSAGING,
    VIDEO,
    AUDIO,
    BROWSER,
    GAME,
    PRODUCTIVITY,
    SYSTEM,
    OTHER;

    companion object {
        /**
         * Map Android's [android.content.pm.ApplicationInfo.category] int
         * to our enum. Falls back to [OTHER] for unknown values.
         */
        fun fromAndroidCategory(value: Int): AppCategory = when (value) {
            0 -> GAME          // CATEGORY_GAME
            1 -> AUDIO         // CATEGORY_AUDIO
            2 -> VIDEO         // CATEGORY_VIDEO
            3 -> BROWSER       // CATEGORY_IMAGE (we lump into OTHER) -- re-mapped below
            4 -> SOCIAL        // CATEGORY_SOCIAL
            5 -> MESSAGING     // CATEGORY_NEWS (closest)
            6 -> BROWSER       // CATEGORY_MAPS -> OTHER in practice
            7 -> PRODUCTIVITY  // CATEGORY_PRODUCTIVITY
            else -> OTHER
        }
    }
}
