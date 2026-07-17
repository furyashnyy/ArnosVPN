package com.arnosvpn.android

import android.annotation.SuppressLint
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.Handler
import android.os.HandlerThread
import android.os.ParcelFileDescriptor
import android.util.Log
import com.arnosvpn.android.protocol.Crypto
import com.arnosvpn.android.protocol.Fingerprint
import com.arnosvpn.android.protocol.Profile
import com.arnosvpn.android.protocol.Session
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.json.JSONObject
import java.io.FileInputStream
import java.io.FileOutputStream
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/**
 * ArnosVpnService establishes the local TUN interface and bridges it to the
 * ArnosVPN server over a WSS tunnel. It stays connected: an application-level
 * keepalive (data frames, which proxies count as activity) prevents idle
 * timeouts, and any drop triggers an automatic reconnect with backoff. When the
 * server re-assigns the same tunnel IP the TUN device is kept, so reconnects are
 * seamless.
 */
class ArnosVpnService : VpnService() {

    companion object {
        const val ACTION_CONNECT = "com.arnosvpn.android.CONNECT"
        const val ACTION_DISCONNECT = "com.arnosvpn.android.DISCONNECT"
        const val ACTION_STATE = "com.arnosvpn.android.STATE"
        const val EXTRA_STATE = "state"
        const val EXTRA_DETAIL = "detail"

        const val STATE_CONNECTING = "connecting"
        const val STATE_CONNECTED = "connected"
        const val STATE_DISCONNECTED = "disconnected"
        const val STATE_ERROR = "error"

        private const val TAG = "ArnosVpn"
        private const val CHANNEL_ID = "arnosvpn"
        private const val NOTIFICATION_ID = 42

        private const val KEEPALIVE_MS = 15_000L
        private const val RECONNECT_MIN_MS = 2_000L
        private const val RECONNECT_MAX_MS = 30_000L
    }

    /** Link bundles the AEAD session with its socket so uplink never mixes a new
     *  socket with an old session (which the server could not decrypt). */
    private class Link(val session: Session, val ws: WebSocket)

    private val running = AtomicBoolean(false)
    @Volatile private var userStopped = false

    private var httpClient: OkHttpClient? = null
    private var profile: Profile? = null

    @Volatile private var link: Link? = null
    @Volatile private var connGen = 0
    @Volatile private var reconnectDelay = RECONNECT_MIN_MS

    private var tun: ParcelFileDescriptor? = null
    @Volatile private var tunOut: FileOutputStream? = null
    @Volatile private var currentIp: String? = null
    @Volatile private var uplinkGen = 0
    private var uplink: Thread? = null

    private lateinit var ctlThread: HandlerThread
    private lateinit var ctl: Handler

    override fun onCreate() {
        super.onCreate()
        ctlThread = HandlerThread("arnos-ctl").apply { start() }
        ctl = Handler(ctlThread.looper)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_DISCONNECT) {
            stopTunnel(STATE_DISCONNECTED, null)
            return START_NOT_STICKY
        }

        val p = ProfileStore(this).load()
        if (p == null) {
            broadcast(STATE_ERROR, "No profile configured")
            stopSelf()
            return START_NOT_STICKY
        }
        profile = p

        if (running.getAndSet(true)) return START_STICKY // already up

        userStopped = false
        VpnRuntime.resetCounters()
        VpnRuntime.since = 0
        httpClient = OkHttpClient.Builder()
            .pingInterval(20, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .retryOnConnectionFailure(true)
            .build()

        startForeground(NOTIFICATION_ID, notification("Connecting…"))
        broadcast(STATE_CONNECTING, p.host)
        ctl.post { connectWebSocket() }
        scheduleKeepAlive()
        return START_STICKY
    }

    // --- connection ---------------------------------------------------------

    private fun connectWebSocket() {
        if (userStopped || !running.get()) return
        val p = profile ?: return
        val gen = ++connGen
        val clientSalt = Crypto.randomBytes(Crypto.SALT_LEN)
        val ts = System.currentTimeMillis() / 1000
        val hello = JSONObject()
            .put("type", "hello").put("v", 2)
            .put("salt", Crypto.b64(clientSalt)).put("ts", ts)
            .put("auth", Crypto.computeAuth(p.psk, clientSalt, ts))
            .put("name", Build.MODEL ?: "android")
            .toString()

        // Browser-shaped upgrade so CDNs/WAFs return 101 rather than 403.
        val request = Request.Builder()
            .url(p.wsUrl(Fingerprint.randomPath()))
            .header("Origin", "https://${p.host}")
            .header("User-Agent", Fingerprint.randomUserAgent())
            .header("Accept-Language", "en-US,en;q=0.9")
            .header("Accept-Encoding", "gzip, deflate, br")
            .header("Cache-Control", "no-cache")
            .header("Sec-Fetch-Dest", "websocket")
            .header("Sec-Fetch-Mode", "websocket")
            .header("Sec-Fetch-Site", "same-origin")
            .build()

        httpClient?.newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(ws: WebSocket, response: Response) {
                if (gen == connGen) ws.send(hello)
            }

            override fun onMessage(ws: WebSocket, text: String) {
                if (gen == connGen) handleControl(ws, p, clientSalt, text)
            }

            override fun onMessage(ws: WebSocket, bytes: ByteString) {
                val l = link ?: return
                if (ws !== l.ws) return
                try {
                    val pkt = l.session.open(bytes.toByteArray())
                    tunOut?.write(pkt)
                    VpnRuntime.addRx(pkt.size.toLong())
                } catch (e: Exception) {
                    Log.w(TAG, "downlink failed", e)
                }
            }

            override fun onFailure(ws: WebSocket, t: Throwable, response: Response?) {
                if (gen == connGen) scheduleReconnect("failure ${response?.code ?: ""}: ${t.message}")
            }

            override fun onClosed(ws: WebSocket, code: Int, reason: String) {
                if (gen == connGen) scheduleReconnect("closed: $reason")
            }
        })
    }

    private fun handleControl(ws: WebSocket, p: Profile, clientSalt: ByteArray, text: String) {
        val obj = JSONObject(text)
        when (obj.optString("type")) {
            "welcome" -> {
                val serverSalt = android.util.Base64.decode(obj.getString("salt"), android.util.Base64.NO_WRAP)
                val session = Crypto.deriveSession(p.psk, clientSalt, serverSalt)
                if (!ensureTun(obj)) return
                link = Link(session, ws) // atomic swap: uplink uses matching pair
                reconnectDelay = RECONNECT_MIN_MS
                updateNotification("Connected · ${currentIp}")
                broadcast(STATE_CONNECTED, currentIp)
                Log.i(TAG, "connected ip=${currentIp}")
            }
            // A server "error" is terminal (bad auth/version) — do not loop.
            "error" -> stopTunnel(STATE_ERROR, obj.optString("msg", "rejected by server"))
            "pong" -> {}
        }
    }

    /** ensureTun builds the TUN device when needed. Kept across reconnects when
     *  the assigned IP is unchanged, so the reconnect is seamless. */
    private fun ensureTun(welcome: JSONObject): Boolean {
        val ip = welcome.getString("ip")
        if (tun != null && ip == currentIp) return true // reuse existing device

        val mask = welcome.getInt("mask")
        val mtu = welcome.optInt("mtu", 1400)
        val builder = Builder().setSession("ArnosVPN").setMtu(mtu)
            .addAddress(ip, mask).addRoute("0.0.0.0", 0)

        val st = SettingsStore(this).read()
        applyDns(builder, welcome, st)
        applySplitApps(builder, st)
        applyDomainRules(builder, st)

        val fd = builder.establish()
        if (fd == null) {
            stopTunnel(STATE_ERROR, "VPN permission not granted")
            return false
        }
        try { tun?.close() } catch (_: Exception) {}
        tun = fd
        tunOut = FileOutputStream(fd.fileDescriptor)
        currentIp = ip
        startUplink(fd, ++uplinkGen)
        return true
    }

    /** applyDns uses the user's DNS override when set, else the server's. */
    private fun applyDns(builder: Builder, welcome: org.json.JSONObject, st: org.json.JSONObject) {
        val custom = st.optString("dns", "").split(",").map { it.trim() }.filter { it.isNotEmpty() }
        if (custom.isNotEmpty()) {
            custom.forEach { runCatching { builder.addDnsServer(it) } }
            return
        }
        val dns = welcome.optJSONArray("dns") ?: return
        for (i in 0 until dns.length()) runCatching { builder.addDnsServer(dns.getString(i)) }
    }

    /** applySplitApps implements per-app VPN routing (split tunneling). Allowed
     *  and disallowed lists can't be mixed, so each mode takes one path. */
    private fun applySplitApps(builder: Builder, st: org.json.JSONObject) {
        val mode = st.optString("splitMode", "off")
        val apps = st.optJSONArray("splitApps")
        val list = (0 until (apps?.length() ?: 0)).mapNotNull { apps?.optString(it) }.filter { it.isNotEmpty() }
        if (mode == "allowed" && list.isNotEmpty()) {
            // Only the chosen apps are tunneled (ours stays out automatically).
            list.forEach { runCatching { builder.addAllowedApplication(it) } }
            return
        }
        if (mode == "disallowed") {
            // Everything is tunneled except the chosen apps.
            list.forEach { runCatching { builder.addDisallowedApplication(it) } }
        }
        // Default / fallback: tunnel everything except our own app.
        runCatching { builder.addDisallowedApplication(packageName) }
    }

    /** applyDomainRules routes "direct" domains around the VPN by excluding their
     *  resolved IPs from the tunnel (Android 13+; older versions tunnel all).
     *  IpPrefix has no guaranteed-public constructor in the SDK stubs, so it is
     *  built reflectively — this always compiles and simply no-ops if unavailable. */
    @SuppressLint("NewApi")
    private fun applyDomainRules(builder: Builder, st: org.json.JSONObject) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val rules = st.optJSONArray("domainRules") ?: return
        val ctor = runCatching {
            android.net.IpPrefix::class.java.getConstructor(
                java.net.InetAddress::class.java, Int::class.javaPrimitiveType,
            )
        }.getOrNull() ?: return
        for (i in 0 until rules.length()) {
            val r = rules.optJSONObject(i) ?: continue
            if (r.optString("action") != "direct") continue
            val host = r.optString("host")
            if (host.isEmpty()) continue
            runCatching {
                for (addr in java.net.InetAddress.getAllByName(host)) {
                    val prefixLen = if (addr is java.net.Inet4Address) 32 else 128
                    val prefix = ctor.newInstance(addr, prefixLen) as android.net.IpPrefix
                    builder.excludeRoute(prefix)
                }
            }
        }
    }

    /** startUplink pumps packets from the TUN device into the current link. */
    private fun startUplink(fd: ParcelFileDescriptor, gen: Int) {
        val input = FileInputStream(fd.fileDescriptor)
        uplink = Thread {
            val buf = ByteArray(65535)
            try {
                while (running.get() && gen == uplinkGen) {
                    val n = input.read(buf)
                    if (n <= 0) { if (n < 0) break else continue }
                    val l = link ?: continue
                    l.ws.send(l.session.seal(buf, 0, n).toByteString())
                    VpnRuntime.addTx(n.toLong())
                }
            } catch (e: Exception) {
                if (running.get() && gen == uplinkGen) Log.w(TAG, "uplink ended", e)
            }
        }.apply { name = "arnos-uplink-$gen"; isDaemon = true; start() }
    }

    private fun scheduleReconnect(reason: String) {
        if (userStopped || !running.get()) return
        link = null
        val delay = reconnectDelay
        reconnectDelay = (reconnectDelay * 2).coerceAtMost(RECONNECT_MAX_MS)
        Log.w(TAG, "reconnecting in ${delay}ms ($reason)")
        broadcast(STATE_CONNECTING, "reconnecting…")
        updateNotification("Reconnecting…")
        ctl.postDelayed({ connectWebSocket() }, delay)
    }

    private fun scheduleKeepAlive() {
        ctl.postDelayed(object : Runnable {
            override fun run() {
                if (!running.get()) return
                // Application-level ping as a TEXT data frame: proxies reset
                // their idle timers on data (not always on WS control pings).
                try { link?.ws?.send("{\"type\":\"ping\"}") } catch (_: Exception) {}
                ctl.postDelayed(this, KEEPALIVE_MS)
            }
        }, KEEPALIVE_MS)
    }

    private fun stopTunnel(state: String, detail: String?) {
        userStopped = true
        if (!running.getAndSet(false)) {
            stopForegroundCompat(); stopSelf(); return
        }
        ctl.removeCallbacksAndMessages(null)
        try { link?.ws?.close(1000, "bye") } catch (_: Exception) {}
        link = null
        uplinkGen++
        try { uplink?.interrupt() } catch (_: Exception) {}
        try { tun?.close() } catch (_: Exception) {}
        httpClient?.dispatcher?.executorService?.shutdown()
        tun = null; tunOut = null; currentIp = null
        broadcast(state, detail)
        stopForegroundCompat()
        stopSelf()
    }

    override fun onDestroy() {
        stopTunnel(STATE_DISCONNECTED, null)
        if (::ctlThread.isInitialized) ctlThread.quitSafely()
        super.onDestroy()
    }

    override fun onRevoke() {
        stopTunnel(STATE_DISCONNECTED, "revoked")
        super.onRevoke()
    }

    // --- notifications & status ---------------------------------------------

    private fun broadcast(state: String, detail: String?) {
        // Keep the process-wide snapshot (read by the WebView control bridge) and
        // the log feed in sync with every state transition.
        when (state) {
            STATE_CONNECTING -> {
                VpnRuntime.connecting = true; VpnRuntime.connected = false; VpnRuntime.error = null
                VpnRuntime.addLog(if (detail == "reconnecting…") "connection lost — reconnecting" else "connecting to ${detail ?: profile?.host}")
            }
            STATE_CONNECTED -> {
                VpnRuntime.connecting = false; VpnRuntime.connected = true; VpnRuntime.error = null
                VpnRuntime.ip = detail
                if (VpnRuntime.since == 0L) VpnRuntime.since = System.currentTimeMillis() / 1000
                VpnRuntime.addLog("connected, assigned ${detail ?: "?"}")
            }
            STATE_ERROR -> {
                VpnRuntime.connecting = false; VpnRuntime.connected = false; VpnRuntime.error = detail
                VpnRuntime.addLog("error: ${detail ?: "unknown"}")
            }
            else -> { // disconnected
                VpnRuntime.connecting = false; VpnRuntime.connected = false
                VpnRuntime.ip = null; VpnRuntime.since = 0
                VpnRuntime.addLog("disconnected")
            }
        }
        sendBroadcast(
            Intent(ACTION_STATE).setPackage(packageName)
                .putExtra(EXTRA_STATE, state).putExtra(EXTRA_DETAIL, detail),
        )
        // Keep the Quick Settings tile in step with every transition.
        VpnTileService.requestUpdate(this)
    }

    private fun notification(text: String): Notification {
        ensureChannel()
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        // A one-tap "Disconnect" straight from the notification, so the tunnel can
        // be dropped without opening the app.
        val stop = PendingIntent.getService(
            this, 1, Intent(this, ArnosVpnService::class.java).setAction(ACTION_DISCONNECT),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val stopAction = Notification.Action.Builder(
            android.graphics.drawable.Icon.createWithResource(this, R.drawable.ic_tile_shield),
            getString(R.string.notif_disconnect),
            stop,
        ).build()
        val builder = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this)
        }
        return builder
            .setContentTitle("ArnosVPN")
            .setContentText(text)
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(stopAction)
            .build()
    }

    private fun updateNotification(text: String) {
        getSystemService(NotificationManager::class.java).notify(NOTIFICATION_ID, notification(text))
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val nm = getSystemService(NotificationManager::class.java)
            if (nm.getNotificationChannel(CHANNEL_ID) == null) {
                nm.createNotificationChannel(
                    NotificationChannel(CHANNEL_ID, "ArnosVPN", NotificationManager.IMPORTANCE_LOW),
                )
            }
        }
    }

    private fun stopForegroundCompat() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } else {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
    }
}
