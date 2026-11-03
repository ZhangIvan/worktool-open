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
| AUD-02 | 2026-11-02 | v2.2 全量（00~07/README/.gitignore/CI/SIGNING） | 独立复核 Agent（复核者≠起草者） | **有条件通过**（无 A 级；B×6、C×8；用户 12 项要求 9 项完全落实、3 项落实有残留） | B1（ADR-01 合规暗示）/B2（依赖审计 Node 视角残留→govulncheck+Gradle+npm 三线）/B3（FR-33=509 语义更正为"上传"）/B4（05 无源编号 236/238/305/306 标待确认）/B5（指纹文件移 docs/ops 入库）/B6（版本行统一 v2.2）与 C1（Release Gate 调整为 §5.1）/C2（§9.6 自指笔误）/C3（README 结构行）/C4（06 交叉引用已落实进 03/04）/C5（幂等键口径 operation+key）/C6（X25519 公钥来源补定）/C7（06 补 28 计数未核对标注）/C8（G13+CI 补 *.p8）**全部当场修复**；**关闭确认（CLOSED 2026-11-02）**：复核员逐项验证 B×6/C×8 全部有效，zcode/main orphan 提交/敏感路径零命中/台账三项事实验证通过；残留 R-1（SIGNING §2 死引用）/R-2（06/07 两处 §8→§5.1 节号）已当场修复 |
| AUD-03 | 2026-11-02 | M1 四车道产物（android/server/protocol/infra + 车道 CI）——workflow dwfrun-fff00499 内独立审计 | 独立审计 Agent（审计员≠四位车道工程师） | **通过**（无 A/B 级；C×13 登记） | C-1/C-2（车道 DoD"CI 出包/CI 绿"）已由 CI 首跑全绿关闭（ci/android/server 三工作流 success@c5febc1，主会话核实与报告互证）；C-3/C-6/C-11/C-12 当场修复（docs/03 T1.3、Dockerfile:20 注释、docs/02 §5 路径、docs/00 CI 行 + ci.yml 注释）；C-4/C-5/C-7/C-8/C-9/C-10/C-13 归 M1 尾/M2 落实（明细见 §8） |
| AUD-04 | 2026-11-02 | M2 三车道业务代码 + 规格修订 C-9/C-10（workflow dwfrun-3143262e；含 1 轮 B 级修复复审=真修） | 独立审计 Agent（审计员≠实现工程师） | **通过**（无 A/B 级；C×4：1 项为修复确认记录，3 项开放） | 3 项开放 C 当场处置：AUD-M2-02（server.yml PR 分支过滤）✅；AUD-M2-03（android/README 模块表过期）✅；AUD-M2-04（crypto/pair RNG 静默兜底→响亮失败，go vet+test 复跑全绿）✅。另：工作流本地门禁 bug（go test 需于 server/ 模块目录执行）由主会话定位修复并人工补推送。AUD-03 §8 同步关闭 C-4/C-8/C-9/C-10 |
| AUD-05 | 2026-11-03 | W-M3 三车道（workflow dwfrun-3c372e32：两轮 CR 修复 + E2E 72/72 + AUD 复审；主会话升级裁决 1 次） | 工作流独立审计员 + 主会话 CR 收口评审员（双重独立） | **通过**（收口评审 A=0；B1=L1 白名单漏判 206/207 成员字段，已修+负例测试 TestL1WhitelistGroupMembers；C×5：C1 注释/C4 干跑展示级/C2 占位注释/C3 pyc ignore 已修，C5 e2e 垫片入口统一转 W-M4） | 门禁 go 分量当时失败经查为 E2E 子进程端口瞬时占用，人工两次复跑 9 包全 ok（非代码缺陷）；工作流内部"CR 13 项残留"由收口评审对当前态重derive：仅 B1 真实存在，已修复；dodDeferred 三项（T3.1/T3.3 真机 DoD、T3.3 断网场景验收、CR 残留→已闭环）登记 09 §F，前两项归 W-M3R |
| AUD-06 | 2026-11-03 | W-M3R 三车道（workflow dwfrun-f63d4173：补执行器/真机装配/取证；升级裁决 dwfq-f63d4173-1） | 工作流独立审计员 | **通过**（B×1=F1 车道自述范围与 git status 不符——台账准确性问题，实际无越线（改动并集=两车道合并范围），本行更正即闭环；C×3=F2 取证 SQL 列名笔误/F3 截图实为 JPEG/F4 送验 APK 未归档——证据卫生，W-M4 流程化） | 真机证据链经审计核验为真：配对成功（deviceId=lab-syn-id…）/hello 全链校验（v2.go:296 HMAC+seq+eventId+nonce+ts）/30×干跑 RAW_CONFIRM errorCode=0/幂等重放 replay=true 不重执行/同键变参 409/FR-46 断网 60s 补投全时间线。**关键发现（升级为 W-M4 第一优先级）**：设备侧 hello_ack 被 rule=format 拒收 → 后续帧 seq_gap 拒收 → **执行层未装配**（Go 产、python 收、Kotlin 拒=三方不一致→901~904 控制帧 schema 未冻结的规格缺陷，T3.4 项）；05 矩阵诚实保持全 ⬜ 无虚标 |

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

## 9. 完成分层规则（AUD-R2 复核采纳，2026-11-03）
台账禁止跨层批量宣布完成。每项须标注层级：**L-文档 / L-实现 / L-构建 / L-静态测试 / L-合成夹具 / L-真实设备**；修复后证据必须**重绑**（代码 revision+APK 构建+设备+版本四元组），禁止挪用旧计数或旧证据充新层。高风险默认行为（权限/亮度/通知/资金/危险清理）上线前以"所有者待确认清单"呈批（docs/11 §D）。

## 8. AUD-03 C 级问题登记（在办，随里程碑消化）

| ID | 位置 | 问题摘要 | 处置 | 归属 |
|----|------|----------|------|------|
| C-1 | docs/03 T1.2 / android.yml | DoD"CI 出 debug APK"未闭合（车道禁 push） | ✅ 已关闭：CI 首跑 android success@c5febc1 | — |
| C-2 | docs/03 T1.3 / server.yml | DoD"CI 绿"未闭合 | ✅ 已关闭：server success@c5febc1（本地 go1.25 与 CI 钉 1.23 的版本差已登记） | — |
| C-3 | docs/03 T1.3 | "+compose"与车道表矛盾（compose 归 infra/M4） | ✅ 已修（文字更正） | — |
| C-4 | docs/03 T2.1 DoD | "夹具被 L1/L2 单测引用"需后续任务认领 | ✅ 已关闭（M2）：android `HmacVectorTest` + server 金标准复算双侧落地 | — |
| C-5 | infra/builder/Dockerfile | docker build 从未实跑；cmdline-tools zip sha256 未独立复核；S1 磁盘 31G | S1 首跑前 `docker system prune`；首跑即验证 zip 校验和与 DoD | T1.4 冒烟（M1 尾） |
| C-6 | Dockerfile:20 | 注释写 minSdk 24（实际 26） | ✅ 已修 | — |
| C-7 | android build.gradle.kts:34 + android.yml:51-57 + SIGNING.md | Release 签名路径缺陷：rootProject.file() 相对 android/ 解析 → android/secrets/（不存在）；CI 无 keystore 落盘步骤 | 签名启用前改绝对路径约定或 base64 secret 落盘，或走 S1 本地签名；修后补真实签名构建 | M4 前 |
| C-8 | android.yml / server.yml paths | protocol/** 变更不触发任何 CI；两 yml 分支过滤口径不一 | ✅ 已关闭（M2）：双 yml paths 含 protocol/**，push+PR 分支过滤统一（AUD-M2-02 补齐 PR 侧） | — |
| C-9 | protocol/spec.md §5.2 vs §5.3 | 明文帧 seq 不受 HMAC 覆盖（AAD 只护加密帧）；根因在 docs/02 §5 输入式 | ✅ 已裁决并落地（M2，2026-11-02 主会话裁决）：HMAC 输入式纳入 seq，spec v1.1.0 + 双侧实现 + 金标准向量全量再生（含篡改 seq 必败反例） | — |
| C-10 | protocol/state-transitions.csv E06/E07 + state-policy | ACK TTL 300s 到期后 dispatched/acked 无离出边 | ✅ 已落地（M2）：E17/E18 对账离出边 + 服务端表驱动实现与测试 | — |
| C-11 | docs/02 §5 vs protocol/spec.md | 配对端点 /pair 与 /api/v2/pair 不一致 | ✅ 已修（docs/02 回写 + 注明以 spec 为准） | — |
| C-12 | ci.yml 注释 vs server.yml | 注释宣称 golangci-lint+镜像，实际仅 vet/test | ✅ 已修（注释口径更正） | — |
| C-13 | android/（无 gradlew） | 未提交 Gradle wrapper，新机器无法 ./gradlew | T1.4 S1 首跑时用镜像内 Gradle 8.7 生成 wrapper 并提交，或 README 明示不提供 | M1 尾 |
