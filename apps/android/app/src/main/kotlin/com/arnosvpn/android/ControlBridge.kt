package com.arnosvpn.android

import android.content.Context
import android.content.Intent
import android.webkit.JavascriptInterface
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.FileOutputStream
import java.net.HttpURLConnection
import java.net.InetSocketAddress
import java.net.Socket
import java.net.URL
import java.util.concurrent.Executors

/**
 * ControlBridge is the Android counterpart of the desktop client's HTTP control
 * server (internal/client/gui.go): it answers the exact same endpoints the shared
 * SPA calls, so the phone runs the identical multi-page UI with full feature
 * parity — server list, add/subscribe, ping, settings, stats and logs.
 *
 * Each request is answered with a JSON envelope {"ok":true,"data":…} or
 * {"ok":false,"error":…} that the SPA's api() helper unwraps. Work runs on a
 * background pool and is resolved back into JS by id, so slow calls (ping,
 * subscription fetch) never block the WebView. Connect/disconnect/QR are
 * delegated to the Activity, which owns VpnService consent and the camera.
 */
class ControlBridge(
    context: Context,
    private val actions: Actions,
) {
    /** Actions the bridge cannot perform itself (need the Activity/UI thread). */
    interface Actions {
        fun onConnect(mode: String)
        fun onDisconnect()
        fun onScanQR()
        /** onScanQRFromFile picks an image and decodes an ArnosVPN QR from it. */
        fun onScanQRFromFile()
        /** onInstallApk launches the system package installer for a downloaded update. */
        fun onInstallApk(file: File)
        /** resolve delivers a request's JSON envelope back to the WebView by id. */
        fun resolve(id: String, envelope: String)
    }

    private val app = context.applicationContext
    private val store = ProfileStore(app)
    private val settings = SettingsStore(app)
    private val pool = Executors.newCachedThreadPool()

    /**
     * request runs a call off the UI thread and resolves it back into JS by id.
     * WebView invokes @JavascriptInterface methods synchronously from the JS
     * thread, so doing the work here (not inline) keeps the UI responsive during
     * a ping or a subscription fetch.
     */
    @JavascriptInterface
    fun request(id: String, method: String, path: String, body: String) {
        pool.execute {
            val envelope = try {
                val data = route(method, path, body)
                JSONObject().put("ok", true).put("data", data).toString()
            } catch (e: Exception) {
                JSONObject().put("ok", false).put("error", (e.message ?: "error")).toString()
            }
            actions.resolve(id, envelope)
        }
    }

    @JavascriptInterface
    fun scanQR() = actions.onScanQR()

    @JavascriptInterface
    fun scanQRFromFile() = actions.onScanQRFromFile()

    private fun route(method: String, path: String, body: String): Any {
        val req = if (body.isBlank()) JSONObject() else JSONObject(body)
        return when {
            path == "/api/state" -> stateJson()
            path == "/api/settings" && method == "GET" -> settings.read()
            path == "/api/settings" -> if (req.length() == 0) settings.reset() else settings.write(req)
            path == "/api/add" -> { store.add(req.getString("uri")); JSONObject() }
            path == "/api/active" -> { store.setActive(req.getString("name")); JSONObject() }
            path == "/api/remove" -> { store.remove(req.getString("name")); JSONObject() }
            path == "/api/subscribe" -> subscribe(req.getString("url"))
            path == "/api/ping" -> ping(req.getString("name"))
            path == "/api/connect" -> { actions.onConnect(req.optString("mode")); JSONObject() }
            path == "/api/disconnect" -> { actions.onDisconnect(); JSONObject() }
            path == "/api/logs" -> JSONObject().put("lines", JSONArray(VpnRuntime.logs()))
            path == "/api/logs/clear" -> { VpnRuntime.clearLogs(); JSONObject() }
            path == "/api/apps" -> installedApps()
            path == "/api/version" -> versionJson()
            path == "/api/update/check" -> updateCheck()
            path == "/api/update/apply" -> updateApply()
            else -> throw IllegalArgumentException("unknown endpoint $path")
        }
    }

    private fun stateJson(): JSONObject {
        val active = store.activeName()
        val servers = JSONArray()
        for (s in store.servers()) {
            val p = s.profileOrNull()
            servers.put(
                JSONObject()
                    .put("name", s.name)
                    .put("host", p?.host ?: "")
                    .put("port", p?.port ?: 0)
                    .put("active", s.name == active),
            )
        }
        return JSONObject()
            .put("connected", VpnRuntime.connected)
            .put("connecting", VpnRuntime.connecting)
            .put("mode", settings.read().optString("mode", "tun"))
            .put("assignedIP", VpnRuntime.ip ?: "")
            .put("socks", settings.read().optString("socks", ""))
            .put("http", settings.read().optString("http", ""))
            .put("active", active ?: "")
            .put("error", VpnRuntime.error ?: "")
            .put("rx", VpnRuntime.rx())
            .put("tx", VpnRuntime.tx())
            .put("since", VpnRuntime.since)
            .put("servers", servers)
    }

    /** ping measures TCP connect latency to a server, like the desktop client. */
    private fun ping(name: String): JSONObject {
        val p = store.servers().firstOrNull { it.name == name }?.profileOrNull()
            ?: return JSONObject().put("ok", false).put("error", "unknown server")
        return try {
            val start = System.nanoTime()
            Socket().use { it.connect(InetSocketAddress(p.host, p.port), 6000) }
            val ms = (System.nanoTime() - start) / 1_000_000
            JSONObject().put("ok", true).put("ms", ms)
        } catch (e: Exception) {
            JSONObject().put("ok", false).put("error", (e.message ?: "unreachable"))
        }
    }

    /** installedApps lists launchable apps for the split-tunnel picker. */
    private fun installedApps(): JSONObject {
        val pm = app.packageManager
        val main = Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_LAUNCHER)
        val seen = HashSet<String>()
        val items = ArrayList<Pair<String, String>>() // package to label
        for (ri in pm.queryIntentActivities(main, 0)) {
            val pkg = ri.activityInfo?.packageName ?: continue
            if (pkg == app.packageName || !seen.add(pkg)) continue
            items.add(pkg to ri.loadLabel(pm).toString())
        }
        items.sortBy { it.second.lowercase() }
        val arr = JSONArray()
        for ((pkg, label) in items) arr.put(JSONObject().put("package", pkg).put("label", label))
        return JSONObject().put("apps", arr)
    }

    // --- in-app updates -------------------------------------------------------
    // Updates are pulled from the project's public GitHub Releases: check the
    // latest release, and on "Обновить" download the APK and hand it to the
    // system installer. No token is needed because the repo is public.

    private fun currentVersion(): String =
        runCatching { app.packageManager.getPackageInfo(app.packageName, 0).versionName ?: "" }
            .getOrDefault("")

    private fun versionJson(): JSONObject =
        JSONObject().put("version", currentVersion()).put("platform", "android")

    /** updateCheck reports whether a newer release than the installed one exists. */
    private fun updateCheck(): JSONObject {
        val rel = githubLatest()
        val latest = rel.optString("tag_name").removePrefix("v")
        val current = currentVersion()
        var asset = ""
        var assetName = ""
        val assets = rel.optJSONArray("assets")
        if (assets != null) {
            for (i in 0 until assets.length()) {
                val a = assets.optJSONObject(i) ?: continue
                val name = a.optString("name")
                if (name.endsWith(".apk")) {
                    asset = a.optString("browser_download_url"); assetName = name; break
                }
            }
        }
        return JSONObject()
            .put("current", current).put("latest", latest)
            .put("hasUpdate", isNewer(latest, current))
            .put("notes", rel.optString("body"))
            .put("asset", asset).put("assetName", assetName)
    }

    /** updateApply downloads the newest APK and opens the system installer. */
    private fun updateApply(): JSONObject {
        val info = updateCheck()
        if (!info.optBoolean("hasUpdate")) {
            throw IllegalStateException("Уже установлена последняя версия (${info.optString("current")})")
        }
        val url = info.optString("asset")
        if (url.isEmpty()) throw IllegalStateException("Для этой версии нет APK-файла")
        val file = downloadApk(url)
        actions.onInstallApk(file)
        return JSONObject().put("ok", true)
    }

    private fun githubLatest(): JSONObject {
        val url = URL("https://api.github.com/repos/$UPDATE_OWNER/$UPDATE_REPO/releases/latest")
        val conn = (url.openConnection() as HttpURLConnection).apply {
            connectTimeout = 15000; readTimeout = 15000
            setRequestProperty("Accept", "application/vnd.github+json")
            setRequestProperty("User-Agent", "ArnosVPN-updater")
        }
        try {
            when (conn.responseCode) {
                HttpURLConnection.HTTP_OK -> {}
                HttpURLConnection.HTTP_NOT_FOUND -> throw IllegalStateException("Релизы ещё не опубликованы")
                else -> throw IllegalStateException("Проверка обновлений не удалась: ${conn.responseCode}")
            }
            val text = conn.inputStream.use { input ->
                val out = ByteArrayOutputStream()
                val buf = ByteArray(8192); var total = 0
                while (true) {
                    val n = input.read(buf); if (n < 0) break
                    total += n; if (total > 4 shl 20) break // 4 MiB cap for JSON
                    out.write(buf, 0, n)
                }
                out.toString("UTF-8")
            }
            return JSONObject(text)
        } finally {
            conn.disconnect()
        }
    }

    private fun downloadApk(rawUrl: String): File {
        val dir = File(app.getExternalFilesDir(null), "updates").apply { mkdirs() }
        val out = File(dir, "arnosvpn-update.apk")
        val conn = (URL(rawUrl).openConnection() as HttpURLConnection).apply {
            connectTimeout = 20000; readTimeout = 120000
            instanceFollowRedirects = true // GitHub asset URLs redirect to a CDN
            setRequestProperty("User-Agent", "ArnosVPN-updater")
        }
        try {
            if (conn.responseCode != HttpURLConnection.HTTP_OK) {
                throw IllegalStateException("Скачивание не удалось: ${conn.responseCode}")
            }
            conn.inputStream.use { input ->
                FileOutputStream(out).use { fos ->
                    val buf = ByteArray(8192); var total = 0L
                    while (true) {
                        val n = input.read(buf); if (n < 0) break
                        total += n
                        if (total > 200L shl 20) throw IllegalStateException("Файл слишком большой")
                        fos.write(buf, 0, n)
                    }
                }
            }
            return out
        } finally {
            conn.disconnect()
        }
    }

    /** isNewer compares dot-separated numeric versions (1.10.0 > 1.9.0). */
    private fun isNewer(a: String, b: String): Boolean {
        val pa = parseVersion(a); val pb = parseVersion(b)
        if (pa == null || pb == null) return a.isNotEmpty() && a != b
        for (i in 0 until maxOf(pa.size, pb.size)) {
            val x = pa.getOrElse(i) { 0 }; val y = pb.getOrElse(i) { 0 }
            if (x != y) return x > y
        }
        return false
    }

    private fun parseVersion(v: String): List<Int>? {
        val s = v.trim().removePrefix("v")
        if (s.isEmpty()) return null
        return s.split(".").map { part ->
            val cut = part.indexOfFirst { it == '-' || it == '+' }
            val num = if (cut >= 0) part.substring(0, cut) else part
            num.toIntOrNull() ?: return null
        }
    }

    /** subscribe fetches a URL of arnos:// URIs (optionally base64) and adds each. */
    private fun subscribe(rawUrl: String): JSONObject {
        val text = httpGet(rawUrl)
        val body = maybeBase64(text) ?: text
        var added = 0
        for (line in body.split("\n")) {
            val t = line.trim()
            if (!t.startsWith("arnos://")) continue
            if (runCatching { store.add(t) }.isSuccess) added++
        }
        if (added == 0) throw IllegalStateException("no arnos:// entries found at that URL")
        return JSONObject().put("added", added)
    }

    private fun httpGet(rawUrl: String): String {
        val url = URL(rawUrl)
        // Only fetch subscriptions over HTTP(S), and only from public hosts, so a
        // crafted subscription URL cannot reach loopback/private addresses (SSRF).
        val scheme = url.protocol.lowercase()
        require(scheme == "http" || scheme == "https") { "subscription URL must be http(s)" }
        for (addr in java.net.InetAddress.getAllByName(url.host)) {
            require(isPublicAddress(addr)) { "refusing to connect to non-public address ${addr.hostAddress}" }
        }
        val conn = (url.openConnection() as HttpURLConnection).apply {
            connectTimeout = 15000
            readTimeout = 15000
            instanceFollowRedirects = false // redirects could bounce to internal hosts
            setRequestProperty("User-Agent", "ArnosVPN/android")
        }
        try {
            conn.inputStream.use { input ->
                val out = ByteArrayOutputStream()
                val buf = ByteArray(8192)
                var total = 0
                while (true) {
                    val n = input.read(buf)
                    if (n < 0) break
                    total += n
                    if (total > 4 shl 20) break // 4 MiB cap
                    out.write(buf, 0, n)
                }
                return out.toString("UTF-8")
            }
        } finally {
            conn.disconnect()
        }
    }

    /** isPublicAddress rejects loopback, private, link-local and wildcard IPs. */
    private fun isPublicAddress(addr: java.net.InetAddress): Boolean =
        !(addr.isLoopbackAddress || addr.isSiteLocalAddress || addr.isLinkLocalAddress ||
            addr.isAnyLocalAddress || addr.isMulticastAddress)

    private companion object {
        // Public GitHub repo the in-app updater reads releases from.
        const val UPDATE_OWNER = "furyashnyy"
        const val UPDATE_REPO = "ArnosVPN"
    }

    /** maybeBase64 decodes a whole-body base64 feed; null if already plain text. */
    private fun maybeBase64(s: String): String? {
        val t = s.trim()
        if (t.contains("arnos://")) return null
        for (flag in intArrayOf(android.util.Base64.DEFAULT, android.util.Base64.URL_SAFE)) {
            val decoded = runCatching {
                String(android.util.Base64.decode(t, flag or android.util.Base64.NO_WRAP))
            }.getOrNull()
            if (decoded != null && decoded.contains("arnos://")) return decoded
        }
        return null
    }
}
