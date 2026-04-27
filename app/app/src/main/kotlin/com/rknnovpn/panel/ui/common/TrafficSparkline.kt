package com.rknnovpn.panel.ui.common

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/**
 * Reusable Canvas-based sparkline chart with cubic bezier smoothing.
 *
 * @param points Normalized values in 0..1 range
 * @param lineColor Stroke color of the line
 * @param fillColor Top color of the gradient fill below the line (fades to transparent)
 * @param strokeWidth Line thickness
 * @param height Chart height
 */
@Composable
fun TrafficSparkline(
    points: List<Float>,
    modifier: Modifier = Modifier,
    lineColor: Color = MaterialTheme.colorScheme.primary,
    fillColor: Color = MaterialTheme.colorScheme.primary.copy(alpha = 0.15f),
    strokeWidth: Dp = 2.dp,
    height: Dp = 80.dp,
) {
    Canvas(
        modifier = modifier
            .fillMaxWidth()
            .height(height),
    ) {
        if (points.size < 2) return@Canvas

        val w = size.width
        val h = size.height
        val strokePx = strokeWidth.toPx()

        // Padding so the line does not clip at edges
        val padY = strokePx
        val usableH = h - padY * 2

        // Map points to pixel coordinates
        val coords = points.mapIndexed { i, v ->
            val x = w * i / (points.size - 1).toFloat()
            val y = padY + usableH * (1f - v.coerceIn(0f, 1f))
            Offset(x, y)
        }

        // Build cubic bezier path
        val linePath = buildCubicPath(coords)

        // Gradient fill below the line
        val fillPath = Path().apply {
            addPath(linePath)
            lineTo(coords.last().x, h)
            lineTo(coords.first().x, h)
            close()
        }

        drawPath(
            path = fillPath,
            brush = Brush.verticalGradient(
                colors = listOf(fillColor, Color.Transparent),
                startY = 0f,
                endY = h,
            ),
        )

        // Stroke the line
        drawPath(
            path = linePath,
            color = lineColor,
            style = Stroke(
                width = strokePx,
                cap = StrokeCap.Round,
                join = StrokeJoin.Round,
            ),
        )
    }
}

/**
 * Build a smooth cubic bezier path through the given points.
 * Uses Catmull-Rom-to-Bezier conversion for natural curves.
 */
private fun DrawScope.buildCubicPath(coords: List<Offset>): Path {
    val path = Path()
    if (coords.isEmpty()) return path

    path.moveTo(coords.first().x, coords.first().y)

    if (coords.size == 2) {
        path.lineTo(coords[1].x, coords[1].y)
        return path
    }

    for (i in 0 until coords.size - 1) {
        val p0 = if (i > 0) coords[i - 1] else coords[i]
        val p1 = coords[i]
        val p2 = coords[i + 1]
        val p3 = if (i + 2 < coords.size) coords[i + 2] else coords[i + 1]

        // Catmull-Rom to cubic Bezier control points
        val cp1x = p1.x + (p2.x - p0.x) / 6f
        val cp1y = p1.y + (p2.y - p0.y) / 6f
        val cp2x = p2.x - (p3.x - p1.x) / 6f
        val cp2y = p2.y - (p3.y - p1.y) / 6f

        path.cubicTo(cp1x, cp1y, cp2x, cp2y, p2.x, p2.y)
    }

    return path
}
