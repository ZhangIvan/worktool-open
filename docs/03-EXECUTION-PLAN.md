# 执行与多 Agent 调度计划 v2

- 版本：v2.2（2026-11-02，用户审阅意见落地）；里程碑定义见 `00-MASTER-PLAN.md` §4；复核规程见 `04-AUDIT-CHECKLIST.md`
- **技术栈确认（v2.2，用户 2026-11-02）**：我方服务端继续 **Go**（ADR-02 维持有效）；与 Codex 线（Node/Fastify，origin/worktool/synthetic-prototype 快照 c83423c）**双线并行、取长补短、互不否决**——任务卡在原 Go 基础上吸收其设计成果（状态机边表/幂等键/墓碑/execution_unknown/RBAC 分权/签名回调，对照见 docs/06、映射见 docs/07）；T2.1 交付 = 我方 `protocol/` 规格（含 02 §5 v2 精确规格）+ 测试夹具。

## 1. 车道（Lane）与文件所有权（防冲突）

| 车道 | 范围 | 独占目录 | 执行者 |
|------|------|----------|--------|
| L1 Android 端 | 端侧全部模块 | `android/**` | Agent 并行 + 主会话合并 |
| L2 服务端 | Go 服务 | `server/**` | Agent 并行 |
| L3 管理端 | React 管理台 | `admin/**` | Agent 并行 |
| L4 协议与文档 | 协议规格/夹具/文档 | `protocol/**`、`docs/**` | 主会话为主（单一事实源） |
| L5 QA/设备/安全 | 真机验证、动态 runbook、安全门禁 | `test/**`、`infra/**` | 主会话调度 + 设备 Agent |

规则：跨车道只读他人目录；公共文件（根 README/CI）仅主会话改；每车道输出必须含自测证据（命令+结果）。

## 2. 任务卡（M1~M3 细化，M4+ 在 M3 结束时细化）

### M1（W47）仓库与构建链
| 卡 ID | 车道 | 任务 | DoD | 依赖 | 可并行 |
|-------|------|------|-----|------|--------|
| T1.1 | L4 | monorepo 骨架 + LICENSE(Apache-2.0)+NOTICE+README+CI 模板 | ✅ 已完成并验证：git 仓库初始化，全部目录/LICENSE/NOTICE/CI 门禁/issue 模板入库（证据：`git log` 首提交 + 04 §6 台账 AUD-01 条目） | — | ✅ |
| T1.2 | L1 | android 壳工程（Kotlin+AGP8+JDK17，`cn.worktool.open`）+ 空无障碍服务（透明声明） | CI 出 debug APK | T1.1 | ✅ |
| T1.3 | L2 | server 骨架（Go module、config、健康检查；compose 交付归 infra 车道/M4） | CI 绿 + 本地 `go test` 通过；含"非 loopback 明文绑定启动即拒绝"断言（SR-09） | T1.1 | ✅ |
| T1.4 | L5 | S1 构建容器方案落地（`infra/builder/`） | 服务器产出 hello APK | SSH 已打通（免密，2026-11-02） | ✅ 可排期 |

### M2（W48-49）通讯与端侧核心
| 卡 ID | 车道 | 任务 | DoD | 依赖 | 可并行 |
|-------|------|------|-----|------|--------|
| T2.1 | L4 | `protocol/spec.md`+JSON Schema+**state-transitions.csv**+夹具（v1 兼容字段表 + 02 §5 v2 精确规格全量落地） | 夹具被 L1/L2 单测引用 | audit/02 ✅ | ✅ |
| T2.2 | L1 | core:protocol（WS/心跳/重连/离线暂存/配对） | 协议单测 ≥80% 分支 | T2.1 | 与 T2.3 并行 |
| T2.3 | L2 | gateway+设备注册+指令下发+结果回收 | 与 T2.2 联调：真机 D1 收发指令 | T2.1 | ✅ |
| T2.4 | L1 | core:accessibility 骨架 + 5 原子操作 + 节点调试页 | D1 真机演示点击/输入 | T1.2 | ✅ |
| T2.5 | L5 | 动态 runbook 前置：RIG 通道打通 + frida-server 部署 | 设备日志可拉取 | **阻塞：等 D1 RIG 细节** | ⏸ |

### M3（W50-51）业务功能与调度
| 卡 ID | 车道 | 任务 | DoD | 依赖 | 可并行 |
|-------|------|------|-----|------|--------|
| T3.1 | L1 | FR-01/02/09/10/15（P0 指令执行器） | 真机通过率 100%（每指令 ≥5 次） | T2.2/2.4 | 指令级并行（每执行器一 Agent） |
| T3.2 | L2 | scheduler（cron/队列/幂等/重试）+ REST 兼容层 + 回调分发（签名+重试+复读机 Demo） | apifox 报文冒烟 100%（FR-34）；回调验收（FR-35）：错误签名拒收、失败重试 ≤3 次；loopback/TLS 策略断言（SR-09） | T2.3 | ✅ |
| T3.3 | L1 | task-engine 本地模式（FR-38/39 + FR-46） | 断网场景验收通过 | T2.2 | ✅ |
| T3.4 | L4 | 协议 v2（配对/HMAC/GCM）规格+双端实现 | 安全单测（重放/篡改用例） | T2.1 | ✅ |

### M4+（W52~）摘要
admin Web（L3 并行启动于 M3 末）、compose 交付、Release v0.1.0、动态验证与渗透自查（L5，对齐 audit/03 runbook 阶段 2~6）。

## 3. 并行调度策略
- 每 milestone 一轮：主会话出任务卡 → 无依赖卡全并行（Agent 每卡一个，含 DoD 自验）→ 主会话集成 → 独立复核（04 规程）→ 合并。
- 设备相关（真机验证）必须串行经主会话调度（单设备资源，D1）。
- 阻塞项（S1 SSH、D1 RIG、GitHub remote、签名 keystore）集中登记，不阻塞其他车道 —— 构建类一律先走 CI。

## 4. 产物与版本
- 产物目录 `dist/{app,server,docs}/`（git ignored，附 sha256 清单）；源码即主仓；Release 仅 GitHub。
- 版本：v0.1.0（M4）→ v0.2.0（P1 清单完成）→ v1.0.0（P2+安全自查完成）。

## 5. 待解锁清单（责任：用户）
| 项 | 影响 | 兜底 |
|----|------|------|
| U1 S1 SSH —— **已打通**（免密；Docker 29.6.2；22 实为开放，此前探测为瞬时超时） | （已解除）T1.4 可启动 | CI 构建 |
| U2 D1 RIG（65000 已开放）—— 凭据/证书待首次连接确认 | T2.5 起真机验证 | adb over WiFi 手动开 |
| U3 GitHub org/repo | 发布 Release | 本地 git 已建，随时可推 |
| U4 签名 keystore | Release APK 签名 | debug 签名先行 |
