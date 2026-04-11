package com.privstack.panel.ipc

import com.privstack.panel.model.AppInfo
import com.privstack.panel.model.AuditReport
import com.privstack.panel.model.DaemonStatus
import com.privstack.panel.model.Node
import com.privstack.panel.model.ProfileConfig
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import javax.inject.Inject
import javax.inject.Singleton

/**
 * Typed, high-level client for every daemon IPC method.
 *
 * Each public function:
 * 1. Builds the params [JsonObject]
 * 2. Delegates to [PrivctlExecutor.execute]
 * 3. Deserialises the [PrivctlResult.Success.data] into the expected model
 *
 * On failure the raw [PrivctlResult] is returned so callers can pattern-match
 * on the specific failure mode.
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

    /** Get daemon runtime status (connection state, traffic, health). */
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

    /** Run a full health check and return the result. */
    suspend fun health(): DaemonClientResult<DaemonStatus> =
        call("health") { json.decodeFromJsonElement(DaemonStatus.serializer(), it) }

    /** Run a privacy/security audit. */
    suspend fun audit(): DaemonClientResult<AuditReport> =
        call("audit") { json.decodeFromJsonElement(AuditReport.serializer(), it) }

    // ---- Configuration ----

    /** Get the full active profile config. */
    suspend fun configGet(): DaemonClientResult<ProfileConfig> =
        call("config.get") { json.decodeFromJsonElement(ProfileConfig.serializer(), it) }

    /** Replace the full profile config. */
    suspend fun configSet(config: ProfileConfig): DaemonClientResult<Unit> {
        val params = buildJsonObject {
            put("config", json.encodeToJsonElement(ProfileConfig.serializer(), config))
        }
        return callVoid("config.set", params)
    }

    /** List all stored profile IDs and names. */
    suspend fun configList(): DaemonClientResult<List<ProfileSummary>> =
        call("config.list") { element ->
            element.jsonArray.map { entry ->
                val obj = entry as JsonObject
                ProfileSummary(
                    id = obj["id"]!!.jsonPrimitive.content,
                    name = obj["name"]!!.jsonPrimitive.content,
                    isActive = obj["active"]?.jsonPrimitive?.content?.toBooleanStrictOrNull() ?: false
                )
            }
        }

    /** Import nodes from share links (one per line). Returns imported count. */
    suspend fun configImport(links: String): DaemonClientResult<List<Node>> {
        val params = buildJsonObject { put("links", links) }
        return call("config.import", params) {
            json.decodeFromJsonElement(ListSerializer(Node.serializer()), it)
        }
    }

    // ---- Logs ----

    /**
     * Fetch recent daemon log lines.
     * @param lines Number of lines to retrieve (default 100).
     * @param level Minimum log level filter ("debug", "info", "warning", "error").
     */
    suspend fun logs(
        lines: Int = 100,
        level: String = "info"
    ): DaemonClientResult<List<String>> {
        val params = buildJsonObject {
            put("lines", lines)
            put("level", level)
        }
        return call("logs", params) { element ->
            element.jsonArray.map { it.jsonPrimitive.content }
        }
    }

    // ---- App management ----

    /** Resolve a UID to package name and app info. */
    suspend fun resolveUid(uid: Int): DaemonClientResult<AppInfo> {
        val params = buildJsonObject { put("uid", uid) }
        return call("app.resolveUid", params) {
            json.decodeFromJsonElement(AppInfo.serializer(), it)
        }
    }

    /** List all installed apps with routing-relevant metadata. */
    suspend fun appList(): DaemonClientResult<List<AppInfo>> =
        call("app.list", timeoutMs = 15_000L) {
            json.decodeFromJsonElement(ListSerializer(AppInfo.serializer()), it)
        }

    // ---- Updates ----
    // All network requests go through the daemon because the APK has NO
    // INTERNET permission.

    /** Check GitHub Releases for a newer version. */
    suspend fun updateCheck(): DaemonClientResult<UpdateCheckInfo> =
        call("update-check", timeoutMs = 30_000L) { element ->
            val obj = element as JsonObject
            UpdateCheckInfo(
                currentVersion = obj["current_version"]?.jsonPrimitive?.content ?: "unknown",
                latestVersion = obj["latest_version"]?.jsonPrimitive?.content ?: "unknown",
                hasUpdate = obj["has_update"]?.jsonPrimitive?.content?.toBooleanStrictOrNull() ?: false,
                changelog = obj["changelog"]?.jsonPrimitive?.content ?: "",
            )
        }

    /** Download the latest module.zip + panel.apk to the daemon's update dir. */
    suspend fun updateDownload(): DaemonClientResult<UpdateDownloadInfo> =
        call("update-download", timeoutMs = 600_000L) { element ->
            val obj = element as JsonObject
            UpdateDownloadInfo(
                modulePath = obj["module_path"]?.jsonPrimitive?.content ?: "",
                apkPath = obj["apk_path"]?.jsonPrimitive?.content ?: "",
                checksums = obj["checksums"]?.jsonPrimitive?.content?.toBooleanStrictOrNull() ?: false,
            )
        }

    /** Install the previously downloaded module + APK. */
    suspend fun updateInstall(): DaemonClientResult<UpdateInstallInfo> =
        call("update-install", timeoutMs = 120_000L) { element ->
            val obj = element as JsonObject
            UpdateInstallInfo(
                moduleInstalled = obj["module_installed"]?.jsonPrimitive?.content?.toBooleanStrictOrNull() ?: false,
                apkInstalled = obj["apk_installed"]?.jsonPrimitive?.content?.toBooleanStrictOrNull() ?: false,
            )
        }

    // ---- Meta ----

    /** Get daemon and core version strings. */
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
}

// ---- Result wrapper with typed data ----

/**
 * Typed result for [DaemonClient] calls.
 * Mirrors [PrivctlResult] failure modes but wraps the success branch in [T].
 */
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
)

data class UpdateDownloadInfo(
    val modulePath: String,
    val apkPath: String,
    val checksums: Boolean,
)

data class UpdateInstallInfo(
    val moduleInstalled: Boolean,
    val apkInstalled: Boolean,
)
