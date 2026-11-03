/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/** 装载门 G1/G2（设计 §1.4）：结构校验 + 包间 resource-id 前缀隔离。 */
class RulePackRegistryTest {

    private fun minimalPackJson(
        packId: String = "wechat",
        packageName: String = "com.tencent.mm",
        namespace: String = "wechat.",
        defaultEnabled: Boolean = false,
        schemaVersion: Int = 1,
        selectorsFormat: String = "ruleconfig-v1",
        riskRequired: Boolean = true,
    ): String = """
        {
          "schemaVersion": $schemaVersion,
          "packId": "$packId",
          "app": "WECHAT",
          "packageName": "$packageName",
          "selectorNamespace": "$namespace",
          "selectorsFile": "selectors.json",
          "selectorsFormat": "$selectorsFormat",
          "defaultEnabled": $defaultEnabled,
          "riskConfirm": { "required": $riskRequired },
          "screens": [],
          "commands": [203],
          "naCommands": []
        }
    """.trimIndent()

    private fun selectorsJson(
        vararg ids: String,
        resourceId: String? = null,
    ): String {
        val matchFields = StringBuilder().apply {
            if (resourceId != null) append("\"resourceId\": \"").append(resourceId).append("\", ")
            append("\"className\": \"android.widget.TextView\"")
        }
        val sel = ids.joinToString(", ") { id ->
            "{ \"id\": \"$id\", \"match\": { $matchFields } }"
        }
        return "{ \"version\": 1, \"selectors\": [ $sel ] }"
    }

    /** #1 内置微信包可装载（G1 全过；FR-56 骨架自身合法）。 */
    @Test
    fun `G1 - 内置微信包装载成功且绑定微信包名`() {
        val registry = RulePackRegistry()
        val pack = WeChatRulePack.loadInto(registry)
        assertEquals("com.tencent.mm", pack.manifest.packageName)
        assertEquals(PackApp.WECHAT, pack.manifest.app)
        assertTrue(pack.manifest.riskConfirm.required)
        assertTrue(!pack.manifest.defaultEnabled)
        assertEquals(registry.byPackId("wechat"), pack)
    }

    /** #2 内置企微包可装载，且与微信包同注册表共存（packId/包名/命名空间互不冲突）。 */
    @Test
    fun `G1 - 内置企微包与微信包共存装载`() {
        val registry = RulePackRegistry()
        WeChatRulePack.loadInto(registry)
        WeComRulePack.loadInto(registry)
        assertEquals(2, registry.registered().size)
        assertEquals("com.tencent.mm", registry.byApp(PackApp.WECHAT)!!.manifest.packageName)
        assertEquals("com.tencent.wework", registry.byApp(PackApp.WEWORK)!!.manifest.packageName)
        assertEquals(registry.byApp(PackApp.WECHAT), registry.byPackageName("com.tencent.mm"))
    }

    /** #3 packId 重复注册拒绝（G1 唯一性）。 */
    @Test
    fun `G1 - packId 重复注册拒绝`() {
        val registry = RulePackRegistry()
        WeChatRulePack.loadInto(registry)
        val e = assertThrows(RulePackException::class.java) {
            registry.register(minimalPackJson(), selectorsJson("wechat.a"))
        }
        assertTrue(e.message!!.contains("已注册"))
    }

    /** #4 选择器 ID 越出命名空间拒绝（G1）。 */
    @Test
    fun `G1 - 选择器 ID 越出命名空间拒绝`() {
        val registry = RulePackRegistry()
        val e = assertThrows(RulePackException::class.java) {
            registry.register(minimalPackJson(), selectorsJson("wechat.a", "wework.b"))
        }
        assertTrue(e.message!!.contains("越出命名空间"))
    }

    /** #5 G2 前缀门：微信包选择器携带企微 resource-id → 装载报错（跨包资源名静态阻断）。 */
    @Test
    fun `G2 - 微信包携带企微 resourceId 拒绝`() {
        val registry = RulePackRegistry()
        WeComRulePack.loadInto(registry) // 企微包名先注册 → 成为微信包的禁入前缀
        val e = assertThrows(RulePackException::class.java) {
            registry.register(minimalPackJson(), selectorsJson("wechat.a", resourceId = "com.tencent.wework:id/chat_input"))
        }
        assertTrue(e.message!!.contains("G2"))
    }

    /** #6 G2 前缀门反向：企微包携带微信 resource-id → 同样拒绝（隔离对称）。 */
    @Test
    fun `G2 - 企微包携带微信 resourceId 拒绝`() {
        val registry = RulePackRegistry()
        WeChatRulePack.loadInto(registry)
        val e = assertThrows(RulePackException::class.java) {
            registry.register(
                minimalPackJson(packId = "wework2", packageName = "com.tencent.wework2", namespace = "wework2.", riskRequired = false),
                selectorsJson("wework2.a", resourceId = "com.tencent.mm:id/anything"),
            )
        }
        assertTrue(e.message!!.contains("G2"))
    }

    /**
     * #6b G2 双向 lint（CR wechat-core-rev-01）：先注册包携带**尚未注册**包的前缀，
     * 后注册该包时必须反查拒绝——旧实现只查「新包 vs 已注册包」，先注册者逃逸
     * （微信先装载、企微后装载时，微信携带 com.tencent.wework:id/ 静默过门）。
     */
    @Test
    fun `G2 - 先注册包携带后注册包前缀被反查拒绝`() {
        val registry = RulePackRegistry()
        // alpha 先注册：当时 beta 不在册，无他包前缀可冲突 → 装载放行（旧实现止步于此）
        registry.register(
            minimalPackJson(packId = "alpha", packageName = "com.alpha", namespace = "alpha.", riskRequired = false),
            selectorsJson("alpha.a", resourceId = "com.beta:id/leak"),
        )
        // beta 后注册：双向 lint 反查 alpha 携带 com.beta:id/ → beta 注册被拒
        val e = assertThrows(RulePackException::class.java) {
            registry.register(
                minimalPackJson(packId = "beta", packageName = "com.beta", namespace = "beta.", riskRequired = false),
                selectorsJson("beta.a"),
            )
        }
        assertTrue(e.message!!.contains("G2"))
        assertTrue(e.message!!.contains("alpha"))
        assertTrue(e.message!!.contains("双向 lint"))
        // 注册被拒 = 不留半态：beta 不在册，alpha 原样保留（调用方按装载失败上报）
        assertNull(registry.byPackId("beta"))
        assertEquals(1, registry.registered().size)
    }

    /** #6c 同一冲突对反序装载同样拒绝（正向拦截路径）——与 #6b 共同证明与注册顺序无关。 */
    @Test
    fun `G2 - 同一冲突对反序装载同样拒绝`() {
        val registry = RulePackRegistry()
        registry.register(
            minimalPackJson(packId = "beta", packageName = "com.beta", namespace = "beta.", riskRequired = false),
            selectorsJson("beta.a"),
        )
        val e = assertThrows(RulePackException::class.java) {
            registry.register(
                minimalPackJson(packId = "alpha", packageName = "com.alpha", namespace = "alpha.", riskRequired = false),
                selectorsJson("alpha.a", resourceId = "com.beta:id/leak"),
            )
        }
        assertTrue(e.message!!.contains("G2"))
        assertEquals(1, registry.registered().size)
    }

    /** #7 屏幕断言选择器引用缺失拒绝（G1；防走错屏的断言表必须自洽）。 */
    @Test
    fun `G1 - 屏幕断言选择器缺失拒绝`() {
        val registry = RulePackRegistry()
        val json = minimalPackJson().replace(
            """"screens": []""",
            """"screens": [ { "id": "wechat.home", "assertSelectors": ["wechat.nav.missing"] } ]""",
        )
        val e = assertThrows(RulePackException::class.java) {
            registry.register(json, selectorsJson("wechat.a"))
        }
        assertTrue(e.message!!.contains("wechat.nav.missing"))
    }

    /** #8 selectorsFormat 非 ruleconfig-v1 拒绝（G1：不认识的格式不得静默跳过选择器装载）。 */
    @Test
    fun `G1 - 未知 selectorsFormat 拒绝`() {
        val registry = RulePackRegistry()
        assertThrows(RulePackException::class.java) {
            registry.register(minimalPackJson(selectorsFormat = "ruleconfig-v2"), selectorsJson("wechat.a"))
        }
    }

    /** #9 schemaVersion 不支持拒绝（向前 fail-closed）。 */
    @Test
    fun `G1 - schemaVersion 不支持拒绝`() {
        val registry = RulePackRegistry()
        assertThrows(RulePackException::class.java) {
            registry.register(minimalPackJson(schemaVersion = 2), selectorsJson("wechat.a"))
        }
    }

    /** #10 选择器文件解析失败拒绝（G1：RuleConfig v1 语法错误透传）。 */
    @Test
    fun `G1 - 选择器文件语法错误拒绝`() {
        val registry = RulePackRegistry()
        val e = assertThrows(RulePackException::class.java) {
            registry.register(minimalPackJson(), "{ not-json")
        }
        assertTrue(e.message!!.contains("解析失败"))
    }

    /** #11 同一指令同时进 commands 与 naCommands 拒绝（清单自洽）。 */
    @Test
    fun `G1 - commands 与 naCommands 重叠拒绝`() {
        val registry = RulePackRegistry()
        val json = minimalPackJson().replace("\"naCommands\": []", "\"naCommands\": [203]")
        val e = assertThrows(RulePackException::class.java) {
            registry.register(json, selectorsJson("wechat.a"))
        }
        assertTrue(e.message!!.contains("naCommands"))
    }

    /** #12 内置微信包红线自检：全部选择器禁填 resourceId（自研无厂商参照，待 calibrate）。 */
    @Test
    fun `红线 - 内置微信包选择器零 resourceId`() {
        val pack = WeChatRulePack.parse()
        val withResId = pack.selectors.selectors.filter { it.match.resourceId != null }
        assertTrue("微信包红线：禁填 resourceId（发现=${withResId.map { it.id }}）", withResId.isEmpty())
    }

    /** #13 内置微信包 naCommands 覆盖企微专属指令（FR-56：221/222/223/208/209/210 N/A）。 */
    @Test
    fun `FR56 - 微信包 naCommands 与 commands 语义`() {
        val pack = WeChatRulePack.parse()
        assertTrue(pack.naForCommand(221))
        assertTrue(pack.naForCommand(210))
        assertTrue(!pack.naForCommand(203))
        assertTrue(pack.supportsCommand(203))
        assertTrue(pack.supportsCommand(212))
        assertTrue(!pack.supportsCommand(221))
        assertNull(pack.selector("wework.chat.input")) // 命名空间隔离：微信包取不到企微选择器
        assertNotNull(pack.screen("wechat.contacts.new_friends"))
    }
}
