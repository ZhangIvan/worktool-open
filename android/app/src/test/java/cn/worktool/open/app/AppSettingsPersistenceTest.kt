/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.SharedPreferences
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 安卓恢复链车道回归（持久层读写往返，纯 JVM）：
 * - SharedPreferencesAppSettingsPersistence 在内存版 SharedPreferences 假件上
 *   「save → 新实例 load」全值往返一致（模拟进程重建后重读）；
 * - 空存储缺省口径与 AppSettings 各键默认一致（干跑关/跳过开/2h/重试 0/日志关）；
 * - 落盘走 AppSettings 同键（同 prefs 文件，与设置页互覆不冲突——键字面量编译期同源）。
 */
class AppSettingsPersistenceTest {

    /** 内存版 SharedPreferences 假件（JVM 无 Robolectric；接口直实现，行为=同步 Map）。 */
    private class InMemoryPrefs : SharedPreferences {

        val map = HashMap<String, Any?>()

        override fun getAll(): MutableMap<String, *> = HashMap(map)

        override fun getString(key: String, defValue: String?): String? =
            map[key] as? String ?: defValue

        override fun getStringSet(key: String, defValues: MutableSet<String>?): MutableSet<String>? =
            @Suppress("UNCHECKED_CAST") (map[key] as? MutableSet<String>) ?: defValues

        override fun getInt(key: String, defValue: Int): Int = map[key] as? Int ?: defValue

        override fun getLong(key: String, defValue: Long): Long = map[key] as? Long ?: defValue

        override fun getFloat(key: String, defValue: Float): Float = map[key] as? Float ?: defValue

        override fun getBoolean(key: String, defValue: Boolean): Boolean =
            map[key] as? Boolean ?: defValue

        override fun contains(key: String): Boolean = map.containsKey(key)

        override fun edit(): SharedPreferences.Editor = InMemoryEditor()

        override fun registerOnSharedPreferenceChangeListener(l: SharedPreferences.OnSharedPreferenceChangeListener?) = Unit

        override fun unregisterOnSharedPreferenceChangeListener(l: SharedPreferences.OnSharedPreferenceChangeListener?) = Unit

        private inner class InMemoryEditor : SharedPreferences.Editor {
            private val pending = LinkedHashMap<String, Any?>()
            private val removals = mutableSetOf<String>()
            private var clearAll = false

            override fun putString(key: String, value: String?): SharedPreferences.Editor =
                apply { pending[key] = value }

            override fun putStringSet(key: String, values: MutableSet<String>?): SharedPreferences.Editor =
                apply { pending[key] = values }

            override fun putInt(key: String, value: Int): SharedPreferences.Editor =
                apply { pending[key] = value }

            override fun putLong(key: String, value: Long): SharedPreferences.Editor =
                apply { pending[key] = value }

            override fun putFloat(key: String, value: Float): SharedPreferences.Editor =
                apply { pending[key] = value }

            override fun putBoolean(key: String, value: Boolean): SharedPreferences.Editor =
                apply { pending[key] = value }

            override fun remove(key: String): SharedPreferences.Editor =
                apply { removals += key }

            override fun clear(): SharedPreferences.Editor = apply { clearAll = true }

            override fun commit(): Boolean = apply { applyChanges() }.let { true }

            override fun apply() = applyChanges()

            private fun applyChanges() {
                if (clearAll) map.clear()
                removals.forEach { map.remove(it); pending.remove(it) }
                map.putAll(pending)
                pending.clear(); removals.clear(); clearAll = false
            }
        }
    }

    @Test
    fun `读写往返-save后新实例load全值一致`() {
        val prefs = InMemoryPrefs()
        val snapshot = SecurityGatesSnapshot(
            dryRunEnabled = true,
            skipExpiredEnabled = false,
            expiredTtlHours = 5,
            retryCount = 3,
            logEnabled = true,
        )
        SharedPreferencesAppSettingsPersistence(prefs).save(snapshot)
        // 「进程重建」模拟：同一存储上的全新持久层实例（构造即 load 的运行时侧读法）
        val reloaded = SharedPreferencesAppSettingsPersistence(prefs).load()

        assertEquals(snapshot, reloaded)
        assertTrue(reloaded.dryRunEnabled)
        assertFalse(reloaded.skipExpiredEnabled)
        assertEquals(5, reloaded.expiredTtlHours)
        assertEquals(3, reloaded.retryCount)
        assertTrue(reloaded.logEnabled)
    }

    @Test
    fun `读写往返-覆盖写取最新快照`() {
        val prefs = InMemoryPrefs()
        val store = SharedPreferencesAppSettingsPersistence(prefs)
        store.save(SecurityGatesSnapshot(false, true, 2, 0, false))
        store.save(SecurityGatesSnapshot(true, true, 8, 4, true))

        assertEquals(SecurityGatesSnapshot(true, true, 8, 4, true), store.load())
    }

    @Test
    fun `空存储load-缺省与AppSettings各键默认同口径`() {
        val loaded = SharedPreferencesAppSettingsPersistence(InMemoryPrefs()).load()

        assertEquals("干跑门默认关", false, loaded.dryRunEnabled)
        assertEquals("过期跳过默认开", true, loaded.skipExpiredEnabled)
        assertEquals("过期 TTL 默认 2h", 2, loaded.expiredTtlHours)
        assertEquals("重试次数默认 0", 0, loaded.retryCount)
        assertEquals("日志落盘默认关", false, loaded.logEnabled)
    }

    @Test
    fun `save落盘走AppSettings同键-单一事实源不触碰server键`() {
        val prefs = InMemoryPrefs()
        SharedPreferencesAppSettingsPersistence(prefs).save(
            SecurityGatesSnapshot(dryRunEnabled = true, skipExpiredEnabled = true, expiredTtlHours = 6, retryCount = 2, logEnabled = true),
        )

        // 键常量编译期引用 AppSettings.K_*（internal companion）——同文件同键，无字面量漂移
        assertEquals(true, prefs.map[AppSettings.K_DRY_RUN])
        assertEquals(true, prefs.map[AppSettings.K_SKIP_EXPIRED])
        assertEquals(6, prefs.map[AppSettings.K_EXPIRED_TTL_HOURS])
        assertEquals(2, prefs.map[AppSettings.K_RETRY_COUNT])
        assertEquals(true, prefs.map[AppSettings.K_LOG])
        assertFalse("持久层不触碰 server 地址键", prefs.map.containsKey(AppSettings.K_SERVER))
        assertNull(prefs.map[AppSettings.K_SERVER])
    }
}
