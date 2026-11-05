/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakeExecutorNode
import cn.worktool.open.feature.executor.fake.FakeScreen
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fake.TEXT_VIEW
import cn.worktool.open.feature.executor.fake.VIEW_GROUP
import cn.worktool.open.feature.executor.fake.fakeNode
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * CommonSteps.openConversation 滚动搜索兜底硬化（rc1 遗留项核查车道，2026-11-05）：
 * W-M6 会话 10b 已落地正向滚动搜索（maxScrolls=8），本批补两处残留缺口——
 * ①滚动方向单向（目标行在视口上方=残留屏态，真机会话 10 首发 201002 即此类环境性失败）；
 * ②容器选择为先序首个可滚动节点（先序更前的横幅/页签等容器劫持滚动）。
 */
class CommonStepsOpenConversationTest {

    private fun conversationRow(text: String, clickable: Boolean = true) = fakeNode(
        text = text,
        resId = "com.tencent.wework:id/ibi",
        cls = TEXT_VIEW,
        clickable = clickable,
    )

    /** 会话行点击效果注册 + 滚动方向感知的内容追加（FakeUiDriver onScroll 无方向参数，按 actionLog 判向）。 */
    private fun FakeUiDriver.appendRowOnScroll(
        screen: FakeScreen,
        container: FakeExecutorNode,
        onForward: FakeExecutorNode.() -> Unit,
        onBackward: FakeExecutorNode.() -> Unit,
    ) {
        onScroll(screen, container) {
            when {
                actionLog.last().endsWith("FORWARD)") -> container.onForward()
                actionLog.last().endsWith("BACKWARD)") -> container.onBackward()
            }
        }
    }

    // ---- ① 反向滚动兜底：目标行在视口上方（残留屏态）时正向滚 8 页不可得 → 上滚命中 ----

    @Test
    fun `滚动兜底-目标行在视口上方时反向滚动搜索命中`() {
        val target = conversationRow("群聊(4)")
        val container = fakeNode(cls = VIEW_GROUP, scrollable = true) {
            add(conversationRow("当前屏会话"))
        }
        val screen = FakeScreen(
            name = "home",
            root = fakeNode(cls = VIEW_GROUP) {
                add(fakeNode(text = "消息", cls = TEXT_VIEW, clickable = true))
                add(container)
            },
        )
        val driver = FakeUiDriver(FakeClock()).apply {
            show(screen)
            onClick(screen, target) { /* 进入聊天页（本步骤只验证定位+点击） */ }
            var deeper = 0
            appendRowOnScroll(
                screen, container,
                onForward = { deeper += 1; add(conversationRow("更深会话$deeper")) }, // 正向滚：列表更长，永无目标
                onBackward = { add(target) }, // 反向滚：目标行回入视口
            )
        }

        val outcome = CommonSteps.openConversation(WeworkScreenRules(), "群聊(4)").action(driver)

        assertTrue("目标行在视口上方应经反向滚动命中，实际=$outcome", outcome is StepOutcome.Success)
        assertTrue("应发生反向滚动", driver.actionLog.any { it.endsWith("BACKWARD)") })
    }

    // ---- ② 容器定向：先序更前的无关可滚动容器不劫持会话列表滚动 ----

    @Test
    fun `滚动兜底-先序更前的无关可滚动容器不劫持列表定位`() {
        val target = conversationRow("群聊(4)")
        val decoy = fakeNode(cls = "android.widget.ScrollView", scrollable = true) // 横幅等：无会话项
        val listContainer = fakeNode(
            resId = "com.tencent.wework:id/conversation_list",
            cls = VIEW_GROUP,
            scrollable = true,
        ) {
            add(conversationRow("首屏会话"))
        }
        val screen = FakeScreen(
            name = "home",
            root = fakeNode(cls = VIEW_GROUP) {
                add(decoy) // 先序在前：旧口径（首个可滚动）会选中它
                add(listContainer)
            },
        )
        val driver = FakeUiDriver(FakeClock()).apply {
            show(screen)
            onClick(screen, target) { /* 进入聊天页 */ }
            appendRowOnScroll(
                screen, decoy,
                onForward = { /* 横幅滚动：内容不变 */ },
                onBackward = { },
            )
            appendRowOnScroll(
                screen, listContainer,
                onForward = { add(target) }, // 会话列表滚动后目标行入树
                onBackward = { },
            )
        }

        val outcome = CommonSteps.openConversation(WeworkScreenRules(), "群聊(4)").action(driver)

        assertTrue("应选中含会话项的列表容器并滚动命中，实际=$outcome", outcome is StepOutcome.Success)
        assertTrue(
            "滚动应落在会话列表容器上",
            driver.actionLog.any { it.startsWith("scroll(com.tencent.wework:id/conversation_list") },
        )
        assertTrue(
            "无关容器不应被滚动",
            driver.actionLog.none { it.startsWith("scroll(android.widget.ScrollView") },
        )
    }

    // ---- ③ 正反向均耗尽仍未见 → NODE_NOT_FOUND（如实失败口径不变） ----

    @Test
    fun `滚动兜底-正反向耗尽仍未见如实NODE_NOT_FOUND`() {
        val container = fakeNode(cls = VIEW_GROUP, scrollable = true) {
            add(conversationRow("首屏会话"))
        }
        val screen = FakeScreen(
            name = "home",
            root = fakeNode(cls = VIEW_GROUP) {
                add(fakeNode(text = "消息", cls = TEXT_VIEW, clickable = true))
                add(container)
            },
        )
        val driver = FakeUiDriver(FakeClock()).apply {
            show(screen)
            var filler = 0
            appendRowOnScroll(
                screen, container,
                onForward = { filler += 1; add(conversationRow("更深会话$filler")) },
                onBackward = { add(conversationRow("更早会话")) },
            )
        }

        val outcome = CommonSteps.openConversation(WeworkScreenRules(), "不存在的群").action(driver)

        assertTrue("耗尽正反向滚动应如实失败，实际=$outcome", outcome is StepOutcome.Failure)
        assertEquals(ExecutorErrorCodes.NODE_NOT_FOUND, (outcome as StepOutcome.Failure).errorCode)
    }
}
