/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.commands

import cn.worktool.open.feature.executor.exec.ExecutorErrorCodes
import cn.worktool.open.feature.executor.exec.StepOutcome
import cn.worktool.open.feature.executor.fake.FakeClock
import cn.worktool.open.feature.executor.fake.FakeUiDriver
import cn.worktool.open.feature.executor.fixture.ChatFixture
import cn.worktool.open.feature.executor.fixture.HomeFixture
import cn.worktool.open.feature.executor.fixture.SearchPageFixture
import cn.worktool.open.feature.executor.runtime.WeworkScreenRules
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * CommonSteps.openConversation 搜索框兜底（原版 intoRoom 搜索语义，2026-11-05）：
 * 正反向滚动兜底耗尽仍未命中 → 回消息主页（openMessageTab）→ 点搜索入口
 * （HOME_SEARCH_ENTRY desc=「搜索，按钮」）→ SEARCH_INPUT（m0f）输入会话名 →
 * SEARCH_RESULT_ITEM 等待精确命中 → 点击进入聊天页。
 *
 * 三场景（任务卡）：搜索命中 / 搜索无结果 / 搜索入口缺失；
 * 另加禁高频护栏断言：兜底单发不重试（单指令至多一次搜索兜底）。
 * 选择器族证据：local-private/evidence/session21/uitree-search-page.xml（m0f +
 * 过滤页签 全部/联系人/群聊）、uitree-msg-home.xml（o7a desc=「搜索，按钮」）。
 */
class CommonStepsSearchFallbackTest {

    /** 滚动永不命中的会话列表（滚动兜底耗尽 → 进入搜索兜底）。 */
    private fun homeWithoutTarget(vararg conversations: String, withSearchEntry: Boolean) =
        HomeFixture(
            conversations = conversations.toList(),
            withSearchEntry = withSearchEntry,
        )

    private fun driverFor(home: HomeFixture, search: SearchPageFixture?, chat: ChatFixture?): FakeUiDriver =
        FakeUiDriver(FakeClock()).apply {
            show(home.screen)
            home.searchEntryNode?.let { entry ->
                search?.let { s ->
                    onClick(home.screen, entry) { push(s.screen) }
                }
            }
            search?.let { s ->
                // 点击输入框聚焦时结果上屏（模拟搜索服务端返回；输入文本由 inputText 落值）
                onClick(s.screen, s.inputNode) {
                    s.resultList.add(s.resultNodes.values.firstOrNull() ?: return@onClick)
                }
                s.resultNodes.forEach { (_, node) ->
                    onClick(s.screen, node) { chat?.let { push(it.screen) } }
                }
            }
        }

    // ---- ① 搜索命中：列表滚尽未中 → 搜索兜底输入会话名 → 结果命中 → 进入聊天页 ----

    @Test
    fun `搜索兜底-滚动耗尽后经搜索入口输入会话名命中结果进入聊天页`() {
        val target = "远端群(3)"
        val home = homeWithoutTarget("会话甲", "会话乙", withSearchEntry = true)
        val search = SearchPageFixture(results = listOf(target))
        val chat = ChatFixture(title = target)
        val driver = driverFor(home, search, chat)

        val outcome = CommonSteps.openConversation(
            WeworkScreenRules(), target,
        ).action(driver)

        assertTrue("滚动未中应经搜索兜底命中，实际=$outcome", outcome is StepOutcome.Success)
        assertTrue("当前应已在聊天页", driver.current === chat.screen)
        assertTrue(
            "应点击搜索入口（o7a，desc=搜索，按钮；actionLog 记 resId）",
            driver.actionLog.any { it.startsWith("click(com.tencent.wework:id/o7a") },
        )
        assertTrue(
            "应在搜索框（m0f）输入会话名",
            driver.actionLog.any { it.startsWith("input(") && it.contains("m0f") },
        )
        assertEquals("搜索框应已落会话名", target, search.inputNode.textValue)
    }

    // ---- ② 搜索无结果：输入后结果列表永不命中 → STEP_TIMEOUT（如实），且兜底单发不重试 ----

    @Test
    fun `搜索兜底-无结果时如实STEP_TIMEOUT且单发不重试`() {
        val target = "不存在的群"
        val home = homeWithoutTarget("会话甲", withSearchEntry = true)
        val search = SearchPageFixture(results = listOf("无关结果")) // 输入后只出无关行，永不精确命中
        val driver = driverFor(home, search, chat = null)

        val outcome = CommonSteps.openConversation(
            WeworkScreenRules(), target,
        ).action(driver)

        assertTrue("无结果应如实失败，实际=$outcome", outcome is StepOutcome.Failure)
        assertEquals(
            ExecutorErrorCodes.STEP_TIMEOUT,
            (outcome as StepOutcome.Failure).errorCode,
        )
        assertTrue("失败原因应含搜索兜底语境", outcome.reason.contains("搜索"))
        val searchInputs = driver.actionLog.count { it.startsWith("input(") && it.contains("m0f") }
        assertEquals("禁高频：单指令至多一次搜索兜底（输入恰好一次）", 1, searchInputs)
    }

    // ---- ③ 搜索入口缺失：回消息主页后无「搜索，按钮」→ NODE_NOT_FOUND（如实） ----

    @Test
    fun `搜索兜底-搜索入口缺失时如实NODE_NOT_FOUND`() {
        val target = "不存在的群"
        val home = homeWithoutTarget("会话甲", withSearchEntry = false)
        val driver = driverFor(home, search = null, chat = null)

        val outcome = CommonSteps.openConversation(
            WeworkScreenRules(), target,
        ).action(driver)

        assertTrue("入口缺失应如实失败，实际=$outcome", outcome is StepOutcome.Failure)
        assertEquals(
            ExecutorErrorCodes.NODE_NOT_FOUND,
            (outcome as StepOutcome.Failure).errorCode,
        )
        assertTrue(
            "不应发生任何搜索框输入",
            driver.actionLog.none { it.startsWith("input(") },
        )
    }
}
