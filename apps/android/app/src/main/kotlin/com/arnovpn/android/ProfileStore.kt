package com.arnovpn.android

import android.content.Context
import com.arnovpn.android.protocol.Profile

/**
 * ProfileStore persists the single active connection profile as its arno://
 * URI. Keeping the canonical URI (rather than parsed fields) means a saved
 * profile always round-trips exactly through the same parser the deep link and
 * QR scanner use.
 */
class ProfileStore(context: Context) {
    private val prefs = context.getSharedPreferences("arnovpn", Context.MODE_PRIVATE)

    fun save(uri: String) {
        // Validate before persisting so we never store an unusable profile.
        Profile.parse(uri)
        prefs.edit().putString(KEY_URI, uri).apply()
    }

    fun loadUri(): String? = prefs.getString(KEY_URI, null)

    fun load(): Profile? = loadUri()?.let {
        runCatching { Profile.parse(it) }.getOrNull()
    }

    fun clear() = prefs.edit().remove(KEY_URI).apply()

    private companion object {
        const val KEY_URI = "profile_uri"
    }
}
