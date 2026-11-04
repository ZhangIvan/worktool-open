/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.content.Context
import android.content.SharedPreferences

/**
 * 安全门持久层（安卓恢复链车道）：dryRun / skipExpired / retryCount / logEnabled
 * 四门快照的 SharedPreferences 读写。
 *
 * 为什么独立于 [AppSettings]：四门是 SessionRuntime 上的 `@Volatile` 字段，进程
 * 被系统回收重建后（boot 自启 / 前台服务自愈，不经 MainActivity）字段落回默认值
 * ——干跑门静默失效 = 预览变实跑、过期跳过失效 = 陈旧指令照常执行。运行时构造时
 * 经本层恢复初始值；每次变更与停止时落盘（写即落盘，SessionRuntime 门 setter 统一走）。
 *
 * 存储与 [AppSettings] 同一 prefs 文件同一键（单一事实源）：设置页写 [AppSettings]
 * 与运行时落盘互不冲突（同键同值域）；键常量直接引用 `AppSettings.K_*`（internal），
 * 杜绝双份字面量漂移。
 *
 * JVM 可测面：[AppSettingsPersistence] 接口 + 快照为纯 Kotlin；SharedPreferences
 * 实现薄封装（读写映射），持久层读写往返回归见 AppSettingsPersistenceTest
 * （内存版 SharedPreferences 假件，不依赖 Robolectric）。
 */

/** 四门快照（AppSettings 存储形态：skip+hours 两键，运行时侧再折叠成 TTL 毫秒门）。 */
data class SecurityGatesSnapshot(
    /** FR-54 端侧干跑门（默认关）。 */
    val dryRunEnabled: Boolean,
    /** U4 过期跳过门开关（默认开）。 */
    val skipExpiredEnabled: Boolean,
    /** U4 过期判定 TTL 小时数（默认 2h；与 skipExpiredEnabled 独立存储防丢用户设定）。 */
    val expiredTtlHours: Int,
    /** 执行失败重试次数（默认 0 = 首错即停）。 */
    val retryCount: Int,
    /** 会话事件日志落盘开关（默认关）。 */
    val logEnabled: Boolean,
)

/** 安全门持久层接口（读写往返；实现决定介质——产品=SharedPreferences）。 */
interface AppSettingsPersistence {

    /** 从持久层读四门快照（介质不可读/损坏由实现抛出或返回默认，调用方 runCatching 兜底）。 */
    fun load(): SecurityGatesSnapshot

    /** 四门快照落盘（全量覆盖写；SharedPreferences.apply 异步持久化）。 */
    fun save(snapshot: SecurityGatesSnapshot)
}

/** [AppSettingsPersistence] 的 SharedPreferences 实现（与 [AppSettings] 同文件同键）。 */
class SharedPreferencesAppSettingsPersistence(private val prefs: SharedPreferences) : AppSettingsPersistence {

    /** 产品装配形态：从 Context 取 application 级 prefs（与 [AppSettings] 同文件）。 */
    constructor(context: Context) : this(
        context.applicationContext.getSharedPreferences(AppSettings.PREFS, Context.MODE_PRIVATE),
    )

    override fun load(): SecurityGatesSnapshot {
        val skipExpired = prefs.getBoolean(AppSettings.K_SKIP_EXPIRED, DEFAULT_SKIP_EXPIRED)
        return SecurityGatesSnapshot(
            dryRunEnabled = prefs.getBoolean(AppSettings.K_DRY_RUN, false),
            skipExpiredEnabled = skipExpired,
            expiredTtlHours = prefs.getInt(AppSettings.K_EXPIRED_TTL_HOURS, DEFAULT_TTL_HOURS),
            retryCount = prefs.getInt(AppSettings.K_RETRY_COUNT, 0),
            logEnabled = prefs.getBoolean(AppSettings.K_LOG, false),
        )
    }

    override fun save(snapshot: SecurityGatesSnapshot) {
        prefs.edit()
            .putBoolean(AppSettings.K_DRY_RUN, snapshot.dryRunEnabled)
            .putBoolean(AppSettings.K_SKIP_EXPIRED, snapshot.skipExpiredEnabled)
            .putInt(AppSettings.K_EXPIRED_TTL_HOURS, snapshot.expiredTtlHours)
            .putInt(AppSettings.K_RETRY_COUNT, snapshot.retryCount)
            .putBoolean(AppSettings.K_LOG, snapshot.logEnabled)
            .apply()
    }

    private companion object {
        /** 空存储默认值（与 [AppSettings] 各键缺省口径一致：干跑关/跳过开/2h/重试 0/日志关）。 */
        const val DEFAULT_SKIP_EXPIRED = true
        const val DEFAULT_TTL_HOURS = 2
    }
}
