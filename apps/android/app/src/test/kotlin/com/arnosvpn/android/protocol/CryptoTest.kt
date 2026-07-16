package com.arnosvpn.android.protocol

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * CryptoTest pins the same wire vectors as the Go server test
 * (internal/protocol/vectors_test.go). Because both implementations assert the
 * identical constants for identical inputs, passing this test proves the
 * Android client and the Go server interoperate at the byte level.
 *
 * This runs as a plain JVM unit test (no emulator): ChaCha20-Poly1305 and the
 * HMAC/HKDF primitives are all provided by the JDK.
 */
class CryptoTest {

    private val authB64 = "XcfGTzUpsovj7VPGo5bqQcC05d9BTevquB8kEtkiZQI="
    private val frameHex = "0000000000000000d0867cadb3d67b40e6d82fb9bd2f190aba7ddc0a602a72c0ec9f6466003b20b0ae090cb6cd4ca9ef939073be"

    private val psk = ByteArray(32) { it.toByte() }
    private val clientSalt = ByteArray(16) { (0xa0 + it).toByte() }
    private val serverSalt = ByteArray(16) { (0xb0 + it).toByte() }
    private val ts = 1700000000L
    private val plaintext = "arnos-vpn-test-vector".toByteArray(Charsets.US_ASCII)

    @Test
    fun authMatchesServer() {
        assertEquals(authB64, Crypto.computeAuth(psk, clientSalt, ts))
    }

    @Test
    fun sealedFrameMatchesServer() {
        val session = Crypto.deriveSession(psk, clientSalt, serverSalt)
        // Deterministic pad (5 zero bytes) so the vector is stable; real seal pads randomly.
        val frame = session.sealRaw(plaintext, 0, plaintext.size, ByteArray(5))
        assertEquals(frameHex, frame.toHex())
    }

    @Test
    fun roundTripThroughServerKeys() {
        // Client seals with c2s; the server side (recvKey = c2s) must open it.
        val client = Crypto.deriveSession(psk, clientSalt, serverSalt)
        val server = ServerSideSession(psk, clientSalt, serverSalt)
        val frame = client.seal(plaintext, 0, plaintext.size)
        assertArrayEquals(plaintext, server.open(frame))
    }

    @Test
    fun rejectsReplayedFrame() {
        // A frame that was already opened must be rejected the second time: the
        // receive counter must strictly advance (anti-replay). The client session
        // receives with the s2c key, so seal with a server-side s2c sender.
        val server = ServerSendSession(psk, clientSalt, serverSalt)
        val client = Crypto.deriveSession(psk, clientSalt, serverSalt)
        val f0 = server.seal(plaintext)
        val f1 = server.seal(plaintext)
        assertArrayEquals(plaintext, client.open(f0))
        assertArrayEquals(plaintext, client.open(f1))

        var rejected = false
        try { client.open(f0) } catch (e: IllegalStateException) { rejected = true }
        assertEquals("replay of f0 must be rejected", true, rejected)

        rejected = false
        try { client.open(f1) } catch (e: IllegalStateException) { rejected = true }
        assertEquals("replay of f1 must be rejected", true, rejected)
    }

    private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
}

/**
 * ServerSideSession mirrors DeriveSession(..., isServer=true): it sends with
 * the s2c key and receives with the c2s key. It exists only so the JVM test
 * can decrypt a client frame the way the real server does.
 */
private class ServerSideSession(psk: ByteArray, clientSalt: ByteArray, serverSalt: ByteArray) {
    private val recv: javax.crypto.spec.SecretKeySpec

    init {
        val prk = hmac(clientSalt + serverSalt, psk)
        val c2s = hmac(prk, "arnos-c2s-v1".toByteArray(Charsets.US_ASCII), byteArrayOf(0x01)).copyOf(32)
        recv = javax.crypto.spec.SecretKeySpec(c2s, "ChaCha20")
    }

    fun open(frame: ByteArray): ByteArray {
        val ctr = java.nio.ByteBuffer.wrap(frame, 0, 8).long
        val nonce = ByteArray(12)
        for (i in 0 until 8) nonce[11 - i] = (ctr ushr (8 * i)).toByte()
        val cipher = javax.crypto.Cipher.getInstance("ChaCha20-Poly1305")
        cipher.init(javax.crypto.Cipher.DECRYPT_MODE, recv, javax.crypto.spec.IvParameterSpec(nonce))
        val pt = cipher.doFinal(frame, 8, frame.size - 8)
        val realLen = ((pt[0].toInt() and 0xff) shl 8) or (pt[1].toInt() and 0xff)
        return pt.copyOfRange(2, 2 + realLen)
    }

    private fun hmac(key: ByteArray, vararg parts: ByteArray): ByteArray {
        val mac = javax.crypto.Mac.getInstance("HmacSHA256")
        mac.init(javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"))
        for (p in parts) mac.update(p)
        return mac.doFinal()
    }
}

/**
 * ServerSendSession seals frames with the s2c key, the way the real server sends
 * to a client. A client Session (recvKey = s2c) can therefore open its frames.
 * Used only to exercise the client's anti-replay path in a JVM unit test.
 */
private class ServerSendSession(psk: ByteArray, clientSalt: ByteArray, serverSalt: ByteArray) {
    private val sendKey: javax.crypto.spec.SecretKeySpec
    private var ctr = 0L

    init {
        val prk = hmac(clientSalt + serverSalt, psk)
        val s2c = hmac(prk, "arnos-s2c-v1".toByteArray(Charsets.US_ASCII), byteArrayOf(0x01)).copyOf(32)
        sendKey = javax.crypto.spec.SecretKeySpec(s2c, "ChaCha20")
    }

    fun seal(payload: ByteArray): ByteArray {
        val c = ctr++
        val nonce = ByteArray(12)
        for (i in 0 until 8) nonce[11 - i] = (c ushr (8 * i)).toByte()
        val pt = ByteArray(2 + payload.size)
        pt[0] = (payload.size ushr 8).toByte()
        pt[1] = payload.size.toByte()
        System.arraycopy(payload, 0, pt, 2, payload.size)
        val cipher = javax.crypto.Cipher.getInstance("ChaCha20-Poly1305")
        cipher.init(javax.crypto.Cipher.ENCRYPT_MODE, sendKey, javax.crypto.spec.IvParameterSpec(nonce))
        val ivct = cipher.doFinal(pt)
        val out = ByteArray(8 + ivct.size)
        for (i in 0 until 8) out[i] = (c ushr (8 * (7 - i))).toByte()
        System.arraycopy(ivct, 0, out, 8, ivct.size)
        return out
    }

    private fun hmac(key: ByteArray, vararg parts: ByteArray): ByteArray {
        val mac = javax.crypto.Mac.getInstance("HmacSHA256")
        mac.init(javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"))
        for (p in parts) mac.update(p)
        return mac.doFinal()
    }
}
