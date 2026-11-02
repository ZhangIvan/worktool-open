/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.app

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.widget.Button
import android.widget.TextView

/**
 * 壳工程入口（T1.2 起）：引导页。
 *
 * 静态展示应用标识与阶段说明；T2.4 增加入口：节点树调试页（无障碍规则校准工具）。
 * 服务端地址配置/扫码配对（模式 B）、权限引导、悬浮窗控制（FR-50）在后续任务卡落地。
 * SR-10：无任何伪装命名。
 */
class MainActivity : Activity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        findViewById<TextView>(R.id.guide_stage).text =
            getString(R.string.guide_stage, AppInfo.VERSION)
        findViewById<Button>(R.id.guide_open_debug).setOnClickListener {
            startActivity(Intent(this, NodeTreeDebugActivity::class.java))
        }
    }
}
