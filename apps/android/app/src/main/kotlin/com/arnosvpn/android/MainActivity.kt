package com.arnosvpn.android

import android.Manifest
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.content.res.ColorStateList
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.arnosvpn.android.databinding.ActivityMainBinding
import com.arnosvpn.android.protocol.Profile
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions

/**
 * MainActivity is the whole UI: it shows connection state and offers exactly
 * three ways to provision — scan a QR, paste an arnos:// URI, or open one via a
 * deep link — then a single connect/disconnect control. There are no config
 * fields to fill in.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var binding: ActivityMainBinding
    private lateinit var store: ProfileStore

    private val stateReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            render(intent.getStringExtra(ArnosVpnService.EXTRA_STATE), intent.getStringExtra(ArnosVpnService.EXTRA_DETAIL))
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
            scan.launch(ScanOptions().setBeepEnabled(false).setPrompt("Scan the ArnosVPN QR"))
        }
        binding.pasteButton.setOnClickListener { promptPaste() }
        binding.serversButton.setOnClickListener { showServers() }
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
            this, stateReceiver, IntentFilter(ArnosVpnService.ACTION_STATE),
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
            val entry = store.add(uri)
            store.setActive(entry.name)
            toast("Server \"${entry.name}\" saved")
            renderProfile()
        } catch (e: Exception) {
            toast("Invalid profile: ${e.message}")
        }
    }

    /** showServers lists saved servers so the user can view, switch, or remove. */
    private fun showServers() {
        val servers = store.servers()
        if (servers.isEmpty()) {
            toast("No servers yet — scan a QR or paste a URI")
            return
        }
        val active = store.activeName()
        val labels = servers.map { s ->
            val host = s.profileOrNull()?.let { "${it.host}:${it.port}" } ?: "?"
            (if (s.name == active) "● " else "○ ") + "${s.name}  ·  $host"
        }.toTypedArray()

        androidx.appcompat.app.AlertDialog.Builder(this)
            .setTitle("Your servers")
            .setItems(labels) { _, i ->
                store.setActive(servers[i].name)
                toast("Active: ${servers[i].name}")
                renderProfile()
            }
            .setNeutralButton("Remove…") { _, _ -> showRemoveServer(servers.map { it.name }) }
            .setPositiveButton("Close", null)
            .show()
    }

    private fun showRemoveServer(names: List<String>) {
        androidx.appcompat.app.AlertDialog.Builder(this)
            .setTitle("Remove a server")
            .setItems(names.toTypedArray()) { _, i ->
                store.remove(names[i])
                toast("Removed ${names[i]}")
                renderProfile()
            }
            .setNegativeButton("Cancel", null)
            .show()
    }

    private fun promptPaste() {
        val input = android.widget.EditText(this).apply { hint = "arnos://connect?..." }
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
        val intent = Intent(this, ArnosVpnService::class.java).setAction(ArnosVpnService.ACTION_CONNECT)
        ContextCompat.startForegroundService(this, intent)
    }

    private fun renderProfile() {
        val profile = store.load()
        val count = store.servers().size
        binding.profileText.text = if (profile != null) {
            val suffix = if (count > 1) "  (+${count - 1} more)" else ""
            "${profile.name.ifEmpty { "server" }} · ${profile.host}:${profile.port}$suffix"
        } else {
            "No profile — scan a QR or paste a URI"
        }
        binding.connectButton.isEnabled = profile != null
        binding.serversButton.isEnabled = count > 0
    }

    private fun render(state: String?, detail: String?) {
        val (label, colorRes) = when (state) {
            ArnosVpnService.STATE_CONNECTING -> getString(R.string.connecting) to R.color.status_connecting
            ArnosVpnService.STATE_CONNECTED -> getString(R.string.connected) to R.color.status_connected
            ArnosVpnService.STATE_ERROR -> "Error" to R.color.status_error
            else -> getString(R.string.status_disconnected) to R.color.status_idle
        }
        binding.statusText.text = label

        val tint = ColorStateList.valueOf(ContextCompat.getColor(this, colorRes))
        binding.statusCircle.backgroundTintList = tint
        binding.statusGlow.backgroundTintList = tint

        when (state) {
            ArnosVpnService.STATE_CONNECTED -> binding.profileText.text =
                detail?.takeIf { it.isNotBlank() }?.let { "Exit IP · $it" } ?: getString(R.string.connected)
            ArnosVpnService.STATE_ERROR -> binding.profileText.text = detail ?: "unknown error"
            ArnosVpnService.STATE_CONNECTING -> binding.profileText.text = detail ?: ""
            else -> renderProfile() // restore the profile line when idle
        }

        val active = state == ArnosVpnService.STATE_CONNECTED || state == ArnosVpnService.STATE_CONNECTING
        binding.connectButton.text = getString(if (active) R.string.disconnect else R.string.connect)
        val btnColor = if (state == ArnosVpnService.STATE_CONNECTED) R.color.status_connected else R.color.arnos_primary
        binding.connectButton.backgroundTintList = ColorStateList.valueOf(ContextCompat.getColor(this, btnColor))
        binding.connectButton.setOnClickListener {
            if (active) {
                startService(Intent(this, ArnosVpnService::class.java).setAction(ArnosVpnService.ACTION_DISCONNECT))
            } else {
                onConnectClicked()
            }
        }
    }

    private fun toast(msg: String) = Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
}
