/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.android

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import cn.worktool.open.core.accessibility.ops.ClipboardGateway

/**
 * [ClipboardGateway] 真机实现：系统剪贴板（inputText 的 PASTE 回退通道）。
 *
 * 隐私注记：剪贴板内容仅为待输入文本本体（本就即将写入目标输入框），不涉及额外采集；
 * 写入失败（如厂商策略限制）如实返回 false，由操作库转为 CLIPBOARD_UNAVAILABLE。
 */
class SystemClipboardGateway(private val context: Context) : ClipboardGateway {

    override fun setText(text: String): Boolean = try {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
        cm.setPrimaryClip(ClipData.newPlainText(CLIP_LABEL, text))
        true
    } catch (_: Exception) {
        false
    }

    companion object {
        private const val CLIP_LABEL = "worktool-open-input"
    }
}
