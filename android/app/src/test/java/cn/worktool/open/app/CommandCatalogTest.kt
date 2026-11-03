/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import cn.worktool.open.core.protocol.MessageTypes
import cn.worktool.open.core.task.RiskLevel
import cn.worktool.open.core.task.RiskLevels
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** 指令列表构建（docs/05 P0 行镜像 + FR-54 干跑预览）。 */
class CommandCatalogTest {

    @Test
    fun `目录 - 覆盖 docs05 P0 全部指令号`() {
        val rows = CommandCatalog.p0()
        val types = rows.flatMap { it.allTypes }.toSet()
        val expected = setOf(
            MessageTypes.TYPE_RECEIVE_MESSAGE_LIST, MessageTypes.TYPE_CONSOLE_TOAST, // 101/102
            MessageTypes.SEND_MESSAGE, // 203
            MessageTypes.INIT_GROUP, // 206
            MessageTypes.UPDATE_GROUP, // 207
            MessageTypes.MODIFY_GROUP_MEMBER_INFO, // 225
            MessageTypes.PASS_ALL_FRIEND_REQUEST, // 212
            MessageTypes.GET_GROUP_INFO, MessageTypes.GET_FRIEND_INFO, // 501/502
            MessageTypes.GET_LOCAL_FILE, // 509
        )
        assertEquals(expected, types)
    }

    @Test
    fun `目录 - 风险级与 task-engine RiskLevels 口径一致`() {
        for (row in CommandCatalog.p0()) {
            // 上报行（101/102）无执行语义不分级；其余行必须逐号等于类型级兜底表
            if (row.risk == null) {
                assertEquals(listOf(101, 102), row.allTypes)
            } else {
                assertEquals(RiskLevels.of(row.commandType), row.risk)
            }
        }
        // 关键分级锚点（FR-53 分级裁决③基线，docs/01 CR-R1-0①：L1={206,207,213,220,
        // 227,228,229,230}、L2={219,234,304}，其余已实现指令=L0——207 含拉人载荷时
        // 由执行器侧动态升 L1（裁决①：载荷动态分级为单一事实源，类型表仅兜底）；
        // 旧 docs/05「207/225…L1」行为裁决前表述，225 已实现未入 L1 基线集 → L0）
        assertEquals(RiskLevel.L1, RiskLevels.of(MessageTypes.INIT_GROUP))
        assertEquals(RiskLevel.L1, RiskLevels.of(MessageTypes.UPDATE_GROUP))
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.MODIFY_GROUP_MEMBER_INFO))
        assertEquals(RiskLevel.L0, RiskLevels.of(MessageTypes.SEND_MESSAGE))
    }

    @Test
    fun `目录 - 每行必含参数说明且非空`() {
        for (row in CommandCatalog.p0()) {
            assertTrue("行 ${row.title} 参数说明为空", row.paramsNote.isNotBlank())
        }
    }

    @Test
    fun `目录 - 上次执行状态回填与渲染`() {
        val outcomes = mapOf(
            MessageTypes.SEND_MESSAGE to CommandCatalog.Row.Outcome(success = true, errorCode = 0, atMillis = 1),
            MessageTypes.INIT_GROUP to CommandCatalog.Row.Outcome(success = false, errorCode = 20301, atMillis = 2),
        )
        val rows = CommandCatalog.p0(outcomes)
        assertEquals("成功", CommandCatalog.lastStatusText(rows.first { it.commandType == MessageTypes.SEND_MESSAGE }.lastOutcome))
        assertEquals("失败(code=20301)", CommandCatalog.lastStatusText(rows.first { it.commandType == MessageTypes.INIT_GROUP }.lastOutcome))
        assertEquals("未执行", CommandCatalog.lastStatusText(rows.first { it.commandType == MessageTypes.GET_GROUP_INFO }.lastOutcome))
    }

    @Test
    fun `干跑预览 - FR54 同引擎报告且指令号一致`() {
        val row = CommandCatalog.p0().first { it.commandType == MessageTypes.SEND_MESSAGE }
        val report = CommandCatalog.dryRunPreview(row) { 42_000L }
        assertEquals(MessageTypes.SEND_MESSAGE, report.commandType)
        assertEquals(RiskLevels.of(MessageTypes.SEND_MESSAGE), report.riskLevel)
        assertEquals(42_000L, report.atMillis)
        assertNotNull(report.toString()) // 报告自述「不产生真实副作用」
        assertTrue(report.params.isEmpty()) // 预览意图未携带参数（详情页预览仅展示形态）
    }

    @Test
    fun `风险标签 - 上报类与L0L1渲染`() {
        assertEquals("上报", CommandCatalog.riskLabel(null))
        assertEquals("L0", CommandCatalog.riskLabel(RiskLevel.L0))
        assertEquals("L1", CommandCatalog.riskLabel(RiskLevel.L1))
        assertEquals("L2", CommandCatalog.riskLabel(RiskLevel.L2))
    }
}
