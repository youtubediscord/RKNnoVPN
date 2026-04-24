package com.privstack.panel.ipc

import com.privstack.panel.`import`.UriExporter
import com.privstack.panel.BuildConfig
import com.privstack.panel.model.AppInfo
import com.privstack.panel.model.AuditReport
import com.privstack.panel.model.BackendHealthSnapshot
import com.privstack.panel.model.BackendStatusV2
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.DesiredStateV2
import com.privstack.panel.model.HealthConfig
import com.privstack.panel.model.Node
import com.privstack.panel.model.NodeProbeResultV2
import com.privstack.panel.model.ProfileConfig
import com.privstack.panel.model.ResetReport
import com.privstack.panel.model.InboundsConfig
import com.privstack.panel.model.Protocol
import com.privstack.panel.model.TunConfig
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.builtins.serializer
import kotlinx.serialization.json.add
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonObjectBuilder
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

private const val METHOD_NOT_FOUND_CODE = -32601
private const val COMPATIBILITY_ERROR_CODE = -32090

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
    companion object {
        const val MIN_CONTROL_PROTOCOL_VERSION = 3
        const val MIN_SCHEMA_VERSION = 4
        val REQUIRED_METHODS: Set<String> = setOf(
            "backend.status",
            "backend.start",
            "backend.stop",
            "backend.restart",
            "backend.reset",
            "backend.applyDesiredState",
            "diagnostics.health",
            "diagnostics.testNodes",
            "config-set-many",
            "panel-get",
            "panel-set",
            "config-import",
            "network-reset",
            "version",
        )
    }

    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }
    private val prettyJson = Json {
        prettyPrint = true
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }

    // ---- Connection lifecycle ----

    /** Get daemon runtime status (connection state, active node, health). */
    suspend fun status(): DaemonClientResult<DaemonStatus> {
        return when (val result = backendStatus()) {
            is DaemonClientResult.Ok -> {
                val v2Status = result.data.toDaemonStatus()
                when (val legacy = legacyStatus()) {
                    is DaemonClientResult.Ok -> DaemonClientResult.Ok(legacy.data.withV2Status(v2Status))
                    else -> DaemonClientResult.Ok(v2Status)
                }
            }
            is DaemonClientResult.DaemonError ->
                if (isMethodNotFound(result)) {
                    legacyStatus()
                } else {
                    result
                }
            else -> result.asFailure()
        }
    }

    /** Start the proxy connection using the active profile. */
    suspend fun start(): DaemonClientResult<Unit> {
        requireCompatible("backend.start", "backend.status")?.let { return it }
        return when (val result = backendStart()) {
            is DaemonClientResult.Ok -> DaemonClientResult.Ok(Unit)
            is DaemonClientResult.DaemonError -> result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Stop the proxy connection (keeps daemon alive). */
    suspend fun stop(): DaemonClientResult<Unit> {
        requireCompatible("backend.stop", "backend.status")?.let { return it }
        return when (val result = backendStop()) {
            is DaemonClientResult.Ok -> DaemonClientResult.Ok(Unit)
            is DaemonClientResult.DaemonError -> result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Reload the active configuration without full restart. */
    suspend fun reload(): DaemonClientResult<Unit> {
        requireCompatible("backend.restart", "backend.status")?.let { return it }
        return when (val result = backendRestart()) {
            is DaemonClientResult.Ok -> DaemonClientResult.Ok(Unit)
            is DaemonClientResult.DaemonError -> result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Force-remove PrivStack network rules and stop the proxy core. */
    suspend fun networkReset(): DaemonClientResult<ResetReport> {
        return when (val result = backendReset()) {
            is DaemonClientResult.Ok -> result
            is DaemonClientResult.DaemonError ->
                if (isMethodNotFound(result)) legacyNetworkReset() else result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Run a full health check and return the result in the dashboard shape. */
    suspend fun health(): DaemonClientResult<DaemonStatus> {
        return when (val statusResult = backendStatus()) {
            is DaemonClientResult.Ok -> when (val healthResult = diagnosticsHealth()) {
                is DaemonClientResult.Ok -> {
                    val v2Status = statusResult.data.toDaemonStatus(healthOverride = healthResult.data)
                    when (val legacy = legacyStatus()) {
                        is DaemonClientResult.Ok -> DaemonClientResult.Ok(legacy.data.withV2Status(v2Status))
                        else -> DaemonClientResult.Ok(v2Status)
                    }
                }
                is DaemonClientResult.DaemonError ->
                    if (isMethodNotFound(healthResult)) legacyHealth() else healthResult.asFailure()
                else -> healthResult.asFailure()
            }
            is DaemonClientResult.DaemonError ->
                if (isMethodNotFound(statusResult)) legacyHealth() else statusResult.asFailure()
            else -> statusResult.asFailure()
        }
    }

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
        val healthResult = fetchSection("health")
        val runtimeResult = fetchSection("runtime_v2")

        val node = nodeResult.dataOrReturnFailure() ?: return nodeResult.asFailure()
        val transport = transportResult.dataOrReturnFailure() ?: return transportResult.asFailure()
        val routing = routingResult.dataOrReturnFailure() ?: return routingResult.asFailure()
        val apps = appsResult.dataOrReturnFailure() ?: return appsResult.asFailure()
        val dns = dnsResult.dataOrReturnFailure() ?: return dnsResult.asFailure()
        val health = when (healthResult) {
            is DaemonClientResult.Ok -> healthResult.data
            is DaemonClientResult.DaemonError ->
                if (healthResult.message.contains("unknown config key: health", ignoreCase = true)) {
                    json.encodeToJsonElement(DaemonHealthSection.serializer(), DaemonHealthSection())
                } else {
                    return healthResult
                }
            else -> return healthResult.asFailure()
        }
        val runtime = when (runtimeResult) {
            is DaemonClientResult.Ok -> runtimeResult.data
            is DaemonClientResult.DaemonError ->
                if (runtimeResult.message.contains("unknown config key: runtime_v2", ignoreCase = true)) {
                    json.encodeToJsonElement(DaemonRuntimeV2Section.serializer(), DaemonRuntimeV2Section())
                } else {
                    return runtimeResult
                }
            else -> return runtimeResult.asFailure()
        }

        val panelResult = when (val result = panelGet()) {
            is DaemonClientResult.DaemonError ->
                if (isMethodNotFound(result)) legacyPanelGet() else result
            else -> result
        }
        val panel = when (panelResult) {
            is DaemonClientResult.Ok -> panelResult.data
            else -> return panelResult.asFailure()
        }

        val nodeSection = json.decodeFromJsonElement(DaemonNodeSection.serializer(), node)
        val transportSection = json.decodeFromJsonElement(DaemonTransportSection.serializer(), transport)
        val routingSection = json.decodeFromJsonElement(DaemonRoutingSection.serializer(), routing)
        val appsSection = json.decodeFromJsonElement(DaemonAppsSection.serializer(), apps)
        val dnsSection = json.decodeFromJsonElement(DaemonDnsSection.serializer(), dns)
        val healthSection = json.decodeFromJsonElement(DaemonHealthSection.serializer(), health)
        val runtimeSection = json.decodeFromJsonElement(DaemonRuntimeV2Section.serializer(), runtime)

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
            put("health_raw", health)
        }

        return DaemonClientResult.Ok(
            ProfileConfig(
                id = panel.id.ifBlank { "default" },
                name = panel.name.ifBlank { "Default" },
                activeNodeId = activeNodeId,
                nodes = effectiveNodes,
                runtime = runtimeSection.toPanelRuntime(),
                routing = routingSection.toPanelRouting(appsSection),
                dns = dnsSection.toPanelDns(),
                health = healthSection.toPanelHealth(),
                tun = panel.tun ?: TunConfig(),
                inbounds = panel.inbounds ?: InboundsConfig(),
                extra = extra,
            )
        )
    }

    /** Replace the full profile config by fanning out to daemon config sections. */
    suspend fun configSet(
        config: ProfileConfig,
        reload: Boolean = true,
    ): DaemonClientResult<Unit> {
        requireCompatible("config-set-many")?.let { return it }
        val activeNode = config.nodes.find { it.id == config.activeNodeId } ?: config.nodes.firstOrNull()
        val daemonNode = activeNode?.toDaemonNodeSection() ?: DaemonNodeSection()
        val daemonTransport = activeNode?.toDaemonTransportSection() ?: DaemonTransportSection()
        val extra = config.extra?.jsonObject
        val daemonRouting = config.routing.toDaemonRoutingSection(extra?.obj("routing_raw"))
        val daemonApps = config.routing.toDaemonAppsSection()
        val daemonDns = config.dns.toDaemonDnsSection(extra?.obj("dns_raw"))
        val daemonHealth = config.health.toDaemonHealthSection(extra?.obj("health_raw"))
        val daemonRuntime = config.runtime.toDaemonRuntimeSection()
        val values = buildConfigSetValues(
            config = config,
            daemonNode = daemonNode,
            daemonTransport = daemonTransport,
            daemonRouting = daemonRouting,
            daemonApps = daemonApps,
            daemonDns = daemonDns,
            daemonHealth = daemonHealth,
            daemonRuntime = daemonRuntime,
            includePanel = false,
        )
        val params = buildJsonObject {
            put("values", values)
            put("reload", reload)
        }
        return callVoid("config-set-many", params, timeoutMs = 60_000L)
    }

    private suspend fun panelGet(): DaemonClientResult<DaemonPanelSection> =
        call("panel-get") {
            json.decodeFromJsonElement(DaemonPanelSection.serializer(), it)
        }

    suspend fun panelSet(
        config: ProfileConfig,
        reload: Boolean = true,
    ): DaemonClientResult<Unit> {
        requireCompatible("panel-set")?.let { return it }
        return when (val result = callVoid(
            "panel-set",
            buildJsonObject {
                put(
                    "panel",
                    json.encodeToJsonElement(
                        DaemonPanelSection.serializer(),
                        config.toDaemonPanelSection(),
                    )
                )
                put("reload", reload)
            },
            timeoutMs = 60_000L,
        )) {
            is DaemonClientResult.DaemonError -> result
            else -> result
        }
    }

    private fun buildConfigSetValues(
        config: ProfileConfig,
        daemonNode: DaemonNodeSection,
        daemonTransport: DaemonTransportSection,
        daemonRouting: DaemonRoutingSection,
        daemonApps: DaemonAppsSection,
        daemonDns: DaemonDnsSection,
        daemonHealth: DaemonHealthSection,
        daemonRuntime: DaemonRuntimeV2Section,
        includePanel: Boolean,
    ): JsonObject = buildJsonObject {
        if (includePanel) {
            put(
                "panel",
                json.encodeToJsonElement(
                    DaemonPanelSection.serializer(),
                    config.toDaemonPanelSection(),
                )
            )
        }
        put("node", json.encodeToJsonElement(DaemonNodeSection.serializer(), daemonNode))
        put("transport", json.encodeToJsonElement(DaemonTransportSection.serializer(), daemonTransport))
        put("routing", json.encodeToJsonElement(DaemonRoutingSection.serializer(), daemonRouting))
        put("apps", json.encodeToJsonElement(DaemonAppsSection.serializer(), daemonApps))
        put("dns", json.encodeToJsonElement(DaemonDnsSection.serializer(), daemonDns))
        put("health", json.encodeToJsonElement(DaemonHealthSection.serializer(), daemonHealth))
        put("runtime_v2", json.encodeToJsonElement(DaemonRuntimeV2Section.serializer(), daemonRuntime))
    }

    private suspend fun legacyPanelGet(): DaemonClientResult<DaemonPanelSection> {
        val panelResult = fetchSection("panel")
        return when (panelResult) {
            is DaemonClientResult.Ok ->
                runCatching {
                    json.decodeFromJsonElement(DaemonPanelSection.serializer(), panelResult.data)
                }.fold(
                    onSuccess = { DaemonClientResult.Ok(it) },
                    onFailure = { DaemonClientResult.ParseError(panelResult.data.toString(), it) }
                )
            is DaemonClientResult.DaemonError ->
                if (panelResult.message.contains("unknown config key: panel", ignoreCase = true)) {
                    DaemonClientResult.Ok(DaemonPanelSection())
                } else {
                    panelResult
                }
            else -> panelResult.asFailure()
        }
    }

    private suspend fun legacyPanelSet(
        config: ProfileConfig,
        reload: Boolean,
    ): DaemonClientResult<Unit> {
        val activeNode = config.nodes.find { it.id == config.activeNodeId } ?: config.nodes.firstOrNull()
        val extra = config.extra?.jsonObject
        val params = buildJsonObject {
            put(
                "values",
                buildConfigSetValues(
                    config = config,
                    daemonNode = activeNode?.toDaemonNodeSection() ?: DaemonNodeSection(),
                    daemonTransport = activeNode?.toDaemonTransportSection() ?: DaemonTransportSection(),
                    daemonRouting = config.routing.toDaemonRoutingSection(extra?.obj("routing_raw")),
                    daemonApps = config.routing.toDaemonAppsSection(),
                    daemonDns = config.dns.toDaemonDnsSection(extra?.obj("dns_raw")),
                    daemonHealth = config.health.toDaemonHealthSection(extra?.obj("health_raw")),
                    daemonRuntime = config.runtime.toDaemonRuntimeSection(),
                    includePanel = true,
                )
            )
            put("reload", reload)
        }
        return callVoid("config-set-many", params, timeoutMs = 60_000L)
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
        requireCompatible("config-import")?.let { return it.asFailure() }
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

    suspend fun nodeTest(
        nodeIds: List<String> = emptyList(),
        url: String = "",
        timeoutMs: Int = 5_000,
    ): DaemonClientResult<NodeTestInfo> {
        requireCompatible("diagnostics.testNodes")?.let { return it.asFailure() }
        when (val result = diagnosticsTestNodes(nodeIds, url, timeoutMs)) {
            is DaemonClientResult.Ok -> {
                return DaemonClientResult.Ok(
                    NodeTestInfo(
                        url = url,
                        results = result.data.map { probe ->
                            NodeTestResult(
                                id = probe.id,
                                name = probe.name,
                                tag = probe.id,
                                server = probe.server,
                                port = probe.port,
                                protocol = probe.protocol,
                                tcpMs = probe.tcpDirect?.toInt(),
                                urlMs = probe.tunnelDelay?.toInt(),
                                tcpStatus = probe.tcpStatus.ifBlank {
                                    if (probe.tcpDirect != null) "ok" else "fail"
                                },
                                urlStatus = probe.urlStatus.ifBlank {
                                    if (probe.tunnelDelay != null) "ok" else "not_run"
                                },
                                verdict = probe.verdict.ifBlank {
                                    if (probe.tunnelDelay != null) "usable" else "unknown"
                                },
                                status = null,
                                tcpError = if (probe.errorClass == "tcp_direct_failed") probe.errorClass else null,
                                urlError = when (probe.errorClass) {
                                    "tunnel_delay_failed",
                                    "tunnel_unavailable",
                                    "dns_bootstrap_failed",
                                    "runtime_not_ready",
                                    "runtime_degraded",
                                    "proxy_dns_unavailable",
                                    "outbound_url_failed",
                                    "http_helper_unavailable",
                                    "api_disabled",
                                    "api_unavailable",
                                    "outbound_missing",
                                    "tls_handshake_failed" -> probe.errorClass
                                    else -> null
                                },
                            )
                        },
                    )
                )
            }
            is DaemonClientResult.DaemonError -> return result.asFailure()
            is DaemonClientResult.RootDenied -> return result.asFailure()
            is DaemonClientResult.Timeout -> return result.asFailure()
            is DaemonClientResult.DaemonNotFound -> return result.asFailure()
            is DaemonClientResult.ParseError -> return result.asFailure()
            is DaemonClientResult.Failure -> return result.asFailure()
        }
    }

    suspend fun backendStatus(): DaemonClientResult<BackendStatusV2> =
        call("backend.status") { json.decodeFromJsonElement(BackendStatusV2.serializer(), it) }

    suspend fun backendStart(): DaemonClientResult<BackendStatusV2> =
        call("backend.start", timeoutMs = 30_000L) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
        }

    suspend fun backendStop(): DaemonClientResult<BackendStatusV2> =
        call("backend.stop", timeoutMs = 30_000L) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
        }

    suspend fun backendRestart(): DaemonClientResult<BackendStatusV2> =
        call("backend.restart", timeoutMs = 60_000L) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
        }

    suspend fun backendReset(): DaemonClientResult<ResetReport> =
        call("backend.reset", timeoutMs = 60_000L) {
            json.decodeFromJsonElement(ResetReport.serializer(), it)
        }

    suspend fun legacyNetworkReset(): DaemonClientResult<ResetReport> =
        call("network-reset", timeoutMs = 60_000L) {
            json.decodeFromJsonElement(ResetReport.serializer(), it)
        }

    suspend fun backendApplyDesiredState(desiredState: DesiredStateV2): DaemonClientResult<BackendStatusV2> =
        call(
            method = "backend.applyDesiredState",
            params = json.encodeToJsonElement(DesiredStateV2.serializer(), desiredState).jsonObject,
            timeoutMs = 15_000L,
        ) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
        }

    suspend fun diagnosticsHealth(): DaemonClientResult<BackendHealthSnapshot> =
        call("diagnostics.health", timeoutMs = 30_000L) {
            json.decodeFromJsonElement(BackendHealthSnapshot.serializer(), it)
        }

    suspend fun diagnosticsTestNodes(
        nodeIds: List<String> = emptyList(),
        url: String = "",
        timeoutMs: Int = 5_000,
    ): DaemonClientResult<List<NodeProbeResultV2>> {
        val params = buildJsonObject {
            putJsonArray("node_ids") {
                nodeIds.forEach { add(it) }
            }
            if (url.isNotBlank()) {
                put("url", url)
            }
            put("timeout_ms", timeoutMs)
        }
        return call("diagnostics.testNodes", params, timeoutMs = timeoutMs.toLong() + 5_000L) {
            val obj = it.jsonObject
            obj["results"]?.jsonArray?.map { item ->
                json.decodeFromJsonElement(NodeProbeResultV2.serializer(), item)
            }.orEmpty()
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

    suspend fun runtimeLogs(lines: Int = 160): DaemonClientResult<RuntimeLogsInfo> {
        val params = buildJsonObject {
            put("lines", lines)
            putJsonArray("files") {
                add("privd")
                add("sing-box")
            }
        }
        return call("logs", params, timeoutMs = 15_000L) { element ->
            val obj = element.jsonObject
            val files = obj["logs"]?.jsonArray?.map { item ->
                val log = item.jsonObject
                RuntimeLogFile(
                    name = log["name"]?.jsonPrimitive?.content.orEmpty(),
                    path = log["path"]?.jsonPrimitive?.content.orEmpty(),
                    lines = log["lines"]?.jsonArray?.map { line -> line.jsonPrimitive.content }.orEmpty(),
                    missing = log["missing"]?.jsonPrimitive?.booleanOrNull ?: false,
                    error = log["error"]?.jsonPrimitive?.contentOrNull.orEmpty(),
                )
            }.orEmpty()
            RuntimeLogsInfo(files = files)
        }
    }

    suspend fun diagnosticBundle(lines: Int = 220): DaemonClientResult<String> {
        val params = buildJsonObject {
            put("lines", lines)
        }
        val doctorResult = call("doctor", params, timeoutMs = 30_000L) { element ->
            prettyJson.encodeToString(JsonElement.serializer(), element)
        }
        return when (doctorResult) {
            is DaemonClientResult.Ok -> doctorResult
            is DaemonClientResult.DaemonError ->
                if (isMethodNotFound(doctorResult)) {
                    when (val fallback = runtimeLogs(lines)) {
                        is DaemonClientResult.Ok -> DaemonClientResult.Ok(fallback.data.text)
                        else -> fallback.asFailure()
                    }
                } else {
                    doctorResult.asFailure()
                }
            else -> doctorResult.asFailure()
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

    suspend fun updateInstall(
        modulePath: String = "",
        apkPath: String = "",
    ): DaemonClientResult<UpdateInstallInfo> {
        val params = buildJsonObject {
            if (modulePath.isNotBlank()) {
                put("module_path", modulePath)
            }
            if (apkPath.isNotBlank()) {
                put("apk_path", apkPath)
            }
        }
        return call("update-install", params, timeoutMs = 120_000L) { element ->
            val obj = element as JsonObject
            UpdateInstallInfo(
                moduleInstalled = obj["module_installed"]?.jsonPrimitive?.booleanOrNull ?: false,
                apkInstalled = obj["apk_installed"]?.jsonPrimitive?.booleanOrNull ?: false,
                apkError = obj["apk_error"]?.jsonPrimitive?.content,
            )
        }
    }

    // ---- Meta ----

    suspend fun version(): DaemonClientResult<VersionInfo> =
        call("version") { element ->
            val obj = element as JsonObject
            VersionInfo(
                daemonVersion = obj["daemon"]?.jsonPrimitive?.content ?: "unknown",
                coreVersion = obj["core"]?.jsonPrimitive?.content ?: "unknown",
                privctlVersion = obj["privctl"]?.jsonPrimitive?.content ?: "unknown",
                moduleVersion = obj["module"]?.jsonObject?.get("version")?.jsonPrimitive?.content ?: "unknown",
                moduleVersionCode = obj["module"]?.jsonObject?.get("versionCode")?.jsonPrimitive?.content ?: "",
                modulePath = obj["module"]?.jsonObject?.get("path")?.jsonPrimitive?.content ?: "",
                singBoxAvailable = obj["sing_box"]?.jsonObject?.get("error")?.jsonPrimitive?.contentOrNull.isNullOrBlank(),
                singBoxError = obj["sing_box"]?.jsonObject?.get("error")?.jsonPrimitive?.contentOrNull ?: "",
                controlProtocolVersion = obj["control_protocol_version"]?.jsonPrimitive?.intOrNull
                    ?: obj["control_protocol"]?.jsonPrimitive?.intOrNull
                    ?: 0,
                schemaVersion = obj["schema_version"]?.jsonPrimitive?.intOrNull ?: 0,
                panelMinVersion = obj["panel_min_version"]?.jsonPrimitive?.contentOrNull ?: "",
                capabilities = obj["capabilities"]?.jsonArray?.mapNotNull {
                    it.jsonPrimitive.contentOrNull
                }.orEmpty(),
                supportedMethods = obj["supported_methods"]?.jsonArray?.mapNotNull {
                    it.jsonPrimitive.contentOrNull
                }.orEmpty(),
            )
        }

    // ---- Internal helpers ----

    private suspend fun legacyStatus(): DaemonClientResult<DaemonStatus> =
        call("status") { json.decodeFromJsonElement(DaemonStatus.serializer(), it) }

    private suspend fun legacyHealth(): DaemonClientResult<DaemonStatus> =
        call("health") { json.decodeFromJsonElement(DaemonStatus.serializer(), it) }

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
            is PrivctlResult.Error -> DaemonClientResult.DaemonError(raw.code, raw.message, raw.details)
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

    private fun isMethodNotFound(result: DaemonClientResult.DaemonError): Boolean =
        result.code == METHOD_NOT_FOUND_CODE ||
            result.message.contains("unknown command", ignoreCase = true) ||
            result.message.contains("method not found", ignoreCase = true)

    private suspend fun requireCompatible(vararg requiredMethods: String): DaemonClientResult<Unit>? {
        return when (val result = version()) {
            is DaemonClientResult.Ok -> {
                val info = result.data
                val required = requiredMethods.toSet()
                val enforceReleaseMatch = required.none { it in REPAIR_METHODS }
                val requiresSingBox = required.any { it in SING_BOX_METHODS }
                val requiresSchemaV4 = enforceReleaseMatch && required.any { it in SCHEMA_V4_METHODS }
                when {
                    enforceReleaseMatch && info.releaseMismatch(BuildConfig.VERSION_NAME) != null ->
                        DaemonClientResult.DaemonError(
                            COMPATIBILITY_ERROR_CODE,
                            info.releaseMismatch(BuildConfig.VERSION_NAME)!!,
                        )
                    info.controlProtocolVersion < MIN_CONTROL_PROTOCOL_VERSION ->
                        DaemonClientResult.DaemonError(
                            COMPATIBILITY_ERROR_CODE,
                            "APK и модуль несовместимы: APK ${BuildConfig.VERSION_NAME}, daemon ${info.daemonVersion}, module ${info.moduleVersion}; control protocol ${info.controlProtocolVersion}, нужен $MIN_CONTROL_PROTOCOL_VERSION",
                        )
                    requiresSchemaV4 && info.schemaVersion < MIN_SCHEMA_VERSION ->
                        DaemonClientResult.DaemonError(
                            COMPATIBILITY_ERROR_CODE,
                            "APK и модуль несовместимы: daemon config schema ${info.schemaVersion}, нужна $MIN_SCHEMA_VERSION",
                        )
                    enforceReleaseMatch && info.missingCapabilities(requiredMethods.toList()).isNotEmpty() ->
                        DaemonClientResult.DaemonError(
                            COMPATIBILITY_ERROR_CODE,
                            "APK и модуль несовместимы: daemon ${info.daemonVersion}, module ${info.moduleVersion}; нет capabilities ${info.missingCapabilities(requiredMethods.toList()).joinToString(", ")}",
                        )
                    requiresSingBox && !info.singBoxAvailable ->
                        DaemonClientResult.DaemonError(
                            COMPATIBILITY_ERROR_CODE,
                            "APK и модуль несовместимы: sing-box недоступен (${info.singBoxError.ifBlank { "unknown" }})",
                        )
                    else -> {
                        val missing = info.missingRequiredMethods(requiredMethods.toList() + "version")
                        if (missing.isEmpty()) {
                            null
                        } else {
                            DaemonClientResult.DaemonError(
                                COMPATIBILITY_ERROR_CODE,
                                "APK и модуль несовместимы: APK ${BuildConfig.VERSION_NAME}, daemon ${info.daemonVersion}, module ${info.moduleVersion}; нет методов ${missing.joinToString(", ")}",
                            )
                        }
                    }
                }
            }
            is DaemonClientResult.DaemonError ->
                DaemonClientResult.DaemonError(
                    COMPATIBILITY_ERROR_CODE,
                    "APK и модуль несовместимы: daemon не сообщает capabilities (${result.message})",
                    result.details,
                )
            is DaemonClientResult.RootDenied -> result
            is DaemonClientResult.Timeout -> result
            is DaemonClientResult.DaemonNotFound -> result
            is DaemonClientResult.ParseError -> DaemonClientResult.DaemonError(
                COMPATIBILITY_ERROR_CODE,
                "APK и модуль несовместимы: некорректный ответ version",
            )
            is DaemonClientResult.Failure -> result
        }
    }

    private fun buildNodeFromSections(
        node: DaemonNodeSection,
        transport: DaemonTransportSection
    ): Node? {
        if (node.address.isBlank() || node.protocol.isBlank()) return null

        val outbound = buildJsonObject {
            put("protocol", node.protocol)
            when (node.protocol) {
                "vless", "vmess" -> putJsonObject("settings") {
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

                "trojan", "shadowsocks" -> putJsonObject("settings") {
                    putJsonArray("servers") {
                        add(
                            buildJsonObject {
                                put("address", node.address)
                                put("port", node.port)
                                put("password", node.uuid.ifBlank { node.password })
                                if (node.protocol == "shadowsocks") {
                                    put("method", node.ssMethod.ifBlank { "aes-128-gcm" })
                                    if (node.ssPlugin.isNotBlank()) put("plugin", node.ssPlugin)
                                    if (node.ssPluginOpts.isNotBlank()) put("plugin_opts", node.ssPluginOpts)
                                }
                            }
                        )
                    }
                }

                "socks" -> putJsonObject("settings") {
                    put("address", node.address)
                    put("port", node.port)
                    put("version", node.socksVersion.ifBlank { "5" })
                    if (node.username.isNotBlank()) put("username", node.username)
                    if (node.password.isNotBlank()) put("password", node.password)
                    if (node.network.isNotBlank()) put("network", node.network)
                }

                "hysteria2" -> putJsonObject("settings") {
                    put("address", node.address)
                    put("port", node.port)
                    put("password", node.password.ifBlank { node.uuid })
	                    if (node.serverPorts.isNotEmpty()) {
	                        putJsonArray("server_ports") {
	                            node.serverPorts.forEach { add(it) }
	                        }
	                    }
                    if (node.obfsType.isNotBlank() || node.obfsPassword.isNotBlank()) {
                        putJsonObject("obfs") {
                            put("type", node.obfsType.ifBlank { "salamander" })
                            put("password", node.obfsPassword)
                        }
                    }
                }

                "tuic" -> putJsonObject("settings") {
                    put("address", node.address)
                    put("port", node.port)
                    put("uuid", node.uuid)
                    put("password", node.password)
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
        val seed = "${node.protocol}|${node.address}|${node.port}|${node.uuid}|${node.username}|${node.password}"
        return UUID.nameUUIDFromBytes(seed.toByteArray(StandardCharsets.UTF_8)).toString()
    }
}

// ---- Result wrapper with typed data ----

sealed class DaemonClientResult<out T> {
    data class Ok<T>(val data: T) : DaemonClientResult<T>()
    data class DaemonError(
        val code: Int,
        val message: String,
        val details: JsonElement? = null,
    ) : DaemonClientResult<Nothing>()
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
    val privctlVersion: String,
    val moduleVersion: String = "unknown",
    val moduleVersionCode: String = "",
    val modulePath: String = "",
    val singBoxAvailable: Boolean = true,
    val singBoxError: String = "",
    val controlProtocolVersion: Int = 0,
    val schemaVersion: Int = 0,
    val panelMinVersion: String = "",
    val capabilities: List<String> = emptyList(),
    val supportedMethods: List<String> = emptyList(),
) {
    fun missingRequiredMethods(required: Collection<String>): List<String> {
        if (supportedMethods.isEmpty()) return required.toList()
        return required.filterNot { it in supportedMethods }
    }

    fun missingCapabilities(requiredMethods: Collection<String>): List<String> {
        if (capabilities.isEmpty()) return listOf("capabilities")
        return requiredMethods
            .mapNotNull(::capabilityForMethod)
            .distinct()
            .filterNot { it in capabilities }
    }

    fun releaseMismatch(apkVersion: String): String? {
        val apk = stableReleaseVersion(apkVersion) ?: return null
        val daemon = stableReleaseVersion(daemonVersion)
        if (daemon != null && daemon != apk) {
            return "APK и daemon несовместимы: APK $apkVersion, daemon $daemonVersion. Установите matching release."
        }
        val module = stableReleaseVersion(moduleVersion)
        if (module != null && module != apk) {
            return "APK и модуль несовместимы: APK $apkVersion, module $moduleVersion. Установите matching release."
        }
        return null
    }
}

private fun stableReleaseVersion(raw: String): String? {
    val normalized = raw.trim().removePrefix("v")
    if (!Regex("""\d+\.\d+\.\d+""").matches(normalized)) return null
    return normalized
}

private val REPAIR_METHODS = setOf("backend.stop", "backend.reset", "network-reset", "network.reset")
private val SING_BOX_METHODS = setOf("backend.start", "backend.restart")
private val SCHEMA_V4_METHODS = setOf(
    "backend.applyDesiredState",
    "backend.start",
    "backend.restart",
    "config-import",
    "config-set-many",
    "panel-set",
)

private fun capabilityForMethod(method: String): String? = when (method) {
    "backend.status", "backend.start", "backend.stop", "backend.restart",
    "backend.applyDesiredState" -> "backend.root-tproxy"
    "backend.reset", "network-reset", "network.reset" -> "backend.reset.structured"
    "config-import", "config.import" -> "config.import.v2"
    "config-set-many", "panel-get", "panel-set" -> "panel.nodes"
    "diagnostics.health" -> "diagnostics.health.v2"
    "diagnostics.testNodes" -> "diagnostics.testNodes.v2"
    "self-check", "self.check" -> "privacy.self-check.v1"
    "logs", "doctor" -> "runtime.logs"
    else -> null
}

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

data class NodeTestInfo(
    val url: String,
    val results: List<NodeTestResult>,
)

data class RuntimeLogFile(
    val name: String,
    val path: String,
    val lines: List<String>,
    val missing: Boolean,
    val error: String,
)

data class RuntimeLogsInfo(
    val files: List<RuntimeLogFile>,
) {
    val text: String
        get() = files.joinToString("\n\n") { file ->
            buildString {
                append("== ")
                append(file.path.ifBlank { file.name })
                append(" ==")
                when {
                    file.missing -> append("\n(missing)")
                    file.error.isNotBlank() -> append("\n(error: ").append(file.error).append(")")
                    file.lines.isEmpty() -> append("\n(empty)")
                    else -> append('\n').append(file.lines.joinToString("\n"))
                }
            }
        }
}

data class NodeTestResult(
    val id: String,
    val name: String,
    val tag: String,
    val server: String,
    val port: Int,
    val protocol: String,
    val tcpMs: Int?,
    val urlMs: Int?,
    val tcpStatus: String = "",
    val urlStatus: String = "",
    val verdict: String = "",
    val status: Int?,
    val tcpError: String?,
    val urlError: String?,
)

private fun BackendStatusV2.toDaemonStatus(
    healthOverride: BackendHealthSnapshot? = null,
): DaemonStatus {
    val effectiveHealth = healthOverride ?: health
    val connectionState = when (appliedState.phase) {
        com.privstack.panel.model.BackendPhase.HEALTHY,
        com.privstack.panel.model.BackendPhase.RULES_APPLIED,
        com.privstack.panel.model.BackendPhase.DNS_APPLIED,
        com.privstack.panel.model.BackendPhase.OUTBOUND_CHECKED,
        com.privstack.panel.model.BackendPhase.DEGRADED ->
            if (effectiveHealth.healthy) {
                com.privstack.panel.model.ConnectionState.CONNECTED
            } else {
                com.privstack.panel.model.ConnectionState.ERROR
            }
        com.privstack.panel.model.BackendPhase.STOPPED -> com.privstack.panel.model.ConnectionState.DISCONNECTED
        com.privstack.panel.model.BackendPhase.APPLYING,
        com.privstack.panel.model.BackendPhase.STARTING,
        com.privstack.panel.model.BackendPhase.CONFIG_CHECKED,
        com.privstack.panel.model.BackendPhase.STOPPING,
        com.privstack.panel.model.BackendPhase.RESETTING -> com.privstack.panel.model.ConnectionState.CONNECTING
        com.privstack.panel.model.BackendPhase.CORE_SPAWNED,
        com.privstack.panel.model.BackendPhase.CORE_LISTENING,
        com.privstack.panel.model.BackendPhase.FAILED -> com.privstack.panel.model.ConnectionState.ERROR
    }

    return DaemonStatus(
        state = connectionState,
        activeNodeId = desiredState.activeProfileId,
        uptime = 0L,
        health = com.privstack.panel.model.HealthResult(
            healthy = effectiveHealth.healthy,
            coreRunning = effectiveHealth.coreReady,
            tunActive = false,
            dnsOperational = effectiveHealth.dnsReady,
            routingReady = effectiveHealth.routingReady,
            egressReady = effectiveHealth.egressReady,
            operationalHealthy = effectiveHealth.operationalHealthy,
            backendKind = appliedState.backendKind,
            phase = appliedState.phase,
            lastCode = effectiveHealth.lastCode.ifBlank { null },
            lastError = effectiveHealth.lastError.ifBlank { null },
            checkedAt = effectiveHealth.checkedAt?.let(::epochSeconds) ?: 0L,
        ),
    )
}

private fun parseBackendKind(raw: String): com.privstack.panel.model.BackendKind =
    runCatching { com.privstack.panel.model.BackendKind.valueOf(raw) }
        .getOrDefault(com.privstack.panel.model.BackendKind.ROOT_TPROXY)

private fun parseFallbackPolicy(raw: String): com.privstack.panel.model.FallbackPolicy =
    runCatching { com.privstack.panel.model.FallbackPolicy.valueOf(raw) }
        .getOrDefault(com.privstack.panel.model.FallbackPolicy.OFFER_RESET)

private fun epochSeconds(raw: String): Long =
    runCatching { java.time.Instant.parse(raw).epochSecond }.getOrDefault(0L)

private fun DaemonStatus.withV2Status(v2: DaemonStatus): DaemonStatus =
    copy(
        state = v2.state,
        activeNodeId = v2.activeNodeId ?: activeNodeId,
        health = v2.health,
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
    val username: String = "",
    val password: String = "",
    val flow: String = "",
    @SerialName("ss_method")
    val ssMethod: String = "",
    @SerialName("ss_plugin")
    val ssPlugin: String = "",
    @SerialName("ss_plugin_opts")
    val ssPluginOpts: String = "",
    @SerialName("server_ports")
    val serverPorts: List<String> = emptyList(),
    @SerialName("obfs_type")
    val obfsType: String = "",
    @SerialName("obfs_password")
    val obfsPassword: String = "",
    @SerialName("alter_id")
    val alterId: Int = 0,
    val security: String = "",
    @SerialName("socks_version")
    val socksVersion: String = "",
    val network: String = "",
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
                    appendSharedTlsFields(extra)
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
                    appendSharedTlsFields(extra)
                }
            }
            tlsServer.isNotBlank() || fingerprint.isNotBlank() -> {
                put("security", "tls")
                putJsonObject("tlsSettings") {
                    if (tlsServer.isNotBlank()) put("serverName", tlsServer)
                    if (fingerprint.isNotBlank()) put("fingerprint", fingerprint)
                    appendSharedTlsFields(extra)
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
	                        host.split(",").map(String::trim).filter(String::isNotBlank).forEach { add(it) }
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
	                                                host.split(",").map(String::trim).filter(String::isNotBlank).forEach { add(it) }
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

    private fun JsonObjectBuilder.appendSharedTlsFields(extra: Map<String, String>) {
	        extra["alpn"]?.takeIf { it.isNotBlank() }?.let { alpn ->
	            putJsonArray("alpn") {
	                alpn.split(",").map(String::trim).filter(String::isNotBlank).forEach { add(it) }
	            }
	        }
        if (extra["insecure"] == "true") put("allowInsecure", true)
        extra["pin_sha256"]?.takeIf { it.isNotBlank() }?.let { put("certificatePublicKeySha256", it) }
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
    @SerialName("always_direct_apps")
    val alwaysDirectApps: List<String> = emptyList(),
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
            "direct" -> com.privstack.panel.model.RoutingMode.DIRECT
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
            alwaysDirectAppList = alwaysDirectApps,
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

@Serializable
private data class DaemonHealthSection(
    val enabled: Boolean = true,
    @SerialName("interval_sec")
    val intervalSec: Int = 30,
    val threshold: Int = 3,
    @SerialName("check_url")
    val checkUrl: String = "https://www.gstatic.com/generate_204",
    @SerialName("timeout_sec")
    val timeoutSec: Int = 5,
    @SerialName("dns_probe_domains")
    val dnsProbeDomains: List<String> = listOf(
        "connectivitycheck.gstatic.com",
        "cloudflare.com",
        "example.com",
    ),
    @SerialName("egress_urls")
    val egressUrls: List<String> = listOf(
        "https://www.gstatic.com/generate_204",
        "https://cp.cloudflare.com/generate_204",
    ),
    @SerialName("dns_is_hard_readiness")
    val dnsIsHardReadiness: Boolean = false,
) {
    fun toPanelHealth(): HealthConfig =
        HealthConfig(
            enabled = enabled,
            intervalSec = intervalSec,
            threshold = threshold,
            checkUrl = checkUrl,
            timeoutSec = timeoutSec,
            dnsProbeDomains = dnsProbeDomains,
            egressUrls = egressUrls,
            dnsIsHardReadiness = dnsIsHardReadiness,
        )
}

@Serializable
private data class DaemonRuntimeV2Section(
    @SerialName("backend_kind")
    val backendKind: String = "ROOT_TPROXY",
    @SerialName("fallback_policy")
    val fallbackPolicy: String = "OFFER_RESET",
) {
    fun toPanelRuntime(): com.privstack.panel.model.RuntimeConfig =
        com.privstack.panel.model.RuntimeConfig(
            backendKind = parseBackendKind(backendKind),
            fallbackPolicy = parseFallbackPolicy(fallbackPolicy),
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
            com.privstack.panel.model.RoutingMode.DIRECT -> "direct"
            com.privstack.panel.model.RoutingMode.RULES -> "rules"
        })
        put("custom_direct", bridgeJson.encodeToJsonElement(ListSerializer(String.serializer()), direct))
        put("custom_proxy", bridgeJson.encodeToJsonElement(ListSerializer(String.serializer()), proxy))
        put("custom_block", bridgeJson.encodeToJsonElement(ListSerializer(String.serializer()), block))
        put(
            "always_direct_apps",
            bridgeJson.encodeToJsonElement(ListSerializer(String.serializer()), alwaysDirectAppList)
        )
    }
    return bridgeJson.decodeFromJsonElement(DaemonRoutingSection.serializer(), merged)
}

private fun ProfileConfig.toDaemonPanelSection(): DaemonPanelSection =
    DaemonPanelSection(
        id = id,
        name = name,
        activeNodeId = activeNodeId.orEmpty(),
        nodes = nodes,
        tun = tun,
        inbounds = inbounds,
        extra = extra?.jsonObject?.obj("panel"),
    )

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
        com.privstack.panel.model.RoutingMode.DIRECT -> DaemonAppsSection(mode = "off")
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

private fun HealthConfig.toDaemonHealthSection(base: JsonObject?): DaemonHealthSection {
    val merged = buildJsonObject {
        base?.forEach { (key, value) -> put(key, value) }
        put("enabled", enabled)
        put("interval_sec", intervalSec)
        put("threshold", threshold)
        put("check_url", checkUrl)
        put("timeout_sec", timeoutSec)
        putJsonArray("dns_probe_domains") {
            dnsProbeDomains.map(String::trim).filter(String::isNotBlank).distinct().forEach { add(it) }
        }
        putJsonArray("egress_urls") {
            egressUrls.map(String::trim).filter(String::isNotBlank).distinct().forEach { add(it) }
        }
        put("dns_is_hard_readiness", dnsIsHardReadiness)
    }
    return bridgeJson.decodeFromJsonElement(DaemonHealthSection.serializer(), merged)
}

private fun com.privstack.panel.model.RuntimeConfig.toDaemonRuntimeSection(): DaemonRuntimeV2Section =
    DaemonRuntimeV2Section(
        backendKind = backendKind.name,
        fallbackPolicy = fallbackPolicy.name,
    )

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
                ssPlugin = serverEntry?.get("plugin")?.jsonPrimitive?.content.orEmpty(),
                ssPluginOpts = serverEntry?.get("plugin_opts")?.jsonPrimitive?.content.orEmpty(),
            )
        }
        Protocol.SOCKS -> {
            DaemonNodeSection(
                address = server,
                port = port,
                protocol = "socks",
                username = settings?.get("username")?.jsonPrimitive?.content.orEmpty(),
                password = settings?.get("password")?.jsonPrimitive?.content.orEmpty(),
                socksVersion = settings?.get("version")?.jsonPrimitive?.content.orEmpty(),
                network = settings?.get("network")?.jsonPrimitive?.content.orEmpty(),
            )
        }
        Protocol.HYSTERIA2 -> {
            val obfs = settings?.get("obfs")?.jsonObject
            DaemonNodeSection(
                address = server,
                port = port,
                protocol = "hysteria2",
                password = settings?.get("password")?.jsonPrimitive?.content.orEmpty(),
                serverPorts = settings?.get("server_ports")?.jsonArray
                    ?.mapNotNull { it.jsonPrimitive.contentOrNull }
                    .orEmpty(),
                obfsType = obfs?.get("type")?.jsonPrimitive?.content.orEmpty(),
                obfsPassword = obfs?.get("password")?.jsonPrimitive?.content.orEmpty(),
            )
        }
        Protocol.TUIC -> {
            DaemonNodeSection(
                address = server,
                port = port,
                protocol = "tuic",
                uuid = settings?.get("uuid")?.jsonPrimitive?.content.orEmpty(),
                password = settings?.get("password")?.jsonPrimitive?.content.orEmpty(),
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
    val settings = outbound["settings"]?.jsonObject
    val network = stream?.get("network")?.jsonPrimitive?.content.orEmpty().ifBlank { "tcp" }
    val tls = stream?.get("tlsSettings")?.jsonObject
    val reality = stream?.get("realitySettings")?.jsonObject
    val security = stream?.get("security")?.jsonPrimitive?.content.orEmpty()

    val transportProtocol = when {
        this.protocol == Protocol.HYSTERIA2 || this.protocol == Protocol.TUIC -> "tcp"
        security == "reality" || reality != null -> "reality"
        else -> network
    }

    val extra = mutableMapOf<String, String>()
    if (this.protocol == Protocol.HYSTERIA2 || this.protocol == Protocol.TUIC) {
        extra["network"] = network
    }
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

    if (transportProtocol == "reality") {
        extra["public_key"] = reality?.get("publicKey")?.jsonPrimitive?.content.orEmpty()
        extra["short_id"] = reality?.get("shortId")?.jsonPrimitive?.content.orEmpty()
    } else if (security == "tls") {
        extra["security"] = "tls"
    }

    val tlsLike = reality ?: tls
    val alpn = tlsLike?.get("alpn")?.jsonArray
        ?.mapNotNull { it.jsonPrimitive.contentOrNull }
        ?.joinToString(",")
    if (!alpn.isNullOrBlank()) {
        extra["alpn"] = alpn
    }
    val insecure = tlsLike?.get("allowInsecure")?.jsonPrimitive?.booleanOrNull
    if (insecure == true) {
        extra["insecure"] = "true"
    }
    tlsLike?.get("certificatePublicKeySha256")?.jsonPrimitive?.contentOrNull?.let {
        extra["pin_sha256"] = it
    }
    tlsLike?.get("certificate_public_key_sha256")?.jsonArray
        ?.mapNotNull { it.jsonPrimitive.contentOrNull }
        ?.joinToString(",")
        ?.takeIf { it.isNotBlank() }
        ?.let { extra["pin_sha256"] = it }

    if (this.protocol == Protocol.TUIC) {
        settings?.get("congestion_control")?.jsonPrimitive?.contentOrNull?.let {
            extra["congestion_control"] = it
        }
        settings?.get("udp_relay_mode")?.jsonPrimitive?.contentOrNull?.let {
            extra["udp_relay_mode"] = it
        }
        settings?.get("udp_over_stream")?.jsonPrimitive?.contentOrNull?.let {
            extra["udp_over_stream"] = it
        }
        settings?.get("zero_rtt_handshake")?.jsonPrimitive?.contentOrNull?.let {
            extra["zero_rtt_handshake"] = it
        }
        settings?.get("heartbeat")?.jsonPrimitive?.contentOrNull?.let {
            extra["heartbeat"] = it
        }
    }

    return DaemonTransportSection(
        protocol = transportProtocol,
        tlsServer = reality?.get("serverName")?.jsonPrimitive?.content
            ?: tls?.get("serverName")?.jsonPrimitive?.content.orEmpty(),
        fingerprint = reality?.get("fingerprint")?.jsonPrimitive?.content
            ?: tls?.get("fingerprint")?.jsonPrimitive?.content.orEmpty(),
        extra = extra.filterValues { it.isNotBlank() },
    )
}
