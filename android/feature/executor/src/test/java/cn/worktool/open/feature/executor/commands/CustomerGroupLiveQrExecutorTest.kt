/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.ExecutorRegistry
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakePauseGate
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.RecordingStructuredSink
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.LiveQrFixture
import cn.worktool.open.feature.executor.fixture.WorkbenchFixture
import cn.worktool.open.feature.executor.intent.CommandIntent
import cn.worktool.open.feature.executor.risk.RiskLevel
import cn.worktool.open.feature.executor.runtime.CommandRunner
import cn.worktool.open.feature.executor.runtime.ExecutionStatus
import cn.worktool.open.feature.executor.runtime.StepTimeouts
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * 239 客户群群活码执行器场景（docs/10 B3，L1）：
 * create 成功（504 通道推送 extraText=239）/ select 绑定已有群 /
 * 保存到相册失败 / 暂停拦截 / 干跑。
 */
class CustomerGroupLiveQrExecutorTest {

    private val timeouts = StepTimeouts(awaitNodeMs = 300, pollMs = 100)
    private val clock = FakeClock()
    private val gate = FakePauseGate()
    private val structured = RecordingStructuredSink()

    private fun world(saveOk: Boolean = true, existingGroups: List<String> = emptyList()): FakeUiDriver {
        val home = HomeFixture(conversations = listOf("工作群"))
        val workbench = WorkbenchFixture(withCustomerGroup = true)
        home.screen.root.children.last().add(workbench.workbenchTab)
        val liveQr = LiveQrFixture(saveClickable = saveOk)
        existingGroups.forEach { liveQr.screen.root.add(
            cn.worktool.open.feature.executor.fake.fakeNode(text = it, cls = cn.worktool.open.feature.executor.fake.TEXT_VIEW, clickable = true),
        ) }
        return FakeUiDriver(clock).apply {
            show(home.screen)
            onClick(home.screen, workbench.workbenchTab) { push(workbench.screen) }
            workbench.customerGroupEntry?.let { entry ->
                onClick(workbench.screen, entry) { push(liveQr.screen) }
            }
            onClick(liveQr.screen, liveQr.confirmNode) {
                // 生成成功：保存按钮与活码内容可见
                liveQr.saveNode?.let { add -> liveQr.screen.root.add(add) }
                liveQr.screen.root.add(liveQr.qrContentNode)
            }
            liveQr.saveNode?.let { save ->
                onClick(liveQr.screen, save) { /* 保存到相册成功 */ }
            }
        }
    }

    @Test
    fun `239成功-create模式生成活码并经504推送239标记`() {
        val driver = world()
        val registry = ExecutorRegistry.of(CreateCustomerGroupLiveQrCodeExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.CreateCustomerGroupLiveQrCode(
                groupMode = "create",
                selectList = emptyList(),
                newGroupName = "活动客户群",
            ),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(RiskLevel.L1, report.riskLevel)
        val (type, payload) = structured.results.single()
        assertEquals("经 504 通道推送", MessageTypes.GET_GROUP_QRCODE, type)
        assertEquals("239", payload["extraText"]!!.toString().trim('"'))
        assertEquals("活动客户群", payload["groupName"]!!.toString().trim('"'))
    }

    @Test
    fun `239成功-select模式绑定已有群`() {
        val driver = world(existingGroups = listOf("客户群A"))
        val registry = ExecutorRegistry.of(CreateCustomerGroupLiveQrCodeExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.CreateCustomerGroupLiveQrCode(
                groupMode = "select",
                selectList = listOf("客户群A"),
                newGroupName = "活动客户群",
            ),
            registry,
        )

        assertEquals(ExecutionStatus.SUCCEEDED, report.status)
        assertEquals(1, structured.results.size)
    }

    @Test
    fun `239负例-select模式群名不匹配`() {
        val driver = world(existingGroups = listOf("别的群"))
        val registry = ExecutorRegistry.of(CreateCustomerGroupLiveQrCodeExecutor(structured, timeouts = timeouts))
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.CreateCustomerGroupLiveQrCode(
                groupMode = "select",
                selectList = listOf("客户群A"),
                newGroupName = "活动客户群",
            ),
            registry,
        )

        assertEquals(ExecutionStatus.FAILED, report.status)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, report.errorCode)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `239暂停拦截-L1零副作用`() {
        val driver = world()
        val registry = ExecutorRegistry.of(CreateCustomerGroupLiveQrCodeExecutor(structured, timeouts = timeouts))
        gate.paused = true
        val report = CommandRunner(driver, gate).execute(
            CommandIntent.CreateCustomerGroupLiveQrCode(null, emptyList(), "活动客户群"),
            registry,
        )

        assertEquals(ExecutionStatus.PAUSED, report.status)
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
    }

    @Test
    fun `239干跑-零动作零推送`() {
        val driver = world()
        val registry = ExecutorRegistry.of(CreateCustomerGroupLiveQrCodeExecutor(structured, timeouts = timeouts))
        val dry = CommandRunner(driver, gate).dryRun(
            CommandIntent.CreateCustomerGroupLiveQrCode(null, emptyList(), "活动客户群"),
            registry,
        )
        assertEquals(0, driver.actionLog.size)
        assertEquals(0, structured.results.size)
        assertTrue(dry.plannedSteps.isNotEmpty())
    }
}
