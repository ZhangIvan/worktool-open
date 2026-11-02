package io.worktool.open.prototype.policy

object VariantFactory {
    fun endpointPolicy():EndpointPolicy=LabEndpointPolicy()
    fun commandPolicy():CommandPolicy=LabFixtureCommandPolicy()
    val presentation=VariantPresentation(
        "LAB CLEARTEXT · .lab · 仅合成固定文本/nonce与内置note摘要",
        "手输单个 RFC1918 字面 IPv4 + 显式端口 HTTP origin（无默认值）",
        "仅实验候选：明文会暴露配对与会话材料，只能使用一次性实验身份。每次配对或恢复连接都必须重新手输并确认同一精确 origin；禁止真实账号、真实文本与用户文件。",
        "LAB CLEARTEXT：明文传输，无TLS。只准一次性实验凭据与固定合成fixture；确认下方唯一 origin。后台或暂停会断线并清除此本地许可，下次必须重新输入确认；已经发送的内容不可撤回。",
        "LAB FIXTURE PING"
    )
}
