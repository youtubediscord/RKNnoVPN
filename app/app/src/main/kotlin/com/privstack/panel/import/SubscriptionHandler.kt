package com.privstack.panel.`import`

import android.util.Log
import com.privstack.panel.model.Node
import com.privstack.panel.model.Protocol
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive

/**
 * Handles proxy subscription URLs.
 *
 * Because the panel APK does not hold the INTERNET permission, the actual HTTP fetch
 * is delegated to the daemon process via IPC. This class provides:
 *
 * - Parsing the subscription response body (base64-encoded list of URIs).
 * - Parsing the `subscription-userinfo` response header.
 * - Merging newly fetched nodes with the user's existing list (preserving overrides).
 */
object SubscriptionHandler {

    private const val TAG = "SubscriptionHandler"

    // ---------- data classes ----------

    /**
     * Parsed `subscription-userinfo` header.
     *
     * Format: `upload=<bytes>; download=<bytes>; total=<bytes>; expire=<unix_timestamp>`
     */
    @Serializable
    data class SubscriptionInfo(
        val uploadBytes: Long = 0,
        val downloadBytes: Long = 0,
        val totalBytes: Long = 0,
        /** Unix timestamp (seconds) when the subscription expires, or 0 if unset. */
        val expireTimestamp: Long = 0
    ) {
        /** Remaining traffic in bytes, or null if total is unknown. */
        val remainingBytes: Long?
            get() = if (totalBytes > 0) (totalBytes - uploadBytes - downloadBytes).coerceAtLeast(0) else null

        /** Whether the subscription has expired. */
        fun isExpired(nowSeconds: Long = System.currentTimeMillis() / 1000): Boolean =
            expireTimestamp > 0 && nowSeconds >= expireTimestamp
    }

    /**
     * Result of fetching and parsing a subscription URL.
     */
    data class SubscriptionResult(
        /** Nodes parsed from the response body. */
        val nodes: List<Node>,
        /** Quota / expiry info from the subscription-userinfo header, if present. */
        val info: SubscriptionInfo?,
        /** Number of lines in the body that could not be parsed. */
        val parseFailures: Int
    )

    /**
     * User-facing summary for merge preview.
     */
    data class MergePreview(
        /** Nodes that are new (not present in the existing list). */
        val added: List<Node>,
        /** Nodes that matched an existing entry and will update it. */
        val updated: List<Node>,
        /** Existing nodes that are no longer in the subscription. */
        val removed: List<Node>,
        /** Nodes kept unchanged. */
        val unchanged: List<Node>
    )

    // ---------- response parsing ----------

    /**
     * Parse a subscription response body.
     *
     * Typical format: the body is base64-encoded, and when decoded it contains one proxy URI
     * per line.  Some providers return plain text (one URI per line) without base64 wrapping.
     *
     * @param body    Raw response body (may be base64 or plain).
     * @param headers Response headers (case-insensitive map). Used to extract subscription-userinfo.
     * @return A [SubscriptionResult] with parsed nodes and metadata.
     */
    fun parseResponse(body: String, headers: Map<String, String> = emptyMap()): SubscriptionResult {
        val info = parseSubscriptionUserinfo(headers)

        val decoded = decodeBody(body)
        val lines = decoded.lines()
            .map { it.trim() }
            .filter { it.isNotEmpty() }

        var failures = 0
        val nodes = mutableListOf<Node>()

        for (line in lines) {
            val node = LinkParser.parse(line)
            if (node != null) {
                nodes += node
            } else {
                failures++
                Log.d(TAG, "Skipped unparseable line: ${line.take(80)}")
            }
        }

        return SubscriptionResult(nodes, info, failures)
    }

    /**
     * Parse the `subscription-userinfo` header value.
     *
     * Format: `upload=123; download=456; total=789; expire=1700000000`
     */
    fun parseSubscriptionUserinfo(headers: Map<String, String>): SubscriptionInfo? {
        // Find header (case-insensitive).
        val value = headers.entries
            .firstOrNull { it.key.equals("subscription-userinfo", ignoreCase = true) }
            ?.value ?: return null

        val parts = mutableMapOf<String, Long>()
        for (segment in value.split(';')) {
            val trimmed = segment.trim()
            val eq = trimmed.indexOf('=')
            if (eq == -1) continue
            val key = trimmed.substring(0, eq).trim().lowercase()
            val num = trimmed.substring(eq + 1).trim().toLongOrNull() ?: continue
            parts[key] = num
        }

        if (parts.isEmpty()) return null

        return SubscriptionInfo(
            uploadBytes = parts["upload"] ?: 0,
            downloadBytes = parts["download"] ?: 0,
            totalBytes = parts["total"] ?: 0,
            expireTimestamp = parts["expire"] ?: 0
        )
    }

    // ---------- merge strategy ----------

    /**
     * Compute a merge preview: compares [incoming] nodes from a subscription refresh
     * with the user's [existing] node list.
     *
     * Match key: `(server, port, credential)` where credential is the protocol-specific
     * identifying secret (UUID for VLESS/VMess, password for Trojan/SS).
     *
     * User overrides (custom name, group assignment, latency) on matched nodes are preserved.
     */
    fun previewMerge(existing: List<Node>, incoming: List<Node>): MergePreview {
        val existingByKey = existing.associateBy { nodeMatchKey(it) }
        val incomingByKey = incoming.associateBy { nodeMatchKey(it) }

        val added = mutableListOf<Node>()
        val updated = mutableListOf<Node>()
        val unchanged = mutableListOf<Node>()
        val removed = mutableListOf<Node>()

        // Walk incoming nodes.
        for ((key, inNode) in incomingByKey) {
            val exNode = existingByKey[key]
            if (exNode == null) {
                added += inNode
            } else if (hasOutboundChanged(exNode, inNode)) {
                // Outbound changed: update but keep user overrides.
                updated += inNode.copy(
                    id = exNode.id,
                    name = exNode.name,
                    group = exNode.group,
                    latencyMs = exNode.latencyMs,
                    createdAt = exNode.createdAt
                )
            } else {
                unchanged += exNode
            }
        }

        // Existing nodes not present in incoming.
        for ((key, exNode) in existingByKey) {
            if (key !in incomingByKey) {
                removed += exNode
            }
        }

        return MergePreview(added, updated, removed, unchanged)
    }

    /**
     * Apply a [MergePreview] to produce the new node list.
     *
     * By default, removed nodes are kept (marked stale) rather than deleted.
     * Pass [dropRemoved] = true to actually delete them.
     */
    fun applyMerge(preview: MergePreview, dropRemoved: Boolean = false): List<Node> {
        val result = mutableListOf<Node>()
        result += preview.unchanged
        result += preview.updated
        result += preview.added
        if (!dropRemoved) {
            result += preview.removed
        }
        return result
    }

    // ---------- internals ----------

    /**
     * Decode the subscription body.  Tries base64 first; falls back to plain text.
     */
    private fun decodeBody(body: String): String {
        val trimmed = body.trim()

        // Quick heuristic: if it starts with a known scheme, it's already plain.
        if (LinkParser.detectUris(trimmed.lines().first()).isNotEmpty()) {
            return trimmed
        }

        // Try base64 decode.
        val decoded = LinkParser.tryBase64Decode(trimmed)
        if (decoded != null && decoded.isNotEmpty() && looksLikeUriList(decoded)) {
            return decoded
        }

        // Fallback: treat as plain text.
        return trimmed
    }

    /**
     * Heuristic: does [text] look like it contains at least one proxy URI?
     */
    private fun looksLikeUriList(text: String): Boolean {
        return text.lines().any { line ->
            val t = line.trim()
            t.startsWith("vless://", true) ||
                t.startsWith("vmess://", true) ||
                t.startsWith("trojan://", true) ||
                t.startsWith("ss://", true) ||
                t.startsWith("socks://", true) ||
                t.startsWith("socks4://", true) ||
                t.startsWith("socks4a://", true) ||
                t.startsWith("socks5://", true) ||
                t.startsWith("vpn://", true) ||
                t.startsWith("hysteria2://", true) ||
                t.startsWith("hy2://", true) ||
                t.startsWith("tuic://", true)
        }
    }

    /**
     * Build a match key for deduplication.
     *
     * Key = `protocol|server|port|credential`
     */
    private fun nodeMatchKey(node: Node): String {
        val credential = extractCredential(node)
        return "${node.protocol}|${node.server}|${node.port}|$credential"
    }

    /**
     * Extract the protocol-specific credential from a node's outbound config.
     */
    private fun extractCredential(node: Node): String {
        return try {
            val settings = (node.outbound["settings"] as? JsonObject) ?: return ""

            when (node.protocol) {
                Protocol.VLESS, Protocol.VMESS -> {
                    settings["vnext"]?.jsonArray
                        ?.firstOrNull()?.jsonObject
                        ?.get("users")?.jsonArray
                        ?.firstOrNull()?.jsonObject
                        ?.get("id")?.jsonPrimitive?.content
                        ?: ""
                }
                Protocol.TROJAN -> {
                    settings["servers"]?.jsonArray
                        ?.firstOrNull()?.jsonObject
                        ?.get("password")?.jsonPrimitive?.content
                        ?: ""
                }
                Protocol.SHADOWSOCKS -> {
                    val server = settings["servers"]?.jsonArray
                        ?.firstOrNull()?.jsonObject
                    val method = server?.get("method")?.jsonPrimitive?.content ?: ""
                    val password = server?.get("password")?.jsonPrimitive?.content ?: ""
                    "$method:$password"
                }
                Protocol.SOCKS -> {
                    val username = settings["username"]?.jsonPrimitive?.content ?: ""
                    val password = settings["password"]?.jsonPrimitive?.content ?: ""
                    "$username:$password"
                }
                Protocol.HYSTERIA2 -> settings["password"]?.jsonPrimitive?.content ?: ""
                Protocol.TUIC -> {
                    val uuid = settings["uuid"]?.jsonPrimitive?.content ?: ""
                    val password = settings["password"]?.jsonPrimitive?.content ?: ""
                    "$uuid:$password"
                }
                else -> ""
            }
        } catch (_: Exception) {
            ""
        }
    }

    /**
     * Check whether the outbound config has materially changed between two nodes.
     */
    private fun hasOutboundChanged(old: Node, new: Node): Boolean {
        return old.outbound.toString() != new.outbound.toString()
    }
}
