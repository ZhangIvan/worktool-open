package io.worktool.open.prototype.core

import io.worktool.open.prototype.policy.EndpointPolicy
import io.worktool.open.prototype.transport.OkHttpDeviceLink
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Test
import java.io.File
import java.net.HttpURLConnection
import java.net.Proxy
import java.net.ProxySelector
import java.net.URI
import java.nio.file.Files
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.Callable
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit.SECONDS

/** Opt-in integration: real transport and engine, synthetic command and in-memory journal. */
class GoOkHttpLoopbackTest {
    @Test fun reconnectDeliversPendingResultWithoutRepeatingSyntheticEffect() {
        val helperPath = System.getenv("WORKTOOL_LOOPBACK_HELPER")
        assumeTrue("opt in with WORKTOOL_LOOPBACK_HELPER", helperPath != null)
        val helper = File(requireNotNull(helperPath))
        require(helper.isAbsolute && helper.isFile && helper.canExecute())
        val evidenceFile = Files.createTempFile("worktool-loopback-stderr-", ".log").toFile()
        val stdout = Executors.newSingleThreadExecutor { task ->
            Thread(task, "worktool-fixture-startup").apply { isDaemon = true }
        }
        var process: Process? = null
        var link: OkHttpDeviceLink? = null
        var passed = false
        var primaryFailure: Throwable? = null
        try {
            val fixture = ProcessBuilder(helper.absolutePath, "-reconnect").redirectError(evidenceFile).start()
            process = fixture
            val readyLine = stdout.submit(Callable { fixture.inputStream.bufferedReader().readLine() })
            val origin = requireNotNull(readyLine.get(5, SECONDS)) { "fixture did not announce origin" }
            val endpoint = ExactLoopback(origin)
            // Do not run where the JVM would route this local fixture through any proxy.
            assertEquals(listOf(Proxy.NO_PROXY), ProxySelector.getDefault()?.select(URI(origin)))
            val transport = OkHttpDeviceLink(endpoint)
            link = transport
            val journal = ObservableJournal()
            val errors = ArrayBlockingQueue<String>(8)
            val engine = NativeCommandEngine(journal, transport, object : EngineEvents {
                override fun changed() = Unit
                override fun confirmationRequired(commandId: String, exportId: String, scopeDigest: String) {
                    errors.offer("unexpected local confirmation")
                }
                override fun protocolError(code: String) { errors.offer(code) }
            })
            val enrolled = ArrayBlockingQueue<Result<Identity>>(1)
            transport.enroll(origin, "enrollment_fixture", "token_fixture") { enrolled.offer(it) }
            val identity = requireNotNull(enrolled.poll(5, SECONDS)) { "enrollment timed out" }.getOrThrow()
            assertEquals(origin, identity.endpoint)

            val first = SessionListener(engine, resume = true)
            transport.connect(identity, first)
            val firstBinding = first.awaitReady()
            assertEquals(identity.deviceId, firstBinding.deviceId)
            assertEquals("1.0", firstBinding.protocolVersion)
            val signal = URI(origin).resolve("/ready").toURL()
                .openConnection(Proxy.NO_PROXY) as HttpURLConnection
            try {
                signal.instanceFollowRedirects = false
                signal.connectTimeout = 3000
                signal.readTimeout = 3000
                signal.requestMethod = "POST"
                assertEquals("fixture readiness handshake failed", 204, signal.responseCode)
            } finally { signal.disconnect() }

            assertNotNull("first connection did not close after result", first.closed.poll(8, SECONDS))
            val before = journal.pendingResults().single()
            val commandId = before.getString("commandId")
            assertEquals("cmd_text", commandId)
            assertEquals("attempt_text", before.getString("attemptId"))
            assertEquals("succeeded", before.getString("status"))
            assertEquals("applied", before.getString("effectState"))
            assertEquals("APP_LOCAL_SYNTHETIC_APPLIED", before.getString("code"))
            assertFalse(journal.command(commandId)!!.acked)
            val expectedEffect = LocalEffect("synthetic_inbox", "synthetic_target_001", "isolated loopback fixture")
            assertEquals(expectedEffect, journal.singleEffect())

            val second = SessionListener(engine, resume = false)
            transport.connect(identity, second)
            val secondBinding = second.awaitReady()
            assertEquals(firstBinding.tenantId, secondBinding.tenantId)
            assertEquals(firstBinding.deviceId, secondBinding.deviceId)
            assertNotEquals(firstBinding.sessionId, secondBinding.sessionId)
            assertTrue("replayed result did not receive a real Go ACK", journal.acknowledged.await(8, SECONDS))
            assertTrue(journal.command(commandId)!!.acked)
            assertTrue(journal.pendingResults().isEmpty())
            assertEquals(WireValidation.canonicalDigest(before),
                WireValidation.canonicalDigest(JSONObject(journal.command(commandId)!!.result!!)))
            assertEquals(expectedEffect, journal.singleEffect())
            assertTrue("fixture protocol errors: ${errors.toList()}", errors.isEmpty())
            assertTrue("fixture did not finish", fixture.waitFor(5, SECONDS))
            assertEquals("fixture reported failed wire assertions", 0, fixture.exitValue())
            assertTrue("unexpectedly large fixture diagnostics", evidenceFile.length() in 1L..8192L)
            val transcript = evidenceFile.readText()
            val firstResult = transcript.indexOf("RESULT_RECEIVED_WITHOUT_ACK")
            assertTrue(firstResult >= 0 && transcript.indexOf("RESULT_ACK_SENT") > firstResult)
            println("LOOPBACK_VERIFIED: ENROLL, HELLO, ONE_SYNTHETIC_EFFECT, NEW_SESSION_RESULT, EXACT_ACK")
            passed = true
        } catch (failure: Throwable) {
            primaryFailure = failure
            throw failure
        } finally {
            var cleanupFailure: Throwable? = null
            fun attempt(action: () -> Unit) {
                try { action() }
                catch (failure: Throwable) {
                    val first = primaryFailure ?: cleanupFailure
                    if (first == null) cleanupFailure = failure
                    else if (first !== failure) first.addSuppressed(failure)
                }
            }
            attempt { link?.disconnect() }
            attempt { process?.let(::stopOwnedHelper) }
            attempt { stdout.shutdownNow() }
            attempt {
                // Preserve helper stderr on failure for exact local evidence; no payloads or credentials.
                if (passed && cleanupFailure == null) Files.deleteIfExists(evidenceFile.toPath())
                else System.err.println("WorkTool loopback fixture diagnostics: ${evidenceFile.absolutePath}")
            }
            cleanupFailure?.let { throw it }
        }
    }

    private fun stopOwnedHelper(process: Process) {
        if (!process.isAlive) return
        try {
            process.destroy()
            if (process.waitFor(2, SECONDS)) return
        } finally {
            if (process.isAlive) process.destroyForcibly()
        }
        check(process.waitFor(2, SECONDS)) { "owned helper did not stop" }
    }

    private class SessionListener(private val engine: NativeCommandEngine, private val resume: Boolean) : LinkListener {
        private val ready = ArrayBlockingQueue<Binding>(1)
        val closed = ArrayBlockingQueue<String>(1)
        override fun ready(binding: Binding) {
            engine.ready(binding)
            if (resume) engine.resume()
            ready.offer(binding)
        }
        override fun frame(value: JSONObject) = engine.frame(value)
        override fun closed(reasonCode: String) {
            engine.closed(reasonCode)
            closed.offer(reasonCode)
        }
        fun awaitReady(): Binding = requireNotNull(ready.poll(6, SECONDS)) {
            "session did not complete hello; close=${closed.peek()}"
        }
    }

    private class ExactLoopback(private val allowed: String) : EndpointPolicy {
        init {
            val parsed = URI(allowed)
            require(parsed.scheme == "http" && parsed.host == "127.0.0.1" && parsed.port in 1..65535)
            require(allowed == "http://127.0.0.1:${parsed.port}")
        }
        override val requiresExplicitConnectionOrigin = true
        override fun parse(raw: String): String { require(raw == allowed); return allowed }
        override fun confirm(raw: String) = parse(raw)
        override fun authorize(raw: String) = parse(raw)
        override fun clearConsent() = Unit
    }

    private class ObservableJournal : Journal {
        private val rows = linkedMapOf<String, StoredCommand>()
        private val effects = mutableListOf<LocalEffect>()
        private val receipts = mutableMapOf<Pair<String, String>, String>()
        val acknowledged = CountDownLatch(1)
        @Synchronized override fun command(id: String) = rows[id]
        @Synchronized override fun accept(body: JSONObject): Boolean {
            val id = body.getString("commandId")
            if (rows.containsKey(id)) return false
            rows[id] = StoredCommand(id, body.getString("payloadDigest"), body.getString("attemptId"),
                "accepted", body.toString(), null, false, false)
            return true
        }
        @Synchronized override fun markRunning(id: String) {
            val row = rows.getValue(id)
            check(row.stage == "accepted" && !row.stopped)
            rows[id] = row.copy(stage = "running")
        }
        @Synchronized override fun finish(id: String, result: JSONObject, effect: LocalEffect?) {
            val row = rows.getValue(id)
            check(row.result == null)
            rows[id] = row.copy(stage = result.getString("status"), result = result.toString())
            if (effect != null) effects.add(effect)
        }
        override fun requestStop(id: String, attemptId: String) { error("unexpected stop") }
        @Synchronized override fun event(sessionId: String, eventId: String, digest: String): EventDecision {
            val key = sessionId to eventId
            val previous = receipts[key]
            if (previous == null) receipts[key] = digest
            return when (previous) {
                null -> EventDecision.NEW
                digest -> EventDecision.DUPLICATE
                else -> EventDecision.CONFLICT
            }
        }
        @Synchronized override fun pendingResults() = rows.values.filter { !it.acked && it.result != null }
            .map { JSONObject(it.result!!) }
        @Synchronized override fun pendingCommands() = rows.values.filter { it.result == null }
        @Synchronized override fun acknowledge(commandId: String) {
            val row = rows.getValue(commandId)
            check(row.result != null && !row.acked)
            rows[commandId] = row.copy(acked = true)
            acknowledged.countDown()
        }
        @Synchronized override fun recoverInterrupted() {
            check(rows.values.none { it.stage == "running" && it.result == null })
        }
        @Synchronized override fun recent() = rows.values.toList().takeLast(100)
        @Synchronized override fun effectRows() = effects.map { it.value }
        @Synchronized fun singleEffect() = effects.single()
        override fun close() = Unit
    }
}
