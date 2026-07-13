package com.arnosvpn.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import com.arnosvpn.android.protocol.Crypto
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
 * ArnosVPN server over a WSS tunnel. All routes point into the tunnel, so every
 * app's traffic exits from the server and the device's apparent public IP
 * becomes the server's.
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
    }

    private val running = AtomicBoolean(false)
    private var tun: ParcelFileDescriptor? = null
    private var webSocket: WebSocket? = null
    private var httpClient: OkHttpClient? = null
    private var uplink: Thread? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_DISCONNECT -> {
                stopTunnel(STATE_DISCONNECTED, null)
                return START_NOT_STICKY
            }
        }

        val profile = ProfileStore(this).load()
        if (profile == null) {
            broadcast(STATE_ERROR, "No profile configured")
            stopSelf()
            return START_NOT_STICKY
        }

        if (running.getAndSet(true)) {
            return START_STICKY // already up
        }
        startForeground(NOTIFICATION_ID, notification("Connecting…"))
        broadcast(STATE_CONNECTING, profile.host)
        connect(profile)
        return START_STICKY
    }

    private fun connect(profile: Profile) {
        val clientSalt = Crypto.randomBytes(Crypto.SALT_LEN)
        val ts = System.currentTimeMillis() / 1000

        val hello = JSONObject()
            .put("type", "hello")
            .put("v", 2)
            .put("salt", Crypto.b64(clientSalt))
            .put("ts", ts)
            .put("auth", Crypto.computeAuth(profile.psk, clientSalt, ts))
            .put("name", android.os.Build.MODEL ?: "android")
            .toString()

        httpClient = OkHttpClient.Builder()
            .pingInterval(20, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .retryOnConnectionFailure(true)
            .build()

        // Present a full browser-like header set. Many CDNs/WAFs (Cloudflare in
        // particular, common on `cdn.*` hosts) answer a bare WebSocket upgrade
        // with 403 Forbidden; a real Origin + Chrome fingerprint makes the
        // handshake indistinguishable from a browser and gets the 101 upgrade.
        val request = Request.Builder()
            .url(profile.wsUrl(Fingerprint.randomPath()))
            .header("Origin", "https://${profile.host}")
            .header("User-Agent", Fingerprint.randomUserAgent())
            .header("Accept-Language", "en-US,en;q=0.9")
            .header("Accept-Encoding", "gzip, deflate, br")
            .header("Cache-Control", "no-cache")
            .header("Pragma", "no-cache")
            .header("Sec-Fetch-Dest", "websocket")
            .header("Sec-Fetch-Mode", "websocket")
            .header("Sec-Fetch-Site", "same-origin")
            .build()

        webSocket = httpClient!!.newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(ws: WebSocket, response: Response) {
                Log.i(TAG, "tunnel socket open, sending hello")
                ws.send(hello)
            }

            override fun onMessage(ws: WebSocket, text: String) {
                handleControl(ws, profile, clientSalt, text)
            }

            override fun onMessage(ws: WebSocket, bytes: ByteString) {
                val s = session ?: return
                try {
                    val pkt = s.open(bytes.toByteArray())
                    tunOut?.write(pkt)
                } catch (e: Exception) {
                    Log.w(TAG, "downlink decrypt/write failed", e)
                }
            }

            override fun onFailure(ws: WebSocket, t: Throwable, response: Response?) {
                Log.e(TAG, "tunnel failure (http=${response?.code})", t)
                val detail = when (response?.code) {
                    403 -> "403 blocked upstream — if the domain is on Cloudflare, " +
                        "turn off Bot Fight Mode or use a DNS-only (grey-cloud) record"
                    404 -> "404 — check the domain routes to ArnosVPN (Coolify expose)"
                    502, 503, 504 -> "${response.code} — ArnosVPN backend is not reachable behind the proxy"
                    else -> t.message ?: "connection failed"
                }
                stopTunnel(STATE_ERROR, detail)
            }

            override fun onClosed(ws: WebSocket, code: Int, reason: String) {
                stopTunnel(STATE_DISCONNECTED, reason)
            }
        })
    }

    @Volatile private var session: Session? = null
    @Volatile private var tunOut: FileOutputStream? = null

    private fun handleControl(ws: WebSocket, profile: Profile, clientSalt: ByteArray, text: String) {
        val obj = JSONObject(text)
        when (obj.optString("type")) {
            "welcome" -> {
                val serverSalt = android.util.Base64.decode(obj.getString("salt"), android.util.Base64.NO_WRAP)
                session = Crypto.deriveSession(profile.psk, clientSalt, serverSalt)
                establishTun(obj)
            }
            "error" -> stopTunnel(STATE_ERROR, obj.optString("msg", "rejected by server"))
            "pong" -> {} // keepalive
        }
    }

    private fun establishTun(welcome: JSONObject) {
        val ip = welcome.getString("ip")
        val mask = welcome.getInt("mask")
        val mtu = welcome.optInt("mtu", 1400)
        val dns = welcome.optJSONArray("dns")

        val builder = Builder()
            .setSession("ArnosVPN")
            .setMtu(mtu)
            .addAddress(ip, mask)
            .addRoute("0.0.0.0", 0) // route all IPv4 through the tunnel
        if (dns != null) {
            for (i in 0 until dns.length()) builder.addDnsServer(dns.getString(i))
        }
        // Exclude ourselves so the tunnel carrier isn't tunnelled.
        try {
            builder.addDisallowedApplication(packageName)
        } catch (_: Exception) {
        }

        val fd = builder.establish()
        if (fd == null) {
            stopTunnel(STATE_ERROR, "VPN permission not granted")
            return
        }
        tun = fd
        tunOut = FileOutputStream(fd.fileDescriptor)
        startUplink(fd)
        updateNotification("Connected · $ip")
        broadcast(STATE_CONNECTED, ip)
        Log.i(TAG, "tunnel established ip=$ip mtu=$mtu")
    }

    /** startUplink pumps packets from the TUN device into the encrypted socket. */
    private fun startUplink(fd: ParcelFileDescriptor) {
        val input = FileInputStream(fd.fileDescriptor)
        uplink = Thread {
            val buf = ByteArray(65535)
            try {
                while (running.get()) {
                    val n = input.read(buf)
                    if (n <= 0) {
                        if (n < 0) break else continue
                    }
                    val s = session ?: continue
                    val frame = s.seal(buf, 0, n)
                    webSocket?.send(frame.toByteString())
                }
            } catch (e: Exception) {
                if (running.get()) Log.w(TAG, "uplink ended", e)
            }
        }.apply { name = "arnos-uplink"; isDaemon = true; start() }
    }

    private fun stopTunnel(state: String, detail: String?) {
        if (!running.getAndSet(false)) {
            // Even if we were already stopping, make sure we leave the foreground.
            stopForegroundCompat()
            stopSelf()
            return
        }
        try { webSocket?.close(1000, "bye") } catch (_: Exception) {}
        try { uplink?.interrupt() } catch (_: Exception) {}
        try { tun?.close() } catch (_: Exception) {}
        httpClient?.dispatcher?.executorService?.shutdown()
        tun = null
        tunOut = null
        session = null
        broadcast(state, detail)
        stopForegroundCompat()
        stopSelf()
    }

    override fun onDestroy() {
        stopTunnel(STATE_DISCONNECTED, null)
        super.onDestroy()
    }

    override fun onRevoke() {
        // The system or user revoked VPN consent.
        stopTunnel(STATE_DISCONNECTED, "revoked")
        super.onRevoke()
    }

    // --- notifications & status ---------------------------------------------

    private fun broadcast(state: String, detail: String?) {
        val intent = Intent(ACTION_STATE)
            .setPackage(packageName)
            .putExtra(EXTRA_STATE, state)
            .putExtra(EXTRA_DETAIL, detail)
        sendBroadcast(intent)
    }

    private fun notification(text: String): Notification {
        ensureChannel()
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
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
            .build()
    }

    private fun updateNotification(text: String) {
        val nm = getSystemService(NotificationManager::class.java)
        nm.notify(NOTIFICATION_ID, notification(text))
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
