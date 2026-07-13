package com.arnovpn.android.protocol

import java.nio.charset.StandardCharsets
import java.security.SecureRandom
import java.util.Base64
import java.util.concurrent.atomic.AtomicLong
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.spec.IvParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * Crypto implements the client half of the ArnoVPN wire protocol. Every value
 * here must stay byte-for-byte compatible with the Go server in
 * internal/protocol: the auth HMAC context, the HKDF derivation, and the AEAD
 * framing.
 */
object Crypto {
    const val PSK_LEN = 32
    const val SALT_LEN = 16
    const val KEY_LEN = 32
    private const val AUTH_INFO = "arno-auth-v1"
    private const val C2S_INFO = "arno-c2s-v1"
    private const val S2C_INFO = "arno-s2c-v1"

    private val rng = SecureRandom()

    fun randomBytes(n: Int): ByteArray = ByteArray(n).also { rng.nextBytes(it) }

    fun b64(data: ByteArray): String = Base64.getEncoder().encodeToString(data)

    /**
     * computeAuth returns the base64 HMAC-SHA256 that proves knowledge of the
     * PSK. The authenticated message is AUTH_INFO || salt || ascii(ts), exactly
     * as built by the server.
     */
    fun computeAuth(psk: ByteArray, salt: ByteArray, ts: Long): String {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(psk, "HmacSHA256"))
        mac.update(AUTH_INFO.toByteArray(StandardCharsets.US_ASCII))
        mac.update(salt)
        mac.update(ts.toString().toByteArray(StandardCharsets.US_ASCII))
        return b64(mac.doFinal())
    }

    /** hmacSha256 is the HKDF building block. */
    private fun hmac(key: ByteArray, vararg parts: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(key, "HmacSHA256"))
        for (p in parts) mac.update(p)
        return mac.doFinal()
    }

    /** HKDF-Extract per RFC 5869. */
    private fun hkdfExtract(salt: ByteArray, ikm: ByteArray): ByteArray = hmac(salt, ikm)

    /** HKDF-Expand for a single 32-byte output block (L <= hashLen). */
    private fun hkdfExpand32(prk: ByteArray, info: String): ByteArray {
        val t = hmac(prk, info.toByteArray(StandardCharsets.US_ASCII), byteArrayOf(0x01))
        return t.copyOf(KEY_LEN)
    }

    /**
     * deriveSession builds the two directional ChaCha20-Poly1305 keys from the
     * PSK and both salts. The client sends with the client-to-server key and
     * receives with the server-to-client key.
     */
    fun deriveSession(psk: ByteArray, clientSalt: ByteArray, serverSalt: ByteArray): Session {
        val hkdfSalt = clientSalt + serverSalt
        val prk = hkdfExtract(hkdfSalt, psk)
        val c2s = hkdfExpand32(prk, C2S_INFO)
        val s2c = hkdfExpand32(prk, S2C_INFO)
        return Session(sendKey = c2s, recvKey = s2c)
    }
}

/**
 * Session holds directional AEAD state. Each direction has its own key and a
 * monotonic 64-bit counter that forms the nonce and is prefixed to each frame.
 */
class Session(private val sendKey: ByteArray, private val recvKey: ByteArray) {
    private val sendCtr = AtomicLong(0)

    /** seal encrypts one IP packet into a wire frame: counter(8) || ct || tag. */
    fun seal(plaintext: ByteArray, offset: Int, length: Int): ByteArray {
        val ctr = sendCtr.getAndIncrement()
        val nonce = nonceFor(ctr)
        val cipher = Cipher.getInstance("ChaCha20-Poly1305")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(sendKey, "ChaCha20"), IvParameterSpec(nonce))
        val ct = cipher.doFinal(plaintext, offset, length)
        val out = ByteArray(8 + ct.size)
        writeCounter(out, ctr)
        System.arraycopy(ct, 0, out, 8, ct.size)
        return out
    }

    /** open decrypts a frame produced by the server's Seal. */
    fun open(frame: ByteArray): ByteArray {
        require(frame.size >= 8) { "short frame" }
        val ctr = readCounter(frame)
        val nonce = nonceFor(ctr)
        val cipher = Cipher.getInstance("ChaCha20-Poly1305")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(recvKey, "ChaCha20"), IvParameterSpec(nonce))
        return cipher.doFinal(frame, 8, frame.size - 8)
    }

    private fun nonceFor(ctr: Long): ByteArray {
        // 4 zero bytes followed by the big-endian counter, matching the server.
        val n = ByteArray(12)
        n[4] = (ctr ushr 56).toByte()
        n[5] = (ctr ushr 48).toByte()
        n[6] = (ctr ushr 40).toByte()
        n[7] = (ctr ushr 32).toByte()
        n[8] = (ctr ushr 24).toByte()
        n[9] = (ctr ushr 16).toByte()
        n[10] = (ctr ushr 8).toByte()
        n[11] = ctr.toByte()
        return n
    }

    private fun writeCounter(dst: ByteArray, ctr: Long) {
        dst[0] = (ctr ushr 56).toByte()
        dst[1] = (ctr ushr 48).toByte()
        dst[2] = (ctr ushr 40).toByte()
        dst[3] = (ctr ushr 32).toByte()
        dst[4] = (ctr ushr 24).toByte()
        dst[5] = (ctr ushr 16).toByte()
        dst[6] = (ctr ushr 8).toByte()
        dst[7] = ctr.toByte()
    }

    private fun readCounter(src: ByteArray): Long =
        (src[0].toLong() and 0xff shl 56) or
            (src[1].toLong() and 0xff shl 48) or
            (src[2].toLong() and 0xff shl 40) or
            (src[3].toLong() and 0xff shl 32) or
            (src[4].toLong() and 0xff shl 24) or
            (src[5].toLong() and 0xff shl 16) or
            (src[6].toLong() and 0xff shl 8) or
            (src[7].toLong() and 0xff)
}
