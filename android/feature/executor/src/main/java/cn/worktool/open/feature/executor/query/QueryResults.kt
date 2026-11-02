/*
 * Copyright 2026 WorkTool-Open 贡献者（Apache-2.0，见仓库根 LICENSE）。
 */
package cn.worktool.open.feature.executor.query

/**
 * 查询类指令（501/502/509）结构化结果端口。
 *
 * 设计（docs/02 §3 feature:*「导航+读取→结构化结果」）：查询执行器在计划末步把
 * 读取结果交给本端口；生产接线 = app 适配层映射为指令回执（socketType=3，
 * successList/metaJson 载荷，映射字段以 T3.2 apifox 冻结为准，spec §9 Q2）——
 * 本模块经端口保持 JVM 可测（Fake 注入断言结构化结果）。
 *
 * 调用时序保证：仅在计划末步（校验通过后）调用一次；FAILED/PAUSED 不调用
 * （部分读取结果不外发，宁缺勿错）。
 */
fun interface QueryResultSink {

    /** @param commandType 501/502/509（spec §4.2 获取类） */
    fun onQueryResult(commandType: Int, result: QueryResult)
}

/** 查询结果（501/502/509 三形态）。 */
sealed interface QueryResult {

    /**
     * 501 群列表（[groups] = 群显示名，界面读取序）。
     * 元组结构（群主/成员数等）在企微群聊列表页不可得，不虚构——富结构待
     * apifox 对照冻结（spec §9）。
     */
    data class GroupList(val groups: List<String>) : QueryResult

    /** 502 好友列表（[friends] = 联系人显示名，界面读取序）。 */
    data class FriendList(val friends: List<String>) : QueryResult

    /**
     * 509 待上传本地文件报告（dry-run 与真实执行同构产出）：
     * 将上传哪个文件（[fileName]/[path]）+ 大小（[sizeBytes]）。
     */
    data class LocalFile(
        val fileName: String,
        val path: String,
        val sizeBytes: Long,
    ) : QueryResult
}

/** 本地文件访问端口（509 GET_LOCAL_FILE 用；真机实现归 app 接线层）。 */
interface LocalFilePort {

    /** 默认上传目标 = 当前日志文件路径（audit/02 F30：509 默认上传当前日志）。 */
    fun defaultLogFilePath(): String

    /** 文件信息；null = 不存在/不可读。 */
    fun stat(path: String): FileInfo?
}

/** 文件元数据（不读内容——509 端侧只需名字与大小做投递报告）。 */
data class FileInfo(
    val name: String,
    val path: String,
    val sizeBytes: Long,
)
