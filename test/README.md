# test/

真机验证、跨实现一致性 E2E 与取证卫生工具。**入口统一在仓库根 `test/`**（W-M4 收口
AUD-05 C5：删除 `server/test/e2e/` 调用垫片，双路径入口合一；历史垫片仅为 W-M3 E2E
车道 cwd=server/ 的过渡接线，勿再新增）。

## 目录

| 入口 | 用途 | 用法（cwd = 仓库根） |
|------|------|----------------------|
| `test/e2e/run_cross_e2e.py` | 跨实现一致性 E2E（G-2①）：python 独立第三方规范客户端（cryptography 库）按 protocol/ 权威件打真实 Go 服务——配对→hello→干跑→结果→墓碑重放→TTL 对账 + apifox 兼容面冒烟 | `python test/e2e/run_cross_e2e.py [--repo-root PATH] [--keep] [--verbose]`；退出码 0=全过 1=断言失败 2=环境错误 |
| `test/collect_evidence.py` | 取证卫生（AUD-06 C×3 流程化）：F2 `command_events` 按真实列名重采（`edge_id`/`from_state`/`to_state`/`event`/`detail`/`at`，sqlite.go schema）、F3 截图按 magic bytes 校正扩展名、F4 送验 APK 连 sha256 归档 local-private | `python test/collect_evidence.py <子命令> …`（`--help` 看逐项） |

- 动态分析分阶段执行：`local-private/audit/03-reverse-methodology-runbook.md`（阶段 0~6，含 23 条端点交叉验证用例 E1-E6、断网矩阵、隐私验证）
- 指令验收：每指令真机 ≥5 次通过 + 回执错误码=0（`docs/01` 通用验收模板）
- 红线：仅测试小号；禁止对真实联系人执行群发等风险动作
- 真机设备：192.0.2.20（RIG:65000 通道，串行调度，单设备资源）
