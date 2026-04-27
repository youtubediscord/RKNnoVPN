package com.rknnovpn.panel.ipc

import android.util.Log
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.int
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import java.io.BufferedReader
import java.io.IOException
import java.io.InputStream
import java.io.InputStreamReader
import java.io.InterruptedIOException
import java.nio.charset.StandardCharsets
import java.util.concurrent.TimeUnit
import javax.inject.Inject
import javax.inject.Singleton
import kotlin.concurrent.thread
import kotlin.coroutines.resume

/**
 * Low-level executor that shells out to the `privctl` binary via `su`.
 *
 * Every call:
 * 1. Builds a JSON-RPC-style request for `privctl <method>`
 * 2. Streams optional JSON params via stdin to avoid argv length limits
 * 3. Runs it under `su -c "..."`
 * 4. Captures stdout, parses as JSON
 * 4. Maps the response to [PrivctlResult]
 *
 * Thread-safety: all calls are dispatched on [Dispatchers.IO].
 * Timeout default: 5 000 ms, configurable per-call.
 */
@Singleton
class PrivctlExecutor @Inject constructor() {

    private val privctlPath = "/data/adb/rknnovpn/bin/privctl"

    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }

    companion object {
        private const val TAG = "PrivctlExecutor"
        private const val DEFAULT_TIMEOUT_MS = 5_000L
        private const val INLINE_PARAMS_LIMIT = 16 * 1024

        /** Exit code returned by `su` when the user denies the superuser prompt. */
        private const val SU_DENIED_EXIT_CODE = 13
    }

    /**
     * Execute a single privctl JSON-RPC method.
     *
     * @param method  The method name (e.g. "backend.status", "profile.get").
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
            val paramsJson = params.toString()
            val useStdin = params.isNotEmpty() &&
                paramsJson.toByteArray(StandardCharsets.UTF_8).size > INLINE_PARAMS_LIMIT
            val commandString = when {
                params.isEmpty() -> "$privctlPath $method"
                useStdin -> "RKNNOVPN_STDIN_PARAMS=1 $privctlPath $method"
                else -> "$privctlPath $method ${shellQuote(paramsJson)}"
            }
            val command = arrayOf(
                "su", "-c", commandString
            )

            Log.d(TAG, ">>> su -c \"$commandString\"")

            process = Runtime.getRuntime().exec(command)

            cont.invokeOnCancellation {
                terminateProcess(process, "cancelled")
            }

            writeParamsSafely(process, if (useStdin) paramsJson else null)

            var stdout = ""
            var stderr = ""
            val stdoutReader = thread(start = true, name = "privctl-stdout") {
                stdout = readStreamSafely(process.inputStream, "stdout")
            }
            val stderrReader = thread(start = true, name = "privctl-stderr") {
                stderr = readStreamSafely(process.errorStream, "stderr")
            }

            val exitCode = process.waitFor()
            stdoutReader.join()
            stderrReader.join()

            Log.d(TAG, "<<< exit=$exitCode stdout=${stdout.take(200)}")

            val result = parseResponse(exitCode, stdout, stderr, method)
            if (cont.isActive) {
                cont.resume(result)
            }
        } catch (e: Exception) {
            terminateProcess(process, "failed")
            if (cont.isActive) {
                cont.resume(PrivctlResult.UnexpectedError(e))
            }
        }
    }

    private fun terminateProcess(process: Process?, reason: String) {
        if (process == null) return
        try {
            process.outputStream.close()
        } catch (_: IOException) {
        }
        try {
            process.inputStream.close()
        } catch (_: IOException) {
        }
        try {
            process.errorStream.close()
        } catch (_: IOException) {
        }
        try {
            process.destroy()
            if (!process.waitFor(300, TimeUnit.MILLISECONDS)) {
                Log.w(TAG, "privctl process still alive after $reason; forcing kill")
                process.destroyForcibly()
                process.waitFor(1, TimeUnit.SECONDS)
            }
        } catch (e: Exception) {
            Log.d(TAG, "privctl process cleanup after $reason failed: ${e.message}")
        }
    }

    private fun writeParamsSafely(process: Process, paramsJson: String?) {
        process.outputStream.use { output ->
            if (!paramsJson.isNullOrEmpty()) {
                output.write(paramsJson.toByteArray(StandardCharsets.UTF_8))
                output.write('\n'.code)
                output.flush()
            }
        }
    }

    private fun readStreamSafely(stream: InputStream, streamName: String): String {
        return try {
            BufferedReader(InputStreamReader(stream)).use { it.readText().trim() }
        } catch (e: InterruptedIOException) {
            Log.d(TAG, "privctl $streamName reader interrupted")
            ""
        } catch (e: IOException) {
            Log.d(TAG, "privctl $streamName reader closed: ${e.message}")
            ""
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

        // Old privctl binary that doesn't know the requested command.
        // It prints "error: unknown command ..." to stderr and exits 1.
        // Detect this before trying to parse stdout (which may contain
        // the usage text and fail JSON parsing).
        if (exitCode != 0 &&
            stderr.contains("unknown command", ignoreCase = true)
        ) {
            return PrivctlResult.Error(
                code = -32601, // MethodNotFound (JSON-RPC standard)
                message = "method not found: $method (privctl does not support this command)"
            )
        }

        // No output at all. New daemons always return a typed IPC envelope;
        // a silent success would make mutating operations look safer than they are.
        if (stdout.isBlank()) {
            return PrivctlResult.Error(
                code = if (exitCode == 0) -32600 else exitCode,
                message = stderr.ifBlank {
                    "Daemon response for $method is missing the typed IPC envelope"
                },
            )
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

        obj.daemonEnvelopeOrNull()?.let { envelope ->
            return resultFromEnvelope(envelope)
        }

        // JSON-RPC error field present. Error responses must carry the typed
        // daemon envelope in error.data so callers can inspect stable details
        // such as configSaved/runtimeApplied/activeOperation.
        val errorObj = obj["error"]
        if (errorObj != null) {
            return try {
                val errJson = errorObj.jsonObject
                val envelope = errJson["data"]?.daemonEnvelopeOrNull()
                    ?: return PrivctlResult.Error(
                        code = errJson["code"]?.jsonPrimitive?.int ?: -32600,
                        message = "Daemon error for $method is missing the typed IPC envelope",
                        details = errJson["data"],
                    )
                val envelopeError = envelope?.get("error")?.jsonObject
                PrivctlResult.Error(
                    code = errJson["code"]?.jsonPrimitive?.int ?: -1,
                    message = envelopeError?.get("message")?.jsonPrimitive?.contentOrNull
                        ?: errJson["message"]?.jsonPrimitive?.content
                        ?: "Unknown daemon error",
                    details = envelopeError?.get("details") ?: errJson["data"],
                    envelope = envelope,
                )
            } catch (e: Exception) {
                PrivctlResult.Error(
                    code = -1,
                    message = errorObj.toString()
                )
            }
        }

        if (exitCode != 0) {
            return PrivctlResult.Error(
                code = exitCode,
                message = stderr.ifBlank { "privctl exited with code $exitCode" },
                details = jsonElement
            )
        }

        // JSON-RPC result field
        val resultField = obj["result"]
        if (resultField != null) {
            val envelope = resultField.daemonEnvelopeOrNull()
            if (envelope != null) {
                return resultFromEnvelope(envelope)
            }
            return PrivctlResult.Error(
                code = -32600,
                message = "Daemon result for $method is missing the typed IPC envelope",
                details = resultField,
            )
        }

        return PrivctlResult.Error(
            code = -32600,
            message = "Daemon response for $method is missing result/error typed IPC envelope",
            details = jsonElement,
        )
    }

    private fun shellQuote(value: String): String {
        if (value.isEmpty()) return "''"
        return "'" + value.replace("'", "'\"'\"'") + "'"
    }

    private fun JsonElement.daemonEnvelopeOrNull(): JsonObject? {
        val obj = runCatching { jsonObject }.getOrNull() ?: return null
        return obj.daemonEnvelopeOrNull()
    }

    private fun JsonObject.daemonEnvelopeOrNull(): JsonObject? {
        val hasEnvelopeStatus = this["ok"]?.jsonPrimitive?.booleanOrNull != null
        val hasEnvelopePayload = containsKey("result") || containsKey("error")
        return if (hasEnvelopeStatus && hasEnvelopePayload) this else null
    }

    private fun resultFromEnvelope(envelope: JsonObject): PrivctlResult {
        val ok = envelope["ok"]?.jsonPrimitive?.booleanOrNull ?: true
        return if (ok) {
            PrivctlResult.Success(envelope["result"] ?: JsonNull, envelope)
        } else {
            val envelopeError = envelope["error"]?.jsonObject
            PrivctlResult.Error(
                code = envelopeError?.get("rpcCode")?.jsonPrimitive?.intOrNull ?: -1,
                message = envelopeError?.get("message")?.jsonPrimitive?.contentOrNull
                    ?: "Unknown daemon error",
                details = envelopeError?.get("details"),
                envelope = envelope,
            )
        }
    }

}

/** Convenience alias for an empty JsonObject. */
fun emptyJsonObject(): JsonObject = JsonObject(emptyMap())
