# UI 交互对等任务卡（T4.9）v1.0

- 版本：v1.0（2026-11-03；源=P-UI 交互比对报告 local-private/parity/ui-compare.md，21 项判定：补 7/采纳 6/驳回 8）
- 与 T3.6（docs/15 API 兼容）并列的两个"一比一收尾"卡。

## 遗漏-补（7 项，全入本卡）
| # | 项 | APK 证据 | 交付 |
|---|----|----------|------|
| U1 | 运行时段（防打扰） | useTimeEnabled 默认 8:00–22:00 + 双 TimePicker（SettingsActivity.java:708-734）+ 非时段弹"机器人休眠中"（ListenActivity.java:230-238） | 端侧运行时段配置 + 非时段拦截对话框（FR-52 同级安全语义） |
| U2 | 悬浮窗暂停/继续 | 气泡+菜单在企微内直接暂停/继续（k2/r.java:165-249） | FR-50 提级至 W-M6：最小集=暂停/恢复两钮 |
| U3 | 待执行队列查看/清空（设备侧） | messageId 列表对话框+清空（SettingsActivity.java:332-360,597） | App 指令列表页增加"待执行队列"视图+清空按钮（对齐 FR-37 设备侧） |
| U4 | 跳过过期指令 | TTL 2h 可配，超时打日志跳过（CustomMessageQueue.java:333） | 端侧积压门：过期指令记 SKIPPED 过期不下发（契约与 contracts TTL 联动） |
| U5 | WS 测试连接（配对前探针） | mode=probe + URL/Code/Reason 结果框（ListenActivity.java:99-137） | 配对页加"测试连接"按钮（TCP/TLS/HTTP 三层探测+结果显示） |
| U6 | 企微版本检测提示 | activity_listen.xml:217-240 | 启动时检测企微版本 vs 兼容矩阵（docs/05），不兼容显式提示 |
| U7 | 引导"不再询问"记忆 | 上游交互惯例 | SetupGuide 三卡各加"不再询问"持久标记 |

## 优化-采纳（6 项，随 U 系顺带）
RTT 延迟彩条（:140-151）｜企微版本检测（=U6）｜引导"不再询问"（=U7）｜强制停止不用 System.exit（=撤离子任务）｜重试次数可配｜离线态显式"重连中"文案。

## 不采纳（8 项，有据驳回，防范围蔓延）
购买/账号登录（厂商商业化）｜应用内更新（FR-44 安全版另行）｜企微 SDK 参数（ADR-05）｜回复策略（server 域 FR-39 已覆盖）｜防卡顿/模拟器兼容（hook 性能补偿，G12 禁）｜数据回传类开关（G3 最小化）｜**消息延迟/群发间隔设置（经反编译证实 APK 为硬编码 5000/1000/500ms，e2/a.java:44-46——"有此设置"为猜想，不成立）**。

## 排期
U2（悬浮窗最小集）+ U5（连接探针）随 **App 完善冲刺落地后补丁**；U1/U3/U4/U6/U7 随 **W-M6 批**。验收=逐项真机截图对照 APK 原行为。

---

## 指令字段契约冻结（2026-11-05 契约冻结车道）——第二批表单自此可按 catalog 消费

> 目的：解除管理台**批量表单第二批**（204/205/218/227/228/229/230/238）的阻塞。
> 表单输入项由 `GET /api/v1/admin/catalog?withSchema=1` 的 `required`+`example`
> 生成（admin/index.html 唯一数据源），因此**目录 example 字段必须能被端侧执行器
> 载荷解析路径消费**——否则表单按目录合成的载荷在设备侧必然 Invalid（死载荷）。

### 冻结口径

- **决策规则**：默认以执行器语义为准修 catalog example；仅当 example 原字段名更符合
  upstream openapi 契约（`local-private/parity/apifox-field-diff.md` + b2/b3 逐页字段表
  + upstream `WeworkController` @param 注解）时，改执行器读取并注明迁移。
- **机器锁定**（防再漂移）：
  - 交换夹具 `protocol/fixtures/admin-command-examples.json`（52 条 example，与
    `server/internal/admin/catalog.go` 字节级同源）——同步性由 Go
    `TestCatalogExamplesFixtureInSync` 锁定；
  - 每条 example 经端侧执行器载荷解析路径（`IntentNormalizer`）断言可消费——
    Kotlin `CatalogExampleContractTest`（52 条全量 Valid + 第二批八指令逐字段
    深断言 + 203 多目标拒绝断言）；
  - 既有不变量维持：example 过 compat 必填预校验、required 路径类型=
    example 值形态、risk=ClassifyRisk（catalog_registry_test.go）。
- **审计结论**：52 条中 **22 条漂移**、30 条一致。漂移分类与处置：
  1. **example 字段幻影/缺漏（catalog 修，19 条）**：202（loop.on→loopEnabled）、
     204/205/208/209/218/227/228/229/230/238（字段缺漏或 `originTitle`/`titleList`
     错位——upstream 与执行器同名，按 upstream 修 catalog）、210/211/224（补执行器
     必填 objectName 等；card 字段为 WorkTool-Open 自拟 schema）、223（corpName→
     objectName）、514（messageId→objectName）、511（补 Required 标注）。
  2. **执行器迁移（example 原字段更合 upstream 契约，4 指令）**：
     - **203**：`targetRoom`→**titleList 主读**（upstream api-23520034
       「titleList:string[]，建议单目标」= prevalidate/catalog 同名）。多元素
       titleList **显式拒绝**（203=单目标语义，防静默截断；多目标走 229/238）；
       候选名 `targetRoom` 降级**迁移期只读别名**（T3.2 apifox 冒烟后撤）。
     - **206**：成员名单 `groupMembers`→**selectList 主读**（upstream initGroup
       「selectList 添加群成员名称列表」）。此迁移修复实际缺陷：目录表单按
       selectList 提交此前被端侧静默忽略（拉人名单丢失）。`groupMembers` 退役。
     - **207/225**：目标群 `targetRoom`→**groupName 主读**（upstream updateGroup /
       modifyGroupMemberInfo 同名；与其余群指令执行器一致）。`targetRoom` 别名同上。
     - 迁移登记：`IntentNormalizer` 各 normalize* 决策注释 + catalog.go 条目注释。
- **Known remainder（如实登记）**：207 公告字段我方执行器读自拟名 `announcement`
  （upstream 207 为 `newGroupAnnouncement`；catalog 原 `groupAnnouncement` 实为
  upstream 206 的字段名）——执行器语义权威保留自拟名，upstream 名留 T3.2 候选；
  protocol 金标夹具（hmac-vector 两个 203 帧）仍携候选名 `targetRoom`，重算金标
  HMAC 向量不在本车道（transport 层字段名无关），别名撤销时一并处理。

### 第二批表单（204/205/218/227/228/229/230/238）字段速查

表单可按 `required`（最小可提交集）+`example`（值形态示例）直接消费；冻结后
各指令字段如下（全部经端侧 `IntentNormalizer` 消费断言）：

| 指令 | required（表单输入项，序即提交键序） | example 关键形态 |
|---|---|---|
| 204 引用回复 | `titleList` + `receivedName` + `originalContent` + `receivedContent` | 三段定位键全必填（发送人+原文+回复正文） |
| 205 转发消息 | `titleList`（源会话）+ `receivedName` + `originalContent` + `nameList`（接收人） | `originTitle` 已废弃（两侧均不消费） |
| 218 推送文件 | `titleList` + `objectName` + `fileUrl` + `fileType`（image/audio/video/*） | objectName 自定义且不同文件不同名（防缓存覆盖） |
| 227 批量转发 | `titleList`（源会话）+ `messageList` + `nameList` | messageList 元素=`{originalContent,textType}`（原文定位键） |
| 228 合并转发 | 同 227 | 同 227（mode=合并） |
| 229 批量发送 | `weworkMessageList` + `nameList` | **无 titleList**；元素最小集=`{textType,receivedContent}`（文本）或 `{textType,objectName,fileUrl,fileType}`（文件） |
| 230 合并发送 | 同 229 | 同 229（mode=合并；`messageList` 是 227/228 字段，勿错用） |
| 238 批量发送文本 | `titleList` + `receivedContent` | receivedContent=**JSON 数组字符串**且 ≥2 条非空文本（如 `"[\"第一条\",\"第二条\"]"`；纯字符串/单条=端侧 101011） |

### 验证（本车道实跑）

- Go：`go -C server test ./...`（含 `TestCatalogExamplesFixtureInSync`、
  `TestCatalogExamplesPassPrevalidate`、`TestCatalogSchemaMatrix` 等全绿）。
- Kotlin：`gradlew :feature:executor:test :core:protocol:test :app:testLabDebugUnitTest`
  （含 `CatalogExampleContractTest` 13 例、`IntentNormalizerTest` 冻结载荷形态回归）。
- 表单链路：`TestCatalogHandlerWithSchemaMatrix`（`?withSchema=1` required 类型表）
  与 `admin/index.html` 表单生成器键名对齐（required→输入项、example→类型兜底）。
