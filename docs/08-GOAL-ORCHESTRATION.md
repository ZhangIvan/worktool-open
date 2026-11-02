# 目标编排章程（Goal Orchestration Charter）v1.0

- 版本：v1.0（2026-11-02，用户指令：以 GOAL 为终点验收、多 workflow 并行推进、中途自测/CR/E2E 一致性验收）
- 编排者：主会话（zcode 线）。执行单元：动态 workflow（每阶段一个）+ 主会话设备环节。审计链：docs/04 §6 台账连续编号。

## 1. 终点 GOAL（终审可验收，非里程碑描述）

**G-0：v0.1.0 发布安全验收全项通过并完成 Release**，分解为：

| # | 验收项 | 判定标准（可机械/可取证） |
|---|--------|--------------------------|
| G-1 功能 | 05 矩阵 P0 行全部 ✅（真机 D1，每指令 ≥5 次成功、回执错误码=0，证据入 local-private/evidence/） | 矩阵 + 证据文件 |
| G-2 系统一致性 E2E | ①跨语言一致性套件绿（独立第三方实现按 protocol/ 夹具打真服务：配对→hello→指令(干跑)→结果→墓碑重放→TTL 对账）②D1 真机 round-trip（App↔自建 server）③apifox 兼容面冒烟 100%（FR-34） | test/e2e 套件退出码 + 真机日志 |
| G-3 质量 | 每车道 CR 通过（独立评审员读 diff，A/B 清零）；AUD 链无未关 A/B；覆盖率门禁（协议 ≥80% 分支、执行器每指令 ≥5 场景） | CR/AUD 记录 + CI |
| G-4 安全 | 门禁 G1~G13 全绿 + 04 §5.1 Release Gate 全项（依赖审计三线/gitleaks/mini 渗透/断网隐私矩阵/FR-52~54 抽查） | Release Gate 清单勾选留痕 |
| G-5 交付 | compose 一键起；Release 附件（APK+server+checksums+支持矩阵）；文档同步 | Release 页 + compose 冒烟 |

## 2. 编排骨架（阶段流水；每阶段统一四门）

```
W-M3 代码并行（workflow）──► D-2.5 设备联调（主会话，与 W-M3 并行启动）
   四门：自测/CR/E2E/AUD            RIG 首连 D1 → 装 APK → 真机 round-trip
        │                                    │
        ▼                                    ▼
W-M3R 真机回归+矩阵填充（workflow，消费 D-2.5 通道）──► G-1 取证
        │
        ▼
W-M4 发布工程（workflow：admin 最小面 + compose + Release Gate 预演）
        │
        ▼
AUD-G 终审（独立审计 vs 本章程 G-1~G-5）──► v0.1.0 Release
```

**四门定义（每个 workflow 内置，顺序固定）**：
1. **自测门**：车道单测 + 本地确定性门禁（JSON/G12/G3/G13/分支/go test -C server）——工程师自证；
2. **CR 门**：每车道一名独立评审员（新上下文）读改动 diff，对照 docs/04 §3 质量门 + spec 对齐 + "测试是否测到点上"，出 typed findings（A/B 必修，C 登记）；
3. **E2E 一致性门**：跨组件真实链路（独立第三方实现按 protocol/ 夹具作规范符合性客户端打真 Go 服务 + apifox 兼容面冒烟），以命令退出码判定；
4. **审计门**：AUD 独立审计（审计员≠实现者≠评审员），结果入 04 §6 台账，A/B 清零才可推送。

**越级规则**：任一门未过不得进入下一阶段；E2E 失败优先怀疑规格歧义（三实现不一致=规格缺陷），先修规格再改实现。

## 3. 阶段台账

| 阶段 | 形态 | 四门 | 状态 | 产出 |
|------|------|------|------|------|
| W-M3 | workflow（T3.1 执行器/T3.2 调度+apifox/T3.3 本地模式 + CR + E2E） | 全四门 | ✅ 完成（E2E 72/72、AUD-05 通过、CR 经主会话收口评审 B1 修复后清零；见 04 §6） | 三车道 + E2E 套件 + FR-53 分级裁决 |
| D-2.5 | 主会话（RIG 首连 ✅：Android12/root/企微 5.0.13 在位；round-trip 并入 W-M3R） | 自测+AUD 抽查 | ✅ 通道达成 | RIG server_info 证据 |
| W-M3R | workflow（**执行器补齐 101/102/501/502/509** + 真机装配 S1/D1 + round-trip 取证 + 05 矩阵回填；**脚本已预写** `.zcode/workflow-drafts/W-M3R-真机装配与矩阵填充.dwf.ts`，W-M3 落地即发射） | 全四门 | ⏸ 脚本就绪 | G-1 取证 |
| W-M4 | workflow（admin 面/compose/Release Gate 预演；C-7 签名修复） | 全四门 | ⏸ | G-4/G-5 预演 |
| W-M5 | workflow（B2 批：22 执行器+9 REST 端点，docs/10 §B2） | 全四门 | ⏸ | v0.2 |
| W-M6 | workflow（B3 批：30 执行器+15 端点，docs/10 §B3） | 全四门 | ⏸ | v0.3 |
| W-M7 | workflow（B4 批：长尾+平台项，docs/10 §B4） | 全四门 | ⏸ | v1.0 候选 |
| AUD-G | 终审（G-1~G-5 + docs/10 对等矩阵逐行） | 对章程+对等计划 | ⏸ | v0.1.0 → v1.0 全量对等 |

## 4. 编排者职责（主会话）
- 阶段间集成与推送（或授权 workflow 内推送）；设备/凭据类交互（RIG/keystore/S1）；规格裁决（如 C-9 类）；台账维护；终审组织。
- 任何 workflow 失败：先抢救报告（report 项），再修复脚本 Amend 复用缓存，不推倒重来。
