package com.privstack.panel.`import`

import android.util.Base64
import com.privstack.panel.model.Node
import com.privstack.panel.model.Protocol
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import java.net.URLEncoder

/**
 * Export a [Node] back to its shareable URI format.
 *
 * Produces standard proxy links understood by v2rayN, v2rayNG, NekoBox, etc.
 */
object UriExporter {

    /**
     * Export a node to its canonical share link.
     * Falls back to the stored [Node.link] if the protocol has no exporter yet.
     */
    fun toUri(node: Node): String = when (node.protocol) {
        Protocol.VLESS        -> toVlessUri(node)
        Protocol.VMESS        -> toVmessUri(node)
        Protocol.TROJAN       -> toTrojanUri(node)
        Protocol.SHADOWSOCKS  -> toShadowsocksUri(node)
        else                  -> node.link
    }

    /**
     * vless://uuid@host:port?params#name
     */
    fun toVlessUri(node: Node): String {
        val settings = node.outbound.obj("settings") ?: return node.link
        val vnext = settings.arr("vnext")?.firstOrNull()?.jsonObject ?: return node.link
        val user = vnext.arr("users")?.firstOrNull()?.jsonObject ?: return node.link

        val uuid = user.str("id")
        val host = formatHost(vnext.str("address"))
        val port = vnext.str("port")

        val params = mutableMapOf<String, String>()

        // Encryption.
        val encryption = user.str("encryption")
        params["encryption"] = encryption.ifEmpty { "none" }

        // Flow.
        val flow = user.str("flow")
        if (flow.isNotEmpty()) params["flow"] = flow

        // Stream settings.
        appendStreamParams(node.outbound, params)

        val query = encodeQuery(params)
        val name = urlEncode(node.name)

        return "vless://$uuid@$host:$port?$query#$name"
    }

    /**
     * vmess://base64(json)
     *
     * Uses the v2rayN sharing standard (version 2).
     */
    fun toVmessUri(node: Node): String {
        val settings = node.outbound.obj("settings") ?: return node.link
        val vnext = settings.arr("vnext")?.firstOrNull()?.jsonObject ?: return node.link
        val user = vnext.arr("users")?.firstOrNull()?.jsonObject ?: return node.link
        val stream = node.outbound.obj("streamSettings")

        val vmessObj = buildJsonObject {
            put("v", "2")
            put("ps", node.name)
            put("add", vnext.str("address"))
            put("port", vnext.str("port"))
            put("id", user.str("id"))
            put("aid", user.str("alterId").ifEmpty { "0" })
            put("scy", user.str("security").ifEmpty { "auto" })

            // Network / transport.
            val network = stream?.str("network") ?: "tcp"
            put("net", network)

            // TLS.
            val security = stream?.str("security") ?: ""
            put("tls", if (security == "tls") "tls" else "")

            // Transport-specific fields.
            val transport = getTransportSettings(stream, network)
            when (network) {
                "ws" -> {
                    put("host", transport?.str("headers", "Host") ?: "")
                    put("path", transport?.str("path") ?: "")
                }
                "grpc" -> {
                    put("path", transport?.str("serviceName") ?: "")
                    put("type", "gun")
                }
                "tcp" -> {
                    val header = transport?.obj("header")
                    put("type", header?.str("type") ?: "none")
                    if (header?.str("type") == "http") {
                        val req = header.obj("request")
                        put("path", req?.str("path") ?: "")
                        val hosts = req?.obj("headers")?.arr("Host")
                        put("host", hosts?.joinToString(",") { it.jsonPrimitive.content } ?: "")
                    }
                }
                "http", "h2" -> {
                    val hosts = transport?.arr("host")
                    put("host", hosts?.joinToString(",") { it.jsonPrimitive.content } ?: "")
                    put("path", transport?.str("path") ?: "")
                }
                "kcp", "mkcp" -> {
                    val headerType = transport?.str("header_type") ?: "none"
                    put("type", headerType)
                    val seed = transport?.str("seed") ?: ""
                    if (seed.isNotEmpty()) put("path", seed)
                }
                "quic" -> {
                    val headerType = transport?.str("header_type") ?: "none"
                    put("type", headerType)
                    val quicSecurity = transport?.str("security") ?: "none"
                    put("host", quicSecurity)
                    val key = transport?.str("key") ?: ""
                    put("path", key)
                }
                "httpupgrade" -> {
                    put("host", transport?.str("host") ?: "")
                    put("path", transport?.str("path") ?: "")
                }
                "splithttp" -> {
                    put("host", transport?.str("host") ?: "")
                    put("path", transport?.str("path") ?: "")
                }
                else -> {
                    put("type", "none")
                }
            }

            // TLS fields.
            val tlsSettings = getTlsSettings(stream, security)
            put("sni", tlsSettings?.str("serverName") ?: "")
            put("fp", tlsSettings?.str("fingerprint") ?: "")
            val alpn = tlsSettings?.arr("alpn")
            put("alpn", alpn?.joinToString(",") { it.jsonPrimitive.content } ?: "")
        }

        val jsonStr = Json.encodeToString(JsonObject.serializer(), vmessObj)
        val encoded = Base64.encodeToString(jsonStr.toByteArray(Charsets.UTF_8), Base64.NO_WRAP)
        return "vmess://$encoded"
    }

    /**
     * trojan://password@host:port?params#name
     */
    fun toTrojanUri(node: Node): String {
        val settings = node.outbound.obj("settings") ?: return node.link
        val server = settings.arr("servers")?.firstOrNull()?.jsonObject ?: return node.link

        val password = urlEncode(server.str("password"))
        val host = formatHost(server.str("address"))
        val port = server.str("port")

        val params = mutableMapOf<String, String>()
        appendStreamParams(node.outbound, params)

        // Trojan defaults to TLS; only emit security if it's not "tls".
        if (params["security"].isNullOrEmpty()) {
            params["security"] = "tls"
        }

        val query = encodeQuery(params)
        val name = urlEncode(node.name)

        return "trojan://$password@$host:$port?$query#$name"
    }

    /**
     * ss://base64(method:password)@host:port#name  (SIP002 format)
     */
    fun toShadowsocksUri(node: Node): String {
        val settings = node.outbound.obj("settings") ?: return node.link
        val server = settings.arr("servers")?.firstOrNull()?.jsonObject ?: return node.link

        val method = server.str("method")
        val password = server.str("password")
        val host = formatHost(server.str("address"))
        val port = server.str("port")

        val userInfo = Base64.encodeToString(
            "$method:$password".toByteArray(Charsets.UTF_8),
            Base64.NO_WRAP or Base64.NO_PADDING or Base64.URL_SAFE
        )

        val name = urlEncode(node.name)
        return "ss://$userInfo@$host:$port#$name"
    }

    // ---------- stream-settings helpers ----------

    /**
     * Read stream settings from the outbound and append standard query params.
     */
    private fun appendStreamParams(outbound: JsonObject, params: MutableMap<String, String>) {
        val stream = outbound.obj("streamSettings") ?: return

        val network = stream.str("network")
        if (network.isNotEmpty() && network != "tcp") {
            params["type"] = network
        } else if (network == "tcp") {
            params["type"] = "tcp"
        }

        // Security.
        val security = stream.str("security")
        if (security.isNotEmpty() && security != "none") {
            params["security"] = security
        }

        // TLS / Reality fields.
        val tlsSettings = getTlsSettings(stream, security)
        if (tlsSettings != null) {
            val sni = tlsSettings.str("serverName")
            if (sni.isNotEmpty()) params["sni"] = sni

            val fp = tlsSettings.str("fingerprint")
            if (fp.isNotEmpty()) params["fp"] = fp

            val alpn = tlsSettings.arr("alpn")
            if (alpn != null && alpn.isNotEmpty()) {
                params["alpn"] = alpn.joinToString(",") { it.jsonPrimitive.content }
            }

            val allowInsecure = tlsSettings.str("allowInsecure")
            if (allowInsecure == "true") params["allowInsecure"] = "1"

            // Reality-specific.
            if (security == "reality") {
                val pbk = tlsSettings.str("publicKey")
                if (pbk.isNotEmpty()) params["pbk"] = pbk
                val sid = tlsSettings.str("shortId")
                if (sid.isNotEmpty()) params["sid"] = sid
                val spx = tlsSettings.str("spiderX")
                if (spx.isNotEmpty()) params["spx"] = urlEncode(spx)
            }
        }

        // Transport-specific fields.
        val transport = getTransportSettings(stream, network)
        if (transport != null) {
            when (network) {
                "ws" -> {
                    val path = transport.str("path")
                    if (path.isNotEmpty()) params["path"] = path
                    val host = transport.str("headers", "Host")
                    if (host.isNotEmpty()) params["host"] = host
                }
                "grpc" -> {
                    val svcName = transport.str("serviceName")
                    if (svcName.isNotEmpty()) params["serviceName"] = svcName
                    val mode = transport.str("mode")
                    if (mode.isNotEmpty()) params["mode"] = mode
                    val authority = transport.str("authority")
                    if (authority.isNotEmpty()) params["authority"] = authority
                }
                "tcp" -> {
                    val header = transport.obj("header")
                    val headerType = header?.str("type")
                    if (!headerType.isNullOrEmpty() && headerType != "none") {
                        params["headerType"] = headerType
                        if (headerType == "http") {
                            val req = header.obj("request")
                            val path = req?.str("path")
                            if (!path.isNullOrEmpty()) params["path"] = path
                            val hosts = req?.obj("headers")?.arr("Host")
                            if (hosts != null && hosts.isNotEmpty()) {
                                params["host"] = hosts.joinToString(",") { it.jsonPrimitive.content }
                            }
                        }
                    }
                }
                "kcp", "mkcp" -> {
                    val headerType = transport.str("header_type")
                    if (headerType.isNotEmpty()) params["headerType"] = headerType
                    val seed = transport.str("seed")
                    if (seed.isNotEmpty()) params["seed"] = seed
                }
                "quic" -> {
                    val sec = transport.str("security")
                    if (sec.isNotEmpty()) params["quicSecurity"] = sec
                    val key = transport.str("key")
                    if (key.isNotEmpty()) params["key"] = key
                    val headerType = transport.str("header_type")
                    if (headerType.isNotEmpty()) params["headerType"] = headerType
                }
                "http", "h2" -> {
                    val path = transport.str("path")
                    if (path.isNotEmpty()) params["path"] = path
                    val hosts = transport.arr("host")
                    if (hosts != null && hosts.isNotEmpty()) {
                        params["host"] = hosts.joinToString(",") { it.jsonPrimitive.content }
                    }
                }
                "httpupgrade" -> {
                    val path = transport.str("path")
                    if (path.isNotEmpty()) params["path"] = path
                    val host = transport.str("host")
                    if (host.isNotEmpty()) params["host"] = host
                }
                "splithttp" -> {
                    val path = transport.str("path")
                    if (path.isNotEmpty()) params["path"] = path
                    val host = transport.str("host")
                    if (host.isNotEmpty()) params["host"] = host
                }
            }
        }
    }

    /**
     * Look up the TLS settings object within streamSettings.
     */
    private fun getTlsSettings(stream: JsonObject?, security: String): JsonObject? {
        if (stream == null) return null
        return when (security) {
            "tls"     -> stream.obj("tlsSettings")
            "reality" -> stream.obj("realitySettings")
            "xtls"    -> stream.obj("xtlsSettings")
            else      -> null
        }
    }

    /**
     * Look up the transport settings object within streamSettings for the given network.
     */
    private fun getTransportSettings(stream: JsonObject?, network: String): JsonObject? {
        if (stream == null) return null
        return when (network) {
            "ws"          -> stream.obj("wsSettings")
            "grpc"        -> stream.obj("grpcSettings")
            "tcp"         -> stream.obj("tcpSettings")
            "kcp", "mkcp" -> stream.obj("kcpSettings")
            "quic"        -> stream.obj("quicSettings")
            "http", "h2"  -> stream.obj("httpSettings")
            "httpupgrade"  -> stream.obj("httpupgradeSettings")
            "splithttp"   -> stream.obj("splithttpSettings")
            else          -> null
        }
    }

    // ---------- formatting helpers ----------

    /**
     * Wrap IPv6 addresses in brackets for URI representation.
     */
    private fun formatHost(address: String): String {
        return if (address.contains(':') && !address.startsWith('[')) "[$address]" else address
    }

    private fun urlEncode(s: String): String = URLEncoder.encode(s, "UTF-8")
        .replace("+", "%20")

    private fun encodeQuery(params: Map<String, String>): String {
        return params.entries.joinToString("&") { (k, v) ->
            "${urlEncode(k)}=${urlEncode(v)}"
        }
    }

    // ---------- JsonObject navigation extensions ----------

    /** Get a nested JsonObject by key, or null. */
    private fun JsonObject.obj(key: String): JsonObject? =
        this[key]?.let { if (it is JsonObject) it else null }

    /** Get a nested JsonArray by key, or null. */
    private fun JsonObject.arr(key: String): JsonArray? =
        this[key]?.let { if (it is JsonArray) it else null }

    /** Get a string primitive by key, or empty string. */
    private fun JsonObject.str(key: String): String =
        this[key]?.jsonPrimitive?.content.orEmpty()

    /** Get a nested string: obj[key1][key2] as string. */
    private fun JsonObject.str(key1: String, key2: String): String =
        obj(key1)?.str(key2).orEmpty()
}
