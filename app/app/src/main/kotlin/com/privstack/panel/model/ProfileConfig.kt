package com.privstack.panel.model

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject

/**
 * Full profile configuration matching the daemon's canonical config.json schema.
 *
 * A profile is the complete set of settings the daemon needs to establish a
 * connection: which node to use, how to route traffic, DNS behaviour, etc.
 */
@Serializable
data class ProfileConfig(
    val id: String,
    val name: String,
    /** ID of the active node within this profile. */
    val activeNodeId: String? = null,
    val nodes: List<Node> = emptyList(),
    val routing: RoutingConfig = RoutingConfig(),
    val dns: DnsConfig = DnsConfig(),
    val health: HealthConfig = HealthConfig(),
    val tun: TunConfig = TunConfig(),
    val inbounds: InboundsConfig = InboundsConfig(),
    /** Arbitrary extension fields the daemon may add in future versions. */
    val extra: JsonObject? = null
)

@Serializable
data class RoutingConfig(
    val mode: RoutingMode = RoutingMode.PROXY_ALL,
    /** Package names routed through the proxy (only for PER_APP mode). */
    val appProxyList: List<String> = emptyList(),
    /** Package names bypassing the proxy (only for PER_APP_BYPASS mode). */
    val appBypassList: List<String> = emptyList(),
    /** Domain rules for direct access (e.g. "domain:ru", "geosite:ru"). */
    val directDomains: List<String> = emptyList(),
    /** Domain rules forced through the proxy. */
    val proxyDomains: List<String> = emptyList(),
    /** Domain rules that should be blocked. */
    val blockDomains: List<String> = emptyList(),
    /** IP CIDR rules for direct access. */
    val directIps: List<String> = emptyList(),
    /** IP CIDR rules forced through the proxy. */
    val proxyIps: List<String> = emptyList(),
    /** IP CIDR rules that should be blocked. */
    val blockIps: List<String> = emptyList()
)

@Serializable
enum class RoutingMode {
    /** All traffic goes through the proxy. */
    PROXY_ALL,
    /** Only listed apps go through the proxy. */
    PER_APP,
    /** All apps except listed ones go through the proxy. */
    PER_APP_BYPASS,
    /** Use domain/IP rule sets for split routing. */
    RULES
}

@Serializable
data class DnsConfig(
    /** Remote DNS server used for proxied queries. */
    val remoteDns: String = "https://1.1.1.1/dns-query",
    /** Direct DNS server for domains resolved without proxy. */
    val directDns: String = "https://77.88.8.8/dns-query",
    /** Whether to block QUIC to force HTTP/2 fallback. */
    val blockQuic: Boolean = false,
    /** Fake-DNS / DNS-hijack enabled. */
    val fakeDns: Boolean = false
)

@Serializable
data class HealthConfig(
    val enabled: Boolean = true,
    val intervalSec: Int = 30,
    val threshold: Int = 3,
    val checkUrl: String = "https://www.gstatic.com/generate_204",
    val timeoutSec: Int = 5
)

@Serializable
data class TunConfig(
    val enabled: Boolean = true,
    /** TUN interface MTU. */
    val mtu: Int = 9000,
    /** IPv4 address assigned to the TUN interface. */
    val ipv4Address: String = "172.19.0.1/30",
    /** Whether to enable IPv6 on the TUN interface. */
    val ipv6: Boolean = false,
    /** Auto-route: let the daemon manage system routing table. */
    val autoRoute: Boolean = true,
    /** Strict-route: block leaks outside the TUN. */
    val strictRoute: Boolean = true
)

@Serializable
data class InboundsConfig(
    val socksPort: Int = 10808,
    val httpPort: Int = 10809,
    /** Whether to expose inbound ports on LAN (0.0.0.0) vs localhost only. */
    val allowLan: Boolean = false
)
