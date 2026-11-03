/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.accessibility.rules.permissions

/**
 * 企微权限弹窗 4 类（FR-57；docs/01 FR-57 所引 M0 审计证据 apk-analysis/jadx-bad2/sources/j2/m0.java:383-560）。
 *
 * 每类一个特征词组：窗口文本语料命中任一特征词 + [PermissionDialogLexicon.GENERIC_ALLOW]
 * 命中（且不含否定词）即判定为该类弹窗（弱信号文本门，见词表内「真机校准点」标注）。
 */
enum class PermissionDialogType(val code: String, val featureWords: List<String>) {

    /** 设备信息权限弹窗（READ_PHONE_STATE 类预提示）。 */
    DEVICE_INFO("DEVICE_INFO", listOf("设备信息")),

    /** 拨打电话权限弹窗（CALL_PHONE 类预提示）。 */
    CALL_PHONE("CALL_PHONE", listOf("拨打电话")),

    /** 录音/麦克风权限弹窗（RECORD_AUDIO 类预提示）。 */
    MICROPHONE("MICROPHONE", listOf("麦克风", "录音")),

    /** 照片权限弹窗（READ_MEDIA_IMAGES / 相册访问类预提示）。 */
    PHOTOS("PHOTOS", listOf("照片")),
}

/**
 * FR-57 权限弹窗选择器文本特征常量表（唯一事实源；匹配语义见 [PermissionGrantWatcher]）。
 *
 * 文案证据：上游逆向 j2/m0.java（M0 审计，docs/01 FR-57 行所引）：
 * - 主选项三元组 `{"使用应用时允许", "仅在使用该应用时允许", "允许访问全部"}`（m0.java:396）；
 * - 系统弹窗一次性选项 `{"本次使用时允许", "仅限这一次"}`（m0.java:397）；
 * - 否定词（只用于排除识别，**绝不点击**）`{"不允许", "禁止", "禁止且不再询问"}`（m0.java:398）；
 * - 模糊兜底守卫：包含「允许」、不含否定词、不含 '?/？'、文本长度 ≤20、可点祖先 ≤4 层
 *   （m0.java:451-455,457-467）；点击走「可点祖先 ACTION_CLICK → 失败手势补点」（m0.java:519-538）。
 *
 * 真机校准点（D1/真机回归时逐项核实后改此表，不改逻辑）：
 * - [CAL_STEP2]：二级按钮文案上游为「仅在使用该应用时允许」（m0.java:396），本表并列
 *   「仅在使用中允许」变体（任务口径）；MIUI 各版本措辞可能再变。
 * - [CAL_PKG]：包名门默认仅企微（com.tencent.wework，即企微自有预提示页）。系统运行时
 *   弹窗（permissioncontroller / MIUI 安全中心）事件 packageName 非企微；若真机回归发现
 *   需覆盖系统弹窗，扩 [PermissionGrantConfig.watchPackages] 并启用 SYSTEM_ONE_SHOT 层。
 * - [CAL_CLASS]：弹窗 className 特征（如 android.app.Dialog / miui.*）未采信——M0 证据仅
 *   文本特征；真机 dump 后可加强信号，收敛「照片+允许」弱文本误触发面。
 * - [CAL_FUZZY]：模糊兜底层的长度/问号守卫阈值沿上游值；真机可校准。
 */
object PermissionDialogLexicon {

    /** 通用允许词（窗口语料存在性检查；m0.java:395）。 */
    const val GENERIC_ALLOW = "允许"

    /** 一级：使用应用时允许（m0.java:396 第一项）。 */
    val STEP1_WHILE_USE: List<String> = listOf("使用应用时允许")

    /**
     * 二级：仅在使用中允许。
     * [CAL_STEP2] 真机校准点：上游证据文案「仅在使用该应用时允许」+ 任务口径变体「仅在使用中允许」。
     */
    val STEP2_ONLY_IN_USE: List<String> = listOf("仅在使用该应用时允许", "仅在使用中允许")

    /** 三级：允许访问全部（m0.java:396 第三项）。 */
    val STEP3_ALLOW_ALL: List<String> = listOf("允许访问全部")

    /**
     * 系统弹窗一次性选项层（m0.java:397；[CAL_PKG] 真机校准点）。
     * 仅当 [PermissionGrantConfig.acceptSystemOneShot]=true 才纳入点击候选（默认关闭：
     * 一次性授权不持久，每次会话重复弹窗；仅用于真机解锁自动化通路）。
     */
    val SYSTEM_ONE_SHOT: List<String> = listOf("本次使用时允许", "仅限这一次")

    /** 否定词：模糊层排除用，任何情况下绝不点击（m0.java:398,451）。 */
    val DENY_WORDS: List<String> = listOf("不允许", "禁止", "禁止且不再询问")

    /** 模糊兜底：文本最大长度（m0.java:455）。 */
    const val FUZZY_MAX_TEXT_LEN = 20

    /** 文本节点→可点目标向上爬升层数上限（含自身；m0.java:457-467,519-534）。 */
    const val CLICKABLE_CLIMB_DEPTH = 4

    /** 三级授权流的级别顺序（严格层优先于模糊层；同屏合并形态按此序逐个点击）。 */
    val GRANT_STEP_LABELS: List<Pair<String, List<String>>> = listOf(
        "STEP1" to STEP1_WHILE_USE,
        "STEP2" to STEP2_ONLY_IN_USE,
        "STEP3" to STEP3_ALLOW_ALL,
    )
}
