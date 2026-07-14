package com.arnosvpn.android

import android.content.Context
import org.json.JSONObject

/**
 * SettingsStore persists the app settings as a JSON blob, mirroring the desktop
 * client's Settings struct so the shared SPA's Settings page works unchanged.
 * Options that only affect the desktop (SOCKS/HTTP bind addresses, system proxy,
 * autostart) are still stored and round-tripped for parity; on Android the tunnel
 * is always delivered through the system VpnService (TUN).
 */
class SettingsStore(context: Context) {
    private val prefs = context.getSharedPreferences("arnosvpn", Context.MODE_PRIVATE)

    fun read(): JSONObject {
        val base = defaults()
        val raw = prefs.getString(KEY, null) ?: return base
        val saved = runCatching { JSONObject(raw) }.getOrNull() ?: return base
        for (k in saved.keys()) base.put(k, saved.get(k))
        return base
    }

    /** write merges the provided keys over the stored settings and persists. */
    fun write(patch: JSONObject): JSONObject {
        val merged = read()
        for (k in patch.keys()) merged.put(k, patch.get(k))
        prefs.edit().putString(KEY, merged.toString()).apply()
        return merged
    }

    fun reset(): JSONObject {
        prefs.edit().remove(KEY).apply()
        return defaults()
    }

    private fun defaults(): JSONObject = JSONObject()
        .put("theme", "system")
        .put("mode", "tun")
        .put("socks", "127.0.0.1:1080")
        .put("http", "127.0.0.1:8080")
        .put("dns", "1.1.1.1,8.8.8.8")
        .put("preferredIP", "auto")
        .put("allowLan", false)
        .put("systemProxy", false)
        .put("autostart", false)
        .put("fragment", false)
        .put("mux", false)
        .put("tunDns", "1.1.1.1")

    private companion object {
        const val KEY = "settings"
    }
}
