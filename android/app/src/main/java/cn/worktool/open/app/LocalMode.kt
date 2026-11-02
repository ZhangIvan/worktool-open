/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.task.ActionExecutor
import cn.worktool.open.core.task.LocalEventReporter
import cn.worktool.open.core.task.LocalModeConfig
import cn.worktool.open.core.task.LocalTaskEngine
import cn.worktool.open.core.task.RuleConfigParser
import cn.worktool.open.core.task.RuleSetConfig

/**
 * 本地模式接线点（T3.3；app 壳对 core:task-engine 的最小装配）。
 *
 * 分工边界（docs/02 §3 / docs/03 车道）：
 * - 本模块只做**装配**：解析配置 → 构造引擎 → 暴露 T3.1 执行器注册入口；
 * - 指令执行器（203 发消息 / 212 通过好友等）= T3.1 的 feature:* 车道实现
 *   [ActionExecutor] 后经 [registerExecutor] 注入（合并前引擎诚实返回 NoExecutor）；
 * - 模式 B 上报 = app 把 [LocalEventReporter] 绑定到 `ProtocolClient.V2Session.sendReport`
 *   （AtLeastOnce 队列），重连合并由引擎 onReconnected 驱动；
 * - 连接状态回调（fallback 仲裁输入）= 协议层 ReconnectingWsClient 事件 →
 *   `engine.fallbackArbiter().onServerReachable()/onServerUnreachable()`。
 *
 * 规则/模式配置的持久化（用户编辑面）与真机事件源接入随后续任务卡落地。
 */
object LocalMode {

    @Volatile
    private var engineHolder: LocalTaskEngine? = null

    /**
     * 构建本地任务引擎（幂等：重复调用返回首个实例，配置热更新走重建流程——
     * 后续任务卡落地，本接线点保持最小面）。
     *
     * 安全默认（docs/02 §5.2，CR-t33-R-04）：**pureLocalMode 默认 false**——
     * 架构冻结「定时调度属服务端；端侧 cron 仅当不可达超阈值或纯本地模式，
     * fallback 默认关」。模式 A 须显式传 pureLocalMode = true（显式 opt-in），
     * 防止模式 B（已配置服务端）调用方漏传该参时事实源分工被静默翻转。
     *
     * @param modeConfigText 本地模式配置 JSON（null = 全默认：fallback 关、阈值 120s）
     * @param rulesText 规则集 JSON（null = 空规则集）
     * @param pureLocalMode true = 模式 A（未配置服务端，本地 cron 恒可用）；默认 false = 模式 B
     * @param reporter 模式 B 的 source=local 上报通道绑定（模式 A 传 null）
     */
    @Synchronized
    fun engine(
        modeConfigText: String? = null,
        rulesText: String? = null,
        pureLocalMode: Boolean = false,
        reporter: LocalEventReporter? = null,
    ): LocalTaskEngine {
        engineHolder?.let { return it }
        val config = modeConfigText?.let { RuleConfigParser.parseConfig(it) } ?: LocalModeConfig()
        val rules: RuleSetConfig = rulesText?.let { RuleConfigParser.parseRules(it) } ?: RuleSetConfig()
        return LocalTaskEngine(
            config = config,
            rules = rules,
            pureLocalMode = pureLocalMode,
            reporter = reporter,
        ).also { engineHolder = it }
    }

    /** T3.1 执行器注册（PASS_FRIEND = supportedType 212；发消息 = 203）。 */
    fun registerExecutor(executor: ActionExecutor) {
        // error() 抛 IllegalStateException：与「未装配即注册」的调用契约一致
        // （LocalModeTest 期望；CR-t33-R-01 对齐，requireNotNull 会抛 IllegalArgumentException）。
        engineHolder ?: error("先调 engine() 完成装配")
        engineHolder!!.registerExecutor(executor)
    }

    /** 释放当前引擎（测试/重建用）。 */
    @Synchronized
    fun reset() {
        engineHolder = null
    }
}
