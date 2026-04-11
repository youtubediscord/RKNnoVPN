package com.privstack.panel.model

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject

/**
 * Proxy node representing a single upstream server configuration.
 *
 * [outbound] carries protocol-specific fields (uuid, password, tls settings, etc.)
 * in the daemon's canonical xray-outbound schema so we never lose data we don't
 * model explicitly.
 */
@Serializable
data class Node(
    val id: String,
    val name: String,
    val protocol: Protocol,
    val server: String,
    val port: Int,
    /** Original share-link URI (vless://..., trojan://..., etc.) */
    val link: String,
    /** Protocol-specific outbound config in daemon's canonical schema. */
    val outbound: JsonObject,
    val group: String = "Default",
    /** Round-trip latency in milliseconds, null if never tested. */
    val latencyMs: Int? = null,
    val createdAt: Long = System.currentTimeMillis()
) {
    /** Human-readable label: "name (server:port)" */
    val displayLabel: String
        get() = "$name ($server:$port)"
}

@Serializable
enum class Protocol {
    VLESS,
    VMESS,
    TROJAN,
    SHADOWSOCKS,
    HYSTERIA2,
    TUIC;

    companion object {
        /**
         * Parse a protocol from a share-link scheme or config string.
         * Returns null for unrecognised values.
         */
        fun fromString(value: String): Protocol? = when (value.lowercase()) {
            "vless" -> VLESS
            "vmess" -> VMESS
            "trojan" -> TROJAN
            "ss", "shadowsocks" -> SHADOWSOCKS
            "hysteria2", "hy2" -> HYSTERIA2
            "tuic" -> TUIC
            else -> null
        }
    }
}
