package io.worktool.open.prototype.policy

import io.worktool.open.prototype.core.*
import org.json.JSONArray
import org.json.JSONObject

enum class ConnectionMode(val wireVersion: String) {
    SYNTHETIC_1_0("1.0"), OWN_FIXTURE_1_1("1.1");
    fun allowed(ownFixtureFeature: Boolean): Boolean = this == SYNTHETIC_1_0 || ownFixtureFeature
}

/** Captured per explicit connection; no capability elevation from remote frames or service state. */
data class ConnectionProfile(val mode: ConnectionMode, val fixtureTarget: FixtureTarget? = null) {
    init {
        require((mode == ConnectionMode.OWN_FIXTURE_1_1) == (fixtureTarget != null))
        fixtureTarget?.let {
            require(it.targetPackage == "io.worktool.open.prototype" &&
                it.targetActivity == "io.worktool.open.prototype.ui.FixtureActivity")
        }
    }
    fun hello(): JSONObject {
        val caps = JSONArray().put("message.send_text").put("file.export")
        val hello = JSONObject().put("kind", "hello").put("supportedVersions", JSONArray().put(mode.wireVersion))
            .put("mode", "synthetic").put("capabilities", caps)
        fixtureTarget?.let {
            caps.put("fixture.ui.submit_text")
            hello.put("drivers", JSONArray().put(JSONObject().put("driverId", "fixture.ui").put("driverVersion", 1)
                .put("targetPackage", it.targetPackage).put("targetActivity", it.targetActivity)))
        }
        return hello
    }
    companion object {
        val SYNTHETIC = ConnectionProfile(ConnectionMode.SYNTHETIC_1_0)
        /** Full journal gate, before engine.ready/recovery/resend. Never uses recent(100). */
        fun canRecover(version: String, journal: Journal): Boolean {
            require(version in setOf("1.0", "1.1"))
            if (version == "1.1") return true
            fun fixture(row: StoredCommand): Boolean {
                val body = JSONObject(row.body)
                require(body.getString("commandId") == row.id)
                return body.getString("capability") == "fixture.ui.submit_text"
            }
            if (journal.pendingCommands().any { fixture(it) }) return false
            return journal.pendingResults().none { result ->
                val row = journal.command(result.getString("commandId")) ?: error("RESULT_COMMAND_MISSING")
                require(row.result != null && !row.acked)
                fixture(row)
            }
        }
    }
}
