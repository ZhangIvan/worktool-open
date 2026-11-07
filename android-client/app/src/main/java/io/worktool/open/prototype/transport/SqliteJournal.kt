package io.worktool.open.prototype.transport

import android.content.Context
import android.database.Cursor
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import io.worktool.open.prototype.core.*
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest

/** One no-backup DB per authenticated tenant/device. No ledger mixing across pairing changes. */
class SqliteJournal(context: Context, tenantId: String, deviceId: String) : Journal {
    private val scope = "$tenantId\u0000$deviceId"
    private val filename = MessageDigest.getInstance("SHA-256").digest(scope.toByteArray()).joinToString("") { "%02x".format(it) }
    private val helper = object : SQLiteOpenHelper(context, File(context.noBackupFilesDir, "ledger-$filename.sqlite").absolutePath, null, 1) {
        override fun onConfigure(db: SQLiteDatabase) { db.setForeignKeyConstraintsEnabled(true) }
        override fun onCreate(db: SQLiteDatabase) {
            db.execSQL("CREATE TABLE commands(id TEXT PRIMARY KEY,digest TEXT NOT NULL,attempt TEXT NOT NULL,stage TEXT NOT NULL,body TEXT NOT NULL,result TEXT,acked INTEGER NOT NULL DEFAULT 0,stopped INTEGER NOT NULL DEFAULT 0)")
            db.execSQL("CREATE TABLE events(session TEXT NOT NULL,id TEXT NOT NULL,digest TEXT NOT NULL,PRIMARY KEY(session,id))")
            db.execSQL("CREATE TABLE stops(id TEXT NOT NULL,attempt TEXT NOT NULL,PRIMARY KEY(id,attempt))")
            db.execSQL("CREATE TABLE effects(command_id TEXT PRIMARY KEY REFERENCES commands(id),kind TEXT NOT NULL,reference_id TEXT NOT NULL,value TEXT NOT NULL)")
        }
        override fun onUpgrade(db: SQLiteDatabase, old: Int, new: Int) { error("MIGRATION_REQUIRED") }
    }
    private val db get() = helper.writableDatabase
    private fun <T> tx(block: () -> T): T { db.beginTransaction(); try { val r=block(); db.setTransactionSuccessful(); return r } finally { db.endTransaction() } }
    private fun row(c: Cursor) = StoredCommand(c.getString(0),c.getString(1),c.getString(2),c.getString(3),c.getString(4),if(c.isNull(5)) null else c.getString(5),c.getInt(6)!=0,c.getInt(7)!=0)
    @Synchronized override fun command(id: String): StoredCommand? = db.rawQuery("SELECT * FROM commands WHERE id=?",arrayOf(id)).use { if(it.moveToFirst()) row(it) else null }
    @Synchronized override fun accept(body: JSONObject): Boolean = tx {
        val id=body.getString("commandId"); val existing=command(id)
        if(existing!=null) { require(existing.digest==body.getString("payloadDigest") && existing.attemptId==body.getString("attemptId")) { "COMMAND_CONFLICT" }; false }
        else { db.execSQL("INSERT INTO commands(id,digest,attempt,stage,body) VALUES(?,?,?,'accepted',?)",arrayOf(id,body.getString("payloadDigest"),body.getString("attemptId"),body.toString())); db.execSQL("UPDATE commands SET stopped=1 WHERE id=? AND EXISTS(SELECT 1 FROM stops WHERE stops.id=commands.id AND stops.attempt=commands.attempt)",arrayOf(id)); true }
    }
    @Synchronized override fun markRunning(id: String) = tx {
        val c=command(id) ?: error("NOT_FOUND"); check(c.stage=="accepted" && !c.stopped && c.result==null)
        db.execSQL("UPDATE commands SET stage='running' WHERE id=?",arrayOf(id))
    }
    @Synchronized override fun finish(id: String, result: JSONObject, effect: LocalEffect?) = tx {
        val c=command(id) ?: error("NOT_FOUND")
        require(result.getString("commandId")==id && result.getString("attemptId")==c.attemptId)
        if(c.result!=null) { require(JSONObject(c.result).toString()==result.toString()) { "RESULT_CONFLICT" } }
        else {
            if(effect!=null) { check(c.stage=="running" && !c.stopped); db.execSQL("INSERT INTO effects VALUES(?,?,?,?)",arrayOf(id,effect.kind,effect.referenceId,effect.value)) }
            db.execSQL("UPDATE commands SET stage=?,result=? WHERE id=?",arrayOf(result.getString("status"),result.toString(),id))
        }
    }
    @Synchronized override fun requestStop(id: String, attemptId: String) {
        tx { db.execSQL("INSERT OR IGNORE INTO stops VALUES(?,?)",arrayOf(id,attemptId)); db.execSQL("UPDATE commands SET stopped=1 WHERE id=? AND attempt=? AND result IS NULL",arrayOf(id,attemptId)) }
    }
    @Synchronized override fun event(sessionId: String,eventId: String,digest: String): EventDecision = tx {
        val old=db.rawQuery("SELECT digest FROM events WHERE session=? AND id=?",arrayOf(sessionId,eventId)).use { if(it.moveToFirst()) it.getString(0) else null }
        if(old!=null) { if(old==digest) EventDecision.DUPLICATE else EventDecision.CONFLICT }
        else { db.execSQL("INSERT INTO events VALUES(?,?,?)",arrayOf(sessionId,eventId,digest)); EventDecision.NEW }
    }
    @Synchronized override fun pendingResults(): List<JSONObject> = db.rawQuery("SELECT result FROM commands WHERE result IS NOT NULL AND acked=0",null).use { c -> buildList { while(c.moveToNext()) add(JSONObject(c.getString(0))) } }
    @Synchronized override fun nextPendingResult(): JSONObject? = db.rawQuery("SELECT result FROM commands WHERE result IS NOT NULL AND acked=0 ORDER BY rowid LIMIT 1",null).use { c -> if(c.moveToFirst()) JSONObject(c.getString(0)) else null }
    @Synchronized override fun acknowledge(commandId: String) { db.execSQL("UPDATE commands SET acked=1 WHERE id=? AND result IS NOT NULL",arrayOf(commandId)) }
    @Synchronized override fun recoverInterrupted() = tx {
        val interrupted=db.rawQuery("SELECT * FROM commands WHERE stage='running' AND result IS NULL",null).use { c -> buildList { while(c.moveToNext()) add(row(c)) } }
        for(c in interrupted) finish(c.id,JSONObject().put("commandId",c.id).put("attemptId",c.attemptId).put("status","execution_unknown").put("effectState","unknown").put("code","PROCESS_INTERRUPTED").put("evidenceRefs",JSONArray().put("journal_recovery")))
    }
    @Synchronized override fun pendingCommands(): List<StoredCommand> = db.rawQuery("SELECT * FROM commands WHERE result IS NULL ORDER BY rowid",null).use { c -> buildList { while(c.moveToNext()) add(row(c)) } }
    @Synchronized override fun recent(): List<StoredCommand> = db.rawQuery("SELECT * FROM commands ORDER BY rowid DESC LIMIT 100",null).use { c -> buildList { while(c.moveToNext()) add(row(c)) } }
    @Synchronized override fun effectRows(): List<String> = db.rawQuery("SELECT kind,reference_id,value FROM effects ORDER BY rowid DESC LIMIT 100",null).use { c -> buildList { while(c.moveToNext()) add(JSONObject().put("kind",c.getString(0)).put("referenceId",c.getString(1)).put("value",c.getString(2)).toString()) } }
    @Synchronized override fun close() { helper.close() }
}
