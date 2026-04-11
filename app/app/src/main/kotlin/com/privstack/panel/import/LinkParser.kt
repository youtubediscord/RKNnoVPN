package com.privstack.panel.`import`

import android.util.Base64
import android.util.Log
import com.privstack.panel.model.Node
import com.privstack.panel.model.Protocol
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.addJsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import java.net.URLDecoder
import java.util.UUID

/**
 * Universal proxy link parser.
 *
 * Handles vless://, vmess://, trojan://, ss:// (SIP002 + legacy), and vpn:// (Amnezia).
 * Produces [Node] instances with the protocol-specific outbound stored as a [JsonObject]
 * in the daemon's canonical xray schema.
 */
object LinkParser {

    private const val TAG = "LinkParser"

    private val json = Json { ignoreUnknownKeys = true }

    // Schemes we recognise in free-text / clipboard detection.
    private val KNOWN_SCHEMES = listOf("vless://", "vmess://", "trojan://", "ss://", "vpn://")

    // Subscription URL heuristics.
    private val SUB_URL_REGEX = Regex(
        """^https?://[^\s]+(/sub|/api|/link|subscribe|token=|sub\?|clash\?)""",
        RegexOption.IGNORE_CASE
    )

    // ---------- public API ----------

    /**
     * Parse a single proxy URI into a [Node], or null if the format is unrecognised.
     */
    fun parse(raw: String): Node? {
        val trimmed = raw.trim()
        return try {
            when {
                trimmed.startsWith("vless://", ignoreCase = true)  -> parseVless(trimmed)
                trimmed.startsWith("vmess://", ignoreCase = true)  -> parseVmess(trimmed)
                trimmed.startsWith("trojan://", ignoreCase = true) -> parseTrojan(trimmed)
                trimmed.startsWith("ss://", ignoreCase = true)     -> parseShadowsocks(trimmed)
                trimmed.startsWith("vpn://", ignoreCase = true)    -> parseAmnezia(trimmed)
                else -> null
            }
        } catch (e: Exception) {
            Log.w(TAG, "Failed to parse link: ${e.message}")
            null
        }
    }

    /**
     * Scan free-form [text] (possibly multi-line) and return every recognised proxy URI found.
     */
    fun detectUris(text: String): List<String> {
        val results = mutableListOf<String>()
        for (line in text.lines()) {
            val trimmed = line.trim()
            if (trimmed.isEmpty()) continue
            for (scheme in KNOWN_SCHEMES) {
                var start = 0
                while (true) {
                    val idx = trimmed.indexOf(scheme, start, ignoreCase = true)
                    if (idx == -1) break
                    // Extend to end of non-whitespace run.
                    val end = trimmed.indexOfAny(charArrayOf(' ', '\t', '\r', '\n'), idx)
                        .takeIf { it != -1 } ?: trimmed.length
                    results += trimmed.substring(idx, end)
                    start = end
                }
            }
        }
        return results
    }

    /**
     * Heuristic: does [text] look like a subscription URL rather than a direct proxy link?
     */
    fun isSubscriptionUrl(text: String): Boolean {
        val trimmed = text.trim()
        if (!trimmed.startsWith("http://", ignoreCase = true) &&
            !trimmed.startsWith("https://", ignoreCase = true)
        ) return false
        // Obvious sub patterns.
        if (SUB_URL_REGEX.containsMatchIn(trimmed)) return true
        // If it's an HTTP(S) URL but not a known scheme, treat as subscription.
        return KNOWN_SCHEMES.none { trimmed.startsWith(it, ignoreCase = true) }
    }

    // ---------- per-protocol parsers ----------

    /**
     * vless://uuid@host:port?params#name
     */
    private fun parseVless(uri: String): Node? {
        val parsed = parseStandardUri(uri, "vless") ?: return null

        val outbound = buildJsonObject {
            put("protocol", "vless")
            putJsonObject("settings") {
                putJsonArray("vnext") {
                    addJsonObject {
                        put("address", parsed.host)
                        put("port", parsed.port)
                        putJsonArray("users") {
                            addJsonObject {
                                put("id", parsed.userInfo)
                                put("encryption", parsed.params["encryption"] ?: "none")
                                val flow = parsed.params["flow"]
                                if (!flow.isNullOrEmpty()) put("flow", flow)
                            }
                        }
                    }
                }
            }
            val tlsObj = parseTls(parsed.params)
            if (tlsObj != null) put("streamSettings", buildStreamSettings(parsed.params, tlsObj))
            else put("streamSettings", buildStreamSettings(parsed.params, null))
        }

        return Node(
            id = UUID.randomUUID().toString(),
            name = parsed.fragment.ifEmpty { "${parsed.host}:${parsed.port}" },
            protocol = Protocol.VLESS,
            server = parsed.host,
            port = parsed.port,
            link = uri,
            outbound = outbound
        )
    }

    /**
     * vmess://base64(json)
     *
     * The JSON follows the v2rayN sharing format with fields: v, ps, add, port, id, aid, scy,
     * net, type, host, path, tls, sni, alpn, fp.
     */
    private fun parseVmess(uri: String): Node? {
        val payload = uri.removePrefix("vmess://").removePrefix("VMESS://")
        val decoded = tryBase64Decode(payload) ?: return null
        val obj = try {
            json.parseToJsonElement(decoded).jsonObject
        } catch (_: Exception) {
            return null
        }

        fun str(key: String): String = obj[key]?.jsonPrimitive?.content.orEmpty()
        fun intOrZero(key: String): Int = str(key).toIntOrNull() ?: 0

        val host = str("add")
        val port = intOrZero("port").takeIf { it > 0 } ?: return null
        val userId = str("id").ifEmpty { return null }
        val name = str("ps").ifEmpty { "$host:$port" }

        // Build canonical params map so we can reuse shared helpers.
        val params = mutableMapOf<String, String>()
        params["type"] = str("net").ifEmpty { "tcp" }
        if (str("tls") == "tls") params["security"] = "tls"
        if (str("sni").isNotEmpty()) params["sni"] = str("sni")
        if (str("fp").isNotEmpty()) params["fp"] = str("fp")
        if (str("alpn").isNotEmpty()) params["alpn"] = str("alpn")
        if (str("host").isNotEmpty()) params["host"] = str("host")
        if (str("path").isNotEmpty()) params["path"] = str("path")
        if (str("type").isNotEmpty() && str("type") != "none") params["headerType"] = str("type")

        val outbound = buildJsonObject {
            put("protocol", "vmess")
            putJsonObject("settings") {
                putJsonArray("vnext") {
                    addJsonObject {
                        put("address", host)
                        put("port", port)
                        putJsonArray("users") {
                            addJsonObject {
                                put("id", userId)
                                put("alterId", intOrZero("aid"))
                                put("security", str("scy").ifEmpty { "auto" })
                            }
                        }
                    }
                }
            }
            val tlsObj = parseTls(params)
            put("streamSettings", buildStreamSettings(params, tlsObj))
        }

        return Node(
            id = UUID.randomUUID().toString(),
            name = name,
            protocol = Protocol.VMESS,
            server = host,
            port = port,
            link = uri,
            outbound = outbound
        )
    }

    /**
     * trojan://password@host:port?params#name
     */
    private fun parseTrojan(uri: String): Node? {
        val parsed = parseStandardUri(uri, "trojan") ?: return null

        val outbound = buildJsonObject {
            put("protocol", "trojan")
            putJsonObject("settings") {
                putJsonArray("servers") {
                    addJsonObject {
                        put("address", parsed.host)
                        put("port", parsed.port)
                        put("password", parsed.userInfo)
                    }
                }
            }
            // Trojan implies TLS by default.
            val security = parsed.params["security"] ?: "tls"
            val tlsParams = parsed.params.toMutableMap().apply {
                putIfAbsent("security", security)
            }
            val tlsObj = parseTls(tlsParams)
            put("streamSettings", buildStreamSettings(tlsParams, tlsObj))
        }

        return Node(
            id = UUID.randomUUID().toString(),
            name = parsed.fragment.ifEmpty { "${parsed.host}:${parsed.port}" },
            protocol = Protocol.TROJAN,
            server = parsed.host,
            port = parsed.port,
            link = uri,
            outbound = outbound
        )
    }

    /**
     * SIP002: ss://base64(method:password)@host:port#name
     * Legacy:  ss://base64(method:password@host:port)#name
     */
    private fun parseShadowsocks(uri: String): Node? {
        val body = uri.removePrefix("ss://").removePrefix("SS://")

        // Extract fragment (name).
        val fragmentIdx = body.lastIndexOf('#')
        val fragment = if (fragmentIdx != -1) urlDecode(body.substring(fragmentIdx + 1)) else ""
        val noFragment = if (fragmentIdx != -1) body.substring(0, fragmentIdx) else body

        // Try SIP002 first: userinfo@host:port
        val atIdx = noFragment.lastIndexOf('@')
        if (atIdx != -1) {
            val userInfoEncoded = noFragment.substring(0, atIdx)
            val hostPort = noFragment.substring(atIdx + 1)
            val decoded = tryBase64Decode(userInfoEncoded)
            if (decoded != null) {
                val colonIdx = decoded.indexOf(':')
                if (colonIdx != -1) {
                    val method = decoded.substring(0, colonIdx)
                    val password = decoded.substring(colonIdx + 1)
                    val (host, port) = parseHostPort(hostPort) ?: return null
                    return buildSsNode(uri, host, port, method, password, fragment)
                }
            }
            // SIP002 plain (method:password not base64-encoded but URL-encoded).
            val plainDecoded = urlDecode(userInfoEncoded)
            val colonIdx = plainDecoded.indexOf(':')
            if (colonIdx != -1) {
                val method = plainDecoded.substring(0, colonIdx)
                val password = plainDecoded.substring(colonIdx + 1)
                val (host, port) = parseHostPort(hostPort) ?: return null
                return buildSsNode(uri, host, port, method, password, fragment)
            }
        }

        // Legacy: entire body (minus fragment) is base64.
        val decoded = tryBase64Decode(noFragment) ?: return null
        // Format: method:password@host:port
        val legacyAtIdx = decoded.lastIndexOf('@')
        if (legacyAtIdx == -1) return null
        val methodPassword = decoded.substring(0, legacyAtIdx)
        val hostPort = decoded.substring(legacyAtIdx + 1)
        val colonIdx = methodPassword.indexOf(':')
        if (colonIdx == -1) return null
        val method = methodPassword.substring(0, colonIdx)
        val password = methodPassword.substring(colonIdx + 1)
        val (host, port) = parseHostPort(hostPort) ?: return null
        return buildSsNode(uri, host, port, method, password, fragment)
    }

    private fun buildSsNode(
        link: String,
        host: String,
        port: Int,
        method: String,
        password: String,
        name: String
    ): Node {
        val outbound = buildJsonObject {
            put("protocol", "shadowsocks")
            putJsonObject("settings") {
                putJsonArray("servers") {
                    addJsonObject {
                        put("address", host)
                        put("port", port)
                        put("method", method)
                        put("password", password)
                    }
                }
            }
            putJsonObject("streamSettings") {
                put("network", "tcp")
            }
        }
        return Node(
            id = UUID.randomUUID().toString(),
            name = name.ifEmpty { "$host:$port" },
            protocol = Protocol.SHADOWSOCKS,
            server = host,
            port = port,
            link = link,
            outbound = outbound
        )
    }

    /**
     * vpn:// (Amnezia format) -- delegates to [AmneziaImporter].
     */
    private fun parseAmnezia(uri: String): Node? = AmneziaImporter.import(uri)

    // ---------- shared builders ----------

    /**
     * Build the xray `streamSettings` object from query params and an optional TLS block.
     */
    private fun buildStreamSettings(params: Map<String, String>, tlsObj: JsonObject?): JsonObject {
        return buildJsonObject {
            val network = params["type"] ?: "tcp"
            put("network", network)

            // TLS / Reality.
            val security = params["security"] ?: ""
            if (security.isNotEmpty()) {
                put("security", security)
            }
            if (tlsObj != null) {
                when (security) {
                    "tls"     -> put("tlsSettings", tlsObj)
                    "reality" -> put("realitySettings", tlsObj)
                    "xtls"    -> put("xtlsSettings", tlsObj)
                    else      -> put("tlsSettings", tlsObj)
                }
            }

            // Transport.
            val transportObj = parseTransport(params)
            when (network) {
                "ws"         -> put("wsSettings", transportObj)
                "grpc"       -> put("grpcSettings", transportObj)
                "tcp"        -> put("tcpSettings", transportObj)
                "kcp", "mkcp" -> put("kcpSettings", transportObj)
                "quic"       -> put("quicSettings", transportObj)
                "http", "h2" -> put("httpSettings", transportObj)
                "httpupgrade" -> put("httpupgradeSettings", transportObj)
                "splithttp"  -> put("splithttpSettings", transportObj)
            }
        }
    }

    /**
     * Build transport-specific settings from query params.
     */
    private fun parseTransport(params: Map<String, String>): JsonObject = buildJsonObject {
        when (params["type"] ?: "tcp") {
            "ws" -> {
                put("path", params["path"] ?: "/")
                putJsonObject("headers") {
                    val host = params["host"]
                    if (!host.isNullOrEmpty()) put("Host", host)
                }
            }
            "grpc" -> {
                put("serviceName", params["serviceName"] ?: "")
                val mode = params["mode"]
                if (!mode.isNullOrEmpty()) put("mode", mode)
                val authority = params["authority"]
                if (!authority.isNullOrEmpty()) put("authority", authority)
            }
            "tcp" -> {
                val headerType = params["headerType"]
                if (!headerType.isNullOrEmpty() && headerType != "none") {
                    putJsonObject("header") {
                        put("type", headerType)
                        if (headerType == "http") {
                            putJsonObject("request") {
                                put("path", params["path"] ?: "/")
                                putJsonObject("headers") {
                                    val host = params["host"]
                                    if (!host.isNullOrEmpty()) {
                                        putJsonArray("Host") {
                                            host.split(",").forEach { add(it.trim()) }
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
            "kcp", "mkcp" -> {
                val headerType = params["headerType"]
                if (!headerType.isNullOrEmpty()) put("header_type", headerType)
                val seed = params["seed"]
                if (!seed.isNullOrEmpty()) put("seed", seed)
            }
            "quic" -> {
                put("security", params["quicSecurity"] ?: "none")
                val key = params["key"]
                if (!key.isNullOrEmpty()) put("key", key)
                val headerType = params["headerType"]
                if (!headerType.isNullOrEmpty()) put("header_type", headerType)
            }
            "http", "h2" -> {
                put("path", params["path"] ?: "/")
                val host = params["host"]
                if (!host.isNullOrEmpty()) {
                    putJsonArray("host") {
                        host.split(",").forEach { add(it.trim()) }
                    }
                }
            }
            "httpupgrade" -> {
                put("path", params["path"] ?: "/")
                val host = params["host"]
                if (!host.isNullOrEmpty()) put("host", host)
            }
            "splithttp" -> {
                put("path", params["path"] ?: "/")
                val host = params["host"]
                if (!host.isNullOrEmpty()) put("host", host)
            }
        }
    }

    /**
     * Build TLS / Reality settings from query params.  Returns null when security is empty/none.
     */
    private fun parseTls(params: Map<String, String>): JsonObject? {
        val security = params["security"] ?: ""
        if (security.isEmpty() || security == "none") return null

        return buildJsonObject {
            val sni = params["sni"] ?: params["peer"]
            if (!sni.isNullOrEmpty()) put("serverName", sni)

            val fp = params["fp"]
            if (!fp.isNullOrEmpty()) put("fingerprint", fp)

            val alpn = params["alpn"]
            if (!alpn.isNullOrEmpty()) {
                putJsonArray("alpn") {
                    alpn.split(",").forEach { add(it.trim()) }
                }
            }

            val allowInsecure = params["allowInsecure"] ?: params["insecure"]
            if (allowInsecure == "1" || allowInsecure?.lowercase() == "true") {
                put("allowInsecure", true)
            }

            // Reality-specific fields.
            if (security == "reality") {
                val pbk = params["pbk"]
                if (!pbk.isNullOrEmpty()) put("publicKey", pbk)
                val sid = params["sid"]
                if (!sid.isNullOrEmpty()) put("shortId", sid)
                val spx = params["spx"]
                if (!spx.isNullOrEmpty()) put("spiderX", urlDecode(spx))
            }
        }
    }

    // ---------- URI helpers ----------

    /**
     * Result of parsing a standard scheme://userinfo@host:port?query#fragment URI.
     */
    private data class ParsedUri(
        val userInfo: String,
        val host: String,
        val port: Int,
        val params: Map<String, String>,
        val fragment: String
    )

    /**
     * Parse a URI of the form `scheme://userinfo@host:port?params#fragment`.
     *
     * Handles IPv6 hosts in brackets (e.g. `[::1]`).
     */
    private fun parseStandardUri(raw: String, scheme: String): ParsedUri? {
        // Strip scheme.
        val afterScheme = raw.removePrefix("$scheme://").removePrefix("${scheme.uppercase()}://")

        // Fragment.
        val fragmentIdx = afterScheme.lastIndexOf('#')
        val fragment = if (fragmentIdx != -1) urlDecode(afterScheme.substring(fragmentIdx + 1)) else ""
        val noFragment = if (fragmentIdx != -1) afterScheme.substring(0, fragmentIdx) else afterScheme

        // Query.
        val queryIdx = noFragment.indexOf('?')
        val params = if (queryIdx != -1) parseQuery(noFragment.substring(queryIdx + 1)) else emptyMap()
        val noQuery = if (queryIdx != -1) noFragment.substring(0, queryIdx) else noFragment

        // userinfo @ host:port
        val atIdx = noQuery.indexOf('@')
        if (atIdx == -1) return null
        val userInfo = urlDecode(noQuery.substring(0, atIdx))
        val hostPort = noQuery.substring(atIdx + 1)

        val (host, port) = parseHostPort(hostPort) ?: return null
        return ParsedUri(userInfo, host, port, params, fragment)
    }

    /**
     * Parse `host:port` or `[ipv6]:port`.
     */
    internal fun parseHostPort(input: String): Pair<String, Int>? {
        val trimmed = input.trim()
        return if (trimmed.startsWith("[")) {
            // IPv6 in brackets.
            val closeBracket = trimmed.indexOf(']')
            if (closeBracket == -1) return null
            val host = trimmed.substring(1, closeBracket)
            val rest = trimmed.substring(closeBracket + 1)
            val port = if (rest.startsWith(":")) rest.substring(1).toIntOrNull() else null
            if (port == null || port !in 1..65535) return null
            host to port
        } else {
            val lastColon = trimmed.lastIndexOf(':')
            if (lastColon == -1) return null
            val host = trimmed.substring(0, lastColon)
            val port = trimmed.substring(lastColon + 1).toIntOrNull()
            if (port == null || port !in 1..65535) return null
            host to port
        }
    }

    /**
     * Decode a query string (`key=value&key2=value2`) into a map.
     * Values are URL-decoded.
     */
    private fun parseQuery(query: String): Map<String, String> {
        val map = mutableMapOf<String, String>()
        for (pair in query.split('&')) {
            val eq = pair.indexOf('=')
            if (eq == -1) {
                map[urlDecode(pair)] = ""
            } else {
                map[urlDecode(pair.substring(0, eq))] = urlDecode(pair.substring(eq + 1))
            }
        }
        return map
    }

    /**
     * Try decoding a base64 string (standard or URL-safe, with or without padding).
     * Returns null on failure.
     */
    internal fun tryBase64Decode(s: String): String? {
        // Normalise: replace URL-safe chars, strip whitespace.
        val cleaned = s.trim()
            .replace('-', '+')
            .replace('_', '/')

        // Add padding if needed.
        val padded = when (cleaned.length % 4) {
            2 -> "$cleaned=="
            3 -> "$cleaned="
            else -> cleaned
        }

        return try {
            String(Base64.decode(padded, Base64.NO_WRAP), Charsets.UTF_8)
        } catch (_: Exception) {
            try {
                String(Base64.decode(cleaned, Base64.NO_WRAP or Base64.URL_SAFE), Charsets.UTF_8)
            } catch (_: Exception) {
                null
            }
        }
    }

    /**
     * Convenience base64 decode returning raw bytes.
     */
    internal fun tryBase64DecodeBytes(s: String): ByteArray? {
        val cleaned = s.trim()
            .replace('-', '+')
            .replace('_', '/')
        val padded = when (cleaned.length % 4) {
            2 -> "$cleaned=="
            3 -> "$cleaned="
            else -> cleaned
        }
        return try {
            Base64.decode(padded, Base64.NO_WRAP)
        } catch (_: Exception) {
            try {
                Base64.decode(cleaned, Base64.NO_WRAP or Base64.URL_SAFE)
            } catch (_: Exception) {
                null
            }
        }
    }

    private fun urlDecode(s: String): String = try {
        URLDecoder.decode(s, "UTF-8")
    } catch (_: Exception) {
        s
    }
}

