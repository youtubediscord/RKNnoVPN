package com.privstack.panel.ipc

import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import java.io.BufferedReader
import java.io.InputStreamReader
import javax.inject.Inject
import javax.inject.Singleton
import kotlin.coroutines.resume

/**
 * Low-level executor that shells out to the `privctl` binary via `su`.
 *
 * Every call:
 * 1. Builds a JSON-RPC-style request:  `privctl <method> '<params>'`
 * 2. Runs it under `su -c "..."`
 * 3. Captures stdout, parses as JSON
 * 4. Maps the response to [PrivctlResult]
 *
 * Thread-safety: all calls are dispatched on [Dispatchers.IO].
 * Timeout default: 5 000 ms, configurable per-call.
 */
@Singleton
class PrivctlExecutor @Inject constructor() {

    private val privctlPath = "/data/adb/privstack/bin/privctl"

    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }

    companion object {
        private const val TAG = "PrivctlExecutor"
        private const val DEFAULT_TIMEOUT_MS = 5_000L

        /** Exit code returned by `su` when the user denies the superuser prompt. */
        private const val SU_DENIED_EXIT_CODE = 13

        /** Common error code the daemon sends when it is not running. */
        private const val DAEMON_NOT_RUNNING_CODE = -32001
    }

    /**
     * Execute a single privctl JSON-RPC method.
     *
     * @param method  The method name (e.g. "status", "config.get").
     * @param params  Optional parameter object.
     * @param timeoutMs  Maximum wall-clock time for the command.
     * @return A [PrivctlResult] that is never null.
     */
    suspend fun execute(
        method: String,
        params: JsonObject = emptyJsonObject(),
        timeoutMs: Long = DEFAULT_TIMEOUT_MS
    ): PrivctlResult = withContext(Dispatchers.IO) {
        try {
            val result = withTimeoutOrNull(timeoutMs) {
                executeRaw(method, params)
            }
            result ?: PrivctlResult.Timeout(timeoutMs, method)
        } catch (e: Exception) {
            Log.e(TAG, "execute($method) failed unexpectedly", e)
            PrivctlResult.UnexpectedError(e)
        }
    }

    // ---- internals ----

    private suspend fun executeRaw(
        method: String,
        params: JsonObject
    ): PrivctlResult = suspendCancellableCoroutine { cont ->
        var process: Process? = null
        try {
            val paramsJson = if (params.isEmpty()) "" else " '${params}'"
            val command = arrayOf(
                "su", "-c", "$privctlPath $method$paramsJson"
            )

            Log.d(TAG, ">>> su -c \"$privctlPath $method${if (params.isEmpty()) "" else " ..."}\"")

            process = Runtime.getRuntime().exec(command)

            cont.invokeOnCancellation {
                process.destroy()
            }

            val stdout = BufferedReader(InputStreamReader(process.inputStream))
                .use { it.readText().trim() }
            val stderr = BufferedReader(InputStreamReader(process.errorStream))
                .use { it.readText().trim() }

            val exitCode = process.waitFor()

            Log.d(TAG, "<<< exit=$exitCode stdout=${stdout.take(200)}")

            val result = parseResponse(exitCode, stdout, stderr, method)
            cont.resume(result)
        } catch (e: Exception) {
            process?.destroy()
            cont.resume(PrivctlResult.UnexpectedError(e))
        }
    }

    private fun parseResponse(
        exitCode: Int,
        stdout: String,
        stderr: String,
        method: String
    ): PrivctlResult {
        // su denied
        if (exitCode == SU_DENIED_EXIT_CODE ||
            stderr.contains("permission denied", ignoreCase = true) ||
            stderr.contains("not found", ignoreCase = true) && stderr.contains("su")
        ) {
            return PrivctlResult.RootDenied(stderr.ifBlank { "su exited with code $exitCode" })
        }

        // privctl binary missing
        if (stderr.contains("not found", ignoreCase = true) &&
            stderr.contains("privctl", ignoreCase = true)
        ) {
            return PrivctlResult.DaemonNotFound(privctlPath)
        }

        // No output at all
        if (stdout.isBlank()) {
            return if (exitCode == 0) {
                // Some void commands (stop) may produce no output on success
                PrivctlResult.Success(buildJsonObject { put("ok", true) })
            } else {
                PrivctlResult.Error(
                    code = exitCode,
                    message = stderr.ifBlank { "privctl exited with code $exitCode and no output" }
                )
            }
        }

        // Try parsing JSON-RPC response
        val jsonElement: JsonElement = try {
            json.parseToJsonElement(stdout)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to parse stdout as JSON: ${stdout.take(100)}", e)
            return PrivctlResult.Error(
                code = -32700,
                message = "Invalid JSON from daemon: ${e.message}"
            )
        }

        val obj = try {
            jsonElement.jsonObject
        } catch (e: Exception) {
            return PrivctlResult.Error(
                code = -32700,
                message = "Expected JSON object, got: ${jsonElement::class.simpleName}"
            )
        }

        // JSON-RPC error field present
        val errorObj = obj["error"]
        if (errorObj != null) {
            return try {
                val errJson = errorObj.jsonObject
                PrivctlResult.Error(
                    code = errJson["code"]?.jsonPrimitive?.int ?: -1,
                    message = errJson["message"]?.jsonPrimitive?.content ?: "Unknown daemon error",
                    details = errJson["data"]
                )
            } catch (e: Exception) {
                PrivctlResult.Error(
                    code = -1,
                    message = errorObj.toString()
                )
            }
        }

        // JSON-RPC result field
        val resultField = obj["result"]
        if (resultField != null) {
            return PrivctlResult.Success(resultField)
        }

        // Fallback: treat the entire object as the result
        return PrivctlResult.Success(jsonElement)
    }
}

/** Convenience alias for an empty JsonObject. */
fun emptyJsonObject(): JsonObject = JsonObject(emptyMap())
