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
