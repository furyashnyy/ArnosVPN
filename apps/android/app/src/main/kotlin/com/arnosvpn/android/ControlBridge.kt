package com.arnosvpn.android

import android.content.Context
import android.content.Intent
import android.webkit.JavascriptInterface
import com.arnosvpn.android.protocol.Crypto
import com.arnosvpn.android.protocol.Fingerprint
import com.arnosvpn.android.protocol.Profile
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONArray
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

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

    /**
     * ping measures a real VPN handshake round-trip (TLS + WebSocket upgrade +
     * PSK auth + welcome), not a bare TCP connect. This reflects whether the
     * tunnel actually works: a wrong port, a blocked upgrade or a bad PSK shows
     * as an error instead of a misleadingly-fast TCP round-trip to the host.
     */
    private fun ping(name: String): JSONObject {
        val p = store.servers().firstOrNull { it.name == name }?.profileOrNull()
            ?: return JSONObject().put("ok", false).put("error", "unknown server")
        return try {
            JSONObject().put("ok", true).put("ms", handshakeRttMs(p))
        } catch (e: Exception) {
            JSONObject().put("ok", false).put("error", (e.message ?: "unreachable"))
        }
    }

    /** handshakeRttMs opens a one-shot WSS tunnel handshake and returns its
     *  round-trip time in ms, then closes it. Throws on any failure. */
    private fun handshakeRttMs(p: Profile): Long {
        val client = OkHttpClient.Builder()
            .connectTimeout(8, TimeUnit.SECONDS)
            .readTimeout(8, TimeUnit.SECONDS)
            .build()
        val clientSalt = Crypto.randomBytes(Crypto.SALT_LEN)
        val ts = System.currentTimeMillis() / 1000
        val hello = JSONObject()
            .put("type", "hello").put("v", 2)
            .put("salt", Crypto.b64(clientSalt)).put("ts", ts)
            .put("auth", Crypto.computeAuth(p.psk, clientSalt, ts))
            .put("name", "ping").toString()
        val request = Request.Builder()
            .url(p.wsUrl(Fingerprint.randomPath()))
            .header("Origin", "https://${p.host}")
            .header("User-Agent", Fingerprint.randomUserAgent())
            .header("Sec-Fetch-Dest", "websocket")
            .header("Sec-Fetch-Mode", "websocket")
            .header("Sec-Fetch-Site", "same-origin")
            .build()
        val latch = CountDownLatch(1)
        val result = AtomicReference<Any?>(null) // Long ms on success, Throwable on failure
        val start = System.nanoTime()
        val ws = client.newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(ws: WebSocket, response: Response) { ws.send(hello) }
            override fun onMessage(ws: WebSocket, text: String) {
                when (JSONObject(text).optString("type")) {
                    "welcome" -> {
                        result.set((System.nanoTime() - start) / 1_000_000)
                        latch.countDown(); ws.close(1000, "ping")
                    }
                    "error" -> {
                        result.set(IllegalStateException(JSONObject(text).optString("msg", "rejected")))
                        latch.countDown(); ws.close(1000, "ping")
                    }
                }
            }
            override fun onFailure(ws: WebSocket, t: Throwable, response: Response?) {
                result.set(if (response != null) IllegalStateException("HTTP ${response.code}") else t)
                latch.countDown()
            }
        })
        try {
            if (!latch.await(9, TimeUnit.SECONDS)) throw java.io.IOException("timeout")
            val r = result.get()
            if (r is Long) return r
            throw (r as? Throwable ?: IllegalStateException("ping failed"))
        } finally {
            ws.cancel()
            client.dispatcher.executorService.shutdown()
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
