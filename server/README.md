# server/

WorkTool-Open 调度后台（Go 单二进制；SQLite 默认 / Postgres 可选；compose 交付）。

- 模块规划：`docs/02-ARCHITECTURE.md` §4（gateway / scheduler / api（apifox 兼容 + 回调分发）/ admin / store）
- 任务卡：T1.3（骨架）、T2.3（WS 网关）、T3.2（调度+REST+回调）、T3.4（协议 v2）
- 部署目标：192.0.2.10（`infra/` 下 compose；构建产物落 `dist/server/`）
