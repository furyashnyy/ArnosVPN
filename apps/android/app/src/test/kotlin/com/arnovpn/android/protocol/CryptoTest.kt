package com.arnovpn.android.protocol

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

    private val authB64 = "zLJxYyyzQhwNFXcTqynuTczB1YssAli3ynQBI2vBLd4="
    private val frameHex = "0000000000000000674d301ce11e2596a02e38cd5d46102ffb75ed5a2129515e346affdc6513f78fade3a103"

    private val psk = ByteArray(32) { it.toByte() }
    private val clientSalt = ByteArray(16) { (0xa0 + it).toByte() }
    private val serverSalt = ByteArray(16) { (0xb0 + it).toByte() }
    private val ts = 1700000000L
    private val plaintext = "arno-vpn-test-vector".toByteArray(Charsets.US_ASCII)

    @Test
    fun authMatchesServer() {
        assertEquals(authB64, Crypto.computeAuth(psk, clientSalt, ts))
    }

    @Test
    fun sealedFrameMatchesServer() {
        val session = Crypto.deriveSession(psk, clientSalt, serverSalt)
        val frame = session.seal(plaintext, 0, plaintext.size) // first packet, counter 0
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
        val c2s = hmac(prk, "arno-c2s-v1".toByteArray(Charsets.US_ASCII), byteArrayOf(0x01)).copyOf(32)
        recv = javax.crypto.spec.SecretKeySpec(c2s, "ChaCha20")
    }

    fun open(frame: ByteArray): ByteArray {
        val ctr = java.nio.ByteBuffer.wrap(frame, 0, 8).long
        val nonce = ByteArray(12)
        for (i in 0 until 8) nonce[11 - i] = (ctr ushr (8 * i)).toByte()
        val cipher = javax.crypto.Cipher.getInstance("ChaCha20-Poly1305")
        cipher.init(javax.crypto.Cipher.DECRYPT_MODE, recv, javax.crypto.spec.IvParameterSpec(nonce))
        return cipher.doFinal(frame, 8, frame.size - 8)
    }

    private fun hmac(key: ByteArray, vararg parts: ByteArray): ByteArray {
        val mac = javax.crypto.Mac.getInstance("HmacSHA256")
        mac.init(javax.crypto.spec.SecretKeySpec(key, "HmacSHA256"))
        for (p in parts) mac.update(p)
        return mac.doFinal()
    }
}
