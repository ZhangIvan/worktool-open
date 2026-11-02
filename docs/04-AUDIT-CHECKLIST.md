# 审计与复核规程 v2（WorkTool-Open）

- 版本：v2.2（2026-11-02，用户审阅意见落地）；核心原则：**起草者 ≠ 复核者**（作者/独审分离），一切结论要有证据链

## 1. 复核流程（每个里程碑 / 每次合并）
1. 产出方自检（本清单 §2~§5 勾选）→ 2. 独立方复核（新开 Agent，不修改文件，只出问题清单）→ 3. 产出方修复并逐条回应 → 4. 复核方关闭问题 → 5. 记录到 §6 审计台账。
问题分级：A=阻断（必须修）/ B=应修（本里程碑内）/ C=建议（登记即可）。

## 2. 规划/需求文档质量门
- [ ] 每条 FR 有唯一 ID、落位（端/服/双）、优先级、可测验收标准
- [ ] 每条 NFR/SR 有验证方式（可执行，非口号）
- [ ] 需求↔模块↔测试 覆盖矩阵无孤儿（有需求无模块 / 有模块无需求均不允许）
- [ ] 与 audit 报告事实无冲突；引用处标注来源（audit/xx 或 apifox）
- [ ] Out of Scope 明确（防范围蔓延与合规风险）

## 3. 代码质量门（PR 级）
- [ ] 变更关联任务卡 ID / 需求 ID；测试先行或同步补充
- [ ] 单测覆盖：协议层分支 ≥80%；执行器每指令 ≥5 真机用例
- [ ] 无跨车道目录越权修改；公共文件仅主会话变更
- [ ] CI 绿（android.yml + server.yml）

## 4. 安全门禁（SR 验证，含机械门禁）
| 门禁 | 方法 | 关联 |
|------|------|------|
| G1 无 trust-all | `grep -rE "checkServerTrusted|return true" --include=*.kt` 人工确认无恒真实现 | SR-01 |
| G2 无硬编码密钥 | CI grep 黑名单（完整常量 `9876543210abcdef`、`0123456789abcdef`、AK 形态正则）；`SecretKey` 类名仅作人工复审提示，不入机械黑名单 | SR-02 |
| G3 无遥测 SDK | CI 检查产物 APK：`unzip -l` 无 umeng/talkingdata/libcrashsdk/libumeng-spy；smali grep 无 umengcloud/ucweb 域名 | SR-03 |
| G4 出网白名单 | manifest networkSecurityConfig 审查 + 动态抓包用例（audit/03 §5 断网矩阵） | SR-04 |
| G5 存储加密 | 设备数据检查用例（prefs/db 无明文 robotId/token） | SR-05 |
| G6 日志脱敏 | logcat 用例：消息正文/手机号/token 不出现 | SR-06 |
| G7 更新安全 | 集成用例：错签名 APK 拒装；默认关闭 | SR-07 |
| G8 服务端鉴权 | 协议单测：无 token/错 HMAC/重放 全部拒绝 | SR-08 |
| G9 无明文回退默认值 | 配置矩阵单测（默认配置不含 ws:// 与裸 IP） | SR-09 |
| G10 组件最小化 | manifest diff 审查（无伪装名、exported 白名单、allowBackup=false、无 QUERY_ALL_PACKAGES） | SR-10 |
| G11 外链资源白名单 | 服务端用例：fileUrl/fileBase64 指向非白名单域一律拒收（默认拒） | SR-11 |
| G12 产品零 hook | 产品变体源码（android 主/release 源集）不得出现 frida/Xposed/DexClassLoader/注入类依赖；lab 工具仅限 test 源集与 D1 动态验证场景；变体源集隔离脚本校验（范式参考 Codex 线 verify_variant_isolation） | SR-13 |
| G13 涉密路径零入库 | `git ls-files` 不得含 `docs/audit`、`local-private/`、`secrets/`、`*.jks`/`*.keystore`/`*.pem`/`*.p12`/`*.p8` | 政策（00 P7） |

## 5. 合规门禁
- [ ] LICENSE=Apache-2.0 + NOTICE（含 gallonyin 版权与修改声明）
- [ ] 专有 SDK 零残留（G3）；第三方依赖 license 清单（`docs/THIRD-PARTY-NOTICES.md`）
- [ ] 命名与宣传无"官方/腾讯"暗示；README 含平台风控与合规使用告知（R2）
- [ ] 反编译代码不直接入库（行为级重写，防上游闭源版权污染）——抽查相似度
- [ ] 命名不含上游商标混淆使用（现名 WorkTool-Open 按"描述性使用+显著致谢"策略，R4 法务复核中；复核不通过则更名）
- [ ] 敏感资料不入库（G13）；测试 hook 与产品实现边界（G12）逐 PR 复核
- [ ] 合规表述不含"无障碍即合规"暗示：无障碍 API 是技术路线选择，不等于平台条款合规（风险告知在 README/00 R2）

## 5.1 v0.1.0 发布安全验收（Release Gate）

- [ ] G1~G13 全绿（CI 机械项 + 人工审查项附输出/截图）
- [ ] 依赖审计：govulncheck（server/Go）+ Gradle 依赖审查（android）+ npm audit（admin），审计产物存 `dist/docs/` 并附 Release
- [ ] secrets 扫描：gitleaks 全仓零命中
- [ ] mini 渗透用例（D1 或 staging）：未授权 WS/REST 拒绝；伪造/过期签名拒绝；重放（同 eventId/nonce/幂等键变参）拒绝；fileUrl 越域拒收（G11）；更新包错签名拒装（G7）
- [ ] 隐私断网矩阵报告（runbook 阶段 5）通过：默认配置出网仅用户配置服务端
- [ ] FR-52/53/54 抽查：L2 无确认不执行；接管暂停 ≤5s 生效；干跑报告与真实路径同构
- [ ] 当轮 AUD 台账关闭，A/B 级问题清零
- [ ] `docs/05` 支持矩阵已按当期真机结果更新（NOT_RUN 项不得标支持）

## 6. 审计台账
| ID | 日期 | 对象 | 复核方 | 结论 | 问题处理 |
|----|------|------|--------|------|----------|
| AUD-01 | 2026-11-02 | docs/00~04 v2.0→v2.1（起草：主会话） | 独立复核 Agent（复核者≠起草者） | **通过**（关闭确认 2026-11-02；首轮有条件通过 A×2/B×11/C×9 → 修复后复审通过，无阻断） | 全部处置：A1 骨架真实落地（git 首提交为证）并更正 T1.1 状态；A2 本台账即闭合记录；B1~B11 已修（01 §1.8 与 §3、00 §2/§4/§6/§7、02 §3、03 §2/§3/§5、04 §4/§5/§7、audit/03 回填 23 条端点用例）；C1~C9 已修入文。关闭复审提出 C4 尾巴（00 残留 4 处悬空 Q 引用）与新引入 R1~R5（T1.4 状态矛盾、FR-49/50/51 未入矩阵、G11 缺失、audit/03 §4 过期时态、版本号不齐）均已当场修复，见后续 git 提交 |
| AUD-02 | 2026-11-02 | v2.2 全量（00~07/README/.gitignore/CI/SIGNING） | 独立复核 Agent（复核者≠起草者） | **有条件通过**（无 A 级；B×6、C×8；用户 12 项要求 9 项完全落实、3 项落实有残留） | B1（ADR-01 合规暗示）/B2（依赖审计 Node 视角残留→govulncheck+Gradle+npm 三线）/B3（FR-33=509 语义更正为"上传"）/B4（05 无源编号 236/238/305/306 标待确认）/B5（指纹文件移 docs/ops 入库）/B6（版本行统一 v2.2）与 C1（Release Gate 调整为 §5.1）/C2（§9.6 自指笔误）/C3（README 结构行）/C4（06 交叉引用已落实进 03/04）/C5（幂等键口径 operation+key）/C6（X25519 公钥来源补定）/C7（06 补 28 计数未核对标注）/C8（G13+CI 补 *.p8）**全部当场修复**；关闭确认已发回复核员 |

## 7. 覆盖矩阵（种子版，M2 起随实现滚动更新）
| 需求组 | 端侧模块 | 服务端模块 | 测试 | 状态 |
|--------|----------|------------|------|------|
| FR-01~08 消息 | feature:message | gateway/api | 协议单测+真机 | 未开始 |
| FR-09~14 群管理 | feature:group | api | 真机 | 未开始 |
| FR-15~21 好友 | feature:contact | api | 真机 | 未开始 |
| FR-22~27 效率 | feature:efficiency | scheduler | 真机 | 未开始 |
| FR-28~33 查询 | feature:query | api | 真机 | 未开始 |
| FR-34/35/37/40/41/42 平台（服务端） | — | gateway/api/scheduler/admin | apifox 冒烟 | 未开始 |
| FR-36/38/39 平台（端侧） | core:task-engine | scheduler | 本地模式用例 | 未开始 |
| FR-43 通讯加密 | core:protocol | gateway | 协议安全单测 | 未开始 |
| FR-44 自更新 | lib:updater | update-api | G7 用例 | 未开始 |
| FR-45 OCR 兜底 | lib:ocr | — | R5 评估 | 未开始 |
| FR-46 离线暂存重发 | core:protocol | gateway | 断网矩阵 | 未开始 |
| FR-47 多地址容灾 | app（配置层） | — | 配置矩阵 | 未开始 |
| FR-48 端侧诊断 | app（诊断层） | — | 自检用例 | 未开始 |
| FR-52 用户接管 | 端侧暂停门禁+悬浮窗 | 会话/调度/审计 | 暂停生效用例 | 未开始（pause gate 设计参考 Codex 线） |
| FR-53 误发保护 | 执行前复检 | 风险分级/审批/熔断 | 分级与熔断用例 | 未开始（approver 分权设计参考 Codex 线） |
| FR-54 干跑模式 | policy 开关 | api 层 | 干跑报告用例 | 未开始 |
| SR-12 审计留痕 | 审计面 | 审计面 | 审计用例 | 未开始（审计面设计参考 Codex 线） |
| SR-13 产品零 hook | 产品源集 | — | G12 | 未开始 |
| SR-14 静态数据保护 | 存储层 | storage | 存储审查 | 未开始 |
| SR-01~11 | 全部 | 全部 | §4 门禁 G1~G11 | 未开始 |
| FR-49 运行日志上报 | app（日志管道，脱敏） | api | SR-06 用例 | 未开始 |
| FR-50 悬浮窗控制 | app（float 模块） | — | 真机用例 | 未开始 |
| FR-51 音视频自动拒接 | feature:* | — | 真机用例 | 未开始 |
