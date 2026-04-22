package com.privstack.panel.`import`

import android.util.Base64
import android.util.Log
import com.privstack.panel.model.Node
import com.privstack.panel.model.Protocol
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.nio.ByteBuffer
import java.util.UUID
import java.util.zip.Inflater
import java.util.zip.InflaterInputStream

/**
 * Handles the Amnezia `vpn://` format.
 *
 * Payload structure:
 * ```
 * vpn://<base64url-payload>
 * ```
 *
 * Decoding steps:
 * 1. Base64-URL decode the payload.
 * 2. Strip the 4-byte qCompress header (big-endian uncompressed size).
 * 3. zlib decompress the remainder.
 * 4. Parse the resulting JSON.
 * 5. Extract an xray outbound from `containers[].xray.outbounds[0]`.
 * 6. Convert to [Node].
 */
object AmneziaImporter {

    private const val TAG = "AmneziaImporter"

    private val json = Json { ignoreUnknownKeys = true }

    /**
     * Import a single `vpn://` URI into a [Node], or null on failure.
     */
    fun import(uri: String): Node? {
        return try {
            val payload = if (uri.length >= 6 && uri.substring(0, 6).equals("vpn://", ignoreCase = true)) {
                uri.substring(6)
            } else {
                uri
            }
            if (payload.isBlank()) return null

            val raw = base64UrlDecode(payload) ?: run {
                Log.w(TAG, "base64url decode failed")
                return null
            }

            val decompressed = qDecompress(raw) ?: run {
                Log.w(TAG, "zlib decompress failed")
                return null
            }

            val jsonStr = String(decompressed, Charsets.UTF_8)
            parseAmneziaJson(jsonStr, uri)
        } catch (e: Exception) {
            Log.w(TAG, "Amnezia import failed: ${e.message}")
            null
        }
    }

    /**
     * Parse already-decompressed Amnezia JSON and produce a [Node].
     *
     * The JSON schema typically looks like:
     * ```json
     * {
     *   "containers": [
     *     {
     *       "container": "amnezia-xray",
     *       "xray": {
     *         "outbounds": [ { ...xray outbound... } ],
     *         ...
     *       }
     *     }
     *   ],
     *   "dns1": "...",
     *   "dns2": "...",
     *   "hostName": "...",
     *   "description": "..."
     * }
     * ```
     */
    internal fun parseAmneziaJson(jsonStr: String, originalUri: String): Node? {
        val root = try {
            json.parseToJsonElement(jsonStr).jsonObject
        } catch (e: Exception) {
            Log.w(TAG, "JSON parse failed: ${e.message}")
            return null
        }

        val containers = root["containers"]?.jsonArray ?: run {
            Log.w(TAG, "No 'containers' array in Amnezia JSON")
            return null
        }

        // Look for the first container that has an xray config with outbounds.
        for (container in containers) {
            val obj = container.jsonObject
            val xrayConfig = obj["xray"]?.jsonObject ?: continue
            val outbounds = xrayConfig["outbounds"]?.jsonArray ?: continue
            if (outbounds.isEmpty()) continue

            val outbound = outbounds[0].jsonObject

            val protocolStr = outbound["protocol"]?.jsonPrimitive?.content ?: continue
            val protocol = Protocol.fromString(protocolStr) ?: continue

            // Extract server + port from the outbound.
            val (server, port) = extractServerPort(outbound, protocol) ?: continue

            // Build node name from description or hostName.
            val description = root["description"]?.jsonPrimitive?.content
            val hostName = root["hostName"]?.jsonPrimitive?.content
            val name = description?.takeIf { it.isNotEmpty() }
                ?: hostName?.takeIf { it.isNotEmpty() }
                ?: "$server:$port"

            return Node(
                id = UUID.randomUUID().toString(),
                name = name,
                protocol = protocol,
                server = server,
                port = port,
                link = originalUri,
                outbound = outbound
            )
        }

        Log.w(TAG, "No usable xray outbound found in Amnezia containers")
        return null
    }

    /**
     * Extract (server, port) from an xray outbound [JsonObject] based on [protocol].
     */
    private fun extractServerPort(outbound: JsonObject, protocol: Protocol): Pair<String, Int>? {
        val settings = outbound["settings"]?.jsonObject ?: return null

        return when (protocol) {
            Protocol.VLESS, Protocol.VMESS -> {
                val vnext = settings["vnext"]?.jsonArray ?: return null
                if (vnext.isEmpty()) return null
                val first = vnext[0].jsonObject
                val address = first["address"]?.jsonPrimitive?.content ?: return null
                val port = first["port"]?.jsonPrimitive?.content?.toIntOrNull() ?: return null
                address to port
            }
            Protocol.TROJAN, Protocol.SHADOWSOCKS -> {
                val servers = settings["servers"]?.jsonArray ?: return null
                if (servers.isEmpty()) return null
                val first = servers[0].jsonObject
                val address = first["address"]?.jsonPrimitive?.content ?: return null
                val port = first["port"]?.jsonPrimitive?.content?.toIntOrNull() ?: return null
                address to port
            }
            Protocol.SOCKS -> {
                val address = settings["address"]?.jsonPrimitive?.content ?: return null
                val port = settings["port"]?.jsonPrimitive?.content?.toIntOrNull() ?: return null
                address to port
            }
            else -> null
        }
    }

    // ---------- decompression ----------

    /**
     * Base64-URL decode (with automatic padding repair).
     */
    private fun base64UrlDecode(input: String): ByteArray? {
        val cleaned = input.trim()
            .replace('-', '+')
            .replace('_', '/')

        val padded = when (cleaned.length % 4) {
            2 -> "$cleaned=="
            3 -> "$cleaned="
            else -> cleaned
        }

        return try {
            Base64.decode(padded, Base64.NO_WRAP)
        } catch (_: Exception) {
            try {
                Base64.decode(input.trim(), Base64.URL_SAFE or Base64.NO_WRAP)
            } catch (_: Exception) {
                null
            }
        }
    }

    /**
     * Remove the 4-byte qCompress header and zlib-decompress the rest.
     *
     * Qt's `qCompress()` prepends a 4-byte big-endian uint32 representing the uncompressed
     * size, followed by the zlib-compressed data. We strip those 4 bytes and inflate.
     */
    internal fun qDecompress(data: ByteArray): ByteArray? {
        if (data.size < 5) return null

        // First 4 bytes: big-endian uncompressed size (used as a hint).
        val expectedSize = ByteBuffer.wrap(data, 0, 4).int.toLong() and 0xFFFFFFFFL

        // Remaining bytes: zlib stream.
        val compressedData = data.copyOfRange(4, data.size)

        return try {
            zlibDecompress(compressedData, expectedSize)
        } catch (_: Exception) {
            // Fallback: maybe there's no qCompress header and it's raw zlib.
            try {
                zlibDecompress(data, 0)
            } catch (_: Exception) {
                null
            }
        }
    }

    /**
     * Decompress a zlib byte array.
     */
    private fun zlibDecompress(data: ByteArray, sizeHint: Long): ByteArray {
        val inflater = Inflater()
        return try {
            inflater.setInput(data)
            val buffer = ByteArrayOutputStream(
                if (sizeHint in 1..10_000_000) sizeHint.toInt() else 4096
            )
            val chunk = ByteArray(4096)
            while (!inflater.finished()) {
                val count = inflater.inflate(chunk)
                if (count == 0 && inflater.needsInput()) break
                buffer.write(chunk, 0, count)
            }
            buffer.toByteArray()
        } finally {
            inflater.end()
        }
    }
}
