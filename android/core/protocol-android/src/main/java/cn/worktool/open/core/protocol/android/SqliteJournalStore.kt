/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol.android

import android.content.ContentValues
import android.content.Context
import android.database.sqlite.SQLiteDatabase
import android.database.sqlite.SQLiteOpenHelper
import cn.worktool.open.core.protocol.delivery.JournalEntry
import cn.worktool.open.core.protocol.delivery.JournalState
import cn.worktool.open.core.protocol.delivery.JournalStore

/**
 * SQLite journal 落盘实现（T2.2 ④：AtLeastOnce 队列的持久层；
 * 语义与 JVM 单测中的 InMemoryJournalStore 等价，接口 = :core:protocol JournalStore）。
 *
 * 崩溃恢复：进程重启后重新打开数据库，[entries] 返回全部未完成条目由队列
 * flushPending 重放（eventId 不变 → 服务端幂等回执，spec §5.4.3）。
 * 线程模型：单连接 + 全接口互斥（写频度低：每帧一次 append/update）。
 *
 * 设备侧行为验证 = T2.5 真机联调（deferred；表结构与 SQL 变更须同步 :core:protocol 单测）。
 */
class SqliteJournalStore(context: Context, dbName: String = "worktool_protocol_journal.db") :
    JournalStore {

    private val helper = Helper(context.applicationContext, dbName)

    override fun append(entry: JournalEntry) = synchronized(helper) {
        helper.writableDatabase.execSQL(
            "INSERT OR IGNORE INTO outbound(event_id, frame_text, created_at, state, attempts) VALUES (?,?,?,?,?)",
            arrayOf(entry.eventId, entry.frameText, entry.createdAt, entry.state.name, entry.attempts),
        )
    }

    override fun update(eventId: String, state: JournalState, attempts: Int) = synchronized(helper) {
        val values = ContentValues().apply {
            put("state", state.name)
            put("attempts", attempts)
        }
        helper.writableDatabase.update("outbound", values, "event_id = ?", arrayOf(eventId))
    }

    override fun entries(): List<JournalEntry> = synchronized(helper) {
        val out = mutableListOf<JournalEntry>()
        helper.readableDatabase.query(
            "outbound",
            arrayOf("event_id", "frame_text", "created_at", "state", "attempts"),
            null, null, null, null, "created_at ASC",
        ).use { cursor ->
            while (cursor.moveToNext()) {
                out += JournalEntry(
                    eventId = cursor.getString(0),
                    frameText = cursor.getString(1),
                    createdAt = cursor.getLong(2),
                    state = runCatching { JournalState.valueOf(cursor.getString(3)) }
                        .getOrDefault(JournalState.PENDING),
                    attempts = cursor.getInt(4),
                )
            }
        }
        out
    }

    override fun recordInboundStart(commandId: String, type: Int, epoch: Long): Boolean =
        synchronized(helper) {
            fun count(): Int = helper.readableDatabase.rawQuery(
                "SELECT COUNT(*) FROM inbound WHERE command_id = ?",
                arrayOf(commandId),
            ).use { it.moveToFirst(); it.getInt(0) }

            val before = count()
            helper.writableDatabase.execSQL(
                "INSERT OR IGNORE INTO inbound(command_id, type, epoch, started_at) VALUES (?,?,?,?)",
                arrayOf(commandId, type, epoch, System.currentTimeMillis()),
            )
            // 本次调用确实插入（此前不存在）→ true = 首次启动，允许副作用
            count() > before
        }

    override fun appliedEpoch(): Long = synchronized(helper) {
        helper.readableDatabase.rawQuery("SELECT value FROM meta WHERE key = 'appliedEpoch'", null).use {
            if (it.moveToFirst()) it.getLong(0) else 0L
        }
    }

    override fun setAppliedEpoch(epoch: Long) = synchronized(helper) {
        helper.writableDatabase.execSQL(
            "INSERT OR REPLACE INTO meta(key, value) VALUES ('appliedEpoch', ?)",
            arrayOf(epoch),
        )
    }

    private class Helper(context: Context, name: String) :
        SQLiteOpenHelper(context, name, null, SCHEMA_VERSION) {

        override fun onCreate(db: SQLiteDatabase) {
            db.execSQL(
                """CREATE TABLE IF NOT EXISTS outbound (
                    event_id TEXT PRIMARY KEY,
                    frame_text TEXT NOT NULL,
                    created_at INTEGER NOT NULL,
                    state TEXT NOT NULL,
                    attempts INTEGER NOT NULL DEFAULT 0
                )""",
            )
            db.execSQL(
                """CREATE TABLE IF NOT EXISTS inbound (
                    command_id TEXT PRIMARY KEY,
                    type INTEGER NOT NULL,
                    epoch INTEGER NOT NULL,
                    started_at INTEGER NOT NULL
                )""",
            )
            db.execSQL("CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value INTEGER NOT NULL)")
        }

        override fun onUpgrade(db: SQLiteDatabase, oldVersion: Int, newVersion: Int) {
            // v1 起步：无历史版本迁移；破坏性变更须按 docs/03 流程登记
        }

        companion object {
            const val SCHEMA_VERSION = 1
        }
    }
}
