/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeRemoteFiles
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingBatchSink
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fixture.FilePickerFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.MicroDiskFixture
import cn.worktool.open.feature.executor.fixture.PlusPanelFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 218/224/208/209 推送内容族执行器场景（docs/10 B2）：
 * 218 成功（下载→选择→发送）/ 下载失败 / 224 成功（icon 降级）/
 * 208/209 成功 / 微盘无此文件 / 暂停拦截（L1）/ 干跑。
 */
class PushExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val sink = RecordingBatchSink()

    private val fileUrl = "https://cdn.example.com/static/logo.png"

    /** 聊天 + 「+」面板 → 文件选择器（218）。 */
    private fun fileWorld(files: List<String> = listOf("logo.png")): Triple<FakeUiDriver, ChatFixture, FakeRemoteFiles> {
        val home = HomeFixture(conversations = listOf("仑哥", "其他群"))
        val chat = ChatFixture(title = "仑哥")
        val plus = PlusPanelFixture(entries = setOf("文件"))
        val picker = FilePickerFixture(files = files)
        val remote = FakeRemoteFiles(reachable = setOf(fileUrl))
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("仑哥")) { push(chat.screen) }
            onClick(chat.screen, chat.sendNode!!) { chat.commitInputAsMessage() }
            chat.inputNode.let { onClick(chat.screen, it) { } } // 输入框点击停留
            // 「+」入口挂在聊天页（RichChatFixture 简化：直接在输入框旁挂 + 节点）
            val plusEntry = cn.worktool.open.feature.executor.fake.fakeNode(text = "+", clickable = true)
            chat.screen.root.add(plusEntry)
            onClick(chat.screen, plusEntry) { push(plus.screen) }
            plus.entryNodes["文件"]?.let { fileEntry -> onClick(plus.screen, fileEntry) { push(picker.screen) } }
            picker.fileNodes.values.forEach { f -> onClick(picker.screen, f) { pop(); pop() } }
        }
        return Triple(driver, chat, remote)
    }

    private val pushFileIntent = CommandIntent.PushFile(
        targets = listOf("仑哥"),
        objectName = "logo.png",
        fileUrl = fileUrl,
        fileType = "image",
    )

    @Test
    fun `218成功-下载选择发送`() {
        val (driver, chat, remote) = fileWorld()
        val registry = ExecutorRegistry.of(PushFileExecutor(remote, sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(pushFileIntent, registry)

        assertEquals(
            "errorStep=${report.errorStepId} reason=${report.errorReason}",
            ExecutionStatus.SUCCEEDED,
            report.status,
        )
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertTrue(sink.batches.single().second.single().succeeded)
        assertTrue("应有下载动作", remote.fetched.isNotEmpty())
    }

    @Test
    fun `218负例-下载失败整指令失败零目标执行`() {
        val (driver, _, remoteUnreachable) = fileWorld().let { Triple(it.first, it.second, FakeRemoteFiles(reachable = emptySet())) }
        val registry = ExecutorRegistry.of(PushFileExecutor(remoteUnreachable, sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(pushFileIntent, registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.DOWNLOAD_FAILED, report.errorCode)
        assertEquals("fetch-file", report.errorStepId)
        assertEquals("零矩阵外发", 0, sink.batches.size)
    }

    @Test
    fun `218干跑-零下载零动作`() {
        val (driver, _, remote) = fileWorld()
        val registry = ExecutorRegistry.of(PushFileExecutor(remote, sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(pushFileIntent, registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, remote.fetched.size)
        assertTrue(dry.plannedSteps.first().contains("fetch-file"))
    }

    // ---- 224 ----

    @Test
    fun `224成功-链接卡片发送且icon失败降级`() {
        val home = HomeFixture(conversations = listOf("测试群007"))
        val chat = ChatFixture(title = "测试群007")
        val remote = FakeRemoteFiles(reachable = emptySet()) // icon 不可达 → 降级
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("测试群007")) { push(chat.screen) }
            onClick(chat.screen, chat.sendNode!!) { chat.commitInputAsMessage() }
        }
        val registry = ExecutorRegistry.of(PushLinkExecutor(remote, sink, timeouts = timeouts))
        val intent = CommandIntent.PushLink(
            targets = listOf("测试群007"),
            title = "WorkTool 文档",
            description = "企业微信自动化开放平台",
            url = "https://worktool.example.com/",
            iconUrl = "https://cdn.example.com/icon.png",
        )
        val report = CommandRunner(driver, gate).execute(intent, registry)

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue("URL 已上屏", chat.messageTexts().any { it?.contains("https://worktool.example.com/") == true })
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `224负例-目标会话缺失记败`() {
        val home = HomeFixture(conversations = listOf("其他群"))
        val chat = ChatFixture(title = "其他群")
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("其他群")) { push(chat.screen) }
            onClick(chat.screen, chat.sendNode!!) { chat.commitInputAsMessage() }
        }
        val registry = ExecutorRegistry.of(PushLinkExecutor(FakeRemoteFiles(emptySet()), sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushLink(listOf("测试群007"), "标题", "副标题", "https://worktool.example.com/"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertTrue(!sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `224干跑-零动作零矩阵`() {
        val home = HomeFixture(conversations = listOf("测试群007"))
        val driver = FakeUiDriver(clock).apply { show(home.screen) }
        val registry = ExecutorRegistry.of(PushLinkExecutor(FakeRemoteFiles(emptySet()), sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.PushLink(listOf("测试群007"), "标题", "副标题", "https://worktool.example.com/"),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }

    // ---- 208/209 ----

    private fun microDiskWorld(items: List<String> = listOf("logo2")): Triple<FakeUiDriver, ChatFixture, FakeUiDriver.() -> Unit> {
        val home = HomeFixture(conversations = listOf("仑哥"))
        val chat = ChatFixture(title = "仑哥")
        val plus = PlusPanelFixture(entries = setOf("微盘"))
        val disk = MicroDiskFixture(items = items)
        val wiring: FakeUiDriver.() -> Unit = {
            show(home.screen)
            onClick(home.screen, home.conversationNodes.getValue("仑哥")) { push(chat.screen) }
            val plusEntry = cn.worktool.open.feature.executor.fake.fakeNode(text = "+", clickable = true)
            chat.screen.root.add(plusEntry)
            onClick(chat.screen, plusEntry) { push(plus.screen) }
            plus.entryNodes["微盘"]?.let { entry -> onClick(plus.screen, entry) { push(disk.screen) } }
            disk.itemNodes.values.forEach { item -> onClick(disk.screen, item) { } }
            onClick(disk.screen, disk.sendNode!!) { pop() }
        }
        val driver = FakeUiDriver(clock).apply(wiring)
        return Triple(driver, chat, wiring)
    }

    @Test
    fun `208成功-微盘图片选择发送`() {
        val (driver, _, _) = microDiskWorld()
        val registry = ExecutorRegistry.of(PushMicroDiskImageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroDiskImage(listOf("仑哥"), "logo2"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        // 文件源=微盘已存（与 218 的 fileUrl 下载分流：本执行器无 RemoteFilePort 依赖，类型上零下载）
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `208负例-微盘无此图片`() {
        val (driver, _, _) = microDiskWorld(items = listOf("别的图.png"))
        val registry = ExecutorRegistry.of(PushMicroDiskImageExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroDiskImage(listOf("仑哥"), "logo2"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertTrue(!outcome.succeeded)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, outcome.errorCode)
    }

    @Test
    fun `209成功-微盘文件选择发送`() {
        val (driver, _, _) = microDiskWorld(items = listOf("报告.pdf"))
        val registry = ExecutorRegistry.of(PushMicroDiskFileExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroDiskFile(listOf("仑哥"), "报告.pdf"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertTrue(sink.batches.single().second.single().succeeded)
    }

    @Test
    fun `209负例-微盘无此文件`() {
        val (driver, _, _) = microDiskWorld(items = listOf("别的文件.pdf"))
        val registry = ExecutorRegistry.of(PushMicroDiskFileExecutor(sink, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroDiskFile(listOf("仑哥"), "logo2"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        val outcome = sink.batches.single().second.single()
        assertTrue(!outcome.succeeded)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, outcome.errorCode)
    }

    @Test
    fun `208暂停拦截-L1零副作用`() {
        val (driver, _, _) = microDiskWorld()
        val registry = ExecutorRegistry.of(PushMicroDiskImageExecutor(sink, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.PushMicroDiskImage(listOf("仑哥"), "logo2"),
            registry,
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, sink.batches.size)
    }

    @Test
    fun `209干跑-零动作`() {
        val (driver, _, _) = microDiskWorld()
        val registry = ExecutorRegistry.of(PushMicroDiskFileExecutor(sink, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.PushMicroDiskFile(listOf("仑哥"), "logo2"),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }
}
