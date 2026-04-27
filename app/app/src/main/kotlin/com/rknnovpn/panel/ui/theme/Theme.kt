package com.rknnovpn.panel.ui.theme

import android.os.Build
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext

// Teal/green palette for fallback (pre-Android 12 or dynamic color disabled)
private val TealPrimary = Color(0xFF00897B)
private val TealOnPrimary = Color(0xFFFFFFFF)
private val TealPrimaryContainer = Color(0xFFB2DFDB)
private val TealOnPrimaryContainer = Color(0xFF00251E)

private val GreenSecondary = Color(0xFF43A047)
private val GreenOnSecondary = Color(0xFFFFFFFF)
private val GreenSecondaryContainer = Color(0xFFC8E6C9)
private val GreenOnSecondaryContainer = Color(0xFF002204)

private val TealTertiary = Color(0xFF26A69A)
private val TealOnTertiary = Color(0xFFFFFFFF)
private val TealTertiaryContainer = Color(0xFF80CBC4)
private val TealOnTertiaryContainer = Color(0xFF00201C)

private val ErrorColor = Color(0xFFBA1A1A)
private val OnErrorColor = Color(0xFFFFFFFF)
private val ErrorContainerColor = Color(0xFFFFDAD6)
private val OnErrorContainerColor = Color(0xFF410002)

private val LightColorScheme = lightColorScheme(
    primary = TealPrimary,
    onPrimary = TealOnPrimary,
    primaryContainer = TealPrimaryContainer,
    onPrimaryContainer = TealOnPrimaryContainer,
    secondary = GreenSecondary,
    onSecondary = GreenOnSecondary,
    secondaryContainer = GreenSecondaryContainer,
    onSecondaryContainer = GreenOnSecondaryContainer,
    tertiary = TealTertiary,
    onTertiary = TealOnTertiary,
    tertiaryContainer = TealTertiaryContainer,
    onTertiaryContainer = TealOnTertiaryContainer,
    error = ErrorColor,
    onError = OnErrorColor,
    errorContainer = ErrorContainerColor,
    onErrorContainer = OnErrorContainerColor,
    background = Color(0xFFFBFDF9),
    onBackground = Color(0xFF191C1B),
    surface = Color(0xFFFBFDF9),
    onSurface = Color(0xFF191C1B),
    surfaceVariant = Color(0xFFDAE5E1),
    onSurfaceVariant = Color(0xFF3F4946),
    outline = Color(0xFF6F7976),
    outlineVariant = Color(0xFFBEC9C5),
)

private val DarkTealPrimary = Color(0xFF4DB6AC)
private val DarkTealOnPrimary = Color(0xFF003731)
private val DarkTealPrimaryContainer = Color(0xFF005048)
private val DarkTealOnPrimaryContainer = Color(0xFFB2DFDB)

private val DarkGreenSecondary = Color(0xFF81C784)
private val DarkGreenOnSecondary = Color(0xFF00390B)
private val DarkGreenSecondaryContainer = Color(0xFF005315)
private val DarkGreenOnSecondaryContainer = Color(0xFFC8E6C9)

private val DarkTealTertiary = Color(0xFF80CBC4)
private val DarkTealOnTertiary = Color(0xFF003733)
private val DarkTealTertiaryContainer = Color(0xFF00504A)
private val DarkTealOnTertiaryContainer = Color(0xFFB2DFDB)

private val DarkColorScheme = darkColorScheme(
    primary = DarkTealPrimary,
    onPrimary = DarkTealOnPrimary,
    primaryContainer = DarkTealPrimaryContainer,
    onPrimaryContainer = DarkTealOnPrimaryContainer,
    secondary = DarkGreenSecondary,
    onSecondary = DarkGreenOnSecondary,
    secondaryContainer = DarkGreenSecondaryContainer,
    onSecondaryContainer = DarkGreenOnSecondaryContainer,
    tertiary = DarkTealTertiary,
    onTertiary = DarkTealOnTertiary,
    tertiaryContainer = DarkTealTertiaryContainer,
    onTertiaryContainer = DarkTealOnTertiaryContainer,
    error = Color(0xFFFFB4AB),
    onError = Color(0xFF690005),
    errorContainer = Color(0xFF93000A),
    onErrorContainer = Color(0xFFFFDAD6),
    background = Color(0xFF191C1B),
    onBackground = Color(0xFFE1E3DF),
    surface = Color(0xFF191C1B),
    onSurface = Color(0xFFE1E3DF),
    surfaceVariant = Color(0xFF3F4946),
    onSurfaceVariant = Color(0xFFBEC9C5),
    outline = Color(0xFF899390),
    outlineVariant = Color(0xFF3F4946),
)

@Composable
fun RKNnoVPNTheme(
    darkTheme: Boolean = isSystemInDarkTheme(),
    dynamicColor: Boolean = true,
    content: @Composable () -> Unit,
) {
    val colorScheme = when {
        dynamicColor && Build.VERSION.SDK_INT >= Build.VERSION_CODES.S -> {
            val context = LocalContext.current
            if (darkTheme) dynamicDarkColorScheme(context)
            else dynamicLightColorScheme(context)
        }
        darkTheme -> DarkColorScheme
        else -> LightColorScheme
    }

    MaterialTheme(
        colorScheme = colorScheme,
        content = content,
    )
}
