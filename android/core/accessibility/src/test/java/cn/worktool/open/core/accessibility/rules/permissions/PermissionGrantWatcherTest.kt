/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.permissions

import cn.worktool.open.core.accessibility.event.AccessibilityEventSnapshot
import cn.worktool.open.core.accessibility.fake.FakeGestureDispatcher
import cn.worktool.open.core.accessibility.fake.FakeUiNode
import cn.worktool.open.core.accessibility.ops.AtomicOperations
import cn.worktool.open.core.accessibility.rules.pack.WeComRulePack
import cn.worktool.open.core.accessibility.tree.NodeAction
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * FR-57 权限弹窗自动授权 watcher（[PermissionGrantWatcher]）JVM 单测。
 *
 * 覆盖矩阵（任务口径 ≥12 例；4 类弹窗 × 成功/文本变体/两连败放弃 + 门禁面）：
 * - 成功：#1 拨打电话三级 / #5 设备信息单级 / #8 录音两级 / #11 照片同屏三级；
 * - 文本变体：#2 二级按钮变体 / #7 事件文本特征命中 / #8「录音」特征词 / #17 模糊兜底；
 * - 两连败放弃：#3 按钮缺失 / #9 ACTION 点击连败 / #18 否定词守卫拒绝（→按钮缺失放弃）；
 * - 点击未生效与取消（FR57-R3 补齐）：#20 严格层已点标签仍在屏 INEFFECTIVE_CLICK 两连败 /
 *   #21 模糊层同目标未生效不连点（FR57-R1）/ #22 零点击签名消失 CANCELLED；
 * - 门禁与规程：#4 抑制重臂 / #6 类型开关 / #10 手势补点 / #12 包名门 / #13 非弹窗不触发 /
 *   #14 拟人间隔 ≥800ms / #15 SR-06 审计零正文 / #16 授权超时 / #19 总开关；
 * - #23 企微事件+外窗口语料重叠现状钉子（FR57-R2 已知暴露面，逐窗口收敛时翻转本例）。
 */
class PermissionGrantWatcherTest {

    private var now = 0L
    private val audits = ArrayList<String>()

    /** 排程 Fake：记录延迟（拟人断言），测试逐个 pump 驱动状态机。 */
    private class RecordingScheduler : PermissionGrantWatcher.GrantScheduler {
        val delays = ArrayList<Long>()
        private val tasks = ArrayDeque<Runnable>()
        val totalScheduled: Int get() = delays.size

        override fun schedule(delayMillis: Long, task: Runnable) {
            delays += delayMillis
            tasks.addLast(task)
        }

        /** 执行最早的一个排程任务。 */
        fun pump() {
            check(tasks.isNotEmpty()) { "队列为空，无任务可泵" }
            tasks.removeFirst().run()
        }
    }

    /** 测试夹具：根树可热替换（rootsProvider 每次评估现读，模拟真机窗口刷新）。 */
    private inner class Fixture(
        val config: PermissionGrantConfig = PermissionGrantConfig(),
        val gestures: FakeGestureDispatcher? = null,
        val jitterMillis: Long = 0L,
    ) {
        val sched = RecordingScheduler()
        var root: FakeUiNode? = null
        val watcher = PermissionGrantWatcher(
            config = config,
            rootsProvider = { listOfNotNull(root) },
            operations = AtomicOperations(gestures = gestures),
            scheduler = sched,
            jitter = { jitterMillis },
            audit = { audits += it },
            clock = { now },
        )
    }

    // ---- 造树工具 ----

    /**
     * 弹窗树：标题（含特征词与「允许」；句末全角问号使其天然不进模糊层）+ 按钮。
     * 按钮形态：可点容器 + 文本子节点（MIUI 常见：文案在子节点、点击在父容器）。
     */
    private fun dialogTree(title: String, vararg buttons: Pair<String, Boolean>): FakeUiNode {
        val root = FakeUiNode(className = "android.view.View")
        if (title.isNotEmpty()) root.add(FakeUiNode(text = title))
        buttons.forEach { (label, clickable) ->
            val container = FakeUiNode(isClickable = clickable)
            container.add(FakeUiNode(text = label))
            root.add(container)
        }
        return root
    }

    /** 无任何弹窗签名的「底层界面」树。 */
    private fun plainTree(): FakeUiNode = dialogTree("消息", "消息" to true)

    private fun event(
        texts: List<String>,
        eventType: Int = TYPE_STATE,
        pkg: String = PACKAGE,
    ): AccessibilityEventSnapshot = AccessibilityEventSnapshot(
        eventType = eventType,
        packageName = pkg,
        className = "android.widget.FrameLayout",
        texts = texts,
        eventTimeMillis = now,
    )

    // ---- 4 类弹窗标题语料（含特征词+「允许」） ----

    private val phoneTitle = "是否允许企业微信拨打电话？"
    private val deviceTitle = "是否允许企业微信获取设备信息？"
    private val micTitle = "是否允许企业微信使用录音功能？"
    private val photosTitle = "是否允许企业微信访问照片和视频？"

    // ---- #1 拨打电话：三级分屏全流程成功 ----

    @Test
    fun `拨打电话 - 三级分屏全流程成功`() {
        val f = Fixture().apply { root = dialogTree(phoneTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump() // 入口：分类+进入授权+排第一次节拍
        assertTrue(f.watcher.granting)
        assertEquals(PermissionDialogType.CALL_PHONE, f.watcher.grantingType)
        f.sched.pump() // 节拍1：点「使用应用时允许」
        f.root = dialogTree(phoneTitle, "仅在使用该应用时允许" to true)
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle), TYPE_CONTENT)) // 翻页事件（pending 合并）
        f.sched.pump() // 节拍2：点二级
        f.root = dialogTree(phoneTitle, "允许访问全部" to true)
        f.sched.pump() // 节拍3：点三级 → 终态
        assertFalse(f.watcher.granting)
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(1, s.attempts)
        assertEquals(1, s.granted)
        assertEquals(3, s.clicksOk)
        assertEquals(0, s.manualRequired)
        assertTrue(audits.contains("perm_grant:ENTRY type=CALL_PHONE"))
        assertTrue(audits.contains("perm_grant:DONE type=CALL_PHONE result=GRANTED levels=3"))
    }

    // ---- #2 拨打电话：二级按钮文本变体（任务口径「仅在使用中允许」） ----

    @Test
    fun `拨打电话 - 二级按钮变体 仅在使用中允许 被点击`() {
        val f = Fixture().apply { root = dialogTree(phoneTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump()
        f.sched.pump() // 点一级
        f.root = dialogTree(phoneTitle, "仅在使用中允许" to true)
        f.sched.pump() // 点二级（变体命中）
        f.root = plainTree()
        f.sched.pump() // 签名消失 → GRANTED（两级流）
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(1, s.granted)
        assertEquals(2, s.clicksOk)
        assertTrue(audits.contains("perm_grant:CLICK type=CALL_PHONE level=STEP2 tier=STRICT method=NODE_ACTION"))
        assertTrue(audits.contains("perm_grant:DONE type=CALL_PHONE result=GRANTED levels=2"))
    }

    // ---- #3 拨打电话：按钮两连败（找不到节点）→ MANUAL_REQUIRED + 类型抑制 ----

    @Test
    fun `拨打电话 - 找不到按钮两连败 放弃并记录 MANUAL_REQUIRED`() {
        val f = Fixture().apply { root = dialogTree(phoneTitle) } // 只有标题语料，无任何按钮
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump()
        f.sched.pump() // 节拍1：MISSING_BUTTON failures=1
        f.sched.pump() // 节拍2：failures=2 → 放弃（docs/12 铁律 2：禁止第三次盲试）
        assertFalse(f.watcher.granting)
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(1, s.attempts)
        assertEquals(1, s.manualRequired)
        assertEquals(0, s.clicksOk)
        assertTrue(
            audits.contains(
                "perm_grant:DONE type=CALL_PHONE result=MANUAL_REQUIRED levels=0 reason=MISSING_BUTTON",
            ),
        )
        assertEquals(setOf(PermissionDialogType.CALL_PHONE), f.watcher.suppressedTypes)
    }

    // ---- #4 抑制重臂：弹窗消失后同类型弹窗可再次授权 ----

    @Test
    fun `拨打电话 - MANUAL_REQUIRED 抑制至签名消失后重臂`() {
        val f = Fixture().apply { root = dialogTree(phoneTitle) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump(); f.sched.pump(); f.sched.pump() // 入口 + 两连败放弃
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE).attempts)
        // 抑制期：弹窗仍在 → 再来的同类型事件不重试。
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump()
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE).attempts)
        // 弹窗消失 → 重臂。
        f.root = plainTree()
        f.watcher.onAccessibilityEvent(event(emptyList()))
        f.sched.pump()
        assertTrue(audits.contains("perm_grant:REARM type=CALL_PHONE"))
        assertTrue(f.watcher.suppressedTypes.isEmpty())
        // 新弹窗再次进入授权并点成。
        f.root = dialogTree(phoneTitle, "使用应用时允许" to true)
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump(); f.sched.pump()
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(2, s.attempts)
        assertEquals(1, s.clicksOk)
    }

    // ---- #5 设备信息：单级流（点完一级弹窗即消失）成功 ----

    @Test
    fun `设备信息 - 单级流点完一级签名消失 记 GRANTED`() {
        val f = Fixture().apply { root = dialogTree(deviceTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(deviceTitle)))
        f.sched.pump()
        f.sched.pump() // 点一级
        f.root = plainTree()
        f.watcher.onAccessibilityEvent(event(emptyList(), TYPE_CONTENT))
        f.sched.pump() // 签名消失 → GRANTED levels=1
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.DEVICE_INFO)
        assertEquals(1, s.granted)
        assertEquals(1, s.clicksOk)
        assertTrue(audits.contains("perm_grant:DONE type=DEVICE_INFO result=GRANTED levels=1"))
    }

    // ---- #6 设备信息：类型开关关闭 → 跳过不动作 ----

    @Test
    fun `设备信息 - 类型开关关闭 记 SKIP 零点击`() {
        val f = Fixture(
            config = PermissionGrantConfig(perTypeEnabled = mapOf(PermissionDialogType.DEVICE_INFO to false)),
        ).apply { root = dialogTree(deviceTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(deviceTitle)))
        f.sched.pump()
        assertFalse(f.watcher.granting)
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.DEVICE_INFO)
        assertEquals(1, s.skippedDisabled)
        assertEquals(0, s.attempts)
        assertEquals(0, s.clicksOk)
        assertTrue(audits.contains("perm_grant:SKIP type=DEVICE_INFO reason=TYPE_DISABLED"))
    }

    // ---- #7 设备信息：事件文本命中特征词（树内无特征词，按钮语料补「允许」） ----

    @Test
    fun `设备信息 - 事件文本特征与树按钮语料拼合 可进入授权`() {
        // 树只有按钮（含「允许」），特征词只在事件文本里——语料取并集。
        val f = Fixture().apply { root = dialogTree("", "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf("允许企业微信读取设备信息吗？")))
        f.sched.pump()
        assertTrue(f.watcher.granting)
        assertEquals(PermissionDialogType.DEVICE_INFO, f.watcher.grantingType)
        f.sched.pump() // 点一级
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.DEVICE_INFO).clicksOk)
    }

    // ---- #8 录音：「录音」特征词变体 + 两级流成功 ----

    @Test
    fun `录音 - 录音特征词两级流成功`() {
        val f = Fixture().apply { root = dialogTree(micTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(micTitle)))
        f.sched.pump()
        assertEquals(PermissionDialogType.MICROPHONE, f.watcher.grantingType)
        f.sched.pump() // 点一级
        f.root = dialogTree(micTitle, "仅在使用该应用时允许" to true)
        f.sched.pump() // 点二级
        f.root = plainTree()
        f.sched.pump() // 签名消失 → GRANTED
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.MICROPHONE)
        assertEquals(1, s.granted)
        assertEquals(2, s.clicksOk)
        assertTrue(audits.contains("perm_grant:DONE type=MICROPHONE result=GRANTED levels=2"))
    }

    // ---- #9 录音：ACTION 点击两连败（无手势回退）→ MANUAL_REQUIRED ----

    @Test
    fun `录音 - ACTION点击两连败 放弃 记 CLICK_FAILED`() {
        val f = Fixture().apply { root = dialogTree(micTitle, "使用应用时允许" to true) }
        // 模拟节点声称可点但框架拒绝 ACTION_CLICK，且无手势回退通道。
        f.root!!.children.last().supportedActions.remove(NodeAction.CLICK)
        f.watcher.onAccessibilityEvent(event(listOf(micTitle)))
        f.sched.pump()
        f.sched.pump() // 节拍1：CLICK_FAILED failures=1
        f.sched.pump() // 节拍2：failures=2 → 放弃
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.MICROPHONE)
        assertEquals(2, s.clicksFailed)
        assertEquals(1, s.manualRequired)
        assertEquals(0, s.clicksOk)
        assertTrue(
            audits.contains(
                "perm_grant:CLICK_FAIL type=MICROPHONE level=STEP1 tier=STRICT reason=GESTURE_UNAVAILABLE failures=1",
            ),
        )
        assertTrue(
            audits.contains(
                "perm_grant:DONE type=MICROPHONE result=MANUAL_REQUIRED levels=0 reason=CLICK_FAILED",
            ),
        )
    }

    // ---- #10 录音：ACTION 点击失败改手势补点兜底（docs/01 FR-57 明确要求有用例） ----

    @Test
    fun `录音 - ACTION失败改手势补点 method=GESTURE`() {
        val gestures = FakeGestureDispatcher(result = true)
        val f = Fixture(gestures = gestures).apply { root = dialogTree(micTitle, "使用应用时允许" to true) }
        f.root!!.children.last().supportedActions.remove(NodeAction.CLICK)
        f.watcher.onAccessibilityEvent(event(listOf(micTitle)))
        f.sched.pump()
        f.sched.pump() // 节拍1：ACTION_CLICK 失败 → 手势 tap bounds 中心
        assertEquals(1, gestures.calls.size)
        assertEquals("tap", gestures.calls.single().kind)
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.MICROPHONE).clicksOk)
        assertTrue(audits.contains("perm_grant:CLICK type=MICROPHONE level=STEP1 tier=STRICT method=GESTURE"))
    }

    // ---- #11 照片：同屏三级合并形态（按级别序逐个点击，最深一级即终态） ----

    @Test
    fun `照片 - 同屏三级合并形态逐个点击 GRANTED levels=3`() {
        val f = Fixture().apply {
            root = dialogTree(
                photosTitle,
                "使用应用时允许" to true,
                "仅在使用该应用时允许" to true,
                "允许访问全部" to true,
            )
        }
        f.watcher.onAccessibilityEvent(event(listOf(photosTitle)))
        f.sched.pump()
        f.sched.pump() // 点 STEP1（同屏其余两级未点，节拍继续）
        f.sched.pump() // 点 STEP2
        f.sched.pump() // 点 STEP3 → 终态
        assertFalse(f.watcher.granting)
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.PHOTOS)
        assertEquals(1, s.granted)
        assertEquals(3, s.clicksOk)
        assertTrue(audits.contains("perm_grant:DONE type=PHOTOS result=GRANTED levels=3"))
        // 点击顺序严格按级别序（三级授权流）。
        val levels = audits
            .filter { it.startsWith("perm_grant:CLICK ") }
            .map { it.substringAfter("level=").substringBefore(' ') }
        assertEquals(listOf("STEP1", "STEP2", "STEP3"), levels)
    }

    // ---- #12 包名门：非企微包事件零处理 ----

    @Test
    fun `包名门 - 非企微包事件零调度零审计`() {
        val f = Fixture().apply { root = dialogTree(phoneTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(
            event(listOf(phoneTitle), pkg = "com.google.android.permissioncontroller"),
        )
        // 包名门在排程前拦截：队列零任务（无需 pump，泵空队列会抛错）。
        assertFalse(f.watcher.granting)
        assertEquals(0, f.sched.totalScheduled)
        assertTrue(audits.isEmpty())
    }

    // ---- #13 非弹窗语料：只有特征词没有「允许」不触发 ----

    @Test
    fun `非弹窗 - 特征词缺失允许词 零动作`() {
        val f = Fixture().apply { root = dialogTree("相册里的照片", "取消" to true) }
        f.watcher.onAccessibilityEvent(event(listOf("相册里的照片")))
        f.sched.pump()
        assertFalse(f.watcher.granting)
        assertTrue(audits.isEmpty())
        assertTrue(f.watcher.statsSnapshot().values.all { it.attempts == 0 })
    }

    // ---- #14 拟人间隔：授权节拍 = ≥800ms + 注入抖动（docs/12 铁律 4） ----

    @Test
    fun `拟人 - 节拍间隔等于800ms加抖动`() {
        val f = Fixture(jitterMillis = 250L).apply { root = dialogTree(phoneTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump() // 入口（delay=0，不占拟人节拍）
        f.sched.pump() // 节拍1：点一级
        f.root = dialogTree(phoneTitle, "仅在使用该应用时允许" to true)
        f.sched.pump() // 节拍2：点二级
        f.root = plainTree()
        f.sched.pump() // 节拍3：签名消失收尾
        val delays = f.sched.delays
        assertEquals(0L, delays.first())
        val paced = delays.drop(1)
        assertTrue(paced.isNotEmpty())
        paced.forEach { assertEquals(1050L, it) } // 800（docs/12 下限）+ 250（注入抖动）
        assertTrue(audits.contains("perm_grant:DONE type=CALL_PHONE result=GRANTED levels=2"))
    }

    // ---- #15 SR-06：审计行零弹窗正文 ----

    @Test
    fun `SR06 - 审计行不含弹窗正文与按钮文案`() {
        val f = Fixture().apply {
            root = dialogTree(
                phoneTitle,
                "使用应用时允许" to true,
                "仅在使用该应用时允许" to true,
                "允许访问全部" to true,
            )
        }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump(); f.sched.pump(); f.sched.pump(); f.sched.pump()
        assertTrue(audits.isNotEmpty())
        val forbidden = listOf("拨打电话", "使用应用时允许", "仅在使用该应用时允许", "允许访问全部", phoneTitle)
        audits.forEach { line ->
            forbidden.forEach { word -> assertFalse("审计泄漏正文: $line", line.contains(word)) }
        }
        assertTrue(audits.any { it.contains("type=CALL_PHONE") })
    }

    // ---- #16 授权超时 → MANUAL_REQUIRED（docs/12 铁律 2 语义） ----

    @Test
    fun `超时 - 授权中事件停摆超时 放弃并抑制`() {
        val f = Fixture().apply { root = dialogTree(phoneTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump()
        assertTrue(f.watcher.granting)
        now = 16_000L // > grantTimeoutMillis(15s)
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle), TYPE_CONTENT))
        f.sched.pump() // 节拍：超时判定
        assertFalse(f.watcher.granting)
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE).manualRequired)
        assertTrue(
            audits.contains("perm_grant:DONE type=CALL_PHONE result=MANUAL_REQUIRED levels=0 reason=TIMEOUT"),
        )
        assertEquals(setOf(PermissionDialogType.CALL_PHONE), f.watcher.suppressedTypes)
    }

    // ---- #17 模糊兜底：非标按钮文案（含「允许」且过守卫）可点 ----

    @Test
    fun `模糊兜底 - 非标允许按钮被点击 tier=FUZZY`() {
        // 真机 ROM 文案变体（[CAL_FUZZY]）：不在严格词表，但含「允许」、≤20 字、无问号、无否定词。
        val f = Fixture().apply { root = dialogTree(phoneTitle, "允许仅在使用时调用电话" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump()
        f.sched.pump() // 严格层无命中 → 模糊层命中
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE).clicksOk)
        assertTrue(audits.contains("perm_grant:CLICK type=CALL_PHONE level=UNLEVEL tier=FUZZY method=NODE_ACTION"))
        f.root = plainTree()
        f.sched.pump() // 签名消失（有过成功点击）→ GRANTED
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(1, s.granted)
        assertTrue(audits.contains("perm_grant:DONE type=CALL_PHONE result=GRANTED levels=0"))
    }

    // ---- #18 模糊守卫：否定词按钮绝不点击（→按钮缺失两连败放弃） ----

    @Test
    fun `模糊守卫 - 含否定词按钮不点 两连败放弃 零成功点击`() {
        val f = Fixture().apply { root = dialogTree(phoneTitle, "不允许" to true, "禁止且不再询问" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump()
        f.sched.pump() // 严格/模糊全不命中（否定词被守卫排除）→ MISSING failures=1
        f.sched.pump() // failures=2 → MANUAL_REQUIRED
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(0, s.clicksOk)
        assertEquals(1, s.manualRequired)
        assertTrue(audits.none { it.startsWith("perm_grant:CLICK type=") })
        assertTrue(
            audits.contains(
                "perm_grant:DONE type=CALL_PHONE result=MANUAL_REQUIRED levels=0 reason=MISSING_BUTTON",
            ),
        )
    }

    // ---- #19 总开关关闭 = 零动作零审计 ----

    @Test
    fun `总开关 - 关闭后事件零处理`() {
        val f = Fixture(config = PermissionGrantConfig(masterEnabled = false))
            .apply { root = dialogTree(phoneTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        assertEquals(0, f.sched.totalScheduled)
        assertTrue(audits.isEmpty())
    }

    // ---- #20 点击未生效（FR57-R3 补齐）：单级弹窗点击后树不变 → INEFFECTIVE_CLICK 两连败放弃 ----

    @Test
    fun `点击未生效 - 单级弹窗点击后树不变 计 INEFFECTIVE_CLICK 两连败放弃`() {
        val f = Fixture().apply { root = dialogTree(deviceTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(deviceTitle)))
        f.sched.pump()
        f.sched.pump() // 节拍1：点 STEP1 Ok（假想框架吞了点击，树实际未变）
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.DEVICE_INFO).clicksOk)
        // 树保持原样（同标签仍在、无新级别）→ 节拍2 起「已点标签仍在屏」= 点击未生效。
        f.sched.pump() // 节拍2：INEFFECTIVE_CLICK failures=1
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.DEVICE_INFO).clicksOk)
        assertTrue(audits.contains("perm_grant:OBSERVE type=DEVICE_INFO kind=INEFFECTIVE_CLICK failures=1"))
        f.sched.pump() // 节拍3：failures=2 → 放弃（docs/12 铁律 2）
        assertFalse(f.watcher.granting)
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.DEVICE_INFO)
        assertEquals(1, s.attempts)
        assertEquals(1, s.manualRequired)
        assertEquals(1, s.clicksOk)
        assertTrue(
            audits.contains(
                "perm_grant:DONE type=DEVICE_INFO result=MANUAL_REQUIRED levels=1 reason=INEFFECTIVE_CLICK",
            ),
        )
        assertEquals(setOf(PermissionDialogType.DEVICE_INFO), f.watcher.suppressedTypes)
    }

    // ---- #21 模糊层未生效（FR57-R1）：误触发语料下同一目标不连点，两连败即放弃 ----

    @Test
    fun `模糊层未生效 - 同一弱文本目标第二拍起计 INEFFECTIVE_CLICK 全程仅一次真实点击`() {
        // 弱文本误触发形态：按钮文案不在严格词表，仅过模糊守卫（含「允许」）——旧实现
        // 对该目标每节拍重选连点至 15s 超时（≈12~18 次），且连败计数恒为 0。
        val f = Fixture().apply { root = dialogTree(phoneTitle, "允许仅在使用时调用电话" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(phoneTitle)))
        f.sched.pump()
        f.sched.pump() // 节拍1：模糊层点击 Ok（tier=FUZZY，误触发）
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE).clicksOk)
        // 树不变：同一身份键目标仍在屏 → 节拍2 不再重试，按 INEFFECTIVE_CLICK 计失败。
        f.sched.pump() // 节拍2：INEFFECTIVE_CLICK failures=1
        assertEquals(1, f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE).clicksOk)
        f.sched.pump() // 节拍3：failures=2 → MANUAL_REQUIRED
        assertFalse(f.watcher.granting)
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(1, s.clicksOk) // 全程仅一次真实点击（旧实现 ≥12 次）
        assertEquals(1, s.manualRequired)
        assertTrue(audits.contains("perm_grant:CLICK type=CALL_PHONE level=UNLEVEL tier=FUZZY method=NODE_ACTION"))
        assertEquals(1, audits.count { it.startsWith("perm_grant:CLICK ") }) // 无第二次真实点击
        assertTrue(
            audits.contains(
                "perm_grant:DONE type=CALL_PHONE result=MANUAL_REQUIRED levels=0 reason=INEFFECTIVE_CLICK",
            ),
        )
        assertEquals(setOf(PermissionDialogType.CALL_PHONE), f.watcher.suppressedTypes)
    }

    // ---- #22 取消终态（FR57-R3 补齐）：进入授权后零点击签名消失 → CANCELLED ----

    @Test
    fun `取消 - 进入授权后未发生点击签名即消失 记 CANCELLED`() {
        val f = Fixture().apply { root = dialogTree(micTitle, "使用应用时允许" to true) }
        f.watcher.onAccessibilityEvent(event(listOf(micTitle)))
        f.sched.pump()
        assertTrue(f.watcher.granting)
        f.root = plainTree() // 用户手动关掉弹窗（零点击）
        f.watcher.onAccessibilityEvent(event(emptyList(), TYPE_CONTENT))
        f.sched.pump() // 签名消失且零点击 → CANCELLED（非 GRANTED）
        assertFalse(f.watcher.granting)
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.MICROPHONE)
        assertEquals(1, s.attempts)
        assertEquals(1, s.cancelled)
        assertEquals(0, s.clicksOk)
        assertEquals(0, s.granted)
        assertTrue(
            audits.contains("perm_grant:DONE type=MICROPHONE result=CANCELLED levels=0 reason=SIGNATURE_GONE_NO_CLICK"),
        )
        assertFalse(f.watcher.suppressedTypes.contains(PermissionDialogType.MICROPHONE)) // CANCELLED 不抑制
    }

    // ---- #23 包名门缺口现状钉子（FR57-R2）：企微事件+外窗口语料重叠仍可分类并点进外窗口 ----

    @Test
    fun `包名门缺口 - 企微事件与外窗口语料重叠 分类成功且点击落外窗口（现状钉子）`() {
        // 已知暴露面（FR57-R2）：包名门只在事件层；scanWindows 不按窗口包名收敛，
        // 语料=事件文本+全部交互窗口文本取并集。外窗口（此处模拟无关应用界面）文本
        // 恰含特征词+「允许」时，企微包事件可把它分类成权限弹窗并把点击打进外窗口。
        // 本例钉住该现状（防无意识回归）；逐窗口收敛（分类语料与点击目标按窗口归组，
        // [CAL_PKG] 真机校准时落地）实现后必须翻转本例断言。
        val foreignTitle = "商家客服回访：可拨打电话联系"
        val f = Fixture().apply { root = dialogTree(foreignTitle, "允许" to true) } // 外窗口树
        f.watcher.onAccessibilityEvent(event(listOf("消息"))) // 企微包事件（正文与弹窗无关）
        f.sched.pump() // 入口：并集语料命中 CALL_PHONE 特征词+「允许」→ 分类成功
        assertTrue(f.watcher.granting)
        assertEquals(PermissionDialogType.CALL_PHONE, f.watcher.grantingType)
        f.sched.pump() // 严格层无命中 → 模糊层点中外窗口「允许」按钮
        val s = f.watcher.statsSnapshot().getValue(PermissionDialogType.CALL_PHONE)
        assertEquals(1, s.clicksOk)
        assertTrue(audits.contains("perm_grant:CLICK type=CALL_PHONE level=UNLEVEL tier=FUZZY method=NODE_ACTION"))
    }

    companion object {
        private const val PACKAGE = WeComRulePack.PACKAGE_NAME
        private const val TYPE_STATE = PermissionGrantWatcher.TYPE_WINDOW_STATE_CHANGED
        private const val TYPE_CONTENT = PermissionGrantWatcher.TYPE_WINDOW_CONTENT_CHANGED
    }
}
