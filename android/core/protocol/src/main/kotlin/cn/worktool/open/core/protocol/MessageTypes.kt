/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.core.protocol

/**
 * 指令/消息类型与信封常量（protocol/spec.md §4.1/§4.2 全表）。
 * 9xx 为 WorkTool-Open 扩展控制段（上游已还原类型仅 11~509，无碰撞）。
 */
object MessageTypes {
    // socketType（spec §4.1）
    const val SOCKET_HEART_BEAT = 0
    const val SOCKET_CONFIRM = 1
    const val SOCKET_MESSAGE_LIST = 2
    const val SOCKET_RAW_CONFIRM = 3

    // 心跳/上报
    const val HEART_BEAT = 11
    const val TYPE_RECEIVE_MESSAGE_LIST = 101
    const val TYPE_CONSOLE_TOAST = 102

    // 操作 201~234（spec §4.2）
    const val STOP_AND_GO_HOME = 201
    const val LOOP_RECEIVE_NEW_MESSAGE = 202
    const val SEND_MESSAGE = 203
    const val REPLY_MESSAGE = 204
    const val RELAY_MESSAGE = 205
    const val INIT_GROUP = 206
    const val UPDATE_GROUP = 207
    const val PUSH_MICRO_DISK_IMAGE = 208
    const val PUSH_MICRO_DISK_FILE = 209
    const val PUSH_MICROPROGRAM = 210
    const val PUSH_OFFICE = 211
    const val PASS_ALL_FRIEND_REQUEST = 212
    const val ADD_FRIEND_BY_PHONE = 213
    const val SHOW_GROUP_INFO = 214
    const val INIT_ANTI_HARASSMENT_RULE = 215
    const val UPDATE_ANTI_HARASSMENT_RULE = 216
    const val DELETED_ANTI_HARASSMENT_RULE = 217
    const val PUSH_FILE = 218
    const val DISMISS_GROUP = 219
    const val ADD_FRIEND_BY_GROUP = 220
    const val ADD_NEED_DEAL = 221
    const val CLOCK_IN = 222
    const val SWITCH_CORP = 223
    const val PUSH_LINK = 224
    const val MODIFY_GROUP_MEMBER_INFO = 225
    const val RECALL_MESSAGE = 226
    const val RELAY_MULTI_MESSAGE = 227
    const val RELAY_MERGE_MESSAGE = 228
    const val SEND_MULTI_MESSAGE = 229
    const val SEND_MERGE_MESSAGE = 230
    const val VOICE_CALL = 231
    const val VIDEO_CALL = 232
    const val SCAN_QR_CODE = 233
    const val DELETE_CONTACT = 234

    // 日志 301~304
    const val ROBOT_LOG = 301
    const val ROBOT_ERROR_LOG = 302
    const val ROBOT_CONTROLLER_TEST = 303
    const val ROBOT_QUEUE_CLEAR = 304

    // 获取 501~509
    const val GET_GROUP_INFO = 501
    const val GET_FRIEND_INFO = 502
    const val GET_MY_INFO = 503
    const val GET_GROUP_QRCODE = 504
    const val GET_RECENT_LIST = 505
    const val GET_CORP_LIST = 506
    const val GET_ALL_FRIEND_INFO = 507
    const val GET_ALL_GROUP_INFO = 508
    const val GET_LOCAL_FILE = 509

    // v2 控制类 901~904（WorkTool-Open 扩展段）
    const val CREDENTIAL_ROTATE = 901
    const val CREDENTIAL_REVOKED = 902
    const val RESYNC_REQUEST = 903
    const val JOURNAL_REPLAY = 904

    /** 上游已还原 type 范围（9xx 之外的合法值域；9xx 为我方扩展）。 */
    val UPSTREAM_RANGE = 11..509

    /** v2 扩展控制段。 */
    val V2_CONTROL_RANGE = 901..904

    fun isKnown(type: Int): Boolean = type in UPSTREAM_RANGE || type in V2_CONTROL_RANGE
}

/** roomType 枚举（spec §4.3）。 */
object RoomTypes {
    const val UNKNOWN = 0
    const val EXTERNAL_GROUP = 1
    const val EXTERNAL_CONTACT = 2
    const val INTERNAL_GROUP = 3
    const val INTERNAL_CONTACT = 4
}

/** textType 枚举（spec §4.3，0~18）。 */
object TextTypes {
    const val UNKNOWN = 0
    const val TEXT = 1
    const val IMAGE = 2
    const val VOICE = 3
    const val CARD = 4
    const val VIDEO = 5
    const val LOCATION = 6
    const val MICROPROGRAM = 7
    const val LINK = 8
    const val FILE = 9
    const val WARNING = 10
    const val TENCENT_DOC = 11
    const val GROUP_SOLITAIRE = 12
    const val MERGED_CHAT = 13
    const val GROUP_FORM = 14
    const val QUOTED_REPLY = 15
    const val INTERNAL_GROUP_BOT_NOTICE = 16
    const val CHANNEL_VIDEO = 17
    const val CHANNEL_LIVE = 18
}
