/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeQrJoin
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.TEXT_VIEW
import cn.worktool.open.feature.executor.fake.fakeNode
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fake.FakeScreen
import cn.worktool.open.feature.executor.fixture.ScanResultFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.ports.QrJoinOutcome
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 233 扫一扫 + 243 二维码入群 + 215/216/217/231/232 排除性占位执行器场景。
 */
class ScanAndUnimplementedExecutorsTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()

    // ---- 233 ----

    private fun scanWorld(withJoinButton: Boolean = true): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        // 主界面「+」菜单挂「扫一扫」入口（HomeFixture 复用 plusButton 流简化：直接挂主界面）
        val scanEntry = fakeNode(text = "扫一扫", cls = TEXT_VIEW, clickable = true)
        home.screen.root.add(scanEntry)
        // 扫一扫页（相册入口）
        val albumScreen = FakeScreen(
            name = "scan",
            root = fakeNode(cls = cn.worktool.open.feature.executor.fake.VIEW_GROUP) {
                add(fakeNode(text = "相册", cls = TEXT_VIEW, clickable = true))
            },
        )
        val result = ScanResultFixture(withJoin = withJoinButton)
        if (!withJoinButton) result.screen.root.add(fakeNode(text = "二维码", cls = TEXT_VIEW))
        val driver = FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, scanEntry) { push(albumScreen) }
            albumScreen.root.children.first { it.textValue == "相册" }.let { album ->
                onClick(albumScreen, album) { push(result.screen) }
            }
            result.joinNode?.let { onClick(result.screen, it) { /* 入群成功 */ } }
        }
        return driver
    }

    @Test
    fun `233成功-相册扫码入群`() {
        val driver = scanWorld()
        val registry = ExecutorRegistry.of(ScanQrCodeExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.ScanQrCode(qrContent = "https://work.weixin.qq.com/ca/xxxx"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L0, report.riskLevel)
        assertEquals("join-group", report.executedSteps.last().id)
    }

    @Test
    fun `233负例-扫码结果无加入入口`() {
        val driver = scanWorld(withJoinButton = false)
        val registry = ExecutorRegistry.of(ScanQrCodeExecutor(timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(CommandIntent.ScanQrCode(), registry)

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertTrue(report.errorReason.orEmpty().contains("加入群聊"))
    }

    @Test
    fun `233干跑-零动作`() {
        val driver = scanWorld()
        val registry = ExecutorRegistry.of(ScanQrCodeExecutor(timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(CommandIntent.ScanQrCode(), registry)
        assertEquals(0, driver.actionLog.size)
        assertEquals(4, dry.plannedSteps.size)
    }

    // ---- 243 ----

    @Test
    fun `243成功-下载写相册并个微扫码入群`() {
        val qrJoin = FakeQrJoin(albumOk = true, joinOutcome = QrJoinOutcome.JOINED)
        val driver = FakeUiDriver(clock)
        val registry = ExecutorRegistry.of(QrCodeJoinGroupExecutor(qrJoin))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.QrCodeJoinGroup(fileUrl = "https://example.com/group-qr.jpg"),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        assertEquals(listOf("https://example.com/group-qr.jpg"), qrJoin.savedUrls)
        assertEquals(1, qrJoin.joinAttempts)
    }

    @Test
    fun `243负例-二维码过期`() {
        val qrJoin = FakeQrJoin(joinOutcome = QrJoinOutcome.QR_EXPIRED)
        val driver = FakeUiDriver(clock)
        val registry = ExecutorRegistry.of(QrCodeJoinGroupExecutor(qrJoin))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.QrCodeJoinGroup(fileUrl = "https://example.com/expired.jpg"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.QR_CODE_INVALID, report.errorCode)
        assertEquals("wechat-scan-album-join", report.errorStepId)
    }

    @Test
    fun `243负例-下载写相册失败`() {
        val qrJoin = FakeQrJoin(albumOk = false)
        val driver = FakeUiDriver(clock)
        val registry = ExecutorRegistry.of(QrCodeJoinGroupExecutor(qrJoin))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.QrCodeJoinGroup(fileUrl = "https://example.com/x.jpg"),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.DOWNLOAD_FAILED, report.errorCode)
        assertEquals("零次扫码尝试", 0, qrJoin.joinAttempts)
    }

    @Test
    fun `243暂停拦截-零下载零扫码`() {
        val qrJoin = FakeQrJoin()
        val driver = FakeUiDriver(clock)
        val registry = ExecutorRegistry.of(QrCodeJoinGroupExecutor(qrJoin))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.QrCodeJoinGroup(fileUrl = "https://example.com/x.jpg"),
            registry,
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, qrJoin.savedUrls.size)
        assertEquals(0, qrJoin.joinAttempts)
    }

    // ---- 215/216/217/231/232（排除性占位：显式「未实现」回执） ----

    private val unimplementedTypes = listOf(
        MessageTypes.INIT_ANTI_HARASSMENT_RULE,
        MessageTypes.UPDATE_ANTI_HARASSMENT_RULE,
        MessageTypes.DELETED_ANTI_HARASSMENT_RULE,
        MessageTypes.VOICE_CALL,
        MessageTypes.VIDEO_CALL,
    )

    @Test
    fun `未实现族-五指令显式未实现回执且零界面动作`() {
        for (type in unimplementedTypes) {
            val driver = FakeUiDriver(clock)
            val registry = ExecutorRegistry.of(UnimplementedCommandExecutor(type))
            val report = CommandRunner(driver, FakePauseGate())
                .execute(CommandIntent.UnimplementedCall(type), registry)

            assertEquals("type=$type", ExecutionStatus.FAILED, report.status)
            assertEquals("type=$type", ExecutorErrorCodes.COMMAND_NOT_IMPLEMENTED, report.errorCode)
            assertEquals("type=$type", "reply-not-implemented", report.errorStepId)
            assertEquals("type=$type 零界面动作", 0, driver.actionLog.size)
            assertEquals(RiskLevel.L1, report.riskLevel)
        }
    }

    @Test
    fun `未实现族-干跑仍产出计划报告`() {
        for (type in unimplementedTypes) {
            val driver = FakeUiDriver(clock)
            val registry = ExecutorRegistry.of(UnimplementedCommandExecutor(type))
            val dry = CommandRunner(driver, FakePauseGate())
                .dryRun(CommandIntent.UnimplementedCall(type), registry)

            assertEquals("type=$type", type, dry.commandType)
            assertEquals(RiskLevel.L1, dry.riskLevel)
            assertTrue("type=$type", dry.plannedSteps.single().contains("未实现"))
            assertEquals(0, driver.actionLog.size)
        }
    }

    @Test
    fun `未实现族-暂停态亦显式未实现语义不静默`() {
        // 上游内联分支语义：不静默丢弃——即使暂停门在，执行器注册面已保证显式回执路径
        val type = MessageTypes.VOICE_CALL
        val registry = ExecutorRegistry.of(UnimplementedCommandExecutor(type))
        val gatePaused = FakePauseGate().apply { paused = true }
        val report = CommandRunner(FakeUiDriver(clock), gatePaused)
            .execute(CommandIntent.UnimplementedCall(type), registry)

        assertEquals(ExecutionStatus.PAUSED, report.status)
        // 恢复后执行即显式未实现（上用例已验证）；暂停态零副作用
        assertEquals(0, report.executedSteps.size)
    }
}
