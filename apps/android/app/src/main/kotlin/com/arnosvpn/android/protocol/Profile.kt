package com.arnosvpn.android.protocol

import android.net.Uri
import android.util.Base64

/**
 * Profile is the full connection description carried by an arnos://connect URI.
 * It mirrors internal/provision.Profile on the server so a QR printed by the
 * server configures the app with no manual entry.
 */
data class Profile(
    val host: String,
    val port: Int,
    val path: String,
    val sni: String,
    val psk: ByteArray,
    val name: String,
) {
    /** wsUrl is the wss:// endpoint the client upgrades on. */
    fun wsUrl(): String {
        val p = if (path.startsWith("/")) path else "/$path"
        return "wss://$host:$port$p"
    }

    /** wsUrl with an explicit (e.g. randomized) path for fingerprint variety. */
    fun wsUrl(overridePath: String): String {
        val p = if (overridePath.startsWith("/")) overridePath else "/$overridePath"
        return "wss://$host:$port$p"
    }

    companion object {
        const val SCHEME = "arnos"

        /** parse decodes an arnos://connect?... URI. */
        fun parse(raw: String): Profile {
            val uri = Uri.parse(raw.trim())
            require(uri.scheme == SCHEME) { "not an arnos:// URI" }
            val host = requireNotNull(uri.getQueryParameter("host")) { "missing host" }
            val port = uri.getQueryParameter("port")?.toIntOrNull() ?: 443
            val path = uri.getQueryParameter("path")?.ifEmpty { "/" } ?: "/"
            val sni = uri.getQueryParameter("sni")?.ifEmpty { host } ?: host
            val name = uri.getQueryParameter("name") ?: ""
            val pskStr = requireNotNull(uri.getQueryParameter("psk")) { "missing psk" }
            val psk = Base64.decode(pskStr, Base64.URL_SAFE or Base64.NO_PADDING or Base64.NO_WRAP)
            require(psk.size == Crypto.PSK_LEN) { "psk must be ${Crypto.PSK_LEN} bytes" }
            return Profile(host, port, path, sni, psk, name)
        }
    }

    // Data class equals/hashCode over a ByteArray need explicit handling.
    override fun equals(other: Any?): Boolean {
        if (this === other) return true
        if (other !is Profile) return false
        return host == other.host && port == other.port && path == other.path &&
            sni == other.sni && name == other.name && psk.contentEquals(other.psk)
    }

    override fun hashCode(): Int {
        var result = host.hashCode()
        result = 31 * result + port
        result = 31 * result + path.hashCode()
        result = 31 * result + sni.hashCode()
        result = 31 * result + psk.contentHashCode()
        result = 31 * result + name.hashCode()
        return result
    }
}
