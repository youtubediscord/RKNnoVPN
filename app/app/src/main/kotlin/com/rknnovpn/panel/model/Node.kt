package com.rknnovpn.panel.model

import kotlinx.serialization.SerialName
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
    val link: String = "",
    /** Protocol-specific outbound config in daemon's canonical schema. */
    val outbound: JsonObject,
    val group: String = "Default",
    /** Android package that is expected to own a loopback proxy listener. */
    val ownerPackage: String = "",
    /** TCP connect latency in milliseconds, null if never tested. */
    val latencyMs: Int? = null,
    /** URL response delay through the outbound, null if never tested or core is stopped. */
    val responseMs: Int? = null,
    /** Optional measured response throughput in bytes/sec when the probe URL has a body. */
    val throughputBps: Long? = null,
    val testStatus: String? = null,
    val createdAt: Long = System.currentTimeMillis(),
    /** True when a subscription refresh no longer contains this node. */
    val stale: Boolean = false,
    /** Ownership metadata used to keep manual and subscription nodes separate. */
    val source: NodeSource = NodeSource()
) {
    /** Human-readable label: "name (server:port)" */
    val displayLabel: String
        get() = "$name ($server:$port)"
}

@Serializable
data class NodeSource(
    val type: NodeSourceType = NodeSourceType.MANUAL,
    val url: String = "",
    val providerKey: String = "",
    val lastSeenAt: Long = 0L
)

@Serializable
enum class NodeSourceType {
    MANUAL,
    SUBSCRIPTION
}

@Serializable
enum class Protocol {
    @SerialName("vless")
    VLESS,
    @SerialName("vmess")
    VMESS,
    @SerialName("trojan")
    TROJAN,
    @SerialName("shadowsocks")
    SHADOWSOCKS,
    @SerialName("socks")
    SOCKS,
    @SerialName("hysteria2")
    HYSTERIA2,
    @SerialName("tuic")
    TUIC,
    @SerialName("wireguard")
    WIREGUARD;

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
            "socks", "socks4", "socks4a", "socks5" -> SOCKS
            "hysteria2", "hy2" -> HYSTERIA2
            "tuic" -> TUIC
            "wg", "wireguard" -> WIREGUARD
            else -> null
        }
    }
}
