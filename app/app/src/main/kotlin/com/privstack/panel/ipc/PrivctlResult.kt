package com.privstack.panel.ipc

import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull

/**
 * Sealed result type for every `privctl` invocation.
 *
 * Every IPC call returns exactly one of these variants. Callers should
 * exhaustively `when`-match to handle all failure modes at the call site.
 */
sealed class PrivctlResult {

    /**
     * The daemon returned a successful JSON-RPC response.
     * [data] contains the `result` field from the response; may be [JsonNull]
     * for void methods like `stop`.
     */
    data class Success(
        val data: JsonElement,
        val envelope: JsonElement? = null,
    ) : PrivctlResult()

    /**
     * The daemon returned a JSON-RPC error response.
     * [code] and [message] mirror the JSON-RPC error object fields.
     */
    data class Error(
        val code: Int,
        val message: String,
        val details: JsonElement? = null,
        val envelope: JsonElement? = null,
    ) : PrivctlResult() {
        override fun toString(): String = "PrivctlError($code: $message)"
    }

    /**
     * The `su` command was denied or not available.
     * The device either has no root, or the user denied the superuser prompt.
     */
    data class RootDenied(
        val reason: String = "Superuser access was denied or unavailable"
    ) : PrivctlResult()

    /**
     * The command did not complete within the allowed timeout.
     * [timeoutMs] records the limit that was exceeded.
     */
    data class Timeout(
        val timeoutMs: Long,
        val method: String
    ) : PrivctlResult() {
        override fun toString(): String =
            "PrivctlTimeout(method=$method, limit=${timeoutMs}ms)"
    }

    /**
     * The daemon is not installed or its binary was not found at the expected path.
     */
    data class DaemonNotFound(
        val path: String
    ) : PrivctlResult()

    /**
     * An unexpected exception occurred during execution (I/O error, parse failure, etc.).
     */
    data class UnexpectedError(
        val throwable: Throwable
    ) : PrivctlResult() {
        override fun toString(): String =
            "PrivctlUnexpectedError(${throwable::class.simpleName}: ${throwable.message})"
    }

    // ---- convenience helpers ----

    /** True only for [Success]. */
    val isSuccess: Boolean get() = this is Success

    /** Returns [Success.data] or null for any failure variant. */
    fun dataOrNull(): JsonElement? = (this as? Success)?.data

    /**
     * Returns [Success.data] or throws [PrivctlException] wrapping the failure.
     */
    fun dataOrThrow(): JsonElement = when (this) {
        is Success -> data
        is Error -> throw PrivctlException("Daemon error $code: $message")
        is RootDenied -> throw PrivctlException("Root denied: $reason")
        is Timeout -> throw PrivctlException("Timeout after ${timeoutMs}ms on $method")
        is DaemonNotFound -> throw PrivctlException("Daemon not found at $path")
        is UnexpectedError -> throw PrivctlException("Unexpected: ${throwable.message}", throwable)
    }
}

/**
 * Exception wrapper for [PrivctlResult] failures.
 * Thrown by [PrivctlResult.dataOrThrow] so callers that prefer exceptions
 * can catch a single type.
 */
class PrivctlException(
    message: String,
    cause: Throwable? = null
) : Exception(message, cause)
