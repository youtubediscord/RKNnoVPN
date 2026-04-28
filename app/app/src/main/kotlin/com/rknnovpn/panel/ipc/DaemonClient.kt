package com.rknnovpn.panel.ipc

import com.rknnovpn.panel.BuildConfig
import com.rknnovpn.panel.model.AppInfo
import com.rknnovpn.panel.model.AuditReport
import com.rknnovpn.panel.model.BackendHealthSnapshot
import com.rknnovpn.panel.model.BackendStatusV2
import com.rknnovpn.panel.model.DaemonStatus
import com.rknnovpn.panel.model.DesiredStateV2
import com.rknnovpn.panel.model.Node
import com.rknnovpn.panel.model.NodeProbeResultV2
import com.rknnovpn.panel.model.ProfileConfig
import com.rknnovpn.panel.model.RuntimeCompatibilityStatus
import com.rknnovpn.panel.model.Subscription
import com.rknnovpn.panel.model.SubscriptionSource
import kotlinx.serialization.Serializable
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.add
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
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
import javax.inject.Inject
import javax.inject.Singleton

private const val COMPATIBILITY_ERROR_CODE = -32090
private const val CONFIG_APPLY_ERROR_CODE = -32003

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
    private val executor: DaemonctlExecutor
) {
    companion object {
        const val MIN_CONTROL_PROTOCOL_VERSION = 5
        const val MIN_SCHEMA_VERSION = 5
        val REQUIRED_METHODS: Set<String> = GeneratedDaemonContract.APK_REQUIRED_METHODS
    }

    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
        encodeDefaults = true
    }
    private val prettyJson = Json {
        prettyPrint = true
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }

    fun toDaemonStatus(status: BackendStatusV2): DaemonStatus = status.toDaemonStatus()

    // ---- Connection lifecycle ----

    /** Get daemon runtime status (connection state, active node, health). */
    suspend fun status(): DaemonClientResult<DaemonStatus> {
        return when (val result = backendStatus()) {
            is DaemonClientResult.Ok -> DaemonClientResult.Ok(result.data.toDaemonStatus())
            is DaemonClientResult.DaemonError -> result
            else -> result.asFailure()
        }
    }

    /** Start the proxy connection using the active profile. */
    suspend fun start(): DaemonClientResult<BackendStatusV2> {
        requireCompatible("backend.start", "backend.status")?.let { return it.asFailure() }
        return when (val result = backendStart()) {
            is DaemonClientResult.Ok -> result
            is DaemonClientResult.DaemonError -> result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Stop the proxy connection (keeps daemon alive). */
    suspend fun stop(): DaemonClientResult<BackendStatusV2> {
        return when (val result = backendStop()) {
            is DaemonClientResult.Ok -> result
            is DaemonClientResult.DaemonError -> result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Reload the active configuration without full restart. */
    suspend fun reload(): DaemonClientResult<BackendStatusV2> {
        requireCompatible("backend.restart", "backend.status")?.let { return it.asFailure() }
        return when (val result = backendRestart()) {
            is DaemonClientResult.Ok -> result
            is DaemonClientResult.DaemonError -> result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Force-remove RKNnoVPN network rules and stop the proxy core. */
    suspend fun networkReset(): DaemonClientResult<BackendStatusV2> {
        return when (val result = backendReset()) {
            is DaemonClientResult.Ok -> result
            is DaemonClientResult.DaemonError -> result.asFailure()
            else -> result.asFailure()
        }
    }

    /** Run a full health check and return the result in the dashboard shape. */
    suspend fun health(): DaemonClientResult<DaemonStatus> {
        return when (val statusResult = backendStatus()) {
            is DaemonClientResult.Ok -> when (val healthResult = diagnosticsHealth()) {
                is DaemonClientResult.Ok -> {
                    DaemonClientResult.Ok(statusResult.data.toDaemonStatus(healthOverride = healthResult.data))
                }
                is DaemonClientResult.DaemonError -> healthResult.asFailure()
                else -> healthResult.asFailure()
            }
            is DaemonClientResult.DaemonError -> statusResult.asFailure()
            else -> statusResult.asFailure()
        }
    }

    /** Run a privacy/security audit. */
    suspend fun audit(): DaemonClientResult<AuditReport> =
        call("audit") { json.decodeFromJsonElement(AuditReport.serializer(), it) }

    // ---- Profile ----

    /** Get the daemon-owned profile document. */
    suspend fun profileGet(): DaemonClientResult<ProfileConfig> {
        requireCompatible("profile.get")?.let { return it.asFailure() }
        return call("profile.get") {
            json.decodeFromJsonElement(ProfileConfig.serializer(), it)
        }
    }

    /** Replace the full daemon-owned profile document. */
    suspend fun profileApply(
        config: ProfileConfig,
        reload: Boolean = true,
    ): DaemonClientResult<ConfigMutationInfo> {
        requireCompatible("profile.apply")?.let { return it.asFailure() }
        return callConfigMutation(
            "profile.apply",
            buildJsonObject {
                put("profile", json.encodeToJsonElement(ProfileConfig.serializer(), config))
                put("reload", reload)
            },
        )
    }

    suspend fun profileImportNodes(
        nodes: List<Node>,
        reload: Boolean = true,
    ): DaemonClientResult<ConfigMutationInfo> {
        requireCompatible("profile.importNodes")?.let { return it.asFailure() }
        return callConfigMutation(
            "profile.importNodes",
            buildJsonObject {
                put("nodes", json.encodeToJsonElement(ListSerializer(Node.serializer()), nodes))
                put("reload", reload)
            },
        )
    }

    suspend fun profileSetActiveNode(
        nodeId: String,
        reload: Boolean = true,
    ): DaemonClientResult<ConfigMutationInfo> {
        requireCompatible("profile.setActiveNode")?.let { return it.asFailure() }
        return callConfigMutation(
            "profile.setActiveNode",
            buildJsonObject {
                put("nodeId", nodeId)
                put("reload", reload)
            },
        )
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

    suspend fun subscriptionPreview(url: String): DaemonClientResult<SubscriptionPreviewInfo> {
        requireCompatible("subscription.preview")?.let { return it.asFailure() }
        val params = buildJsonObject { put("url", url) }
        return call("subscription.preview", params, timeoutMs = 60_000L) { element ->
            json.decodeFromJsonElement(SubscriptionPreviewInfo.serializer(), element)
        }
    }

    suspend fun subscriptionRefresh(url: String): DaemonClientResult<ConfigMutationInfo> {
        requireCompatible("subscription.refresh")?.let { return it.asFailure() }
        val params = buildJsonObject { put("url", url) }
        return callConfigMutation("subscription.refresh", params)
    }

    suspend fun nodeTest(
        nodeIds: List<String> = emptyList(),
        url: String = "",
        timeoutMs: Int = 5_000,
    ): DaemonClientResult<NodeTestInfo> {
        when (val result = diagnosticsTestNodes(nodeIds, url, timeoutMs)) {
            is DaemonClientResult.Ok -> {
                return DaemonClientResult.Ok(
                    NodeTestInfo(
                        url = url,
                        results = result.data.map { probe ->
                            val tcpStatus = probe.tcpStatus.ifBlank {
                                if (probe.tcpDirect != null) "ok" else "fail"
                            }
                            val urlStatus = probe.urlStatus.ifBlank {
                                if (probe.tunnelDelay != null) "ok" else "not_run"
                            }
                            val verdict = probe.verdict.ifBlank {
                                when {
                                    tcpStatus == "ok" && urlStatus != "ok" -> "unusable"
                                    urlStatus == "ok" && probe.tunnelDelay != null -> "usable"
                                    else -> "unknown"
                                }
                            }.let { raw ->
                                if (tcpStatus == "ok" && urlStatus != "ok") "unusable" else raw
                            }
                            NodeTestResult(
                                id = probe.id,
                                name = probe.name,
                                tag = probe.id,
                                server = probe.server,
                                port = probe.port,
                                protocol = probe.protocol,
                                tcpMs = probe.tcpDirect?.toInt(),
                                urlMs = probe.tunnelDelay?.toInt(),
                                throughputBps = probe.throughputBps,
                                tcpStatus = tcpStatus,
                                urlStatus = urlStatus,
                                verdict = verdict,
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
        call("backend.start", timeoutMs = ACCEPT_TIMEOUT_MS) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
        }

    suspend fun backendStop(): DaemonClientResult<BackendStatusV2> =
        call("backend.stop", timeoutMs = ACCEPT_TIMEOUT_MS) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
        }

    suspend fun backendRestart(): DaemonClientResult<BackendStatusV2> =
        call("backend.restart", timeoutMs = ACCEPT_TIMEOUT_MS) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
        }

    suspend fun backendReset(): DaemonClientResult<BackendStatusV2> =
        call("backend.reset", timeoutMs = ACCEPT_TIMEOUT_MS) {
            json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
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
                add("daemon")
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
        return call("diagnostics.report", params, timeoutMs = 30_000L) { element ->
            prettyJson.encodeToString(JsonElement.serializer(), element)
        }
    }

    suspend fun selfCheck(): DaemonClientResult<SelfCheckSummary> {
        return call("self-check", timeoutMs = 15_000L) { element ->
            json.decodeFromJsonElement(SelfCheckSummary.serializer(), element)
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
                manifestPath = obj["manifest_path"]?.jsonPrimitive?.content ?: "",
                checksums = obj["checksums"]?.jsonPrimitive?.booleanOrNull ?: false,
            )
        }

    suspend fun updateInstall(
        modulePath: String = "",
        apkPath: String = "",
    ): DaemonClientResult<UpdateInstallInfo> {
        requireCompatible("update-install")?.let { return it.asFailure() }
        val params = buildJsonObject {
            if (modulePath.isNotBlank()) {
                put("module_path", modulePath)
            }
            if (apkPath.isNotBlank()) {
                put("apk_path", apkPath)
            }
        }
        return call("update-install", params, timeoutMs = ACCEPT_TIMEOUT_MS) { element ->
            val status = json.decodeFromJsonElement(BackendStatusV2.serializer(), element)
            UpdateInstallInfo(
                accepted = status.activeOperation?.kind == "update-install",
                operationGeneration = status.activeOperation?.generation ?: 0L,
                runtimeStatus = status,
            )
        }
    }

    // ---- Meta ----

    suspend fun ipcContract(): DaemonClientResult<IpcContractInfo> =
        call("ipc.contract") { element ->
            json.decodeFromJsonElement(IpcContractInfo.serializer(), element)
        }

    suspend fun version(): DaemonClientResult<VersionInfo> =
        call("version") { element ->
            val obj = element as JsonObject
            VersionInfo(
                daemonVersion = obj["daemon"]?.jsonPrimitive?.content ?: "unknown",
                coreVersion = obj["core"]?.jsonPrimitive?.content ?: "unknown",
                daemonctlVersion = obj["daemonctl"]?.jsonPrimitive?.content ?: "unknown",
                moduleVersion = obj["module"]?.jsonObject?.get("version")?.jsonPrimitive?.content ?: "unknown",
                moduleVersionCode = obj["module"]?.jsonObject?.get("versionCode")?.jsonPrimitive?.content ?: "",
                modulePath = obj["module"]?.jsonObject?.get("path")?.jsonPrimitive?.content ?: "",
                currentReleaseVersion = obj["current_release"]?.jsonObject?.get("version")?.jsonPrimitive?.content ?: "",
                currentReleaseOK = obj["current_release"]?.jsonObject?.get("ok")?.jsonPrimitive?.booleanOrNull,
                currentReleaseError = obj["current_release"]?.jsonObject?.get("error")?.jsonPrimitive?.contentOrNull ?: "",
                singBoxAvailable = obj["sing_box"]?.jsonObject?.get("error")?.jsonPrimitive?.contentOrNull.isNullOrBlank(),
                singBoxError = obj["sing_box"]?.jsonObject?.get("error")?.jsonPrimitive?.contentOrNull ?: "",
                controlProtocolVersion = obj["control_protocol_version"]?.jsonPrimitive?.intOrNull
                    ?: obj["control_protocol"]?.jsonPrimitive?.intOrNull
                    ?: 0,
                schemaVersion = obj["schema_version"]?.jsonPrimitive?.intOrNull ?: 0,
                ipcContractVersion = obj["ipc_contract_version"]?.jsonPrimitive?.intOrNull ?: 0,
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

    private suspend fun <T> call(
        method: String,
        params: JsonObject = emptyJsonObject(),
        timeoutMs: Long = 5_000L,
        transform: (JsonElement) -> T
    ): DaemonClientResult<T> {
        return when (val raw = executor.execute(method, params, timeoutMs)) {
            is DaemonctlResult.Success -> try {
                DaemonClientResult.Ok(transform(raw.data))
            } catch (e: Exception) {
                DaemonClientResult.ParseError(raw.data.toString(), e)
            }
            is DaemonctlResult.Error -> DaemonClientResult.DaemonError(raw.code, raw.message, raw.details, raw.envelope)
            is DaemonctlResult.RootDenied -> DaemonClientResult.RootDenied(raw.reason)
            is DaemonctlResult.Timeout -> DaemonClientResult.Timeout(raw.method)
            is DaemonctlResult.DaemonNotFound -> DaemonClientResult.DaemonNotFound(raw.path)
            is DaemonctlResult.UnexpectedError -> DaemonClientResult.Failure(raw.throwable)
        }
    }

    private suspend fun callConfigMutation(
        method: String,
        params: JsonObject,
    ): DaemonClientResult<ConfigMutationInfo> {
        return when (val raw = executor.execute(method, params, timeoutMs = 60_000L)) {
            is DaemonctlResult.Success -> try {
                val info = parseConfigMutationInfo(raw.data)
                if (!info.ok) {
                    DaemonClientResult.DaemonError(
                        CONFIG_APPLY_ERROR_CODE,
                        info.message.ifBlank { "configuration was not applied" },
                        raw.data,
                    )
                } else {
                    DaemonClientResult.Ok(info)
                }
            } catch (e: Exception) {
                DaemonClientResult.ParseError(raw.data.toString(), e)
            }
            is DaemonctlResult.Error -> DaemonClientResult.DaemonError(raw.code, raw.message, raw.details, raw.envelope)
            is DaemonctlResult.RootDenied -> DaemonClientResult.RootDenied(raw.reason)
            is DaemonctlResult.Timeout -> DaemonClientResult.Timeout(raw.method)
            is DaemonctlResult.DaemonNotFound -> DaemonClientResult.DaemonNotFound(raw.path)
            is DaemonctlResult.UnexpectedError -> DaemonClientResult.Failure(raw.throwable)
        }
    }

    private fun parseConfigMutationInfo(element: JsonElement): ConfigMutationInfo {
        val obj = element.jsonObject
        val ok = obj.booleanField("ok") ?: true
        val configSaved = obj.booleanField("config_saved", "configSaved") ?: ok
        val runtimeApplied = obj.booleanField("runtime_applied", "runtimeApplied") ?: ok
        return ConfigMutationInfo(
            ok = ok,
            status = obj["status"]?.jsonPrimitive?.contentOrNull.orEmpty(),
            reload = obj["reload"]?.jsonPrimitive?.booleanOrNull ?: false,
            updated = obj["updated"]?.jsonPrimitive?.intOrNull,
            configSaved = configSaved,
            runtimeApplied = runtimeApplied,
            runtimeApply = (
                obj["runtime_apply"]?.jsonPrimitive?.contentOrNull
                    ?: obj["runtimeApply"]?.jsonPrimitive?.contentOrNull
                ).orEmpty(),
            code = obj["code"]?.jsonPrimitive?.contentOrNull.orEmpty(),
            message = obj["message"]?.jsonPrimitive?.contentOrNull.orEmpty(),
            operation = obj["operation"],
            runtimeStatus = obj["runtimeStatus"]?.let {
                json.decodeFromJsonElement(BackendStatusV2.serializer(), it)
            },
            source = obj["source"]?.let {
                json.decodeFromJsonElement(SubscriptionSource.serializer(), it)
            },
            subscription = obj["subscription"]?.let {
                json.decodeFromJsonElement(Subscription.serializer(), it)
            },
            imported = obj["imported"]?.jsonPrimitive?.intOrNull,
            parseFailures = obj["parseFailures"]?.jsonPrimitive?.intOrNull,
        )
    }

    private suspend fun requireCompatible(vararg requiredMethods: String): DaemonClientResult<Unit>? {
        return when (val result = version()) {
            is DaemonClientResult.Ok -> {
                val info = result.data
                val required = requiredMethods.toSet()
                val enforceReleaseMatch = required.none { it in REPAIR_METHODS }
                val requiresSingBox = required.any { it in SING_BOX_METHODS }
                val requiresSchemaV4 = enforceReleaseMatch && required.any { it in SCHEMA_V4_METHODS }
                if ("ipc.contract" !in info.supportedMethods) {
                    return DaemonClientResult.DaemonError(
                        COMPATIBILITY_ERROR_CODE,
                        "APK и модуль несовместимы: daemon не рекламирует IPC contract",
                    )
                }
                val contract = when (val contractResult = ipcContract()) {
                    is DaemonClientResult.Ok -> contractResult.data
                    is DaemonClientResult.DaemonError -> return DaemonClientResult.DaemonError(
                        COMPATIBILITY_ERROR_CODE,
                        "APK и модуль несовместимы: daemon не отдал IPC contract (${contractResult.message})",
                        contractResult.details,
                    )
                    is DaemonClientResult.ParseError -> return DaemonClientResult.DaemonError(
                        COMPATIBILITY_ERROR_CODE,
                        "APK и модуль несовместимы: некорректный IPC contract",
                    )
                    is DaemonClientResult.RootDenied -> return contractResult
                    is DaemonClientResult.Timeout -> return contractResult
                    is DaemonClientResult.DaemonNotFound -> return contractResult
                    is DaemonClientResult.Failure -> return contractResult
                }
                val missingCapabilities = contract.missingCapabilities(requiredMethods.toList())
                val missingMethods = contract.missingRequiredMethods(requiredMethods.toList() + "version")
                val invalidContractMethods = contract.methods.filter {
                    it.method.isBlank() || (it.method in requiredMethods && it.capability.isBlank())
                }
                if (invalidContractMethods.isNotEmpty()) {
                    return DaemonClientResult.DaemonError(
                        COMPATIBILITY_ERROR_CODE,
                        "APK и модуль несовместимы: IPC contract неполный для ${invalidContractMethods.joinToString(", ") { it.method.ifBlank { "unknown" } }}",
                    )
                }
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
                    enforceReleaseMatch && missingCapabilities.isNotEmpty() ->
                        DaemonClientResult.DaemonError(
                            COMPATIBILITY_ERROR_CODE,
                            "APK и модуль несовместимы: daemon ${info.daemonVersion}, module ${info.moduleVersion}; нет capabilities ${missingCapabilities.joinToString(", ")}",
                        )
                    requiresSingBox && !info.singBoxAvailable ->
                        DaemonClientResult.DaemonError(
                            COMPATIBILITY_ERROR_CODE,
                            "APK и модуль несовместимы: sing-box недоступен (${info.singBoxError.ifBlank { "unknown" }})",
                        )
                    else -> {
                        if (missingMethods.isEmpty()) {
                            null
                        } else {
                            DaemonClientResult.DaemonError(
                                COMPATIBILITY_ERROR_CODE,
                                "APK и модуль несовместимы: APK ${BuildConfig.VERSION_NAME}, daemon ${info.daemonVersion}, module ${info.moduleVersion}; нет методов ${missingMethods.joinToString(", ")}",
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

}

// ---- Result wrapper with typed data ----

sealed class DaemonClientResult<out T> {
    data class Ok<T>(val data: T) : DaemonClientResult<T>()
    data class DaemonError(
        val code: Int,
        val message: String,
        val details: JsonElement? = null,
        val envelope: JsonElement? = null,
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
        is DaemonError -> throw DaemonctlException("Daemon error $code: $message")
        is RootDenied -> throw DaemonctlException("Root denied: $reason")
        is Timeout -> throw DaemonctlException("Timeout on method: $method")
        is DaemonNotFound -> throw DaemonctlException("Daemon not found at: $path")
        is ParseError -> throw DaemonctlException("Parse error on: ${raw.take(100)}", cause)
        is Failure -> throw DaemonctlException("Unexpected failure", throwable)
    }
}

// ---- Supporting data classes ----

data class ProfileSummary(
    val id: String,
    val name: String,
    val isActive: Boolean
)

@Serializable
data class IpcContractInfo(
    val version: Int = 0,
    val controlProtocolVersion: Int = 0,
    val schemaVersion: Int = 0,
    val capabilities: List<String> = emptyList(),
    val apkRequiredMethods: List<String> = emptyList(),
    val methods: List<IpcMethodContractInfo> = emptyList(),
) {
    fun missingRequiredMethods(required: Collection<String>): List<String> {
        val methodNames = methods.mapTo(mutableSetOf()) { it.method }
        if (methodNames.isEmpty()) return required.toList()
        return required.filterNot { it in methodNames }
    }

    fun missingCapabilities(requiredMethods: Collection<String>): List<String> {
        if (capabilities.isEmpty()) return listOf("capabilities")
        val byMethod = methods.associateBy { it.method }
        return requiredMethods
            .mapNotNull { method -> byMethod[method]?.capability?.takeIf { it.isNotBlank() } }
            .distinct()
            .filterNot { it in capabilities }
    }
}

@Serializable
data class IpcMethodContractInfo(
    val method: String = "",
    val capability: String = "",
    val mutating: Boolean = false,
    val async: Boolean = false,
    val request: String = "",
    val result: String = "",
    val errorCodes: List<String> = emptyList(),
    val operation: IpcOperationContractInfo? = null,
    val compatibility: String = "",
)

@Serializable
data class IpcOperationContractInfo(
    val type: String = "",
    val asyncResultVia: String = "",
    val stages: List<String> = emptyList(),
)

data class VersionInfo(
    val daemonVersion: String,
    val coreVersion: String,
    val daemonctlVersion: String,
    val moduleVersion: String = "unknown",
    val moduleVersionCode: String = "",
    val modulePath: String = "",
    val currentReleaseVersion: String = "",
    val currentReleaseOK: Boolean? = null,
    val currentReleaseError: String = "",
    val singBoxAvailable: Boolean = true,
    val singBoxError: String = "",
    val controlProtocolVersion: Int = 0,
    val schemaVersion: Int = 0,
    val ipcContractVersion: Int = 0,
    val panelMinVersion: String = "",
    val capabilities: List<String> = emptyList(),
    val supportedMethods: List<String> = emptyList(),
) {
    fun missingRequiredMethods(required: Collection<String>): List<String> {
        if (supportedMethods.isEmpty()) return required.toList()
        return required.filterNot { it in supportedMethods }
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
        val currentRelease = stableReleaseVersion(currentReleaseVersion)
        if (currentRelease != null && currentRelease != apk) {
            return "APK и current release несовместимы: APK $apkVersion, current $currentReleaseVersion. Установите matching release."
        }
        return null
    }

    fun currentReleaseWarning(): String? {
        if (currentReleaseOK != false) return null
        val detail = currentReleaseError.takeIf { it.isNotBlank() }?.let { ": $it" }.orEmpty()
        return "Каталог текущего релиза повреждён$detail. Запуск не заблокирован; используйте сброс root-части или переустановку модуля, если runtime ведёт себя нестабильно."
    }
}

private fun stableReleaseVersion(raw: String): String? {
    val normalized = raw.trim().removePrefix("v")
    if (!Regex("""\d+\.\d+\.\d+""").matches(normalized)) return null
    return normalized
}

private val REPAIR_METHODS = setOf("backend.stop", "backend.reset")
private const val ACCEPT_TIMEOUT_MS = 10_000L
private val SING_BOX_METHODS = setOf("backend.start", "backend.restart")
private val SCHEMA_V4_METHODS = setOf(
    "backend.applyDesiredState",
    "backend.start",
    "backend.restart",
    "profile.apply",
    "profile.importNodes",
    "profile.setActiveNode",
    "subscription.refresh",
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
    val manifestPath: String = "",
)

data class UpdateInstallInfo(
    val accepted: Boolean,
    val operationGeneration: Long = 0L,
    val runtimeStatus: BackendStatusV2? = null,
)

data class ConfigMutationInfo(
    val ok: Boolean = true,
    val status: String = "",
    val reload: Boolean = false,
    val updated: Int? = null,
    val configSaved: Boolean = true,
    val runtimeApplied: Boolean = true,
    val runtimeApply: String = "",
    val code: String = "",
    val message: String = "",
    val operation: JsonElement? = null,
    val runtimeStatus: BackendStatusV2? = null,
    val source: SubscriptionSource? = null,
    val subscription: Subscription? = null,
    val imported: Int? = null,
    val parseFailures: Int? = null,
)

@Serializable
data class SubscriptionPreviewInfo(
    val source: SubscriptionSource = SubscriptionSource(),
    val subscription: Subscription = Subscription(providerKey = "", url = ""),
    val nodes: List<Node> = emptyList(),
    val added: Int = 0,
    val updated: Int = 0,
    val unchanged: Int = 0,
    val stale: Int = 0,
    val parseFailures: Int = 0,
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

@Serializable
data class SelfCheckSummary(
    val status: String = "unknown",
    val healthCode: String = "",
    val healthDetail: String = "",
    val operationalHealthy: Boolean = false,
    val rebootRequired: Boolean = false,
    val issueCount: Int = 0,
    val issues: List<String> = emptyList(),
    val compatibilityIssues: List<String> = emptyList(),
    val privacyIssues: List<String> = emptyList(),
    val compatibility: SelfCheckCompatibility = SelfCheckCompatibility(),
    val runtime: SelfCheckRuntime = SelfCheckRuntime(),
    val routing: SelfCheckRouting = SelfCheckRouting(),
    val nodeTests: SelfCheckNodeTests = SelfCheckNodeTests(),
)

@Serializable
data class SelfCheckCompatibility(
    val daemonVersion: String = "",
    val moduleVersion: String = "",
    val controlProtocolVersion: Int = 0,
    val schemaVersion: Int = 0,
    val panelMinVersion: String = "",
    val currentReleaseVersion: String = "",
    val currentReleaseOk: Boolean = false,
    val singBoxCheckOk: Boolean = false,
)

@Serializable
data class SelfCheckRuntime(
    val stageOperation: String = "",
    val stageStatus: String = "",
    val failedStage: String = "",
    val lastCode: String = "",
    val rollbackApplied: Boolean = false,
    val runtimeReportAge: String = "",
)

@Serializable
data class SelfCheckRouting(
    val mode: String = "",
    val activeNodeMode: String = "",
    val activeNodeId: String = "",
    val activeNodeName: String = "",
    val activeNodeProtocol: String = "",
    val nodeCount: Int = 0,
    val groups: List<String> = emptyList(),
    val appGroupRouteCount: Int = 0,
    val sharingEnabled: Boolean = false,
)

@Serializable
data class SelfCheckNodeTests(
    val total: Int = 0,
    val usable: Int = 0,
    val unusable: Int = 0,
    val tcpOnly: Int = 0,
)

data class NodeTestResult(
    val id: String,
    val name: String,
    val tag: String,
    val server: String,
    val port: Int,
    val protocol: String,
    val tcpMs: Int?,
    val urlMs: Int?,
    val throughputBps: Long? = null,
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
    val compatibilityIssue = compatibility?.blockingCompatibilityIssue(
        apkVersion = BuildConfig.VERSION_NAME,
        requiredMethods = DaemonClient.REQUIRED_METHODS,
    )
    val displayPhase = activeOperation?.phase ?: appliedState.phase
    val lastFailure = lastOperation?.takeIf { !it.succeeded && activeOperation == null }
    val connectionState = if (compatibilityIssue != null) {
        com.rknnovpn.panel.model.ConnectionState.ERROR
    } else when (displayPhase) {
        com.rknnovpn.panel.model.BackendPhase.HEALTHY,
        com.rknnovpn.panel.model.BackendPhase.RULES_APPLIED,
        com.rknnovpn.panel.model.BackendPhase.DNS_APPLIED,
        com.rknnovpn.panel.model.BackendPhase.OUTBOUND_CHECKED,
        com.rknnovpn.panel.model.BackendPhase.DEGRADED ->
            if (effectiveHealth.healthy) {
                com.rknnovpn.panel.model.ConnectionState.CONNECTED
            } else {
                com.rknnovpn.panel.model.ConnectionState.ERROR
            }
        com.rknnovpn.panel.model.BackendPhase.STOPPED -> com.rknnovpn.panel.model.ConnectionState.DISCONNECTED
        com.rknnovpn.panel.model.BackendPhase.APPLYING,
        com.rknnovpn.panel.model.BackendPhase.STARTING,
        com.rknnovpn.panel.model.BackendPhase.CONFIG_CHECKED,
        com.rknnovpn.panel.model.BackendPhase.CORE_SPAWNED,
        com.rknnovpn.panel.model.BackendPhase.CORE_LISTENING,
        com.rknnovpn.panel.model.BackendPhase.STOPPING,
        com.rknnovpn.panel.model.BackendPhase.RESETTING -> com.rknnovpn.panel.model.ConnectionState.CONNECTING
        com.rknnovpn.panel.model.BackendPhase.FAILED -> com.rknnovpn.panel.model.ConnectionState.ERROR
    }

    return DaemonStatus(
        state = connectionState,
        activeNodeId = desiredState.activeProfileId,
        uptime = 0L,
        health = com.rknnovpn.panel.model.HealthResult(
            healthy = effectiveHealth.healthy,
            coreRunning = effectiveHealth.coreReady,
            tunActive = false,
            dnsOperational = effectiveHealth.dnsReady,
            routingReady = effectiveHealth.routingReady,
            egressReady = effectiveHealth.egressReady,
            operationalHealthy = effectiveHealth.operationalHealthy,
            backendKind = appliedState.backendKind,
            phase = displayPhase,
            lastDebug = effectiveHealth.lastDebug.ifBlank { null },
            rollbackApplied = effectiveHealth.rollbackApplied,
            stageReport = effectiveHealth.stageReport,
            checkedAt = effectiveHealth.checkedAt?.let(::epochSeconds) ?: 0L,
            lastCode = if (compatibilityIssue != null) {
                "DAEMON_INCOMPATIBLE"
            } else {
                effectiveHealth.lastCode.ifBlank { lastFailure?.errorCode?.ifBlank { null } }
            },
            lastError = if (compatibilityIssue != null) {
                compatibilityIssue
            } else {
                effectiveHealth.lastError.ifBlank { lastFailure?.errorMessage?.ifBlank { null } }
            },
            lastUserMessage = compatibilityIssue
                ?: effectiveHealth.lastUserMessage.ifBlank { null },
        ),
        compatibility = compatibility,
        activeOperation = activeOperation,
        lastOperation = lastOperation,
        updateInstall = updateInstall,
    )
}

private fun RuntimeCompatibilityStatus.blockingCompatibilityIssue(
    apkVersion: String,
    requiredMethods: Collection<String>,
): String? {
    releaseMismatch(apkVersion)?.let { return it }
    if (controlProtocolVersion < DaemonClient.MIN_CONTROL_PROTOCOL_VERSION) {
        return "APK и модуль несовместимы: APK $apkVersion, daemon $daemonVersion, module $moduleVersion; control protocol $controlProtocolVersion, нужен ${DaemonClient.MIN_CONTROL_PROTOCOL_VERSION}"
    }
    if (schemaVersion < DaemonClient.MIN_SCHEMA_VERSION) {
        return "APK и модуль несовместимы: daemon config schema $schemaVersion, нужна ${DaemonClient.MIN_SCHEMA_VERSION}"
    }
    val missingMethods = missingRequiredMethods(requiredMethods)
    if (missingMethods.isNotEmpty()) {
        return "APK и модуль несовместимы: APK $apkVersion, daemon $daemonVersion, module $moduleVersion; нет методов ${missingMethods.joinToString(", ")}"
    }
    val missingCapabilities = missingCapabilities(requiredMethods)
    if (missingCapabilities.isNotEmpty()) {
        return "APK и модуль несовместимы: daemon $daemonVersion, module $moduleVersion; нет capabilities ${missingCapabilities.joinToString(", ")}"
    }
    return null
}

private fun RuntimeCompatibilityStatus.missingRequiredMethods(required: Collection<String>): List<String> {
    if (supportedMethods.isEmpty()) return required.toList()
    return required.filterNot { it in supportedMethods }
}

private fun RuntimeCompatibilityStatus.missingCapabilities(requiredMethods: Collection<String>): List<String> {
    if (capabilities.isEmpty()) return listOf("capabilities")
    val byMethod = methods.associateBy { it.method }
    return requiredMethods
        .mapNotNull { method -> byMethod[method]?.capability?.takeIf { it.isNotBlank() } }
        .distinct()
        .filterNot { it in capabilities }
}

private fun RuntimeCompatibilityStatus.releaseMismatch(apkVersion: String): String? {
    val apk = stableReleaseVersion(apkVersion) ?: return null
    val daemon = stableReleaseVersion(daemonVersion)
    if (daemon != null && daemon != apk) {
        return "APK и daemon несовместимы: APK $apkVersion, daemon $daemonVersion. Установите matching release."
    }
    val module = stableReleaseVersion(moduleVersion)
    if (module != null && module != apk) {
        return "APK и модуль несовместимы: APK $apkVersion, module $moduleVersion. Установите matching release."
    }
    val currentRelease = stableReleaseVersion(currentReleaseVersion)
    if (currentRelease != null && currentRelease != apk) {
        return "APK и current release несовместимы: APK $apkVersion, current $currentReleaseVersion. Установите matching release."
    }
    return null
}

private fun epochSeconds(raw: String): Long =
    runCatching { java.time.Instant.parse(raw).epochSecond }.getOrDefault(0L)

private fun JsonObject.booleanField(vararg keys: String): Boolean? {
    for (key in keys) {
        val value = this[key]?.jsonPrimitive?.booleanOrNull
        if (value != null) return value
    }
    return null
}

private fun <T> DaemonClientResult<T>.asFailure(): DaemonClientResult<Nothing> = when (this) {
    is DaemonClientResult.DaemonError -> this
    is DaemonClientResult.RootDenied -> this
    is DaemonClientResult.Timeout -> this
    is DaemonClientResult.DaemonNotFound -> this
    is DaemonClientResult.ParseError -> this
    is DaemonClientResult.Failure -> this
    is DaemonClientResult.Ok -> error("Success result cannot be converted to failure")
}
