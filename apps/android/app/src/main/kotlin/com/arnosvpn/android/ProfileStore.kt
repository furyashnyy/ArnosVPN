package com.arnosvpn.android

import android.content.Context
import com.arnosvpn.android.protocol.Profile
import org.json.JSONArray
import org.json.JSONObject

/** One saved server, stored as its canonical arnos:// URI. */
data class ServerEntry(val name: String, val uri: String) {
    fun profileOrNull(): Profile? = runCatching { Profile.parse(uri) }.getOrNull()
}

/**
 * ProfileStore persists a list of servers (each as its arnos:// URI) plus the
 * active one, so the user can save and switch between multiple endpoints — like
 * a real VPN client's server list.
 */
class ProfileStore(context: Context) {
    private val prefs = context.getSharedPreferences("arnosvpn", Context.MODE_PRIVATE)

    /** add validates and stores a server; the first one added becomes active. */
    fun add(uri: String): ServerEntry {
        val profile = Profile.parse(uri) // throws on invalid
        val name = profile.name.ifEmpty { profile.host }
        val list = servers().toMutableList()
        val idx = list.indexOfFirst { it.name.equals(name, ignoreCase = true) }
        val entry = ServerEntry(name, uri)
        if (idx >= 0) list[idx] = entry else list.add(entry)
        val active = activeName() ?: name
        persist(list, active)
        return entry
    }

    fun servers(): List<ServerEntry> {
        val raw = prefs.getString(KEY_SERVERS, null) ?: return legacyMigrate()
        val arr = runCatching { JSONArray(raw) }.getOrNull() ?: return emptyList()
        return (0 until arr.length()).mapNotNull {
            val o = arr.optJSONObject(it) ?: return@mapNotNull null
            ServerEntry(o.optString("name"), o.optString("uri"))
        }
    }

    fun activeName(): String? = prefs.getString(KEY_ACTIVE, null)

    fun setActive(name: String) {
        if (servers().any { it.name == name }) {
            prefs.edit().putString(KEY_ACTIVE, name).apply()
        }
    }

    fun remove(name: String) {
        val list = servers().filterNot { it.name.equals(name, ignoreCase = true) }
        val active = if (activeName() == name) list.firstOrNull()?.name else activeName()
        persist(list, active)
    }

    /** load returns the active profile (used by the VPN service). */
    fun load(): Profile? {
        val name = activeName() ?: return servers().firstOrNull()?.profileOrNull()
        return servers().firstOrNull { it.name == name }?.profileOrNull()
            ?: servers().firstOrNull()?.profileOrNull()
    }

    private fun persist(list: List<ServerEntry>, active: String?) {
        val arr = JSONArray()
        list.forEach { arr.put(JSONObject().put("name", it.name).put("uri", it.uri)) }
        prefs.edit()
            .putString(KEY_SERVERS, arr.toString())
            .putString(KEY_ACTIVE, active)
            .apply()
    }

    /** Migrate a single legacy profile_uri (older app versions) into the list. */
    private fun legacyMigrate(): List<ServerEntry> {
        val legacy = prefs.getString(KEY_LEGACY_URI, null) ?: return emptyList()
        return runCatching {
            val p = Profile.parse(legacy)
            val entry = ServerEntry(p.name.ifEmpty { p.host }, legacy)
            persist(listOf(entry), entry.name)
            prefs.edit().remove(KEY_LEGACY_URI).apply()
            listOf(entry)
        }.getOrDefault(emptyList())
    }

    private companion object {
        const val KEY_SERVERS = "servers"
        const val KEY_ACTIVE = "active"
        const val KEY_LEGACY_URI = "profile_uri"
    }
}
