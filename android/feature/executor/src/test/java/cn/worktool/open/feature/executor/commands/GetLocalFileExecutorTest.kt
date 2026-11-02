/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.RecordingQuerySink
import cn.worktool.open.feature.executor.fixture.LocalFileWorld
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.query.QueryResult
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 509 GET_LOCAL_FILE 执行器场景（每执行器 ≥5；docs/05 P0 首批行：
 * 语义=上传本地文件（默认当前日志，audit/02 F30），dry-run=报告文件+大小）。
 */
class GetLocalFileExecutorTest {

    private val sink = RecordingQuerySink()

    private fun registry(world: LocalFileWorld) =
        ExecutorRegistry.of(GetLocalFileExecutor(world.filePort, sink))

    private fun runner(world: LocalFileWorld) = CommandRunner(world.driver, world.gate)

    // ---- ① 默认当前日志：dry-run 与真实执行同报「哪个文件 + 大小」 ----

    @Test
    fun `默认日志-执行与干跑同报文件名与大小`() {
        val world = LocalFileWorld()
        val intent = CommandIntent.GetLocalFile()

        val dry = runner(world).dryRun(intent, registry(world))
        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertTrue("干跑报告含文件名", dry.paramsSummary.contains("file=robot.log"))
        assertTrue("干跑报告含大小", dry.paramsSummary.contains("sizeBytes=4096"))
        assertTrue(
            "干跑步骤描述含上传目标与大小",
            dry.plannedSteps.any { it.contains("robot.log") },
        )

        val report = runner(world).execute(intent, registry(world))
        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val result = sink.results.single().second as QueryResult.LocalFile
        assertEquals("robot.log", result.fileName)
        assertEquals(4096L, result.sizeBytes)
        assertEquals("/data/local/tmp/worktool/logs/robot.log", result.path)
    }

    // ---- ② 显式路径 ----

    @Test
    fun `显式路径-按指令指定文件报告`() {
        val world = LocalFileWorld()
        val report = runner(world).execute(
            CommandIntent.GetLocalFile(filePath = "/sdcard/Download/手册.pdf"),
            registry(world),
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        val result = sink.results.single().second as QueryResult.LocalFile
        assertEquals("手册.pdf", result.fileName)
        assertEquals(1_048_576L, result.sizeBytes)
    }

    // ---- ③ 文件缺失 ----

    @Test
    fun `文件缺失-LOCAL_FILE_MISSING且零结果`() {
        val world = LocalFileWorld()
        val intent = CommandIntent.GetLocalFile(filePath = "/no/such/file.txt")

        val dry = runner(world).dryRun(intent, registry(world))
        assertTrue("干跑如实标注缺失", dry.paramsSummary.contains("不存在"))

        val report = runner(world).execute(intent, registry(world))
        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.LOCAL_FILE_MISSING, report.errorCode)
        assertEquals("resolve-file", report.errorStepId)
        assertEquals(0, sink.results.size)
    }

    // ---- ④ 暂停拦截（FR-52） ----

    @Test
    fun `暂停拦截-执行前已暂停则零结果PAUSED`() {
        val world = LocalFileWorld()
        world.gate.paused = true
        world.gate.source = "auto-breaker"
        val report = runner(world).execute(CommandIntent.GetLocalFile(), registry(world))

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(ExecutorErrorCodes.PAUSED_BY_USER_TAKEOVER, report.errorCode)
        assertEquals("auto-breaker", report.pausedBySource)
        assertEquals(0, sink.results.size)
    }

    // ---- ⑤ 干跑（FR-54）：零副作用且不触文件投递 ----

    @Test
    fun `干跑-零界面动作零结果输出`() {
        val world = LocalFileWorld()
        val dry = runner(world).dryRun(CommandIntent.GetLocalFile(), registry(world))

        assertEquals(MessageTypes.GET_LOCAL_FILE, dry.commandType)
        assertEquals(RiskLevel.L0, dry.riskLevel)
        assertEquals(0, world.driver.actionLog.size)
        assertEquals("干跑不出结果端口", 0, sink.results.size)
    }

    // ---- ⑥ 计划与执行间文件消失（滚动删除）→ 执行期复检拦截 ----

    @Test
    fun `窗口期消失-干跑时在执行时已删则复检失败`() {
        val backing = mutableMapOf("/data/local/tmp/worktool/logs/robot.log" to 4096L)
        val world = LocalFileWorld(files = backing, defaultLogPath = "/data/local/tmp/worktool/logs/robot.log")
        val intent = CommandIntent.GetLocalFile()

        val dry = runner(world).dryRun(intent, registry(world))
        assertTrue(dry.paramsSummary.contains("sizeBytes=4096"))

        backing.remove("/data/local/tmp/worktool/logs/robot.log") // 计划后日志滚动删除
        val report = runner(world).execute(intent, registry(world))

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.LOCAL_FILE_MISSING, report.errorCode)
        assertEquals("resolve-file", report.errorStepId)
        assertEquals(0, sink.results.size)
    }
}
