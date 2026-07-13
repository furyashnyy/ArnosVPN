package com.arnovpn.android

import android.Manifest
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.arnovpn.android.databinding.ActivityMainBinding
import com.arnovpn.android.protocol.Profile
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

/**
 * MainActivity is the whole UI: it shows connection state and offers exactly
 * three ways to provision — scan a QR, paste an arno:// URI, or open one via a
 * deep link — then a single connect/disconnect control. There are no config
 * fields to fill in.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private lateinit var store: ProfileStore

    private val stateReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            render(intent.getStringExtra(ArnoVpnService.EXTRA_STATE), intent.getStringExtra(ArnoVpnService.EXTRA_DETAIL))
        }
    }

    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
        if (result.resultCode == RESULT_OK) startTunnel() else toast("VPN permission denied")
    }

    private val notifPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* best effort */ }

    private val scan = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let { onProfileUri(it) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivityMainBinding.inflate(layoutInflater)
        setContentView(binding.root)
        store = ProfileStore(this)

        binding.scanButton.setOnClickListener {
            scan.launch(ScanOptions().setBeepEnabled(false).setPrompt("Scan the ArnoVPN QR"))
        }
        binding.pasteButton.setOnClickListener { promptPaste() }
        binding.connectButton.setOnClickListener { onConnectClicked() }

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            notifPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }

        handleDeepLink(intent)
        renderProfile()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleDeepLink(intent)
    }

    override fun onResume() {
        super.onResume()
        ContextCompat.registerReceiver(
            this, stateReceiver, IntentFilter(ArnoVpnService.ACTION_STATE),
            ContextCompat.RECEIVER_NOT_EXPORTED,
        )
    }

    override fun onPause() {
        super.onPause()
        runCatching { unregisterReceiver(stateReceiver) }
    }

    private fun handleDeepLink(intent: Intent?) {
        val data = intent?.data ?: return
        if (data.scheme == Profile.SCHEME) onProfileUri(data.toString())
    }

    private fun onProfileUri(uri: String) {
        try {
            store.save(uri)
            toast("Profile saved")
            renderProfile()
        } catch (e: Exception) {
            toast("Invalid profile: ${e.message}")
        }
    }

    private fun promptPaste() {
        val input = android.widget.EditText(this).apply { hint = "arno://connect?..." }
        androidx.appcompat.app.AlertDialog.Builder(this)
            .setTitle("Paste connect URI")
            .setView(input)
            .setPositiveButton("Save") { _, _ -> onProfileUri(input.text.toString()) }
            .setNegativeButton("Cancel", null)
            .show()
    }

    private fun onConnectClicked() {
        val profile = store.load()
        if (profile == null) {
            toast("Add a profile first")
            return
        }
        val prepare = VpnService.prepare(this)
        if (prepare != null) {
            vpnPermission.launch(prepare)
        } else {
            startTunnel()
        }
    }

    private fun startTunnel() {
        val intent = Intent(this, ArnoVpnService::class.java).setAction(ArnoVpnService.ACTION_CONNECT)
        ContextCompat.startForegroundService(this, intent)
    }

    private fun renderProfile() {
        val profile = store.load()
        binding.profileText.text = if (profile != null) {
            "${profile.name.ifEmpty { "server" }} · ${profile.host}:${profile.port}"
        } else {
            "No profile — scan a QR or paste a URI"
        }
        binding.connectButton.isEnabled = profile != null
    }

    private fun render(state: String?, detail: String?) {
        binding.statusText.text = when (state) {
            ArnoVpnService.STATE_CONNECTING -> "Connecting…"
            ArnoVpnService.STATE_CONNECTED -> "Connected · ${detail ?: ""}"
            ArnoVpnService.STATE_ERROR -> "Error: ${detail ?: "unknown"}"
            else -> "Disconnected"
        }
        val connected = state == ArnoVpnService.STATE_CONNECTED || state == ArnoVpnService.STATE_CONNECTING
        binding.connectButton.text = if (connected) "Disconnect" else "Connect"
        binding.connectButton.setOnClickListener {
            if (connected) {
                startService(Intent(this, ArnoVpnService::class.java).setAction(ArnoVpnService.ACTION_DISCONNECT))
            } else {
                onConnectClicked()
            }
        }
    }

    private fun toast(msg: String) = Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
}
