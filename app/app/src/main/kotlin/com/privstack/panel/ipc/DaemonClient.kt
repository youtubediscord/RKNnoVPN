package com.privstack.panel.ipc

import com.privstack.panel.`import`.UriExporter
import com.privstack.panel.model.AppInfo
import com.privstack.panel.model.AuditReport
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.Node
import com.privstack.panel.model.ProfileConfig
import com.privstack.panel.model.InboundsConfig
import com.privstack.panel.model.Protocol
import com.privstack.panel.model.TunConfig
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import java.nio.charset.StandardCharsets
import java.util.UUID
import javax.inject.Inject
import javax.inject.Singleton

private val bridgeJson = Json {
    ignoreUnknownKeys = true
    isLenient = true
    coerceInputValues = true
}

/**
 * Typed, high-level client for daemon IPC methods.
 *
 * The daemon persists a runtime-oriented config (`node`, `transport`, etc.),
 * while the APK works with a richer UI profile (`nodes[]`, `activeNodeId`).
 * This client bridges those two shapes so the rest of the app can stay on the
 * UI-facing model without losing information.
 */
@Singleton
class DaemonClient @Inject constructor(
    private val executor: PrivctlExecutor
) {
    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }

    // ---- Connection lifecycle ----

    /** Get daemon runtime status (connection state, active node, health). */
    suspend fun status(): DaemonClientResult<DaemonStatus> =
        call("status") { json.decodeFromJsonElement(DaemonStatus.serializer(), it) }

    /** Start the proxy connection using the active profile. */
    suspend fun start(): DaemonClientResult<Unit> =
        callVoid("start")

    /** Stop the proxy connection (keeps daemon alive). */
    suspend fun stop(): DaemonClientResult<Unit> =
        callVoid("stop")

    /** Reload the active configuration without full restart. */
    suspend fun reload(): DaemonClientResult<Unit> =
        callVoid("reload")

    /** Run a full health check and return the result in the dashboard shape. */
    suspend fun health(): DaemonClientResult<DaemonStatus> =
        call("health") { json.decodeFromJsonElement(DaemonStatus.serializer(), it) }

    /** Run a privacy/security audit. */
    suspend fun audit(): DaemonClientResult<AuditReport> =
        call("audit") { json.decodeFromJsonElement(AuditReport.serializer(), it) }

    // ---- Configuration ----

    /** Get the full APK-facing profile config assembled from daemon sections. */
    suspend fun configGet(): DaemonClientResult<ProfileConfig> {
        val nodeResult = fetchSection("node")
        val transportResult = fetchSection("transport")
        val routingResult = fetchSection("routing")
        val appsResult = fetchSection("apps")
        val dnsResult = fetchSection("dns")

        val node = nodeResult.dataOrReturnFailure() ?: return nodeResult.asFailure()
        val transport = transportResult.dataOrReturnFailure() ?: return transportResult.asFailure()
        val routing = routingResult.dataOrReturnFailure() ?: return routingResult.asFailure()
        val apps = appsResult.dataOrReturnFailure() ?: return appsResult.asFailure()
        val dns = dnsResult.dataOrReturnFailure() ?: return dnsResult.asFailure()

        val panelResult = fetchSection("panel")
        val panel = when (panelResult) {
            is DaemonClientResult.Ok -> json.decodeFromJsonElement(DaemonPanelSection.serializer(), panelResult.data)
            is DaemonClientResult.DaemonError ->
                if (panelResult.message.contains("unknown config key: panel", ignoreCase = true)) {
                    DaemonPanelSection()
                } else {
                    return panelResult
                }
            else -> return panelResult.asFailure()
        }

        val nodeSection = json.decodeFromJsonElement(DaemonNodeSection.serializer(), node)
        val transportSection = json.decodeFromJsonElement(DaemonTransportSection.serializer(), transport)
        val routingSection = json.decodeFromJsonElement(DaemonRoutingSection.serializer(), routing)
        val appsSection = json.decodeFromJsonElement(DaemonAppsSection.serializer(), apps)
        val dnsSection = json.decodeFromJsonElement(DaemonDnsSection.serializer(), dns)

        val storedNodes = panel.nodes
        val effectiveNodes = if (storedNodes.isNotEmpty()) {
            storedNodes
        } else {
            buildList {
                buildNodeFromSections(nodeSection, transportSection)?.let(::add)
            }
        }

        val activeNodeId = when {
            panel.activeNodeId.isNotBlank() -> panel.activeNodeId
            effectiveNodes.isNotEmpty() -> effectiveNodes.first().id
            else -> null
        }

        val extra = buildJsonObject {
            panel.extra?.let { put("panel", it) }
            put("routing_raw", routing)
            put("dns_raw", dns)
        }

        return DaemonClientResult.Ok(
            ProfileConfig(
                id = panel.id.ifBlank { "default" },
                name = panel.name.ifBlank { "Default" },
                activeNodeId = activeNodeId,
                nodes = effectiveNodes,
                routing = routingSection.toPanelRouting(appsSection),
                dns = dnsSection.toPanelDns(),
                tun = panel.tun ?: TunConfig(),
                inbounds = panel.inbounds ?: InboundsConfig(),
                extra = extra,
            )
        )
    }

    /** Replace the full profile config by fanning out to daemon config sections. */
    suspend fun configSet(config: ProfileConfig): DaemonClientResult<Unit> {
        val activeNode = config.nodes.find { it.id == config.activeNodeId } ?: config.nodes.firstOrNull()
        val daemonNode = activeNode?.toDaemonNodeSection() ?: DaemonNodeSection()
        val daemonTransport = activeNode?.toDaemonTransportSection() ?: DaemonTransportSection()
        val extra = config.extra?.jsonObject
        val daemonRouting = config.routing.toDaemonRoutingSection(extra?.obj("routing_raw"))
        val daemonApps = config.routing.toDaemonAppsSection()
        val daemonDns = config.dns.toDaemonDnsSection(extra?.obj("dns_raw"))
        val daemonPanel = DaemonPanelSection(
            id = config.id,
            name = config.name,
            activeNodeId = activeNode?.id.orEmpty(),
            nodes = config.nodes,
            tun = config.tun,
            inbounds = config.inbounds,
            extra = extra?.obj("panel"),
        )

        val values = buildJsonObject {
            put("panel", json.encodeToJsonElement(DaemonPanelSection.serializer(), daemonPanel))
            put("node", json.encodeToJsonElement(DaemonNodeSection.serializer(), daemonNode))
            put("transport", json.encodeToJsonElement(DaemonTransportSection.serializer(), daemonTransport))
            put("routing", json.encodeToJsonElement(DaemonRoutingSection.serializer(), daemonRouting))
            put("apps", json.encodeToJsonElement(DaemonAppsSection.serializer(), daemonApps))
            put("dns", json.encodeToJsonElement(DaemonDnsSection.serializer(), daemonDns))
        }
        val params = buildJsonObject {
            put("values", values)
            put("reload", true)
        }
        return callVoid("config-set-many", params)
    }

    /** List all stored config sections the daemon currently understands. */
    suspend fun configList(): DaemonClientResult<List<ProfileSummary>> =
        call("config-list") { element ->
            element.jsonObject.entries.map { (key, value) ->
                ProfileSummary(
                    id = key,
                    name = value.jsonPrimitive.content,
                    isActive = false,
                )
            }
        }

    /**
     * Legacy import entrypoint retained for compatibility.
     *
     * The repository now performs imports itself, so callers should prefer that.
     */
    suspend fun configImport(links: String): DaemonClientResult<List<Node>> {
        val params = buildJsonObject { put("links", links) }
        return call("config-import", params) {
            json.decodeFromJsonElement(ListSerializer(Node.serializer()), it)
        }
    }

    /** Fetch a subscription URL via the daemon's network access. */
    suspend fun subscriptionFetch(url: String): DaemonClientResult<SubscriptionFetchInfo> {
        val params = buildJsonObject { put("url", url) }
        return call("subscription-fetch", params) { element ->
            val obj = element.jsonObject
            val headers = obj["headers"]?.jsonObject
                ?.mapValues { it.value.jsonPrimitive.content }
                .orEmpty()
            SubscriptionFetchInfo(
                body = obj["body"]?.jsonPrimitive?.content.orEmpty(),
                headers = headers,
                status = obj["status"]?.jsonPrimitive?.intOrNull ?: 0,
            )
        }
    }

    // ---- Logs ----

    suspend fun logs(
        lines: Int = 100,
        level: String = "info"
    ): DaemonClientResult<List<String>> {
        val params = buildJsonObject {
            put("lines", lines)
            put("level", level)
        }
        return call("logs", params) { element ->
            val arr = when {
                element is JsonObject && element.containsKey("lines") ->
                    element["lines"]!!.jsonArray
                else -> element.jsonArray
            }
            arr.map { it.jsonPrimitive.content }
        }
    }

    // ---- App management ----

    suspend fun resolveUid(uid: Int): DaemonClientResult<AppInfo> {
        val params = buildJsonObject { put("uid", uid) }
        return call("app.resolveUid", params) {
            json.decodeFromJsonElement(AppInfo.serializer(), it)
        }
    }

    suspend fun appList(): DaemonClientResult<List<AppInfo>> =
        call("app.list", timeoutMs = 15_000L) {
            json.decodeFromJsonElement(ListSerializer(AppInfo.serializer()), it)
        }

    // ---- Updates ----

    suspend fun updateCheck(): DaemonClientResult<UpdateCheckInfo> =
        call("update-check", timeoutMs = 30_000L) { element ->
            val obj = element as JsonObject
            UpdateCheckInfo(
                currentVersion = obj["current_version"]?.jsonPrimitive?.content ?: "unknown",
                latestVersion = obj["latest_version"]?.jsonPrimitive?.content ?: "unknown",
                hasUpdate = obj["has_update"]?.jsonPrimitive?.booleanOrNull ?: false,
                changelog = obj["changelog"]?.jsonPrimitive?.content ?: "",
                moduleSize = obj["module_size"]?.jsonPrimitive?.longOrNull ?: 0L,
                apkSize = obj["apk_size"]?.jsonPrimitive?.longOrNull ?: 0L,
            )
        }

    suspend fun updateDownload(): DaemonClientResult<UpdateDownloadInfo> =
        call("update-download", timeoutMs = 600_000L) { element ->
            val obj = element as JsonObject
            UpdateDownloadInfo(
                modulePath = obj["module_path"]?.jsonPrimitive?.content ?: "",
                apkPath = obj["apk_path"]?.jsonPrimitive?.content ?: "",
                checksums = obj["checksums"]?.jsonPrimitive?.booleanOrNull ?: false,
            )
        }

    suspend fun updateInstall(): DaemonClientResult<UpdateInstallInfo> =
        call("update-install", timeoutMs = 120_000L) { element ->
            val obj = element as JsonObject
            UpdateInstallInfo(
                moduleInstalled = obj["module_installed"]?.jsonPrimitive?.booleanOrNull ?: false,
                apkInstalled = obj["apk_installed"]?.jsonPrimitive?.booleanOrNull ?: false,
                apkError = obj["apk_error"]?.jsonPrimitive?.content,
            )
        }

    // ---- Meta ----

    suspend fun version(): DaemonClientResult<VersionInfo> =
        call("version") { element ->
            val obj = element as JsonObject
            VersionInfo(
                daemonVersion = obj["daemon"]?.jsonPrimitive?.content ?: "unknown",
                coreVersion = obj["core"]?.jsonPrimitive?.content ?: "unknown",
                privctlVersion = obj["privctl"]?.jsonPrimitive?.content ?: "unknown"
            )
        }

    // ---- Internal helpers ----

    private suspend fun fetchSection(key: String): DaemonClientResult<JsonElement> {
        val params = buildJsonObject { put("key", key) }
        return call("config-get", params) { element ->
            val obj = element.jsonObject
            obj["value"] ?: JsonNull
        }
    }

    private suspend fun <T> call(
        method: String,
        params: JsonObject = emptyJsonObject(),
        timeoutMs: Long = 5_000L,
        transform: (JsonElement) -> T
    ): DaemonClientResult<T> {
        return when (val raw = executor.execute(method, params, timeoutMs)) {
            is PrivctlResult.Success -> try {
                DaemonClientResult.Ok(transform(raw.data))
            } catch (e: Exception) {
                DaemonClientResult.ParseError(raw.data.toString(), e)
            }
            is PrivctlResult.Error -> DaemonClientResult.DaemonError(raw.code, raw.message)
            is PrivctlResult.RootDenied -> DaemonClientResult.RootDenied(raw.reason)
            is PrivctlResult.Timeout -> DaemonClientResult.Timeout(raw.method)
            is PrivctlResult.DaemonNotFound -> DaemonClientResult.DaemonNotFound(raw.path)
            is PrivctlResult.UnexpectedError -> DaemonClientResult.Failure(raw.throwable)
        }
    }

    private suspend fun callVoid(
        method: String,
        params: JsonObject = emptyJsonObject(),
        timeoutMs: Long = 5_000L
    ): DaemonClientResult<Unit> = call(method, params, timeoutMs) { }

    private fun buildNodeFromSections(
        node: DaemonNodeSection,
        transport: DaemonTransportSection
    ): Node? {
        if (node.address.isBlank() || node.protocol.isBlank()) return null

        val outbound = buildJsonObject {
            put("protocol", node.protocol)
            when (node.protocol) {
                "vless", "vmess" -> {
                    putJsonObject("settings") {
                        putJsonArray("vnext") {
                            add(
                                buildJsonObject {
                                    put("address", node.address)
                                    put("port", node.port)
                                    putJsonArray("users") {
                                        add(
                                            buildJsonObject {
                                                put("id", node.uuid)
                                                if (node.protocol == "vmess") {
                                                    put("alterId", node.alterId)
                                                    put("security", node.security.ifBlank { "auto" })
                                                } else {
                                                    put("encryption", "none")
                                                    if (node.flow.isNotBlank()) {
                                                        put("flow", node.flow)
                                                    }
                                                }
                                            }
                                        )
                                    }
                                }
                            )
                        }
                    }
                }
                "trojan", "shadowsocks" -> {
                    putJsonObject("settings") {
                        putJsonArray("servers") {
                            add(
                                buildJsonObject {
                                    put("address", node.address)
                                    put("port", node.port)
                                    put("password", node.uuid)
                                    if (node.protocol == "shadowsocks") {
                                        put("method", node.ssMethod.ifBlank { "aes-128-gcm" })
                                    }
                                }
                            )
                        }
                    }
                }
            }

            put(
                "streamSettings",
                transport.toStreamSettings(
                    realityPublicKey = node.realityPublicKey,
                    realityShortId = node.realityShortID,
                )
            )
        }

        val derived = Node(
            id = legacyNodeId(node),
            name = buildLegacyNodeName(node),
            protocol = Protocol.fromString(node.protocol) ?: Protocol.VLESS,
            server = node.address,
            port = node.port,
            link = "",
            outbound = outbound,
        )

        return derived.copy(link = UriExporter.toUri(derived))
    }

    private fun buildLegacyNodeName(node: DaemonNodeSection): String {
        return listOf(node.protocol.uppercase(), node.address, node.port.toString())
            .filter { it.isNotBlank() }
            .joinToString(" ")
    }

    private fun legacyNodeId(node: DaemonNodeSection): String {
        val seed = "${node.protocol}|${node.address}|${node.port}|${node.uuid}"
        return UUID.nameUUIDFromBytes(seed.toByteArray(StandardCharsets.UTF_8)).toString()
    }
}

// ---- Result wrapper with typed data ----

sealed class DaemonClientResult<out T> {
    data class Ok<T>(val data: T) : DaemonClientResult<T>()
    data class DaemonError(val code: Int, val message: String) : DaemonClientResult<Nothing>()
    data class RootDenied(val reason: String) : DaemonClientResult<Nothing>()
    data class Timeout(val method: String) : DaemonClientResult<Nothing>()
    data class DaemonNotFound(val path: String) : DaemonClientResult<Nothing>()
    data class ParseError(val raw: String, val cause: Throwable) : DaemonClientResult<Nothing>()
    data class Failure(val throwable: Throwable) : DaemonClientResult<Nothing>()

    val isOk: Boolean get() = this is Ok

    fun dataOrNull(): T? = (this as? Ok)?.data

    fun dataOrThrow(): T = when (this) {
        is Ok -> data
        is DaemonError -> throw PrivctlException("Daemon error $code: $message")
        is RootDenied -> throw PrivctlException("Root denied: $reason")
        is Timeout -> throw PrivctlException("Timeout on method: $method")
        is DaemonNotFound -> throw PrivctlException("Daemon not found at: $path")
        is ParseError -> throw PrivctlException("Parse error on: ${raw.take(100)}", cause)
        is Failure -> throw PrivctlException("Unexpected failure", throwable)
    }
}

// ---- Supporting data classes ----

data class ProfileSummary(
    val id: String,
    val name: String,
    val isActive: Boolean
)

data class VersionInfo(
    val daemonVersion: String,
    val coreVersion: String,
    val privctlVersion: String
)

data class UpdateCheckInfo(
    val currentVersion: String,
    val latestVersion: String,
    val hasUpdate: Boolean,
    val changelog: String,
    val moduleSize: Long = 0L,
    val apkSize: Long = 0L,
)

data class UpdateDownloadInfo(
    val modulePath: String,
    val apkPath: String,
    val checksums: Boolean,
)

data class UpdateInstallInfo(
    val moduleInstalled: Boolean,
    val apkInstalled: Boolean,
    val apkError: String? = null,
)

data class SubscriptionFetchInfo(
    val body: String,
    val headers: Map<String, String>,
    val status: Int,
)

@Serializable
private data class DaemonPanelSection(
    val id: String = "default",
    val name: String = "Default",
    @SerialName("active_node_id")
    val activeNodeId: String = "",
    val nodes: List<Node> = emptyList(),
    val tun: com.privstack.panel.model.TunConfig? = null,
    val inbounds: com.privstack.panel.model.InboundsConfig? = null,
    val extra: JsonObject? = null,
)

@Serializable
private data class DaemonNodeSection(
    val address: String = "",
    val port: Int = 443,
    val protocol: String = "",
    val uuid: String = "",
    val flow: String = "",
    @SerialName("ss_method")
    val ssMethod: String = "",
    @SerialName("alter_id")
    val alterId: Int = 0,
    val security: String = "",
    @SerialName("reality_public_key")
    val realityPublicKey: String = "",
    @SerialName("reality_short_id")
    val realityShortID: String = "",
)

@Serializable
private data class DaemonTransportSection(
    val protocol: String = "tcp",
    @SerialName("tls_server")
    val tlsServer: String = "",
    val fingerprint: String = "",
    val extra: Map<String, String> = emptyMap(),
) {
    fun toStreamSettings(
        realityPublicKey: String = "",
        realityShortId: String = "",
    ): JsonObject = buildJsonObject {
        val network = when (protocol) {
            "reality" -> "tcp"
            else -> protocol.ifBlank { "tcp" }
        }
        put("network", network)

        when {
            protocol == "reality" -> {
                put("security", "reality")
                putJsonObject("realitySettings") {
                    if (tlsServer.isNotBlank()) put("serverName", tlsServer)
                    if (fingerprint.isNotBlank()) put("fingerprint", fingerprint)
                    (extra["public_key"] ?: realityPublicKey)
                        .takeIf { it.isNotBlank() }
                        ?.let { put("publicKey", it) }
                    (extra["short_id"] ?: realityShortId)
                        .takeIf { it.isNotBlank() }
                        ?.let { put("shortId", it) }
                }
            }
            extra["security"] == "tls" -> {
                put("security", "tls")
                putJsonObject("tlsSettings") {
                    if (tlsServer.isNotBlank()) put("serverName", tlsServer)
                    if (fingerprint.isNotBlank()) put("fingerprint", fingerprint)
                }
            }
            tlsServer.isNotBlank() || fingerprint.isNotBlank() -> {
                put("security", "tls")
                putJsonObject("tlsSettings") {
                    if (tlsServer.isNotBlank()) put("serverName", tlsServer)
                    if (fingerprint.isNotBlank()) put("fingerprint", fingerprint)
                }
            }
        }

        when (network) {
            "ws" -> putJsonObject("wsSettings") {
                put("path", extra["path"] ?: "/")
                extra["host"]?.takeIf { it.isNotBlank() }?.let { host ->
                    putJsonObject("headers") { put("Host", host) }
                }
            }
            "grpc" -> putJsonObject("grpcSettings") {
                put("serviceName", extra["service_name"] ?: "")
                extra["mode"]?.takeIf { it.isNotBlank() }?.let { put("mode", it) }
                extra["authority"]?.takeIf { it.isNotBlank() }?.let { put("authority", it) }
            }
            "http", "h2" -> putJsonObject("httpSettings") {
                put("path", extra["path"] ?: "/")
                extra["host"]?.takeIf { it.isNotBlank() }?.let { host ->
                    putJsonArray("host") {
                        host.split(",").map(String::trim).filter(String::isNotBlank).forEach(::add)
                    }
                }
            }
            "tcp" -> {
                val headerType = extra["header_type"]
                if (!headerType.isNullOrBlank() && headerType != "none") {
                    putJsonObject("tcpSettings") {
                        putJsonObject("header") {
                            put("type", headerType)
                            if (headerType == "http") {
                                putJsonObject("request") {
                                    put("path", extra["path"] ?: "/")
                                    extra["host"]?.takeIf { it.isNotBlank() }?.let { host ->
                                        putJsonObject("headers") {
                                            putJsonArray("Host") {
                                                host.split(",").map(String::trim).filter(String::isNotBlank).forEach(::add)
                                            }
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
            "kcp", "mkcp" -> putJsonObject("kcpSettings") {
                extra["header_type"]?.takeIf { it.isNotBlank() }?.let { put("header_type", it) }
                extra["seed"]?.takeIf { it.isNotBlank() }?.let { put("seed", it) }
            }
            "quic" -> putJsonObject("quicSettings") {
                put("security", extra["quic_security"] ?: "none")
                extra["key"]?.takeIf { it.isNotBlank() }?.let { put("key", it) }
                extra["header_type"]?.takeIf { it.isNotBlank() }?.let { put("header_type", it) }
            }
            "httpupgrade" -> putJsonObject("httpupgradeSettings") {
                put("path", extra["path"] ?: "/")
                extra["host"]?.takeIf { it.isNotBlank() }?.let { put("host", it) }
            }
            "splithttp" -> putJsonObject("splithttpSettings") {
                put("path", extra["path"] ?: "/")
                extra["host"]?.takeIf { it.isNotBlank() }?.let { put("host", it) }
            }
        }
    }
}

@Serializable
private data class DaemonRoutingSection(
    val mode: String = "whitelist",
    @SerialName("bypass_lan")
    val bypassLan: Boolean = true,
    @SerialName("bypass_china")
    val bypassChina: Boolean = false,
    @SerialName("bypass_russia")
    val bypassRussia: Boolean = false,
    @SerialName("block_ads")
    val blockAds: Boolean = false,
    @SerialName("custom_direct")
    val customDirect: List<String> = emptyList(),
    @SerialName("custom_proxy")
    val customProxy: List<String> = emptyList(),
    @SerialName("custom_block")
    val customBlock: List<String> = emptyList(),
    @SerialName("geoip_path")
    val geoipPath: String = "",
    @SerialName("geosite_path")
    val geositePath: String = "",
) {
    fun toPanelRouting(apps: DaemonAppsSection): com.privstack.panel.model.RoutingConfig {
        val panelMode = when (mode.lowercase()) {
            "all" -> com.privstack.panel.model.RoutingMode.PROXY_ALL
            "whitelist", "include" -> com.privstack.panel.model.RoutingMode.PER_APP
            "blacklist", "exclude" -> com.privstack.panel.model.RoutingMode.PER_APP_BYPASS
            "rules" -> com.privstack.panel.model.RoutingMode.RULES
            else -> if (customDirect.isNotEmpty() || customProxy.isNotEmpty() || customBlock.isNotEmpty()) {
                com.privstack.panel.model.RoutingMode.RULES
            } else {
                com.privstack.panel.model.RoutingMode.PROXY_ALL
            }
        }

        return com.privstack.panel.model.RoutingConfig(
            mode = panelMode,
            appProxyList = if (apps.mode == "whitelist") apps.packages else emptyList(),
            appBypassList = if (apps.mode == "blacklist") apps.packages else emptyList(),
            directDomains = customDirect.filter { !it.contains("/") },
            proxyDomains = customProxy.filter { !it.contains("/") },
            blockDomains = customBlock.filter { !it.contains("/") },
            directIps = customDirect.filter { it.contains("/") },
            proxyIps = customProxy.filter { it.contains("/") },
            blockIps = customBlock.filter { it.contains("/") },
        )
    }
}

@Serializable
private data class DaemonAppsSection(
    val mode: String = "whitelist",
    @SerialName("list")
    val packages: List<String> = emptyList(),
)

@Serializable
private data class DaemonDnsSection(
    @SerialName("proxy_dns")
    val proxyDns: String = "https://1.1.1.1/dns-query",
    @SerialName("direct_dns")
    val directDns: String = "https://77.88.8.8/dns-query",
    @SerialName("block_quic_dns")
    val blockQuicDns: Boolean = false,
    @SerialName("fake_ip")
    val fakeIp: Boolean = false,
) {
    fun toPanelDns(): com.privstack.panel.model.DnsConfig =
        com.privstack.panel.model.DnsConfig(
            remoteDns = proxyDns,
            directDns = directDns,
            blockQuic = blockQuicDns,
            fakeDns = fakeIp,
        )
}

private fun DaemonClientResult<JsonElement>.dataOrReturnFailure(): JsonElement? =
    (this as? DaemonClientResult.Ok)?.data

private fun JsonObject.obj(key: String): JsonObject? =
    this[key] as? JsonObject

private fun <T> DaemonClientResult<T>.asFailure(): DaemonClientResult<Nothing> = when (this) {
    is DaemonClientResult.DaemonError -> this
    is DaemonClientResult.RootDenied -> this
    is DaemonClientResult.Timeout -> this
    is DaemonClientResult.DaemonNotFound -> this
    is DaemonClientResult.ParseError -> this
    is DaemonClientResult.Failure -> this
    is DaemonClientResult.Ok -> error("Success result cannot be converted to failure")
}

private fun com.privstack.panel.model.RoutingConfig.toDaemonRoutingSection(base: JsonObject?): DaemonRoutingSection {
    val direct = directDomains + directIps
    val proxy = proxyDomains + proxyIps
    val block = blockDomains + blockIps

    val merged = buildJsonObject {
        base?.forEach { (key, value) -> put(key, value) }
        put("mode", when (mode) {
            com.privstack.panel.model.RoutingMode.PROXY_ALL -> "all"
            com.privstack.panel.model.RoutingMode.PER_APP -> "whitelist"
            com.privstack.panel.model.RoutingMode.PER_APP_BYPASS -> "blacklist"
            com.privstack.panel.model.RoutingMode.RULES -> "rules"
        })
        put("custom_direct", bridgeJson.encodeToJsonElement(ListSerializer(String.serializer()), direct))
        put("custom_proxy", bridgeJson.encodeToJsonElement(ListSerializer(String.serializer()), proxy))
        put("custom_block", bridgeJson.encodeToJsonElement(ListSerializer(String.serializer()), block))
    }
    return bridgeJson.decodeFromJsonElement(DaemonRoutingSection.serializer(), merged)
}

private fun com.privstack.panel.model.RoutingConfig.toDaemonAppsSection(): DaemonAppsSection =
    when (mode) {
        com.privstack.panel.model.RoutingMode.PER_APP -> DaemonAppsSection(
            mode = "whitelist",
            packages = appProxyList,
        )
        com.privstack.panel.model.RoutingMode.PER_APP_BYPASS -> DaemonAppsSection(
            mode = "blacklist",
            packages = appBypassList,
        )
        else -> DaemonAppsSection(mode = "all")
    }

private fun com.privstack.panel.model.DnsConfig.toDaemonDnsSection(base: JsonObject?): DaemonDnsSection {
    val merged = buildJsonObject {
        base?.forEach { (key, value) -> put(key, value) }
        put("proxy_dns", remoteDns)
        put("direct_dns", directDns)
        put("block_quic_dns", blockQuic)
        put("fake_ip", fakeDns)
    }
    return bridgeJson.decodeFromJsonElement(DaemonDnsSection.serializer(), merged)
}

private fun Node.toDaemonNodeSection(): DaemonNodeSection {
    val settings = outbound["settings"]?.jsonObject
    val stream = outbound["streamSettings"]?.jsonObject
    val tls = stream?.get("tlsSettings")?.jsonObject
    val reality = stream?.get("realitySettings")?.jsonObject

    return when (protocol) {
        Protocol.VLESS, Protocol.VMESS -> {
            val vnext = settings?.get("vnext")?.jsonArray?.firstOrNull()?.jsonObject
            val user = vnext?.get("users")?.jsonArray?.firstOrNull()?.jsonObject
            DaemonNodeSection(
                address = server,
                port = port,
                protocol = protocol.name.lowercase(),
                uuid = user?.get("id")?.jsonPrimitive?.content.orEmpty(),
                flow = user?.get("flow")?.jsonPrimitive?.content.orEmpty(),
                alterId = user?.get("alterId")?.jsonPrimitive?.intOrNull ?: 0,
                security = user?.get("security")?.jsonPrimitive?.content.orEmpty(),
                realityPublicKey = reality?.get("publicKey")?.jsonPrimitive?.content.orEmpty(),
                realityShortID = reality?.get("shortId")?.jsonPrimitive?.content.orEmpty(),
            )
        }
        Protocol.TROJAN -> {
            val serverEntry = settings?.get("servers")?.jsonArray?.firstOrNull()?.jsonObject
            DaemonNodeSection(
                address = server,
                port = port,
                protocol = "trojan",
                uuid = serverEntry?.get("password")?.jsonPrimitive?.content.orEmpty(),
                realityPublicKey = reality?.get("publicKey")?.jsonPrimitive?.content.orEmpty(),
                realityShortID = reality?.get("shortId")?.jsonPrimitive?.content.orEmpty(),
            )
        }
        Protocol.SHADOWSOCKS -> {
            val serverEntry = settings?.get("servers")?.jsonArray?.firstOrNull()?.jsonObject
            DaemonNodeSection(
                address = server,
                port = port,
                protocol = "shadowsocks",
                uuid = serverEntry?.get("password")?.jsonPrimitive?.content.orEmpty(),
                ssMethod = serverEntry?.get("method")?.jsonPrimitive?.content.orEmpty(),
            )
        }
        else -> DaemonNodeSection(
            address = server,
            port = port,
            protocol = protocol.name.lowercase(),
        )
    }
}

private fun Node.toDaemonTransportSection(): DaemonTransportSection {
    val stream = outbound["streamSettings"]?.jsonObject
    val network = stream?.get("network")?.jsonPrimitive?.content.orEmpty().ifBlank { "tcp" }
    val tls = stream?.get("tlsSettings")?.jsonObject
    val reality = stream?.get("realitySettings")?.jsonObject
    val security = stream?.get("security")?.jsonPrimitive?.content.orEmpty()

    val protocol = when {
        security == "reality" || reality != null -> "reality"
        else -> network
    }

    val extra = mutableMapOf<String, String>()
    when (network) {
        "ws" -> {
            val ws = stream?.get("wsSettings")?.jsonObject
            extra["path"] = ws?.get("path")?.jsonPrimitive?.content.orEmpty()
            val headers = ws?.get("headers")?.jsonObject
            extra["host"] = headers?.get("Host")?.jsonPrimitive?.content.orEmpty()
        }
        "grpc" -> {
            val grpc = stream?.get("grpcSettings")?.jsonObject
            extra["service_name"] = grpc?.get("serviceName")?.jsonPrimitive?.content.orEmpty()
            extra["mode"] = grpc?.get("mode")?.jsonPrimitive?.content.orEmpty()
            extra["authority"] = grpc?.get("authority")?.jsonPrimitive?.content.orEmpty()
        }
        "http", "h2" -> {
            val http = stream?.get("httpSettings")?.jsonObject
            extra["path"] = http?.get("path")?.jsonPrimitive?.content.orEmpty()
            val hosts = http?.get("host")?.jsonArray?.mapNotNull { it.jsonPrimitive.contentOrNull }
            if (!hosts.isNullOrEmpty()) {
                extra["host"] = hosts.joinToString(",")
            }
        }
        "tcp" -> {
            val tcp = stream?.get("tcpSettings")?.jsonObject
            val header = tcp?.get("header")?.jsonObject
            extra["header_type"] = header?.get("type")?.jsonPrimitive?.content.orEmpty()
            if (extra["header_type"] == "http") {
                val request = header?.get("request")?.jsonObject
                extra["path"] = request?.get("path")?.jsonPrimitive?.content.orEmpty()
                val hosts = request?.get("headers")?.jsonObject?.get("Host")?.jsonArray
                    ?.mapNotNull { it.jsonPrimitive.contentOrNull }
                if (!hosts.isNullOrEmpty()) {
                    extra["host"] = hosts.joinToString(",")
                }
            }
        }
        "kcp", "mkcp" -> {
            val kcp = stream?.get("kcpSettings")?.jsonObject
            extra["header_type"] = kcp?.get("header_type")?.jsonPrimitive?.content.orEmpty()
            extra["seed"] = kcp?.get("seed")?.jsonPrimitive?.content.orEmpty()
        }
        "quic" -> {
            val quic = stream?.get("quicSettings")?.jsonObject
            extra["quic_security"] = quic?.get("security")?.jsonPrimitive?.content.orEmpty()
            extra["key"] = quic?.get("key")?.jsonPrimitive?.content.orEmpty()
            extra["header_type"] = quic?.get("header_type")?.jsonPrimitive?.content.orEmpty()
        }
        "httpupgrade" -> {
            val upgrade = stream?.get("httpupgradeSettings")?.jsonObject
            extra["path"] = upgrade?.get("path")?.jsonPrimitive?.content.orEmpty()
            extra["host"] = upgrade?.get("host")?.jsonPrimitive?.content.orEmpty()
        }
        "splithttp" -> {
            val split = stream?.get("splithttpSettings")?.jsonObject
            extra["path"] = split?.get("path")?.jsonPrimitive?.content.orEmpty()
            extra["host"] = split?.get("host")?.jsonPrimitive?.content.orEmpty()
        }
    }

    if (protocol == "reality") {
        extra["public_key"] = reality?.get("publicKey")?.jsonPrimitive?.content.orEmpty()
        extra["short_id"] = reality?.get("shortId")?.jsonPrimitive?.content.orEmpty()
    } else if (security == "tls") {
        extra["security"] = "tls"
    }

    return DaemonTransportSection(
        protocol = protocol,
        tlsServer = reality?.get("serverName")?.jsonPrimitive?.content
            ?: tls?.get("serverName")?.jsonPrimitive?.content.orEmpty(),
        fingerprint = reality?.get("fingerprint")?.jsonPrimitive?.content
            ?: tls?.get("fingerprint")?.jsonPrimitive?.content.orEmpty(),
        extra = extra.filterValues { it.isNotBlank() },
    )
}
