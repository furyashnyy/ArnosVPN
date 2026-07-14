package com.arnosvpn.android

import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.atomic.AtomicLong

/**
 * VpnRuntime is the single, process-wide snapshot of the tunnel's live state.
 * The VpnService writes to it; the WebView control bridge reads from it to answer
 * /api/state, /api/logs and the stats page — exactly the data the desktop GUI
 * exposes over HTTP, but in-process on Android.
 */
object VpnRuntime {
    @Volatile var connected = false
    @Volatile var connecting = false
    @Volatile var ip: String? = null
    @Volatile var error: String? = null

    /** Unix seconds the current session started (0 when not connected). */
    @Volatile var since: Long = 0

    private val rxBytes = AtomicLong(0)
    private val txBytes = AtomicLong(0)

    fun addRx(n: Long) { rxBytes.addAndGet(n) }
    fun addTx(n: Long) { txBytes.addAndGet(n) }
    fun rx(): Long = rxBytes.get()
    fun tx(): Long = txBytes.get()
    fun resetCounters() { rxBytes.set(0); txBytes.set(0) }

    private const val MAX_LOG = 500
    private val log = ArrayDeque<String>()
    private val clock = SimpleDateFormat("HH:mm:ss", Locale.US)

    @Synchronized fun addLog(line: String) {
        log.addLast(clock.format(Date()) + "  " + line)
        while (log.size > MAX_LOG) log.removeFirst()
    }

    @Synchronized fun logs(): List<String> = log.toList()
    @Synchronized fun clearLogs() { log.clear() }
}
