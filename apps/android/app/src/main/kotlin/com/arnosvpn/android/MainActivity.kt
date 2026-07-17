package com.arnosvpn.android

import android.Manifest
import android.annotation.SuppressLint
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.webkit.WebViewClient
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.arnosvpn.android.databinding.ActivityMainBinding
import com.arnosvpn.android.protocol.Profile
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import org.json.JSONObject

/**
 * MainActivity hosts the shared ArnosVPN SPA in a WebView, giving the phone the
 * same multi-page interface as the desktop app (servers, add/subscribe, ping,
 * settings, stats, logs). All data endpoints are answered in-process by
 * [ControlBridge]; the parts that need the UI thread — VPN consent, the QR
 * scanner — are delegated here through [ControlBridge.Actions].
 */
class MainActivity : AppCompatActivity(), ControlBridge.Actions {

    companion object {
        /** Sent by the Quick Settings tile when connecting needs VPN consent. */
        const val ACTION_TILE_CONNECT = "com.arnosvpn.android.TILE_CONNECT"
    }

    private lateinit var binding: ActivityMainBinding
    private lateinit var store: ProfileStore

    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == RESULT_OK) startTunnel() else toast("VPN permission denied")
    }

    private val notifPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* best effort */ }

    private val scan = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let { onProfileUri(it) }
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)
        store = ProfileStore(this)

        binding.web.apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            webViewClient = WebViewClient()
            addJavascriptInterface(ControlBridge(this@MainActivity, this@MainActivity), "ArnosBridge")
            loadUrl("file:///android_asset/index.html")
        }

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }

        handleDeepLink(intent)
        handleTileIntent(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleDeepLink(intent)
        handleTileIntent(intent)
    }

    /** handleTileIntent completes a connect started from the Quick Settings tile,
     *  showing the VPN consent dialog (which a tile can't raise on its own). */
    private fun handleTileIntent(intent: Intent?) {
        if (intent?.action == ACTION_TILE_CONNECT) connect()
    }

    override fun onBackPressed() {
        // The SPA uses hash routing, so browser history walks back through pages.
        if (binding.web.canGoBack()) binding.web.goBack() else super.onBackPressed()
    }

    // --- ControlBridge.Actions (called from the JS bridge thread) --------------

    override fun onConnect(mode: String) = runOnUiThread { connect() }

    override fun onDisconnect() = runOnUiThread {
        startService(Intent(this, ArnosVpnService::class.java).setAction(ArnosVpnService.ACTION_DISCONNECT))
    }

    override fun onScanQR() = runOnUiThread {
        scan.launch(ScanOptions().setBeepEnabled(false).setPrompt("Scan the ArnosVPN QR"))
    }

    override fun resolve(id: String, envelope: String) = runOnUiThread {
        val js = "window.__arnosBridgeResolve(${JSONObject.quote(id)},${JSONObject.quote(envelope)})"
        binding.web.evaluateJavascript(js, null)
    }

    // --- provisioning & connection --------------------------------------------

    private fun handleDeepLink(intent: Intent?) {
        val data = intent?.data ?: return
        if (data.scheme == Profile.SCHEME) onProfileUri(data.toString())
    }

    private fun onProfileUri(uri: String) {
        try {
            val entry = store.add(uri)
            store.setActive(entry.name)
            toast("Server \"${entry.name}\" saved")
        } catch (e: Exception) {
            toast("Invalid profile: ${e.message}")
        }
    }

    private fun connect() {
        if (store.load() == null) {
            toast("Add a server first")
            return
        }
        val prepare = VpnService.prepare(this)
        if (prepare != null) vpnPermission.launch(prepare) else startTunnel()
    }

    private fun startTunnel() {
        val intent = Intent(this, ArnosVpnService::class.java).setAction(ArnosVpnService.ACTION_CONNECT)
        ContextCompat.startForegroundService(this, intent)
    }

    private fun toast(msg: String) = Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
}
