# 迭代设计稿：收口后批 deferred 四项（T3.7~T3.10）v0.1

- 版本：v0.1（2026-11-05；源=迭代设计文档车道）
- **性质：设计稿，非实现**——本车道零代码行为变更（仅本文件 + CHANGELOG「收口后批」小节）；
  文中全部表结构/端点/语义为草案，动工前须经编排者评审立卡。设计依据全部锚定现有代码与
  既有规格（逐处标注 `文件:行`），不含臆测字段。
- **契约纪律**：rc2 冻结的指令字段契约（`protocol/fixtures/admin-command-examples.json` +
  `server/internal/admin/catalog_test.go` +
  `android/feature/executor/src/test/java/cn/worktool/open/feature/executor/runtime/CatalogExampleContractTest.kt`）
  **语义不动**；四项设计全部为加法式变更（新表/新列/新端点/新可选字段），存量消费者零破坏。
- 任务卡编号衔接：T3.1~T3.5（docs/03 M3 表）与 T3.6（docs/15 apifox 兼容卡）已占用，
  T4.1~T4.9 为 R2 吸收卡（docs/09 §F / docs/16）——本文四卡续编 **T3.7~T3.10**。
- 门禁基线（与全仓一致，实现卡交付时逐项复跑）：`go -C server test ./...`；
  `MSYS_NO_PATHCONV=1 cmd /c "android\gradlew.bat :feature:executor:test :core:protocol:test :app:testLabDebugUnitTest"`；
  `node test/admin_ui_v3.spec.mjs`。
- 规格事实源：交接包实施规格 v3（`local-private/design-packages/WorkTool-交接包-交互与规格/
  03-实施规格/WorkTool-管理台业务交互与维护规格-v3.txt`，下称「v3 规格」）§3.2/§13；
  交互图05 = `02-任务交互/05-batch-row-validation.png`。

---

## §1 批量执行合同（T3.7）

### 1.1 目标

给管理台「一组独立指令」的批量编排（交互图05 第三种批量）以**服务端一等合同**：
一批（batchId）聚合多行（childKey），每行一条独立指令、独立受理、独立回执；
队列行带乐观锁 revision 防并发丢更新；管理台批量编排行编辑器按交互图05 消费。
**不做**：跨设备原子串行承诺、整批全成/全败承诺（v3 规格 §13.1「多条指令可能分别受理，
不能承诺整批全成或全败」）。

### 1.2 现状与依赖（三种批量必须区别建模；238 兼容策略）

- **三种批量**（v3 规格 §13.1）：①一条指令内多会话（如 238 titleList）；②229/230 一条
  指令含多条消息；③管理台一组独立指令/多设备。**本文只做第三种**；①②按各自指令规范
  （交互图05 页脚注记「本页编排独立指令，不等同于 229/230 的消息列表」）。
- **现状无批量合同**：全仓 grep `batchId/batch_id` 零命中（2026-11-05 实测）。兼容面
  `POST /wework/sendRawMessage` 把 `{socketType, list[≤100]}` 拆逐条 Submit
  （`server/internal/api/compat/router.go:4-5、182-208、280`，`compatMaxList=100`
  router.go:87），随机幂等键（router.go:142），重放由 101014 兜底——风控失败可部分入队，
  不能当全成/全败的批量 API（v3 规格 §13.1【现有】行）。管理台无持久草稿/批次根
  （v3 规格 §13.5「命令写读：缺纯预检、持久草稿、批次根」）。
- **238 兼容策略（本设计的依赖事实，语义冻结不动）**：238 批量发送文本为单载荷数组串——
  `receivedContent` = JSON 数组字符串且 ≥2 条非空文本
  （`protocol/fixtures/admin-command-examples.json` 238 条目；
  `server/internal/admin/catalog.go:248-251` 冻结决策注记；
  端侧解析 `IntentNormalizer.kt:386`；锁测 `CatalogExampleContractTest.kt:161`）。
  结果 = 多目标 × 有序文本**二维矩阵**，逐格成败记录不得压平单一成败
  （docs/10-PARITY-PLAN.md:30 R2 复核采纳）。**本文不改造 238**；批量合同与其正交。
- **队列现状（revision 设计的靶点）**：队列序 = `created_at` FIFO
  （console.go:645-653）；插队 = 改写 `created_at` 为队首前一刻
  （`console.go:777-784`，审计留原值 console.go:786-787）——v3 规格 §13.4 已点名缺陷
  （「当前 priority 是移到队首并修改 created_at…历史耗时不能用被改写的排序时间计算；
  目标任意重排需 queueRevision 与独立排序键」）。队列清空/移除仅 queued 态
  （console.go:691-695、709-741），无并发版本防抖。

### 1.3 数据模型（迁移 v4，全部加法）

```sql
-- 新表：批次根（仅元数据；行载荷在 commands，不复制正文防双源漂移）
CREATE TABLE IF NOT EXISTS batches (
  batch_id    TEXT PRIMARY KEY,          -- 服务端 UUID
  tenant      TEXT NOT NULL DEFAULT '',
  actor       TEXT NOT NULL DEFAULT '',
  note        TEXT NOT NULL DEFAULT '',  -- 任务名/备注（脱敏纪律：不存正文）
  row_count   INTEGER NOT NULL DEFAULT 0,
  status      TEXT NOT NULL DEFAULT 'open',   -- open|submitted|closed（closed=全部行终态）
  created_ms  INTEGER NOT NULL
);

-- commands 加列（既有行默认零值 = 非批量指令，现状语义不变）
ALTER TABLE commands ADD COLUMN batch_id  TEXT NOT NULL DEFAULT '';
ALTER TABLE commands ADD COLUMN child_key TEXT NOT NULL DEFAULT '';  -- 提交方行键（客户端生成）
ALTER TABLE commands ADD COLUMN revision  INTEGER NOT NULL DEFAULT 0; -- 队列行乐观锁，单调递增
ALTER TABLE commands ADD COLUMN sort_key  INTEGER NOT NULL DEFAULT 0; -- 队列独立排序键；0=未参与重排
CREATE INDEX IF NOT EXISTS idx_commands_batch ON commands(batch_id, child_key);
```

- **batchId/childKey 语义**：childKey 由提交方在行级生成并随行提交；服务端幂等键段扩为
  `tenant|actor|batch.row|childKey`（复用既有 `idempotency_keys` 表，sqlite.go:265-271；
  四元组口径见 docs/04 C5）——网络中断后同一行携原 childKey 重试 = 幂等重放返回原
  commandId，**不得整批换新键**（v3 规格 §13.1）。载荷不可变：行受理后 childKey↔
  payloadDigest 绑定，变参即 409（既有 `ErrIdempotencyConflict` 语义，pipeline.go:43-44）。
- **revision 语义（队列行乐观锁）**：凡使 queued 行发生出队/重排/取消竞争的写操作
  （remove/clear/reorder/priority/设备拉取迁移 E03）在同一事务内 `revision = revision + 1`；
  条件更新形如 `UPDATE … WHERE command_id=? AND state='queued' AND revision=?`，
  0 行生效 → 409 `REVISION_CONFLICT` 并回当前行快照（commandId/state/revision/sortKey），
  管理台按 v3 规格 §2.3「并发冲突 409：保留本地编辑，展示最新服务端差异」处理。
  `created_at` **不再改写**（耗时统计恢复真实）；排序改用 sort_key
  （每 target 内单调分配：`MAX(sort_key)+1`，插队=取当前队首 sort_key-1）。
- **逐格回执**：行级 exec_result outbox 已存在（`pipeline.go:586-608` 终态副作用与迁移
  同事务；取消回执 101018 同形，pipeline.go:877-883）——批量合同只新增**聚合读**，
  不改逐行回执链。

### 1.4 接口草案（管理面 `/api/v1/admin/batches`；兼容面本期不动）

| 方法/路径 | 语义 | 关键点 |
|---|---|---|
| `POST /api/v1/admin/batches` | 建批（rows 草稿登记，行可含待补字段） | 行携 clientKey（=childKey）；重复 childKey 建批即 409 |
| `PUT /api/v1/admin/batches/{batchId}/rows/{childKey}` | 改行（仅 open 批） | 载荷不可变行须删旧增新（新 childKey），防审批后偷换参数 |
| `POST /api/v1/admin/batches/{batchId}/submit` | 逐行走既有 Submit 门链 | 每行独立受理；响应逐行 `{childKey, commandId, state, replay, approvalId?, errorCode?}`；部分失败不回滚已受理行；带 `?only=childKey…` 显式「仅提交已选有效行」 |
| `GET /api/v1/admin/batches/{batchId}` | 批次聚合读 | 行清单 + 各行当前态；摘要计数 = 成功/失败/未知/排队/待审批/未提交（v3 规格 §13.1 批次摘要字段；未知项保持未知，不并入失败） |
| `POST /api/v1/admin/batches/{batchId}/cancel` | 批次取消（逐行可取消性探测） | 仅 queued 行可 E04 取消（复用 `CancelBeforeDelivery` pipeline.go:717-734）；运行中/未知行如实报告「未取消」，不宣称全部停止 |
| `GET /api/v1/admin/queue`（扩展） | 行列表加 `revision`、`sortKey` 字段 | additive；`?batchId=` 过滤 |

行级队列操作（`DELETE /queue/{commandId}`、`POST /queue/{commandId}/priority`）加可选
请求头 `If-Match: <revision>`：携带且不匹配 → 409 `REVISION_CONFLICT`；不携带 = 现状
语义（管理台 v1 逐步切换，不强制破坏）。

### 1.5 前端合同（交互图05：批量编排行编辑器）

逐条对照 `05-batch-row-validation.png` 与 v3 规格 §13.2：

- **行模型**：每行 = `{rowId(本地), childKey(提交键), deviceId, commandType, 参数,
  校验态}`；本地草稿态 `editing/validating/invalid/ready/submitting/submission_uncertain`
  只在前端，**不写 command.state、不作为后端任务已创建的证据**（v3 规格 §13.2）。
- **校验**：字段校验按 catalog `required`+`example`（`GET /api/v1/admin/catalog`，
  契约冻结批后第二批表单 204/205/218/227/228/229/230/238 可按目录消费，docs/16 §指令字段
  契约冻结）；逐行错误就地显示（图05 第 2 行 218「缺少 fileType」红框样式），不压成一块
  JSON（v3 规格行 135）。重复行检测（图05 第 4 行「与第 1 行重复」）→ 用户显式选
  「合并重复行 / 保留两行（需确认）」，**不自动重复发送**。
- **行操作**：添加/复制/编辑/删除/拖动排序；删除只删未提交草稿，不取消已受理行；
  拖动只改变显式顺序，不承诺跨设备原子串行（v3 规格 §13.1）。多设备混合行允许
  （图05 手机 A/B 混排）。
- **提交门**：默认「存在 invalid/重复未决行 → 提交禁用」（图05 底部「还有 1 项错误，
  提交已禁用」）；「仅提交已选有效行」须用户显式确认行集合，未选/错误行保留草稿并显示
  未提交。提交中逐行状态显示「已受理/待审批/失败(含原因)/重放」，部分受理时不显示
  「整批未提交」。
- **恢复**：刷新/断网后凭 batchId + childKey 找回同批次与子任务（`GET batches/{id}`），
  不重发已受理行（submission_uncertain 行先查询后处置，v3 规格 §3「请求超时：查询同一
  幂等键」）。**禁 localStorage 存正文/联系人/令牌**（v3 规格 §3.5）。
- **队列视图联动**：行受理后队列页展示 revision；行操作携 If-Match；409 时按快照提示
  「该行已被其他操作变更」，提供刷新/放弃两键，不静默覆盖。

### 1.6 迁移与兼容

1. 建表/加列全部带默认值，既有库一次迁移生效；非批量指令 `batch_id=''`，全部既有查询
   /状态机/回执路径零改动。
2. 兼容面 sendRawMessage 本期不改（契约面=文档原样语义，router.go 已冻结）；后续可把
   一次 list 调用登记为一个隐式 batch（batchId 响应字段加法），仍不承诺原子性——单独立卡。
3. 238/229/230 语义不动（rc2 冻结）；管理台批量编辑器 UI 注记三者边界（图05 页脚样式）。
4. sort_key 引入后 `handleQueuePriority` 的 created_at 改写路径退役（保留一版双写过渡，
   旧行 sort_key=0 视为「未重排，按 created_at」），两版后删除改写语句；回归测试锁定
   created_at 不可变。

### 1.7 里程碑（T3.7）

| 步 | 交付 | 回归测试（先写失败断言） |
|---|---|---|
| M1（W-M5） | 迁移 v4 + batches 表 + 批次建/读端点 | `TestBatchMigratePreservesLegacyRows`（旧行零值语义）；`TestBatchChildKeyIdempotentResubmit`（断网重试同 childKey 返回原 commandId，变参 409） |
| M2（W-M5） | submit 部分受理 + revision 乐观锁 + sort_key 重排 | `TestBatchSubmitPartialAcceptance`（风控拒一行不回滚已受理行）；`TestQueueRevisionConflict`（并发 remove/priority 0 行生效 → 409 带快照）；`TestQueueSortKeyPreservesCreatedAt`（重排不改 created_at） |
| M3（W-M6，随第三批 P2 维护闭环） | 管理台批量编排行编辑器（交互图05）+ 批次摘要/取消 | `test/admin_ui_v3.spec.mjs` 增批量编辑器节（逐行校验态/重复行/提交禁用/409 保留本地编辑）；Go 侧批次聚合计数含未知不并入失败断言 |

### 1.8 开放问题

1. 批级审批 vs 行级审批：L2 行各自带 approvalId（现状 Submit 门链天然逐行）已可工作；
   「整批一次审批」需审批对象支持聚合 digest，是否值得另立审批形态？
2. 「仅提交已选有效行」v1 是否提供（图05 未画出）；若提供，未提交行在批次摘要中的
   计数口径（未提交 ≠ 失败）UI 文案需 owner 定稿。
3. sort_key 全局分配 vs 每 target 独立分配（影响跨 target 拖动语义）；建议每 target，
   待评审。
4. 隐式 batch（compat list→batchId 回显）与 101014 去重（router.go:274）的相互作用
   需要契约测试先行。
5. 批次行 retention：批次根是否随行墓碑保留期（`tombstones.purge_after`，sqlite.go:273-279）
   一并清理，还是长期保留作台账？

---

## §2 cron 时区/DST（T3.8）

### 2.1 目标

定时任务按**任务声明的时区本地时刻**触发（如「Asia/Shanghai 每天 09:00」就是北京时间
09:00，而非服务端 UTC 09:00）；明确 DST 跳变语义（不存在时刻顺延规则、重复时刻取首次）；
next_fire 计算与预览（管理台 cron 页）按同一 zone 口径修正。

### 2.2 现状（缺陷锚点）

- `cron_expr.go:7`：时区=「传入时间的本地时区」，而服务端全线 `Service.Now().UTC()`
  （pipeline.go:115 等）→ 实际一切按 UTC 计。`cron_jobs` 表无 timezone 列
  （sqlite.go:354-371），仅 `next_fire_ms` 纪元毫秒。
- `CronExpr.Next`（cron_expr.go:147-171）逐日逐分搜索，使用 `d.Location()`；传入 UTC
  即得 UTC 日历。`PreviewCronNexts`（cron.go:119-141）纯函数同口径；管理台预览端点
  `GET /api/v1/admin/cron-jobs/preview`（admin.go:155）无时区参数。
- 触发幂等键绑定计划时刻：`cron-<jobId>-<nextFireAt.UnixMilli>`
  （cron.go:211）——推进逻辑（fireCronJob cron.go:187-241，堆积保护跳过时 next_fire
  照常推进）与 zone 无关，改造须保持该键稳定性语义。

### 2.3 数据模型（迁移 v5，加法）

```sql
ALTER TABLE cron_jobs ADD COLUMN timezone TEXT NOT NULL DEFAULT 'UTC';
```

- 取值域：**仅命名 IANA 时区**（`time.LoadLocation` 可加载）+ `"UTC"`；拒 `"Local"`
  （随宿主机漂移，不可复现）。创建/更新时校验，非法值 400。
- 存储不变量：`next_fire_ms/last_fire_ms` 仍为**绝对纪元毫秒**（UTC 时刻），永不存本地
  墙钟——DST 推进只发生在「计算下一时刻」这一步，落库后无歧义。
- 可选加列（M2 再定）：`last_fire_local_date TEXT`（本地日历日，防 DST 重复触发的对账
  证据；先以审计行替代，见 2.4）。

### 2.4 接口草案

- `CronJobInput`（cron.go:24-34）加 `Timezone string`；`CreateCronJob`/更新端点透传；
  审计行 `cron_job_created` 记 `tz=<zone>`。
- `TickCron`/`fireCronJob`：逐任务 `loc, _ := time.LoadLocation(job.Timezone)`，
  `next = expr.Next(now.In(loc))`——`Next` 现实现按 `d.Location()` 取本地日历
  （cron_expr.go:152-166），传 zone 时间即得 zone 日历，**无逐分扫描性能回退**
  （仍逐日）。next 落库转回 UTC 毫秒。
- **DST 跳变语义（草案规则，实现前立卡冻结）**：
  - **不存在时刻（春跳缺口，如本地 02:30 落在 02:00→03:00 跳变内）→ 顺延至缺口终点**：
    即触发本地 03:00（跳变后第一存在的整分）。Go `time.Date` 对不存在时刻默认归一到
    另一偏移的同墙钟——**不得依赖默认行为**，须显式检测
    （`cand.In(loc)` 墙钟 ≠ 请求墙钟即落入缺口）并按本规则重算 + 审计
    `cron_dst_skipped`（jobId、计划墙钟、实际触发时刻）。与 Vixie cron「缺口内任务
    在跳变后立即执行」的行业口径一致。
  - **重复时刻（秋跳，本地 01:30 出现两次）→ 取首次出现**（较早偏移）；审计
    `cron_dst_ambiguous`。幂等键取绝对时刻毫秒，两次出现自然不同键，无重放碰撞。
  - 预览窗口横跨跳变时，预览响应逐条携带 `{utcMs, local: RFC3339(带偏移), dstNote?}`。
- `PreviewCronNexts`（cron.go:119-141）加 zone 参数（同函数纯性不变）；管理台预览端点
  加 `?timezone=`（缺省 UTC=现状）；cron 管理页表单加时区下拉（常用 IANA 集 + UTC），
  列表/详情显示「UTC 毫秒 + 本地墙钟（时区名）」双行，本地行为主展示。

### 2.5 迁移与兼容

1. 默认 `'UTC'`：**存量任务行为零变化**（今天实际就是 UTC 计）。迁移不 reinterpret
   既有任务——「想要北京时间」的旧任务若静默按 Asia/Shanghai 重算会瞬间平移 8 小时，
   属行为变更，禁止自动做；提供逐任务 `PUT timezone`（更新前强制返回新旧 next_fire
   预览对照 + 确认），审计 `cron_job_timezone_changed`。
2. 幂等键 `cron-<jobId>-<计划时刻毫秒>` 不变式保持：zone 变更后计划时刻变化 → 新键，
   旧键自然过期（窗口 `policy.Idempotency.WindowHours`），无双触发。
3. `PreviewCronNexts` 既有调用（管理台预览）不传 zone = UTC，签名加参为独立新函数
   `PreviewCronNextsInZone`（老函数委托），避免改既有测试语义。

### 2.6 里程碑（T3.8）

| 步 | 交付 | 回归测试（先写失败断言） |
|---|---|---|
| M1（W-M5） | 迁移 v5 + CreateCronJob/更新带 timezone + TickCron 按 zone 计算 | `TestCronJobZoneLocalFire`（Asia/Shanghai 「0 9 * * *」在 UTC 01:00 触发——当前实现该断言失败）；`TestCronJobZoneValidation`（拒 Local/乱串） |
| M2（W-M5） | DST 两规则 + 审计 | `TestCronDSTNonexistentTimeDeferred`（构造 2026-03-08 America/New_York 02:30 计划 → 实际 03:00 本地 + cron_dst_skipped 审计）；`TestCronDSTAmbiguousFirstOccurrence`（2026-11-01 01:30 取 EDT 首次）；`TestCronNextFireStoredAsUTC`（落库恒纪元毫秒） |
| M3（W-M5） | 预览带 zone + 管理台时区 UI | `TestCronPreviewZone`（预览响应含 local 偏移与 dstNote）；`test/admin_ui_v3.spec.mjs` cron 节增时区双行展示断言 |

### 2.7 开放问题

1. 中国大陆无现行 DST，但 IANA 数据含历史跳变（1986-1991 夏令时等）与未来政策变更
   可能——是否额外提供「固定偏移」形态（如 `UTC+08:00`）绕开 zone 数据依赖？倾向：
   只收 IANA 命名区，固定偏移不做（防双口径）。
2. 「错过执行」（服务端停机跨过 next_fire）现状=启动后补触发一次（幂等键防重）；zone
   改造是否引入「本地日历日错过即跳过」策略，与 v3 规格 §13.4「定时任务…错过执行…均要
   明确 API 及持久状态后开放」并卡处理。
3. 管理台时区下拉集合（全 IANA 600+ vs 常用精选 + 搜索）由 UI 车道定。
4. 301 环境快照（`SessionRuntime.kt:361-381`）可携设备本地时区——是否做「按设备时区
   兜底」选项（任务未显式声明时区时用目标设备 zone）？涉及跨设备语义，v1 不做，登记。

---

## §3 API token scope/expiry（T3.9）

### 3.1 目标

api_tokens 落**显式 scope 列**（admin/approver/readonly/robot 四枚举）与**过期时间**，
提供轮换端点；把现「保留 robot_id 编码 scope」的过渡实现收编为单一事实源
（该收编点已在代码注释预告：sqlite.go:27「W-M5 RBAC 落 scope 列后收编」）；
审计影响显式登记。

### 3.2 现状（收编靶点）

- 表：`api_tokens(token_hash PK, robot_id, tenant, status active|revoked, created_ms,
  last_used_ms)`（sqlite.go:344-352）——**无 scope、无过期**；仅存 hash
  （sqlite.go:112-121 注释纪律，spec §5.1.3）。
- scope 编码过渡实现：`robot_id` 保留值 `"admin"`（sqlite.go:24-28）/`"approver"`
  （sqlite.go:30-35）= 面 scope；管理面统一鉴权 `requireScope`
  （admin.go:221-247，admin/approver 越权显式 403、业务 token 统一 401）；兼容面
  `authenticate`（openapi_registry.go:252-284）同构硬拒保留名 + robotId 寻址一致校验。
- 签发/吊销：`IssueOpenAPIToken`（openapi_registry.go:216-241，保留名硬拒、原始值
  仅一次返回）；`RevokeOpenAPIToken`（openapi_registry.go:243-248）。**无轮换端点、
  无过期判定**；`TouchAPIToken` last_used 留痕（sqlite.go:1386-1389）。
- 管理台：`GET /api/v1/admin/tokens` 只读投影（console.go:1446-1449）。

### 3.3 数据模型（迁移 v6，加法 + 一次性回填）

```sql
ALTER TABLE api_tokens ADD COLUMN scope        TEXT NOT NULL DEFAULT 'robot'; -- admin|approver|readonly|robot
ALTER TABLE api_tokens ADD COLUMN expires_ms   INTEGER;                       -- NULL=不过期（现状兼容）
ALTER TABLE api_tokens ADD COLUMN rotated_from TEXT NOT NULL DEFAULT '';      -- 轮换链前驱 hash 前 8 位
```

- 回填（迁移内一次性）：`robot_id='admin'` → `scope='admin'`；`'approver'` →
  `scope='approver'`；其余 → `'robot'`。保留 robot_id 列不删（robot scope 的业务寻址
  仍需要；admin/approver 行保留旧值仅作历史可读）。
- **枚举语义**：`admin`=管理面全部（现状 admin 令牌口径）；`approver`=决议面专用
  （P0-3 口径不变）；`readonly`=管理面只读投影（GET 类端点：devices/commands/approvals/
  queue/tokens/catalog/callbacks/logs/audit——精确清单实现时以路由表核对登记，
  开放问题 1）；`robot`=兼容面业务令牌（现状 robot 令牌）。同一枚 token 不得双面行走
  （sqlite.go:30-35 纪律原文保留，改由 scope 列表达）。

### 3.4 接口草案

| 方法/路径 | 语义 | 关键点 |
|---|---|---|
| `POST /api/v1/admin/robots/{robotId}/openapi-tokens`（扩展 body） | 签发，body 加 `{scope?, expiresInMs?}` | scope 缺省=robot（现状兼容）；scope 仅 admin 面可指定且 `admin/approver` 只能走各自专用启动打印通道（保持 P0-3「分持」纪律，不开放经该端点互铸）；expiresInMs 缺省=不过期；上限建议 180d（开放问题 3） |
| `POST /api/v1/admin/tokens/{tokenHashPrefix}/rotate` | 轮换：按前缀定位（列表投影同前缀口径 console.go:1446），新签同 scope/tenant，旧行 `status='revoked'` + rotated_from 链 | 新原始值仅此一次返回；原子事务（新签+旧吊销同 tx，失败整体回滚）；原始 token 不可恢复故「轮换」不等于「重看」 |
| `DELETE /api/v1/admin/robots/{robotId}/openapi-tokens`（现状） | 吊销不变 | 审计已留痕 |
| `GET /api/v1/admin/tokens`（扩展） | 投影加 scope/expires/rotatedFrom/有效态 | hash 前 8 位口径不变（不泄漏全文） |

- **鉴权判定**：`requireScope`/`authenticate` 改读 scope 列；过期判定并入现有 401
  统一口径（`INVALID_TOKEN`，不新增可探测分类码——admin.go:221-229「统一不分类防探测面
  分类」纪律延续），message 注明「token 已过期」。首次因过期拒用落审计
  `api_token_expired`（惰性，不加后台 sweeper；批量清扫可挂既有 retention sweep
  9629254 模式，M2 再定）。
- **readonly 面**：`requireReadonly` = `scope ∈ {admin, readonly}` 且方法=GET；写方法
  403 `READONLY_SCOPE`。只读投影不含 secret 类字段（token hash 前缀已是最敏感字段）。

### 3.5 迁移与兼容与审计影响

1. 迁移后先**双读**（scope 列非默认优先，否则回落 robot_id 保留值判定）一版，回归绿后
   收编为单读；行为逐点不变（admin/approver 403、业务 401、寻址校验全部现状口径）。
2. `IssueOpenAPIToken` 保留名硬拒保留（robot_id 列仍在），scope 列与保留名双保险；
   既有测试（openapi_registry 保留名 403 族）零语义变化。
3. **审计影响**：新增审计 kind：`api_token_issued`（含 scope/有效期，不含原始值）、
   `api_token_rotated`（新旧前缀链）、`api_token_expired`（惰性）；issue/revoke 现有
   日志行（openapi_registry.go:239）升级为结构化审计行。readonly 令牌的全部操作本就
   只读，无新增副作用面；轮换不改变既有 outbox/回调链（token 不参与回调签名）。
4. 启动打印两次 admin/approver 令牌的通道（P0-3）不受影响；该通道令牌同样落 scope 列。

### 3.6 里程碑（T3.9）

| 步 | 交付 | 回归测试（先写失败断言） |
|---|---|---|
| M1（W-M5） | 迁移 v6 + 回填 + 双读 | `TestAPITokenScopeBackfill`（admin/approver/robot 三类旧行回填断言）；`TestScopeDualReadParity`（双读期两判定路径结果一致） |
| M2（W-M5） | 过期判定 + 签发扩展 | `TestAPITokenExpiredRejected401`（注入过去 expires_ms → 401 + api_token_expired 审计）；`TestIssueTokenWithScopeAndExpiry`（robot 面拒铸 admin/approver scope 断言沿用保留名测试口径） |
| M3（W-M5） | rotate 端点 + readonly 面 + 管理台投影扩展 | `TestTokenRotateAtomic`（旧吊销+新签同事务，中断无中间态）；`TestRotateChainsRotatedFrom`；`TestReadonlyScopeWriteRejected`（写端点 403 READONLY_SCOPE）；`test/admin_ui_v3.spec.mjs` tokens 节断言新列 |

### 3.7 开放问题

1. readonly 的精确端点白名单（管理面 GET 全集 vs 仅五工作区读投影）需与 UI 车道对齐后
   在卡内冻结为路由清单，防「半只读」。
2. robot scope 是否细分（如按 commandType 子集授权）——RBAC 五角色（docs/02:56，
   owner/admin/operator/approver/auditor）是 W-M5+ 更大卡；本期四枚举只收编现状，
   不扩权。
3. 默认有效期：默认不过期（现状）vs 默认 90d（安全基线）——需 owner 裁决；草案按
   「缺省不过期 + 签发时必填 expiresInMs 提示」渐进。
4. 轮换宽限期（旧 token 保留 N 分钟双活）是否提供；提供则审计与 401 口径需同步定义，
   v1 草案不提供（轮换=即时切换，调用方自行按新值重试）。

---

## §4 配置三版本状态（T3.10）

### 4.1 目标

配置域建立 **desired（管理台期望）/ persisted（设备/进程已确认落盘）/ runtime（运行中
生效）** 三态模型，管理台不再凭单一布尔显示「已保护/已生效」（v3 规格 §3.2 Policy 字段行，
txt 行 82：「前端不能只看布尔值画『已保护』」）；不一致可视 + 显式收敛指令；重启恢复路径可证
（v3 规格行 206/211「保存/应用/重启恢复三层测试」）。

### 4.2 现状（两处半成品）

- **服务端本地配置**：`admin_settings` KV（console.go:33-67，键 guard_config/
  comm_encryption_enabled/event_callback_config）= 保存即应用 + 持久化 + 启动重放
  （`ApplyPersistedSettings` console.go:94-96）——desired 与 runtime 合一写入，无版本、
  无三态；设置页展示 `source: default|persisted`（console.go:1169-1183；
  admin/index.html:3680）。
- **设备侧配置**：`AppSettings` SharedPreferences（`android/app/src/main/java/
  cn/worktool/open/app/AppSettings.kt:15-132`：serverAddress/log/dryRun/suppressNotice/
  运行时段 FR-52/悬浮窗/跳过过期/重试等）**纯设备本地**，无服务端合同；服务端对设备
  配置不可见、不可收敛。设备唯一自动上报面=301 环境快照（`SessionRuntime.kt:361-381`，
  FR-49③；服务端 301 分支消费 gateway/v1.go:217）。
- 全仓 grep `desired/runtimeApplied` 零命中——三态模型为纯新增。v3 规格 §245 已把
  「配置desired/persisted/applied」登记为缺合同项。

### 4.3 数据模型（迁移 v7，加法；KV 单一事实源不动）

```sql
-- 配置状态影子表：admin_settings.value 仍为现行单一事实源（console.go:69-72 注释纪律），
-- 本表只记版本与三态推进证据，不复制值本身。
CREATE TABLE IF NOT EXISTS config_state (
  domain           TEXT PRIMARY KEY,   -- guard | encryption | event_callbacks | device.<deviceId>.<field-group>
  desired_version  INTEGER NOT NULL DEFAULT 0,  -- 每次管理台保存 +1
  persisted_version INTEGER NOT NULL DEFAULT 0, -- 确认方（进程/设备）确认落盘的版本
  persisted_ms     INTEGER,
  runtime_version  INTEGER NOT NULL DEFAULT 0,  -- 运行中生效版本
  runtime_status   TEXT NOT NULL DEFAULT 'unreportable', -- applied|pending|degraded|failed|unreportable
  last_applied_ms  INTEGER,
  restart_restored INTEGER NOT NULL DEFAULT 0,   -- 最近一次启动由 persisted 重放成功=1
  updated_ms       INTEGER NOT NULL
);
```

- **三态定义**：desired=admin_settings 最新保存（版本自增）；persisted=确认方应答
  「已落盘」（服务端本地域=写 KV 成功即同版本；设备域=设备确认事件）；runtime=确认方
  「正在按该版本执行」（服务端本地域=ApplyPersistedSettings/实时应用成功；设备域=设备
  runtimeStatus 应答）。`applied` 三版相等；`pending`=desired>persisted（待收敛）；
  `degraded/failed`=设备报告运行面无法按版本执行；`unreportable`=确认方无回报能力
  （**不许显示为已生效**）。
- 设备域键形如 `device.<deviceId>.runtime_window`（FR-52 运行时段先行试点域）。

### 4.4 接口草案

| 方法/路径 | 语义 | 关键点 |
|---|---|---|
| `PUT /api/v1/admin/settings/guard`（现状扩展） | 保存即 desired_version+1；本地域 persisted/runtime 同事务推进 | 响应加 `{versions:{desired,persisted,runtime}, runtimeStatus}`（additive） |
| `GET /api/v1/admin/settings`（现状扩展） | 每域附三态版本块 + restartRestored + lastAppliedMs | 管理台设置页据此渲染四态徽标（图例：已生效/待应用/收敛失败/设备未回报），替换单一「persisted 来源」展示 |
| `POST /api/v1/admin/settings/{domain}/reconcile` | 收敛指令：按当前 desired 重发（设备域=下发配置指令；本地域=重放 ApplyPersistedSettings） | 幂等键 `config-<domain>-<desiredVersion>`（复用四元组幂等）；不自动重试——失败落 `config_reconcile_failed` 审计并保持 pending，由人再次触发 |
| 设备域回报（两条既有通道扩展，不新增控制帧形态） | ①配置确认：设备执行配置指令后经既有结果回执携 `configAck:{domain,version}`；②运行态：301 环境快照（SessionRuntime.kt:361-381）加 `configVersions` 块 | 服务端据①推进 persisted、据②推进 runtime；无回报能力的老版本设备 → 域保持 unreportable，UI 显示「设备版本不支持回报」，不假绿 |

- **设备侧落地（试点域=运行时段 FR-52）**：端侧新增配置指令消费（写 `AppSettings`
  对应键，AppSettings.kt:62-74）+ 结果回执携 configAck + 301 携版本；配置指令的类型号
  须经 openapi 注册表超集登记 + catalog 条目 + 契约测试三件套（**rc2 冻结目录语义不动，
  新类型纯追加**，开放问题 2）。

### 4.5 不一致呈现与收敛（UI 合同）

- 每域徽标四态：**已生效**（desired=persisted=runtime 且 status=applied）/
  **待应用**（desired>persisted：显示「已保存，等待设备确认」+ 下发时间）/
  **收敛失败**（pending 超时或 status=failed：显示最近失败原因 + 「重新下发」收敛键）/
  **设备未回报**（unreportable：不绿、不红，中性灰 + 原因）。
- 重启恢复可视化：`restartRestored=1` 显示「重启后已从持久化恢复」（对应 v3 规格行 82
  restartRestored 字段）；恢复失败（ApplyPersistedSettings 出错，P0-2 修复后拒启动语义
  不变）不进入本表（进程起不来即无管理台）。
- 徽标任何形态都**不等于安全门**：写操作鉴权照旧（v3 规格行 80「safeNextActions…
  前端禁用不是安全门」同理）。

### 4.6 迁移与兼容

1. admin_settings value JSON 形状不变（既有读方 api/events.go 分发内核同源只读，
   console.go:69-72）；config_state 为影子表，丢/坏可由重放重建。
2. 本地域（guard/encryption/event_callbacks）迁移即回填三态=当前 KV 版本 1
   （`applied`），现状展示向后兼容（旧 `source` 字段保留输出）。
3. 设备域逐步开通：试点 runtime_window（FR-52，设备有既有持久化与恢复链
   ——CHANGELOG rc1「恢复链统一」条），扩域（悬浮窗/跳过过期/重试次数等）逐个立卡，
   每域须设备确认+运行态两条回报都可用才允许显示「已生效」。
4. 无配置指令消费能力的老设备：域永不离开 unreportable——不阻断管理台保存
   （desired 照常登记），仅如实标注。

### 4.7 里程碑（T3.10）

| 步 | 交付 | 回归测试（先写失败断言） |
|---|---|---|
| M1（W-M6） | 迁移 v7 + 本地域三态回填 + settings 响应版本块 | `TestConfigStateBackfillApplied`（本地域迁移后三版相等）；`TestGuardPutIncrementsDesiredVersion`（保存推进版本，value 形状不变） |
| M2（W-M6） | 收敛端点 + 审计 | `TestReconcileIdempotentPerVersion`（同版本重发命中重放；版本推进后新键）；`TestReconcileFailureStaysPending`（失败保持 pending + config_reconcile_failed 审计，不自动重试） |
| M3（W-M6+） | 设备域试点（runtime_window）：配置指令 + configAck + 301 版本回报 + 管理台四态徽标 | Go：`TestDeviceConfigAckAdvancesPersisted`（回执推进 persisted、伪版本拒收）；Kotlin：配置指令消费写 AppSettings 断言（JVM）；UI：unreportable 不显示已生效断言 |

### 4.8 开放问题

1. 域清单 v1 划界：设备域先行几个（runtime_window 单域试点 vs 一组小配置打包）——
   每扩一域都是端侧新契约，成本不低；倾向单域试点打穿通道再复制。
2. 配置指令类型号与载荷 schema：新指令须走「openapi 注册表 + catalog 登记 + 契约测试」
   三件套（同 docs/16 冻结批纪律），编号建议取未占用段并在立卡时对 apifox 页面核对
   （docs/10 §B「新增指令 MUST 核对 apifox 并留证」纪律）。
3. 服务端本地域是否需要 persisted≠runtime 的中间态（如加密开关需新会话才生效——
   既有会话不重建）：若成立，encryption 域 runtime 推进时机=下一会话 open，需卡内定义
   「pending-applied」过渡展示。
4. config_state 历史留存：影子表只存最新态，版本演进史依赖审计行（audit_log 不可毁），
   是否需要专门版本历史表——倾向不做（审计足够，防第二事实源）。

---

## §5 发布工程联动（本车道同步产出）

- CHANGELOG rc2 节追加「收口后批」小节：登记 rc2 条目落定（face47f）后本冲刺实际入库的
  收尾产出（按 `git log` 实际提交写：233a2a0 / 9629254 / ec38e32 / 9d40409 / 7aa12f7，
  不虚构提交号；契约冻结批与既有 [Unreleased]「指令字段契约冻结」节互引不重复）。
- 本文四卡（T3.7~T3.10）为**设计稿**：后续立卡时以本文为基线，动工前按编排者四门复核；
  本文不产生任何代码/测试行为变更，门禁在本车道以「基线复跑」口径执行（结果见交付说明）。
