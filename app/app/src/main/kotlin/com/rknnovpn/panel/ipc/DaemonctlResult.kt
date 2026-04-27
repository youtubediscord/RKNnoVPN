package com.rknnovpn.panel.ipc

import kotlinx.serialization.json.JsonElement

/**
 * Sealed result type for every `daemonctl` invocation.
 *
 * Every IPC call returns exactly one of these variants. Callers should
 * exhaustively `when`-match to handle all failure modes at the call site.
 */
sealed class DaemonctlResult {

    /**
     * The daemon returned a successful JSON-RPC response.
     * [data] contains the typed daemon envelope result.
     */
    data class Success(
        val data: JsonElement,
        val envelope: JsonElement? = null,
    ) : DaemonctlResult()

    /**
     * The daemon returned a JSON-RPC error response.
     * [code] and [message] mirror the JSON-RPC error object fields.
     */
    data class Error(
        val code: Int,
        val message: String,
        val details: JsonElement? = null,
        val envelope: JsonElement? = null,
    ) : DaemonctlResult() {
        override fun toString(): String = "DaemonctlError($code: $message)"
    }

    /**
     * The `su` command was denied or not available.
     * The device either has no root, or the user denied the superuser prompt.
     */
    data class RootDenied(
        val reason: String = "Superuser access was denied or unavailable"
    ) : DaemonctlResult()

    /**
     * The command did not complete within the allowed timeout.
     * [timeoutMs] records the limit that was exceeded.
     */
    data class Timeout(
        val timeoutMs: Long,
        val method: String
    ) : DaemonctlResult() {
        override fun toString(): String =
            "DaemonctlTimeout(method=$method, limit=${timeoutMs}ms)"
    }

    /**
     * The daemon is not installed or its binary was not found at the expected path.
     */
    data class DaemonNotFound(
        val path: String
    ) : DaemonctlResult()

    /**
     * An unexpected exception occurred during execution (I/O error, parse failure, etc.).
     */
    data class UnexpectedError(
        val throwable: Throwable
    ) : DaemonctlResult() {
        override fun toString(): String =
            "DaemonctlUnexpectedError(${throwable::class.simpleName}: ${throwable.message})"
    }

    // ---- convenience helpers ----

    /** True only for [Success]. */
    val isSuccess: Boolean get() = this is Success

    /** Returns [Success.data] or null for any failure variant. */
    fun dataOrNull(): JsonElement? = (this as? Success)?.data

    /**
     * Returns [Success.data] or throws [DaemonctlException] wrapping the failure.
     */
    fun dataOrThrow(): JsonElement = when (this) {
        is Success -> data
        is Error -> throw DaemonctlException("Daemon error $code: $message")
        is RootDenied -> throw DaemonctlException("Root denied: $reason")
        is Timeout -> throw DaemonctlException("Timeout after ${timeoutMs}ms on $method")
        is DaemonNotFound -> throw DaemonctlException("Daemon not found at $path")
        is UnexpectedError -> throw DaemonctlException("Unexpected: ${throwable.message}", throwable)
    }
}

/**
 * Exception wrapper for [DaemonctlResult] failures.
 * Thrown by [DaemonctlResult.dataOrThrow] so callers that prefer exceptions
 * can catch a single type.
 */
class DaemonctlException(
    message: String,
    cause: Throwable? = null
) : Exception(message, cause)
