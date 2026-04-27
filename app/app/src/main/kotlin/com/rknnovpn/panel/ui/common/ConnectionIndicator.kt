package com.rknnovpn.panel.ui.common

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.ErrorOutline
import androidx.compose.material.icons.filled.PowerSettingsNew
import androidx.compose.material.icons.filled.Shield
import androidx.compose.material.icons.filled.Sync
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.rknnovpn.panel.model.ConnectionState

/**
 * Reusable animated connection state indicator.
 *
 * - [ConnectionState.DISCONNECTED]: gray ring with power icon
 * - [ConnectionState.CONNECTING]: pulsing ring with sync icon
 * - [ConnectionState.CONNECTED]: solid green ring with shield icon
 * - [ConnectionState.ERROR]: red ring with error icon
 * - [ConnectionState.UNKNOWN]: outline ring with power icon
 */
@Composable
fun ConnectionIndicator(
    state: ConnectionState,
    modifier: Modifier = Modifier,
    size: Dp = 160.dp,
    ringWidth: Dp = 6.dp,
) {
    val targetColor = when (state) {
        ConnectionState.CONNECTED -> MaterialTheme.colorScheme.primary
        ConnectionState.CONNECTING -> MaterialTheme.colorScheme.tertiary
        ConnectionState.ERROR -> MaterialTheme.colorScheme.error
        ConnectionState.DISCONNECTED -> MaterialTheme.colorScheme.outline
        ConnectionState.UNKNOWN -> MaterialTheme.colorScheme.outlineVariant
    }

    val animatedColor by animateColorAsState(
        targetValue = targetColor,
        animationSpec = tween(durationMillis = 600),
        label = "ring_color",
    )

    val icon: ImageVector = when (state) {
        ConnectionState.CONNECTED -> Icons.Filled.Shield
        ConnectionState.CONNECTING -> Icons.Filled.Sync
        ConnectionState.ERROR -> Icons.Filled.ErrorOutline
        ConnectionState.DISCONNECTED,
        ConnectionState.UNKNOWN -> Icons.Filled.PowerSettingsNew
    }

    val iconTint by animateColorAsState(
        targetValue = targetColor,
        animationSpec = tween(durationMillis = 600),
        label = "icon_color",
    )

    // Pulsing alpha for CONNECTING state
    val infiniteTransition = rememberInfiniteTransition(label = "pulse")
    val pulseAlpha by infiniteTransition.animateFloat(
        initialValue = 0.3f,
        targetValue = 1.0f,
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 1000, easing = LinearEasing),
            repeatMode = RepeatMode.Reverse,
        ),
        label = "pulse_alpha",
    )

    val ringAlpha = if (state == ConnectionState.CONNECTING) pulseAlpha else 1f

    Box(
        contentAlignment = Alignment.Center,
        modifier = modifier.size(size),
    ) {
        Canvas(modifier = Modifier.size(size)) {
            val stroke = Stroke(
                width = ringWidth.toPx(),
                cap = StrokeCap.Round,
            )
            val radius = (this.size.minDimension - ringWidth.toPx()) / 2f
            drawCircle(
                color = animatedColor.copy(alpha = ringAlpha),
                radius = radius,
                center = Offset(this.size.width / 2f, this.size.height / 2f),
                style = stroke,
            )

            // Outer glow ring for CONNECTING
            if (state == ConnectionState.CONNECTING) {
                val outerRadius = radius + ringWidth.toPx() * 1.5f
                drawCircle(
                    color = animatedColor.copy(alpha = pulseAlpha * 0.25f),
                    radius = outerRadius,
                    center = Offset(this.size.width / 2f, this.size.height / 2f),
                    style = Stroke(width = ringWidth.toPx() * 0.5f),
                )
            }
        }

        Icon(
            imageVector = icon,
            contentDescription = null,
            tint = iconTint,
            modifier = Modifier.size(size * 0.35f),
        )
    }
}
