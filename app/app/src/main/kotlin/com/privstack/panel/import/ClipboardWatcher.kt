package com.rknnovpn.panel.`import`

import android.content.ClipboardManager
import android.content.Context
import com.rknnovpn.panel.model.Node

/**
 * Monitors the system clipboard for proxy URIs.
 *
 * Designed to be called on Activity resume. Checks the primary clipboard text for
 * recognised proxy schemes and returns an [ImportPreview] when one or more links
 * are detected.
 *
 * Usage:
 * ```kotlin
 * override fun onResume() {
 *     super.onResume()
 *     val preview = ClipboardWatcher.check(this)
 *     if (preview != null) {
 *         // Show import dialog
 *     }
 * }
 * ```
 */
object ClipboardWatcher {

    /**
     * SHA-256 of the last clipboard text we already offered to import.
     * Prevents showing the import dialog repeatedly for the same content.
     */
    private var lastOfferedHash: String? = null

    /**
     * Preview returned when the clipboard contains importable proxy links.
     */
    data class ImportPreview(
        /** Raw clipboard text. */
        val rawText: String,
        /** URIs detected in the text. */
        val detectedUris: List<String>,
        /** Nodes that were successfully parsed (subset of detectedUris). */
        val parsedNodes: List<Node>,
        /** Number of URIs that failed to parse. */
        val parseFailures: Int,
        /** True if the clipboard text looks like a subscription URL rather than direct links. */
        val isSubscription: Boolean
    )

    /**
     * Check the clipboard for proxy content.
     *
     * @param context Android context (needed for ClipboardManager).
     * @return An [ImportPreview] if new importable content is detected, or null otherwise.
     */
    fun check(context: Context): ImportPreview? {
        val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
            ?: return null

        if (!clipboard.hasPrimaryClip()) return null

        val clip = clipboard.primaryClip ?: return null
        if (clip.itemCount == 0) return null

        val text = clip.getItemAt(0)?.coerceToText(context)?.toString()
        if (text.isNullOrBlank()) return null

        // Deduplicate: don't re-offer the same clipboard content.
        val hash = hashText(text)
        if (hash == lastOfferedHash) return null

        // Check if it's a subscription URL.
        if (LinkParser.isSubscriptionUrl(text.trim())) {
            lastOfferedHash = hash
            return ImportPreview(
                rawText = text,
                detectedUris = emptyList(),
                parsedNodes = emptyList(),
                parseFailures = 0,
                isSubscription = true
            )
        }

        // Detect proxy URIs.
        val uris = LinkParser.detectUris(text)
        if (uris.isEmpty()) return null

        // Parse each detected URI.
        val nodes = mutableListOf<Node>()
        var failures = 0
        for (uri in uris) {
            val node = LinkParser.parse(uri)
            if (node != null) {
                nodes += node
            } else {
                failures++
            }
        }

        // Only offer if at least one node parsed successfully.
        if (nodes.isEmpty()) return null

        lastOfferedHash = hash

        return ImportPreview(
            rawText = text,
            detectedUris = uris,
            parsedNodes = nodes,
            parseFailures = failures,
            isSubscription = false
        )
    }

    /**
     * Explicitly check a given [text] (not from clipboard) for proxy content.
     * Does not affect the deduplication state.
     */
    fun checkText(text: String): ImportPreview? {
        if (text.isBlank()) return null

        if (LinkParser.isSubscriptionUrl(text.trim())) {
            return ImportPreview(
                rawText = text,
                detectedUris = emptyList(),
                parsedNodes = emptyList(),
                parseFailures = 0,
                isSubscription = true
            )
        }

        val uris = LinkParser.detectUris(text)
        if (uris.isEmpty()) return null

        val nodes = mutableListOf<Node>()
        var failures = 0
        for (uri in uris) {
            val node = LinkParser.parse(uri)
            if (node != null) {
                nodes += node
            } else {
                failures++
            }
        }

        if (nodes.isEmpty()) return null

        return ImportPreview(
            rawText = text,
            detectedUris = uris,
            parsedNodes = nodes,
            parseFailures = failures,
            isSubscription = false
        )
    }

    /**
     * Reset the deduplication state, so the next [check] call will offer
     * even if the clipboard hasn't changed.
     */
    fun resetState() {
        lastOfferedHash = null
    }

    /**
     * Mark the current clipboard content as already offered, without parsing.
     * Useful after the user dismisses an import dialog.
     */
    fun markCurrentAsOffered(context: Context) {
        val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager
            ?: return
        val clip = clipboard.primaryClip ?: return
        if (clip.itemCount == 0) return
        val text = clip.getItemAt(0)?.coerceToText(context)?.toString() ?: return
        lastOfferedHash = hashText(text)
    }

    // ---------- internals ----------

    /**
     * Simple hash for dedup. Uses Java's hashCode for speed; collisions are harmless
     * (worst case: we skip an import offer).
     */
    private fun hashText(text: String): String {
        return try {
            val digest = java.security.MessageDigest.getInstance("SHA-256")
            val bytes = digest.digest(text.toByteArray(Charsets.UTF_8))
            bytes.joinToString("") { "%02x".format(it) }
        } catch (_: Exception) {
            // Fallback to simple hash.
            text.hashCode().toString()
        }
    }
}
