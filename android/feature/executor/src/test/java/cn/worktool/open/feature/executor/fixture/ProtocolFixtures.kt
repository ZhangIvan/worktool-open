/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.fixture

import java.io.File

/**
 * protocol/ 权威夹具访问（spec §7 消费契约：单测以相对仓库根路径引用
 * protocol/fixtures）。core:protocol 的 FixtureFiles 位于其 test 源集不可跨模块
 * 复用，此处等价实现（同一定位策略：向上查找仓库根）。
 */
object ProtocolFixtures {

    fun text(name: String): String = file(name).readText(Charsets.UTF_8)

    private val repoRoot: File by lazy {
        var dir: File? = File(System.getProperty("user.dir") ?: ".")
        while (dir != null) {
            if (File(dir, "protocol/fixtures").isDirectory) return@lazy dir
            dir = dir.parentFile
        }
        error("未找到仓库根（protocol/fixtures 不存在；user.dir=${System.getProperty("user.dir")}）")
    }

    private fun file(name: String): File = File(repoRoot, "protocol/fixtures/$name")
}
