# ADR：保留 canonical payload TEXT，避免 JSONB 的 NUL 转义限制

**状态：PROPOSED，待独审后才实现。** 不改 ATC 接受集、不改黄金向量、不执行 PG。范围仅 commands.payload 的语法 CHECK；不修改 result/body/其他 JSONB 约束。

## 问题与决定

ATC 合法文本可含 U+0000。内部 canonical encoder 写入的是六个 ASCII 字节 `\u0000`，并非把实际 NUL 字节塞进 PG TEXT。旧 `0001_base19.sql` 中 `CHECK(payload IS NULL OR jsonb_typeof(payload::jsonb)='object')` 会在转换 JSONB 时解码并拒绝此转义，导致纯 Go golden 合法却不能入库。

候选追加 **0006_commands_payload_json.sql**：保持 payload 列为 TEXT 和原始 canonical 字节不变；仅替换旧 CHECK 为具名 `commands_payload_json_object CHECK(payload IS NULL OR pg_catalog.json_typeof(payload::pg_catalog.json)='object')`。不改旧迁移文件、不截断文本、不把NUL静默拒绝、不修改digest、不把payload整体存成JSONB。

PostgreSQL 官方文档说明 json 输入对Unicode escape做语法检查，而 jsonb 拒绝 `\u0000`；也警告部分 JSON 处理函数仍可能解码，所以不能泛称“所有 json 操作都安全”。[官方 JSON 类型说明](https://www.postgresql.org/docs/18/datatype-json.html)

官方18分支 `json.c` 中 json_in 验证完整语法但不请求解码字符串；json_typeof仅读取首token判断顶层类型，同样未请求解码。因而上述组合适合保留含NUL转义的**对象原文**检查；此处是源码支持的设计推断，仍须目标PG18.6实际测试证明。[官方 json.c（json_in、json_typeof）](https://github.com/postgres/postgres/blob/REL_18_STABLE/src/backend/utils/adt/json.c)

## 迁移 guard（必须 fail closed）

1. 单个受控迁移事务、有限lock/statement timeout；只操作迁移器选定的隔离schema.commands，先锁定该表，禁止同事务外开放写入窗口。
2. 旧源码的单列payload CHECK预期自动名称为 `commands_payload_check`；**不能仅凭名字DROP，更不能批量删除所有CHECK**。在目标表OID上查pg_constraint，要求恰一条该名称、contype=c、已validated、conkey恰payload的attnum，payload列仍TEXT。
3. 必须核 `pg_get_expr(conbin,conrelid)`/`pg_get_constraintdef` 的实际表达式，精确等于旧 nullable+jsonb_typeof(payload::jsonb)=object 形状；允许的仅是指定PG18 deparser格式，不用“包含payload/jsonb”模糊匹配。初次受控迁移测试在应用0001…0005后读取并断言实际name/definition，固定它为0006 guard证据；若与预期不同，停止、报告，不猜替代名字或删别的约束。
4. 新具名约束若已存在则拒绝本次重复DDL（由迁移账本判断已应用，而非SQL假装成功）；在同事务DROP已验证的旧CHECK并ADD新CHECK，默认验证现存数据。任一异常事务整体回滚；不用NOT VALID绕过旧数据。
5. 负测旧constraint被重命名/表达式篡改/不存在时guard拒绝且保留现约束；确认 `commands_result_check` 和其他约束定义前后不变。标准store身份没有ALTER权限。

本ADR不预称已读取真实PG系统目录；上面的旧自动名是根据现DDL的**预期**，后续实现必须真实catalog核验。0006应保守失败而不是在未知schema上迁移。

## 最小真实PG验收（待实现、NOT_RUN）

| ID | input/action | expected |
|---|---|---|
| PAYLOAD-01 | 完整AdmitAppText，text=`prefix` + U+0000 + `suffix`，F0 | 接受成功，整个准入图/计量守恒；从payload TEXT回读与内部canonical bytes逐字节相等，含字面`\u0000`且无原始NUL；request/command摘要与独立golden一致。 |
| PAYLOAD-02 | 同含NUL请求/key重复提交 | 同一receipt、同一payload字节/摘要、无重复计量。 |
| PAYLOAD-03 | store直接UPDATE payload为坏JSON，例如缺右括号 | 语法错误拒绝，旧数据不变；不是仅顶层首字符检测。 |
| PAYLOAD-04 | payload为合法JSON数组、字符串、数值、true、JSON null | 精确新CHECK拒绝；SQL NULL仍允许以兼容现有清理语义（不等于准入允许缺payload）。 |
| PAYLOAD-05 | payload含换行/引号/非BMP、合法NUL转义对象 | 完整对象语法通过，原始canonical TEXT保持；不使用`->>`抽取NUL文本验证，避免触发另外的解码限制。 |
| PAYLOAD-06 | 0001…0005新schema的预期旧CHECK；另三个schema改名/改表达式/删CHECK后应用0006 | 正常形状可迁移，异常形状全部fail closed；结果CHECK和无关constraints未被修改。 |

应以参数化TEXT读写和Go byte comparison断言，不通过JSONB cast、对象重序列化或日志回显payload验证。坏JSON错误SQLSTATE和新CHECK名称可去敏记录，禁止记录原文或连接串。

## 后续边界

- result、developer_callbacks.body、inbox.reply 等仍存在 JSONB 转换检查，**本ADR不证明任意含NUL内容可在全链路存储或回传**。未来结果合同如果允许此类值，须另案审核；不能在本次偷偷扩范围。
- JSON语法CHECK不替代严格DTO/Unicode/重复key/能力授权；json本身可能接受非规范或重复字段，准入仍只写内部生成的canonical对象。
- 不新增列/表/角色，不改schema数量28，不扩大HTTP/worker/PITR。实现时追加迁移顺序并补真实PG用例后再交源码独审；本地仍不得运行PG。
