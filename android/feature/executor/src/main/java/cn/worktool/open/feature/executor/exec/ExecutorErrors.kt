/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.exec

/**
 * 端侧执行器语义错误码（T3.1）。
 *
 * 族划分对齐 protocol/spec.md §4.3 错误码族（0=成功；10xxx=格式/权限；
 * 20xxx=执行；50xxx=服务端）；本对象内的具体数值为 **WorkTool-Open 端侧扩展
 * 语义码**，非上游已还原值（spec §4.3 已知值仅 101012/201011/201108/50101x）。
 * 回执 errorCode 映射以 T3.2 apifox 冒烟冻结（spec §9 Q2）；与 spec 冲突时以
 * spec 为准并登记 AUD。
 */
object ExecutorErrorCodes {
    /** 载荷不合法（必填字段缺失/空值/无变更项）。 */
    const val PAYLOAD_INVALID = 101001

    /** 指令类型未注册（P0 子集之外）。 */
    const val COMMAND_UNSUPPORTED = 101002

    /** 用户接管暂停（FR-52：暂停期间在线指令只入队不执行，端侧不产生新副作用）。 */
    const val PAUSED_BY_USER_TAKEOVER = 101003

    /**
     * 指令已注册但**按设计不实现**（docs/10 §B：215/216/217/231/232 双侧皆无实现，
     * 显式回执「指令未实现」而非静默——上游行为为消息被忽略无回执）。
     * 上游 101012 映射归 T3.2 回执冻结。
     */
    const val COMMAND_NOT_IMPLEMENTED = 101004

    /** 语义拦截（非群聊目标/群主身份拦截/内部群专属流等非法操作族；上游 101012 族）。 */
    const val ILLEGAL_OPERATION = 101005

    /** 互斥单飞冲突（清理进行中/日志采集进行中再触发；上游 101012 族）。 */
    const val MUTEX_BUSY = 101006

    /** 目标节点未在当前界面找到（含界面根不可得）。 */
    const val NODE_NOT_FOUND = 201001

    /** 等待界面状态超时（节点未在时限内出现）。 */
    const val STEP_TIMEOUT = 201002

    /** 原子操作被界面拒绝（点击/输入不被节点接受）。 */
    const val ACTION_REJECTED = 201003

    /** 执行后校验失败（副作用未见上屏）。 */
    const val VERIFY_FAILED = 201004

    /** 509：目标本地文件不存在或不可读。 */
    const val LOCAL_FILE_MISSING = 201005

    /** 远端文件下载失败（218/243/510 等 fileUrl 族；上游 201107 映射归 T3.2）。 */
    const val DOWNLOAD_FAILED = 201006

    /** 目标群已退出（512 族语义：已退群=失败；对照 515 同态=成功）。 */
    const val GROUP_EXITED = 201008

    /** 二维码无效/过期（243/233 族；上游 201113 映射归 T3.2）。 */
    const val QR_CODE_INVALID = 201009

    /**
     * 目标应用（企微）无法拉起到前台（2026-11-03 会话 9 实证补位：执行器计划
     * 首步即假定企微前台，原 APK 行为=收到指令拉起企微；缺此环节则一切 UI 指令
     * 秒回 NODE_NOT_FOUND）。含：无障碍服务未连接 / 企微未安装 / 拉起后超时未就绪。
     */
    const val TARGET_APP_LAUNCH_FAILED = 201010

    /** 步骤内部异常（防御兜底，正常不应出现）。 */
    const val STEP_ERROR = 201099
}

/** 步骤/执行失败统一载体（errorCode ∈ [ExecutorErrorCodes]）。 */
data class ExecutorFailure(
    val errorCode: Int,
    val reason: String,
    /** 失败发生步骤 ID（报告回填用）。 */
    val stepId: String? = null,
)
