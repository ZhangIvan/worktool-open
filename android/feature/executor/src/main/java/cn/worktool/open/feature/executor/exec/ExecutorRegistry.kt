/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.exec

import cn.worktool.open.feature.executor.commands.InitGroupExecutor
import cn.worktool.open.feature.executor.commands.ModifyGroupMemberInfoExecutor
import cn.worktool.open.feature.executor.commands.PassFriendExecutor
import cn.worktool.open.feature.executor.commands.SendTextExecutor
import cn.worktool.open.feature.executor.commands.UpdateGroupExecutor
import cn.worktool.open.feature.executor.intent.IntentNormalizer

/**
 * 执行器注册表：指令号 → 执行器路由（docs/02 §3「feature:* 按 201~234 对应」）。
 *
 * 注册表是 [IntentNormalizer] 支持集的单一事实来源（normalizer 构造时取
 * [supportedTypes]），避免两处清单漂移。
 */
class ExecutorRegistry private constructor(
    private val executors: Map<Int, CommandExecutor>,
) {

    fun executorFor(commandType: Int): CommandExecutor? = executors[commandType]

    fun supports(commandType: Int): Boolean = executors.containsKey(commandType)

    val supportedTypes: Set<Int> get() = executors.keys

    companion object {

        /** 注册执行器集合；指令号重复 = 接线错误，立即失败。 */
        fun of(vararg executors: CommandExecutor): ExecutorRegistry {
            val map = LinkedHashMap<Int, CommandExecutor>()
            for (e in executors) {
                val prev = map.put(e.commandType, e)
                require(prev == null) {
                    "指令号 ${e.commandType} 重复注册（${prev!!::class.simpleName} vs ${e::class.simpleName}）"
                }
            }
            return ExecutorRegistry(map)
        }

        /**
         * T3.1 P0 默认注册表：203/206/207/225/212
         * （docs/03 T3.1 P0 子集 = FR-02/09/10/15）。
         */
        fun defaultP0(): ExecutorRegistry = of(
            SendTextExecutor(),
            InitGroupExecutor(),
            UpdateGroupExecutor(),
            ModifyGroupMemberInfoExecutor(),
            PassFriendExecutor(),
        )
    }
}
