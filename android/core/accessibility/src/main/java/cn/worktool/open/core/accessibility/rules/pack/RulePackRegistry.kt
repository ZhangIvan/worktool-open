/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

/**
 * 规则包注册表（FR-56，设计 §1.4 装载门）。
 *
 * 装载门 G1/G2（任一失败 → 该包整体不可用，异常透传给调用方按
 * 101002 COMMAND_UNSUPPORTED 上报，不提供「降级装载」）：
 *
 * - **G1 结构门**：`pack.json` 可解析（[RulePackParser]）；`packId` 唯一；
 *   包内全部选择器 ID 以 `selectorNamespace` 前缀开头；`selectorsFile` 文本可被
 *   RuleConfigParser v1 解析；`screens[].assertSelectors` 引用的选择器全部存在。
 * - **G2 resource-id 前缀门（双向全量）**：任意已注册包的选择器 `resourceId` 均不得携带
 *   **其他已注册包**的包名前缀（微信包含 `com.tencent.wework:id/` → 装载报错；企微包含
 *   `com.tencent.mm:id/` 反向同理）——包间隔离在装载期静态阻断，而非运行期才暴露。
 *   每次注册都对「新包 → 已注册包」与「已注册包 → 新包」双向对查（CR wechat-core-rev-01
 *   修复，2026-11-03）：原实现只查前者，lint 结果依赖注册顺序——先注册的包会逃逸后注册
 *   包前缀的静态拦截（如微信先装载、企微后装载时，微信选择器携带 `com.tencent.wework:id/`
 *   静默过门），恰是 W-M5R 真机 calibrate 回填 resourceId 时最危险的泄漏方向。
 *   修复后注册结果与装载顺序无关；**W-M5R 回填前置条件「G2 双向 lint 修复先行」已满足**。
 *
 * 线程模型：注册与查询均在调用方串行初始化期完成（app 装配阶段），运行期只读。
 */
class RulePackRegistry {

    private val packsById = LinkedHashMap<String, RulePack>()

    /** 已注册包快照（packId 有序稳定）。 */
    fun registered(): List<RulePack> = packsById.values.toList()

    fun byPackId(packId: String): RulePack? = packsById[packId]

    fun byApp(app: PackApp): RulePack? = packsById.values.firstOrNull { it.manifest.app == app }

    fun byPackageName(packageName: String): RulePack? =
        packsById.values.firstOrNull { it.manifest.packageName == packageName }

    /**
     * 注册一个包（G1+G2 全量 lint）。同一 packId 重复注册 = 调用方误用，报错。
     *
     * @param packJson pack.json 文本
     * @param selectorsJson 选择器文件文本（manifest.selectorsFile 指向的文件内容）
     */
    fun register(packJson: String, selectorsJson: String): RulePack {
        val manifest = RulePackParser.parseManifest(packJson)

        // G1：packId 唯一
        if (packsById.containsKey(manifest.packId)) {
            throw RulePackException("packId='${manifest.packId}' 已注册（重复装载）")
        }

        val selectors = try {
            RulePackParser.parseSelectors(selectorsJson)
        } catch (e: cn.worktool.open.core.accessibility.rules.RuleConfigException) {
            throw RulePackException(
                "pack '${manifest.packId}' 选择器文件 ${manifest.selectorsFile} 解析失败: ${e.message}", e,
            )
        }

        // G1：选择器 ID 命名空间一致
        val offNamespace = selectors.selectors.map { it.id }
            .filter { !it.startsWith(manifest.selectorNamespace) }
        if (offNamespace.isNotEmpty()) {
            throw RulePackException(
                "pack '${manifest.packId}' 选择器 ID 越出命名空间 '${manifest.selectorNamespace}': " +
                    offNamespace.joinToString(),
            )
        }
        if (selectors.selectors.isEmpty()) {
            throw RulePackException("pack '${manifest.packId}' 选择器集为空")
        }

        // G2：resource-id 不得携带其他包的包名前缀——双向对查（CR wechat-core-rev-01，
        // 2026-11-03）：① 新包选择器 vs 已注册包前缀；② 已注册包选择器 vs 新包前缀。
        // 只做①时 lint 依赖注册顺序（先注册的包逃逸后注册包前缀的拦截）；①+② 后
        // 任何一对包无论谁先装载都会在第二次 register 处被拒，与顺序无关。
        // ②失败 = 新包整体不可用（本次注册被拒、已注册包原样保留），异常透传给调用方。
        val foreignPrefixes = packsById.values
            .map { it.manifest.packageName }
            .filter { it != manifest.packageName }
            .map { "$it:id/" }
        selectors.selectors.forEach { sel ->
            val rid = sel.match.resourceId ?: return@forEach
            foreignPrefixes.filter { rid.contains(it) }.forEach { prefix ->
                throw RulePackException(
                    "G2 前缀门：pack '${manifest.packId}' 选择器 '${sel.id}' resourceId=$rid " +
                        "携带他包前缀 $prefix（跨包资源名禁止）",
                )
            }
        }
        val newPrefix = "${manifest.packageName}:id/"
        for (registered in packsById.values) {
            if (registered.manifest.packageName == manifest.packageName) continue
            for (sel in registered.selectors.selectors) {
                val rid = sel.match.resourceId ?: continue
                if (rid.contains(newPrefix)) {
                    throw RulePackException(
                        "G2 前缀门：pack '${registered.manifest.packId}'（先注册）选择器 " +
                            "'${sel.id}' resourceId=$rid 携带新注册包 '${manifest.packId}' 前缀 " +
                            "$newPrefix（跨包资源名禁止；双向 lint，与注册顺序无关）",
                    )
                }
            }
        }

        // G1：屏幕断言选择器全部存在
        manifest.screens.forEach { screen ->
            screen.assertSelectors.forEach { sid ->
                if (selectors.selector(sid) == null) {
                    throw RulePackException(
                        "pack '${manifest.packId}' 屏幕 '${screen.id}' 断言选择器 '$sid' 在选择器集中不存在",
                    )
                }
            }
        }

        val pack = RulePack(manifest, selectors)
        packsById[manifest.packId] = pack
        return pack
    }
}
