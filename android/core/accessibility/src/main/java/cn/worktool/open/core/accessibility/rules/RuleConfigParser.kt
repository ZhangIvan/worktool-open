/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules

import cn.worktool.open.core.accessibility.json.Json
import cn.worktool.open.core.accessibility.json.JsonValue

/**
 * 规则配置 JSON 解析器（T2.4）。
 *
 * 配置格式（样例见 `assets/accessibility_rules.sample.json`）：
 * ```json
 * {
 *   "version": 1,
 *   "selectors": [
 *     {
 *       "id": "wework.chat.send",
 *       "match": { "text": "发送", "className": "android.widget.TextView" },
 *       "chain": [
 *         { "rel": "child", "index": 2 },
 *         { "rel": "sibling", "offset": -1 },
 *         { "rel": "child", "match": { "textContains": "备注" } }
 *       ]
 *     }
 *   ]
 * }
 * ```
 *
 * 解析策略：**未知键报错**（尽早暴露拼写错误如 `textConains`）；
 * 唯一例外是 `_` 前缀键（`_comment` 等注释约定），解析时忽略。
 */
object RuleConfigParser {

    const val CURRENT_VERSION = 1

    /** 解析完整配置文件文本。 */
    fun parse(text: String): RuleConfig {
        val root = try {
            Json.parse(text)
        } catch (e: Json.JsonParseException) {
            throw RuleConfigException("JSON 语法错误: ${e.message}", e)
        }
        val obj = root as? JsonValue.JsonObject
            ?: throw RuleConfigException("配置根必须是 JSON 对象")
        checkUnknownKeys(obj, "根对象")
        val version = obj.int("version")
            ?: throw RuleConfigException("根对象缺少整型字段 'version'")
        if (version != CURRENT_VERSION) {
            throw RuleConfigException("不支持的配置版本 $version（当前支持 $CURRENT_VERSION）")
        }
        val arr = obj.array("selectors")
            ?: throw RuleConfigException("根对象缺少数组字段 'selectors'")
        val selectors = arr.items.mapIndexed { i, v ->
            val o = v as? JsonValue.JsonObject
                ?: throw RuleConfigException("selectors[$i] 必须是对象")
            try {
                parseSelectorObject(o)
            } catch (e: RuleConfigException) {
                throw RuleConfigException("selectors[$i]: ${e.message}", e)
            }
        }
        if (selectors.isEmpty()) throw RuleConfigException("'selectors' 不能为空")
        val dup = selectors.groupBy { it.id }.filterValues { it.size > 1 }.keys
        if (dup.isNotEmpty()) throw RuleConfigException("选择器 id 重复: ${dup.joinToString()}")
        return RuleConfig(version, selectors)
    }

    /**
     * 解析单个选择器对象（调试页「选择器试掷」直接粘贴一个 selector 对象用）。
     * 输入形状与 `selectors[]` 元素一致。
     */
    fun parseSelectorObject(o: JsonValue.JsonObject): SelectorDef {
        checkUnknownKeys(o, "selector")
        val id = o.string("id")
            ?: throw RuleConfigException("缺少字符串字段 'id'")
        val matchObj = o.obj("match")
            ?: throw RuleConfigException("缺少对象字段 'match'")
        val match = parseMatch(matchObj)
        if (match.isEmpty && o.array("chain") == null) {
            throw RuleConfigException("'match' 至少需要一个条件（text/textContains/resourceId/className）")
        }
        val chainArr = o.array("chain")
        val chain = chainArr?.items?.mapIndexed { i, v ->
            val so = v as? JsonValue.JsonObject
                ?: throw RuleConfigException("chain[$i] 必须是对象")
            try {
                parseChainStep(so)
            } catch (e: RuleConfigException) {
                throw RuleConfigException("chain[$i]: ${e.message}", e)
            }
        } ?: emptyList()
        return SelectorDef(id, match, chain)
    }

    private fun parseMatch(o: JsonValue.JsonObject): MatchSpec {
        checkUnknownKeys(o, "match")
        val text = o.string("text")
        val textContains = o.string("textContains")
        val resourceId = o.string("resourceId")
        val className = o.string("className")
        for (k in listOf("text", "textContains", "resourceId", "className")) {
            if (o.fields[k] != null && o.fields[k] !is JsonValue.JsonString) {
                throw RuleConfigException("match.$k 必须是字符串")
            }
        }
        return MatchSpec(text, textContains, resourceId, className)
    }

    private fun parseChainStep(o: JsonValue.JsonObject): ChainStep {
        checkUnknownKeys(o, "chain 步")
        val relStr = o.string("rel")
            ?: throw RuleConfigException("缺少字符串字段 'rel'（child/sibling）")
        val relation = when (relStr) {
            "child" -> ChainRelation.CHILD
            "sibling" -> ChainRelation.SIBLING
            else -> throw RuleConfigException("rel 必须是 'child' 或 'sibling'，得到 '$relStr'")
        }
        val index = intOrNull(o, "index")?.also {
            if (it < 0) throw RuleConfigException("index 不能为负（得到 $it）")
        }
        val offset = intOrNull(o, "offset")
        val match = o.obj("match")?.let { parseMatch(it) }
        if (match != null && (index != null || offset != null)) {
            throw RuleConfigException("链步的 'match' 与 'index'/'offset' 互斥，只能二选一")
        }
        if (relation == ChainRelation.CHILD && offset != null) {
            throw RuleConfigException("child 关系不接受 'offset'（应使用 'index'）")
        }
        if (relation == ChainRelation.SIBLING && index != null) {
            throw RuleConfigException("sibling 关系不接受 'index'（应使用 'offset'）")
        }
        // T4.4（docs/11 B1）：child 步无谓词时必须显式 index；「缺省回退 0 号子节点」
        // 在零匹配时静默误点，已在解析期拒绝（引擎期 stepFrom 同步拒绝，双保险）。
        if (relation == ChainRelation.CHILD && match == null && index == null) {
            throw RuleConfigException("child 链步必须显式 'index' 或 'match'（缺省 child0 兜底已禁止，T4.4）")
        }
        return ChainStep(relation, index, offset, match)
    }

    private fun intOrNull(o: JsonValue.JsonObject, key: String): Int? {
        val v = o.fields[key] ?: return null
        val n = v as? JsonValue.JsonNumber
            ?: throw RuleConfigException("$key 必须是整数")
        if (!n.isIntegralText) throw RuleConfigException("$key 必须是整数（得到 ${n.raw}）")
        return n.int
    }

    private fun checkUnknownKeys(o: JsonValue.JsonObject, where: String) {
        val unknown = o.fields.keys.filterNot { it.startsWith("_") }
        // 已知键集合按调用上下文宽松校验：解析器逐字段取值，此处只拦「完全不被识别」的键
        val known = setOf(
            "version", "selectors", "id", "match", "chain",
            "text", "textContains", "resourceId", "className",
            "rel", "index", "offset",
        )
        val bad = unknown.filter { it !in known }
        if (bad.isNotEmpty()) {
            throw RuleConfigException("$where 含未知字段: ${bad.joinToString()}（提示：注释请用 _ 前缀键）")
        }
    }
}
