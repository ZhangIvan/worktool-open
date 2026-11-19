package io.worktool.open.prototype.accessibility

import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import io.worktool.open.prototype.core.FixtureTicket
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest

/** Dedicated worker only. Ticket consumption + steps survive process death; no implicit retry. */
class FixtureReceiptStore(context:Context) {
    private val helper=object:SQLiteOpenHelper(context,File(context.noBackupFilesDir,"fixture-driver-receipts.sqlite").absolutePath,null,1){
        override fun onCreate(db:SQLiteDatabase){
            db.execSQL("CREATE TABLE tickets(ticket TEXT PRIMARY KEY,command_id TEXT UNIQUE NOT NULL,attempt TEXT NOT NULL,digest TEXT NOT NULL,text_digest TEXT NOT NULL,package_name TEXT NOT NULL,activity TEXT NOT NULL,window_id INTEGER NOT NULL,service_epoch INTEGER NOT NULL,foreground_epoch INTEGER NOT NULL,deadline INTEGER NOT NULL,phase TEXT NOT NULL,receipt TEXT)")
            db.execSQL("CREATE TABLE fixture_effects(ticket TEXT PRIMARY KEY REFERENCES tickets(ticket),text_value TEXT NOT NULL,receipt_id TEXT NOT NULL)")
        }
        override fun onConfigure(db:SQLiteDatabase){db.setForeignKeyConstraintsEnabled(true)}
        override fun onUpgrade(db:SQLiteDatabase,oldVersion:Int,newVersion:Int)=error("MIGRATION_REQUIRED")
    }
    private val db get()=helper.writableDatabase
    private fun <T> transaction(work:()->T):T {db.beginTransaction();try{val result=work();db.setTransactionSuccessful();return result}finally{db.endTransaction()}}
    fun consume(t:FixtureTicket):Boolean=transaction {
        val exists=db.rawQuery("SELECT 1 FROM tickets WHERE command_id=? OR ticket=?",arrayOf(t.commandId,t.ticketId)).use{it.moveToFirst()}
        if(exists) false else {db.execSQL("INSERT INTO tickets VALUES(?,?,?,?,?,?,?,?,?,?,?,'consumed',NULL)",arrayOf(t.ticketId,t.commandId,t.attemptId,t.payloadDigest,t.textDigest,t.target.targetPackage,t.target.targetActivity,t.windowId,t.serviceEpoch,t.foregroundEpoch,t.deadlineElapsedMs));true}
    }
    fun phase(ticketId:String,phase:String){require(phase in setOf("input_requested","input_changed","input_verified","click_requested","unknown","cancelled","partial","verified"));db.execSQL("UPDATE tickets SET phase=? WHERE ticket=? AND receipt IS NULL",arrayOf(phase,ticketId))}
    fun commit(t:FixtureTicket,text:String):FixtureReceipt=transaction {
        require(hash(text)==t.textDigest){"TEXT_MISMATCH"}
        val values=db.rawQuery("SELECT command_id,attempt,digest,text_digest,phase,receipt FROM tickets WHERE ticket=?",arrayOf(t.ticketId)).use{c->check(c.moveToFirst());List(6){index->if(c.isNull(index))null else c.getString(index)}}
        require(values[0]==t.commandId&&values[1]==t.attemptId&&values[2]==t.payloadDigest&&values[3]==t.textDigest&&values[4]=="click_requested"&&values[5]==null){"TICKET_NOT_COMMITTABLE"}
        val receiptId="receipt_"+hash(t.ticketId).take(40)
        val renderText="Fixture receipt $receiptId\n$text"
        val receipt=FixtureReceipt(receiptId,t.ticketId,t.commandId,t.attemptId,t.payloadDigest,t.textDigest,renderText)
        db.execSQL("INSERT INTO fixture_effects VALUES(?,?,?)",arrayOf(t.ticketId,text,receiptId))
        db.execSQL("UPDATE tickets SET phase='committed',receipt=? WHERE ticket=?",arrayOf(receipt.json().toString(),t.ticketId))
        receipt
    }
    fun receipt(ticketId:String):FixtureReceipt?=db.rawQuery("SELECT receipt FROM tickets WHERE ticket=?",arrayOf(ticketId)).use{c->if(c.moveToFirst()&&!c.isNull(0))FixtureReceipt.parse(JSONObject(c.getString(0)))else null}
    fun markInterruptedUnknown(){db.execSQL("UPDATE tickets SET phase='unknown' WHERE receipt IS NULL AND phase NOT IN ('cancelled','partial')")}
    fun close(){helper.close()}
    companion object {fun hash(value:String)=MessageDigest.getInstance("SHA-256").digest(value.toByteArray(Charsets.UTF_8)).joinToString(""){"%02x".format(it.toInt() and 255)}}
}
data class FixtureReceipt(val receiptId:String,val ticketId:String,val commandId:String,val attemptId:String,val payloadDigest:String,val textDigest:String,val renderText:String){
    fun json()=JSONObject().put("receiptId",receiptId).put("ticketId",ticketId).put("commandId",commandId).put("attemptId",attemptId).put("payloadDigest",payloadDigest).put("textDigest",textDigest).put("renderText",renderText)
    companion object {fun parse(j:JSONObject)=FixtureReceipt(j.getString("receiptId"),j.getString("ticketId"),j.getString("commandId"),j.getString("attemptId"),j.getString("payloadDigest"),j.getString("textDigest"),j.getString("renderText"))}
}
enum class FixtureButtonOutcome { MANUAL_ONLY, COMMITTED, REJECTED }
data class FixtureButtonResult(val outcome:FixtureButtonOutcome,val receipt:FixtureReceipt?=null,val reasonCode:String="")
