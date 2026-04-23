package com.privstack.panel.controlplane

import android.content.Context
import dagger.hilt.android.qualifiers.ApplicationContext
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import javax.inject.Inject
import javax.inject.Singleton

@Singleton
class ControlPlaneClient @Inject constructor(
    @ApplicationContext private val context: Context,
) {
    private val json = Json {
        ignoreUnknownKeys = true
        isLenient = true
        coerceInputValues = true
    }

    suspend fun fetchSubscription(url: String): HttpFetchResult = withContext(Dispatchers.IO) {
        val connection = (URL(url).openConnection() as HttpURLConnection).apply {
            instanceFollowRedirects = true
            requestMethod = "GET"
            connectTimeout = 30_000
            readTimeout = 30_000
            setRequestProperty("User-Agent", "RKNnoVPN-control-plane/2.0")
        }
        try {
            val status = connection.responseCode
            val stream = if (status in 200..299) connection.inputStream else connection.errorStream
            val body = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
            val headers = connection.headerFields
                .filterKeys { it != null }
                .mapValues { (_, values) -> values.firstOrNull().orEmpty() }
            if (status !in 200..299) {
                throw IllegalStateException("HTTP $status")
            }
            HttpFetchResult(body = body, headers = headers, status = status)
        } finally {
            connection.disconnect()
        }
    }

    suspend fun checkForUpdates(currentVersion: String): ReleaseInfo = withContext(Dispatchers.IO) {
        val connection = (URL(RELEASES_URL).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 30_000
            readTimeout = 30_000
            setRequestProperty("Accept", "application/vnd.github+json")
            setRequestProperty("User-Agent", "RKNnoVPN-control-plane/2.0")
        }
        try {
            val status = connection.responseCode
            if (status !in 200..299) {
                throw IllegalStateException("GitHub API returned HTTP $status")
            }
            val body = connection.inputStream.bufferedReader().use { it.readText() }
            val release = json.decodeFromString(GitHubRelease.serializer(), body)
            release.toReleaseInfo(currentVersion)
        } finally {
            connection.disconnect()
        }
    }

    suspend fun downloadUpdate(
        release: ReleaseInfo,
        onProgress: suspend (Float) -> Unit = {},
    ): DownloadedUpdate = withContext(Dispatchers.IO) {
        val updateDir = File(context.cacheDir, "updates").apply {
            deleteRecursively()
            mkdirs()
        }

        val totalSize = (release.moduleSize + release.apkSize).coerceAtLeast(1L)
        var downloaded = 0L
        suspend fun progress(delta: Long) {
            downloaded += delta
            onProgress((downloaded.toFloat() / totalSize.toFloat()).coerceIn(0f, 1f))
        }

        var modulePath = ""
        if (release.moduleUrl.isNotBlank()) {
            val moduleFile = File(updateDir, "module.zip")
            downloadFile(release.moduleUrl, moduleFile) { delta -> progress(delta) }
            modulePath = moduleFile.absolutePath
        }

        var apkPath = ""
        if (release.apkUrl.isNotBlank()) {
            val apkFile = File(updateDir, "panel.apk")
            downloadFile(release.apkUrl, apkFile) { delta -> progress(delta) }
            apkPath = apkFile.absolutePath
        }

        var checksumsVerified = false
        if (release.checksumUrl.isNotBlank()) {
            val checksumFile = File(updateDir, "SHA256SUMS.txt")
            downloadFile(release.checksumUrl, checksumFile) { }
            checksumsVerified = verifyChecksums(checksumFile, updateDir)
            if (!checksumsVerified) {
                throw IllegalStateException("SHA256 checksum verification failed")
            }
        }

        onProgress(1f)
        DownloadedUpdate(
            modulePath = modulePath,
            apkPath = apkPath,
            checksums = checksumsVerified,
        )
    }

    private suspend fun downloadFile(
        url: String,
        destination: File,
        onChunk: suspend (Long) -> Unit,
    ) = withContext(Dispatchers.IO) {
        val connection = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"
            connectTimeout = 60_000
            readTimeout = 600_000
            instanceFollowRedirects = true
            setRequestProperty("User-Agent", "RKNnoVPN-control-plane/2.0")
        }
        try {
            val status = connection.responseCode
            if (status !in 200..299) {
                throw IllegalStateException("Download failed with HTTP $status")
            }
            destination.outputStream().buffered().use { output ->
                connection.inputStream.buffered().use { input ->
                    val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
                    while (true) {
                        val read = input.read(buffer)
                        if (read <= 0) {
                            break
                        }
                        output.write(buffer, 0, read)
                        onChunk(read.toLong())
                    }
                    output.flush()
                }
            }
        } finally {
            connection.disconnect()
        }
    }

    private fun verifyChecksums(sumFile: File, directory: File): Boolean {
        val expected = sumFile.readLines()
            .mapNotNull { line ->
                val trimmed = line.trim()
                if (trimmed.isBlank()) {
                    return@mapNotNull null
                }
                val parts = trimmed.split(Regex("\\s+"), limit = 2)
                if (parts.size != 2) {
                    return@mapNotNull null
                }
                val fileName = parts[1].trimStart('*').trim()
                fileName to parts[0].lowercase()
            }
            .toMap()

        val checks = mapOf(
            "module.zip" to File(directory, "module.zip"),
            "panel.apk" to File(directory, "panel.apk"),
        )

        for ((localName, file) in checks) {
            if (!file.exists()) {
                continue
            }
            val expectedHash = expected.entries.firstOrNull { (name, _) ->
                val lower = name.lowercase()
                (localName == "module.zip" && lower.contains("module") && lower.endsWith(".zip")) ||
                    (localName == "panel.apk" && lower.contains("panel") && lower.endsWith(".apk"))
            }?.value ?: continue
            if (sha256(file) != expectedHash) {
                return false
            }
        }
        return true
    }

    private fun sha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(DEFAULT_BUFFER_SIZE)
            while (true) {
                val read = input.read(buffer)
                if (read <= 0) {
                    break
                }
                digest.update(buffer, 0, read)
            }
        }
        return digest.digest().joinToString("") { byte -> "%02x".format(byte) }
    }

    companion object {
        private const val RELEASES_URL =
            "https://api.github.com/repos/youtubediscord/RKNnoVPN/releases/latest"
    }
}

data class HttpFetchResult(
    val body: String,
    val headers: Map<String, String>,
    val status: Int,
)

data class DownloadedUpdate(
    val modulePath: String,
    val apkPath: String,
    val checksums: Boolean,
)

data class ReleaseInfo(
    val currentVersion: String,
    val latestVersion: String,
    val hasUpdate: Boolean,
    val changelog: String,
    val moduleUrl: String = "",
    val apkUrl: String = "",
    val checksumUrl: String = "",
    val moduleSize: Long = 0L,
    val apkSize: Long = 0L,
)

@Serializable
private data class GitHubRelease(
    @SerialName("tag_name")
    val tagName: String,
    val body: String = "",
    val assets: List<GitHubAsset> = emptyList(),
) {
    fun toReleaseInfo(currentVersion: String): ReleaseInfo {
        var moduleUrl = ""
        var apkUrl = ""
        var checksumUrl = ""
        var moduleSize = 0L
        var apkSize = 0L
        for (asset in assets) {
            val lower = asset.name.lowercase()
            when {
                lower.contains("module") && lower.endsWith(".zip") -> {
                    moduleUrl = asset.browserDownloadUrl
                    moduleSize = asset.size
                }
                lower.contains("panel") && lower.endsWith(".apk") -> {
                    apkUrl = asset.browserDownloadUrl
                    apkSize = asset.size
                }
                lower == "sha256sums.txt" -> checksumUrl = asset.browserDownloadUrl
            }
        }

        return ReleaseInfo(
            currentVersion = currentVersion,
            latestVersion = tagName,
            hasUpdate = compareSemver(currentVersion, tagName),
            changelog = body,
            moduleUrl = moduleUrl,
            apkUrl = apkUrl,
            checksumUrl = checksumUrl,
            moduleSize = moduleSize,
            apkSize = apkSize,
        )
    }
}

@Serializable
private data class GitHubAsset(
    val name: String,
    @SerialName("browser_download_url")
    val browserDownloadUrl: String,
    val size: Long = 0L,
)

private fun compareSemver(currentVersion: String, latestVersion: String): Boolean {
    fun normalize(version: String): List<Int> {
        return version.trim()
            .removePrefix("v")
            .split('.')
            .map { segment -> segment.takeWhile(Char::isDigit).toIntOrNull() ?: 0 }
    }

    val current = normalize(currentVersion)
    val latest = normalize(latestVersion)
    val max = maxOf(current.size, latest.size)
    for (index in 0 until max) {
        val currentPart = current.getOrElse(index) { 0 }
        val latestPart = latest.getOrElse(index) { 0 }
        if (latestPart > currentPart) {
            return true
        }
        if (latestPart < currentPart) {
            return false
        }
    }
    return false
}
