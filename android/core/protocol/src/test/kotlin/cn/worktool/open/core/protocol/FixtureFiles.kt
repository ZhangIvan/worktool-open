/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import java.io.File

/**
 * 夹具访问（protocol/spec.md §7 消费契约：单测以相对仓库根路径引用 protocol/fixtures）。
 * 工作目录可能是模块目录（本地/CI `gradle test`）——向上定位仓库根。
 */
object FixtureFiles {

    val json = Json { ignoreUnknownKeys = false }

    val repoRoot: File by lazy {
        var dir: File? = File(System.getProperty("user.dir") ?: ".")
        while (dir != null) {
            if (File(dir, "protocol/fixtures").isDirectory) return@lazy dir
            dir = dir.parentFile
        }
        error("未找到仓库根（protocol/fixtures 不存在；user.dir=${System.getProperty("user.dir")}）")
    }

    fun fixture(name: String): File = File(repoRoot, "protocol/fixtures/$name")

    fun text(name: String): String = fixture(name).readText(Charsets.UTF_8)

    fun element(name: String): JsonElement = json.parseToJsonElement(text(name))

    /** fixtures 目录全部 json 文件名（README.md 除外）。 */
    fun allFixtureNames(): List<String> =
        fixture(".").listFiles { f -> f.isFile && f.extension == "json" }!!
            .map { it.name }
            .sorted()
}
