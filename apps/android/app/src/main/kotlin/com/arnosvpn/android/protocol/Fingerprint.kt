package com.arnosvpn.android.protocol

import java.security.SecureRandom

/**
 * Fingerprint randomizes the per-connection look of the tunnel so no two
 * sessions are alike: a rotating browser User-Agent and a fresh random request
 * path (the server accepts the WebSocket upgrade on any path). Combined with the
 * protocol's random per-frame padding, each session is an unrepeatable,
 * browser-shaped HTTPS flow.
 */
object Fingerprint {
    private val rng = SecureRandom()

    private val userAgents = listOf(
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36",
        "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36 Edg/124.0.0.0",
        "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36",
        "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1",
        "Mozilla/5.0 (X11; Linux x86_64; rv:126.0) Gecko/20100101 Firefox/126.0",
    )

    private val prefixes = listOf("/assets/", "/static/", "/cdn/", "/img/", "/v1/ws/", "/live/")

    fun randomUserAgent(): String = userAgents[rng.nextInt(userAgents.size)]

    fun randomPath(): String {
        val buf = ByteArray(8)
        rng.nextBytes(buf)
        val hex = buf.joinToString("") { "%02x".format(it) }
        return prefixes[rng.nextInt(prefixes.size)] + hex
    }
}
