/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.pack

import cn.worktool.open.core.accessibility.json.Json
import cn.worktool.open.core.accessibility.json.JsonValue
import cn.worktool.open.core.accessibility.rules.RuleConfig
import cn.worktool.open.core.accessibility.rules.RuleConfigParser

/**
 * 包清单 JSON 解析器（pack.json，schemaVersion=1）。
 *
 * 与 [RuleConfigParser] 同一口径：未知键报错（配置漂移尽早暴露），`_` 前缀键为注释约定。
 * 选择器文件**不在此解析**——`selectorsFormat=ruleconfig-v1` 时直接交给既有
 * [RuleConfigParser.parse]（设计 §1.1：选择器文件保持纯 RuleConfig v1 形态，不扩展字段集）。
 */
object RulePackParser {

    const val CURRENT_SCHEMA_VERSION = 1
    const val SELECTORS_FORMAT_RULECONFIG_V1 = "ruleconfig-v1"

    /** 解析包清单文本（不解析选择器文件；选择器由 [RulePackRegistry.register] 联动装载）。 */
    fun parseManifest(text: String): RulePackManifest {
        val root = try {
            Json.parse(text)
        } catch (e: Json.JsonParseException) {
            throw RulePackException("pack.json JSON 语法错误: ${e.message}", e)
        }
        val obj = root as? JsonValue.JsonObject
            ?: throw RulePackException("pack.json 根必须是 JSON 对象")
        checkUnknownKeys(obj, "pack.json 根")

        val schemaVersion = obj.int("schemaVersion")
            ?: throw RulePackException("pack.json 缺少整型字段 'schemaVersion'")
        if (schemaVersion != CURRENT_SCHEMA_VERSION) {
            throw RulePackException("不支持的 pack schemaVersion $schemaVersion（当前支持 $CURRENT_SCHEMA_VERSION）")
        }
        val packId = obj.string("packId")?.takeIf { it.isNotBlank() }
            ?: throw RulePackException("pack.json 缺少非空字符串字段 'packId'")
        val app = when (val appStr = obj.string("app")) {
            "WEWORK" -> PackApp.WEWORK
            "WECHAT" -> PackApp.WECHAT
            else -> throw RulePackException("app 必须是 'WEWORK' 或 'WECHAT'，得到 '$appStr'")
        }
        val packageName = obj.string("packageName")?.takeIf { it.isNotBlank() }
            ?: throw RulePackException("pack.json 缺少非空字符串字段 'packageName'")
        val selectorNamespace = obj.string("selectorNamespace")?.takeIf { it.isNotBlank() }
            ?: throw RulePackException("pack.json 缺少非空字符串字段 'selectorNamespace'")
        val selectorsFile = obj.string("selectorsFile")?.takeIf { it.isNotBlank() }
            ?: throw RulePackException("pack.json 缺少非空字符串字段 'selectorsFile'")
        val selectorsFormat = obj.string("selectorsFormat")
            ?: throw RulePackException("pack.json 缺少字符串字段 'selectorsFormat'")
        if (selectorsFormat != SELECTORS_FORMAT_RULECONFIG_V1) {
            throw RulePackException("selectorsFormat 必须是 '$SELECTORS_FORMAT_RULECONFIG_V1'，得到 '$selectorsFormat'")
        }
        val defaultEnabled = obj.bool("defaultEnabled")
            ?: throw RulePackException("pack.json 缺少布尔字段 'defaultEnabled'")

        val risk = obj.obj("riskConfirm")?.let { rc ->
            checkUnknownKeys(rc, "riskConfirm")
            RiskConfirmPolicy(
                required = rc.bool("required")
                    ?: throw RulePackException("riskConfirm.required 必须是布尔"),
                policyRef = rc.string("policyRef"),
                reconfirmOnMajorBump = rc.bool("reconfirmOnMajorBump") ?: false,
            )
        } ?: RiskConfirmPolicy(required = false)

        val screensArr = obj.array("screens")
        val screens = screensArr?.items?.mapIndexed { i, v ->
            val so = v as? JsonValue.JsonObject
                ?: throw RulePackException("screens[$i] 必须是对象")
            checkUnknownKeys(so, "screens[$i]")
            val id = so.string("id")?.takeIf { it.isNotBlank() }
                ?: throw RulePackException("screens[$i] 缺少非空字符串字段 'id'")
            val assertArr = so.array("assertSelectors")
                ?: throw RulePackException("screens[$i](${id}) 缺少数组字段 'assertSelectors'")
            if (assertArr.items.isEmpty()) {
                throw RulePackException("screens[$i]($id) assertSelectors 不能为空（G5 每屏至少 1 个断言选择器）")
            }
            val assertSelectors = assertArr.items.mapIndexed { j, av ->
                (av as? JsonValue.JsonString)?.value?.takeIf { it.isNotBlank() }
                    ?: throw RulePackException("screens[$i]($id).assertSelectors[$j] 必须是非空字符串")
            }
            PackScreen(id, assertSelectors)
        } ?: emptyList()

        val commands = parseIntArray(obj, "commands")
        val naCommands = parseIntArray(obj, "naCommands")
        val overlap = commands.toSet() intersect naCommands.toSet()
        if (overlap.isNotEmpty()) {
            throw RulePackException("packId=$packId 同一指令不得同时出现在 commands 与 naCommands: ${overlap.sorted()}")
        }

        return RulePackManifest(
            schemaVersion = schemaVersion,
            packId = packId,
            app = app,
            packageName = packageName,
            selectorNamespace = selectorNamespace,
            selectorsFile = selectorsFile,
            selectorsFormat = selectorsFormat,
            defaultEnabled = defaultEnabled,
            riskConfirm = risk,
            screens = screens,
            commands = commands,
            naCommands = naCommands,
            calibratedOn = obj.string("calibratedOn"),
        )
    }

    /** 解析选择器文件文本（格式固定 ruleconfig-v1 → 复用既有解析器，错误原样透传）。 */
    fun parseSelectors(text: String): RuleConfig = RuleConfigParser.parse(text)

    private fun parseIntArray(obj: JsonValue.JsonObject, key: String): List<Int> {
        val arr = obj.array(key) ?: return emptyList()
        return arr.items.mapIndexed { i, v ->
            val n = v as? JsonValue.JsonNumber
                ?: throw RulePackException("$key[$i] 必须是整数")
            if (!n.isIntegralText) throw RulePackException("$key[$i] 必须是整数（得到 ${n.raw}）")
            n.int
        }
    }

    private fun checkUnknownKeys(o: JsonValue.JsonObject, where: String) {
        val known = setOf(
            "schemaVersion", "packId", "app", "packageName", "selectorNamespace",
            "selectorsFile", "selectorsFormat", "defaultEnabled", "riskConfirm",
            "screens", "commands", "naCommands", "calibratedOn",
            // 嵌套对象键
            "required", "policyRef", "reconfirmOnMajorBump", "id", "assertSelectors",
        )
        val bad = o.fields.keys.filter { !it.startsWith("_") && it !in known }
        if (bad.isNotEmpty()) {
            throw RulePackException("$where 含未知字段: ${bad.joinToString()}（提示：注释请用 _ 前缀键）")
        }
    }
}
