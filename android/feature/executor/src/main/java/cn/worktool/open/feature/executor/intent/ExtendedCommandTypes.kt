/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.intent

/**
 * 对等计划扩展指令号（docs/10-PARITY-PLAN.md §B 68 条全量矩阵）。
 *
 * 这些 type 值来自上游 APK 常量实证（cmd-catalog 对账），但**尚未登记进
 * protocol/spec.md §4.2 全表**——core:protocol `MessageTypes` 因此不含它们
 * （该对象口径 = spec §4.2 已登记集合）。spec 登记属协议车道任务卡；登记完成、
 * core:protocol 补常量后，本对象各项应迁移并入 `MessageTypes` 并删除（迁移时
 * 保持数值不变，两处以本注释为交接凭据）。
 *
 * 车道纪律：本对象仅是**指令号命名常量**，不复制任何外部契约文档内容；
 * 语义依据见各执行器 KDoc（引用 docs/10 §B 行 + spec §4.2 通则）。
 */
object ExtendedCommandTypes {

    /** 235 点击聊天推荐条（docs/10 B3：发起收款/企业名片/推荐客服/商品图册四形态）。 */
    const val CLICK_RECOMMEND_ITEM = 235

    /** 236 删除单向微信客户（docs/10 B3；≤50/次、部分成功）。 */
    const val DELETE_SINGLE_DIRECTIONAL_CONTACT = 236

    /** 237 清理企微存储空间（docs/10 B3；破坏性清理面）。 */
    const val CLEAN_CACHE = 237

    /** 238 批量连续文本发送（docs/10 B2；receivedContent=JSON 数组字符串 ≥2 条）。 */
    const val SEND_BATCH_TEXT_MESSAGE = 238

    /** 239 创建客户群永久入群群活码（docs/10 B3；成功后经 504 通道回传二维码）。 */
    const val CREATE_CUSTOMER_GROUP_LIVE_QRCODE = 239

    /** 240 添加群看板网页（docs/10 B3：群公告网页板式，仅内部群）。 */
    const val ADD_GROUP_BOARD_WEBPAGE = 240

    /** 243 二维码图片 URL 入群（docs/10 B3：个微侧相册扫码入群，依赖 FR-56 规则包）。 */
    const val QR_CODE_JOIN_GROUP = 243

    /** 244 退出群聊（docs/10 B3；异常不重试）。 */
    const val EXIT_GROUP = 244

    /** 283 触发一次新消息接收上报（docs/10 B1；FR-01 执行器族）。 */
    const val DO_RECEIVE_NEW_MESSAGE = 283

    /** 305 清除指定客户端指令（docs/10 B3；被取消者回执 101018 族，端侧映射归 T3.2）。 */
    const val ROBOT_QUEUE_REMOVE = 305

    /** 306 查询待执行队列（docs/10 B3；只读快照）。 */
    const val ROBOT_QUEUE_GET = 306

    /** 307 配置更新通知（docs/10 B3；通知即全量重拉配置）。 */
    const val ROBOT_CONFIG_UPDATED = 307

    /** 510 上传日志文件（docs/10 B3；整日日志打包上传）。 */
    const val UPLOAD_LOG_FILE = 510

    /** 511 会议信息查询（docs/10 B3；结果经消息回调通道推送）。 */
    const val GET_MEETING_INFO = 511

    /** 512 群成员信息查询（docs/10 B3；含群名/备注/群主/公告/成员列表）。 */
    const val GET_GROUP_MEMBER_INFO = 512

    /** 513 设备资源信息查询（docs/10 B3；纯本地采集，不动企微界面）。 */
    const val GET_DEVICE_RESOURCE = 513

    /** 514 获取日志片段（docs/10 B3；gzip+base64 回执载荷）。 */
    const val GET_LOG_SNIPPET = 514

    /** 515 获取群名和群备注（docs/10 B3；轻量查询，不读成员列表）。 */
    const val GET_GROUP_NAME_INFO = 515
}
