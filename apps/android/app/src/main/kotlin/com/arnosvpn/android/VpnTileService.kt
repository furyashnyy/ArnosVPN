package com.arnosvpn.android

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.graphics.drawable.Icon
import android.net.VpnService
import android.os.Build
import android.service.quicksettings.Tile
import android.service.quicksettings.TileService
import androidx.core.content.ContextCompat

/**
 * VpnTileService adds an ArnosVPN toggle to the Quick Settings panel (the tiles
 * in the notification shade), so the tunnel can be switched on and off without
 * opening the app. One tap connects the active server; another disconnects.
 *
 * The tile mirrors [VpnRuntime]: highlighted while connecting/connected, dim when
 * off. [ArnosVpnService] nudges it on every state change via [requestUpdate], so
 * it stays in sync even when toggled from inside the app.
 */
class VpnTileService : TileService() {

    override fun onStartListening() {
        super.onStartListening()
        sync()
    }

    override fun onClick() {
        super.onClick()
        val busy = VpnRuntime.connected || VpnRuntime.connecting
        when {
            busy -> disconnect()
            ProfileStore(this).load() == null -> openApp(null) // no server yet — let them add one
            VpnService.prepare(this) != null -> openApp(MainActivity.ACTION_TILE_CONNECT) // consent needs a UI
            else -> connect()
        }
        sync()
    }

    private fun connect() = ContextCompat.startForegroundService(
        this,
        Intent(this, ArnosVpnService::class.java).setAction(ArnosVpnService.ACTION_CONNECT),
    )

    private fun disconnect() = startService(
        Intent(this, ArnosVpnService::class.java).setAction(ArnosVpnService.ACTION_DISCONNECT),
    )

    /**
     * openApp brings up MainActivity — used when the tap can't be handled from
     * the background alone (VPN consent dialog, or no server configured). It
     * collapses the shade the way a well-behaved tile should.
     */
    private fun openApp(action: String?) {
        val intent = Intent(this, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
            .also { if (action != null) it.action = action }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            val pi = android.app.PendingIntent.getActivity(
                this, 0, intent,
                android.app.PendingIntent.FLAG_UPDATE_CURRENT or android.app.PendingIntent.FLAG_IMMUTABLE,
            )
            startActivityAndCollapse(pi)
        } else {
            @Suppress("DEPRECATION")
            startActivityAndCollapse(intent)
        }
    }

    /** sync repaints the tile from the live [VpnRuntime] snapshot. */
    private fun sync() {
        val tile = qsTile ?: return
        val active = ProfileStore(this).activeName()
        when {
            VpnRuntime.connecting -> {
                tile.state = Tile.STATE_ACTIVE
                tile.subtitleCompat(getString(R.string.tile_connecting))
            }
            VpnRuntime.connected -> {
                tile.state = Tile.STATE_ACTIVE
                tile.subtitleCompat(active ?: getString(R.string.tile_connected))
            }
            else -> {
                tile.state = Tile.STATE_INACTIVE
                tile.subtitleCompat(active ?: getString(R.string.tile_off))
            }
        }
        tile.label = getString(R.string.app_name)
        tile.icon = Icon.createWithResource(this, R.drawable.ic_tile_shield)
        tile.updateTile()
    }

    /** subtitleCompat sets the tile subtitle where the platform supports it (API 29+). */
    private fun Tile.subtitleCompat(text: CharSequence) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) subtitle = text
    }

    companion object {
        /**
         * requestUpdate asks the system to briefly bind the tile so it can redraw
         * from the current state. Safe to call from anywhere; it no-ops if the
         * tile has never been added to Quick Settings.
         */
        fun requestUpdate(context: Context) = runCatching {
            TileService.requestListeningState(context, ComponentName(context, VpnTileService::class.java))
        }
    }
}
