# server/

WorkTool-Open 调度后台（Go 单二进制；SQLite 默认 / Postgres 可选；compose 交付）。

- 模块规划：`docs/02-ARCHITECTURE.md` §4（gateway / scheduler / api（apifox 兼容 + 回调分发）/ admin / store）
- 任务卡：T1.3（骨架）、T2.3（WS 网关）、T3.2（调度+REST+回调）、T3.4（协议 v2）
- 部署目标：192.0.2.10（`infra/` 下 compose；构建产物落 `dist/server/`）

## 运行（T1.3 骨架）

```bash
cd server
go run ./cmd/server                       # 默认 127.0.0.1:8080，/healthz 健康检查
go run ./cmd/server -listen 0.0.0.0:8443 -tls-cert cert.pem -tls-key key.pem   # 对外绑定必须 TLS
```

配置来源优先级：默认值 < 环境变量（`WT_LISTEN` / `WT_TLS_CERT` / `WT_TLS_KEY`）< 命令行 flag（`-listen` / `-tls-cert` / `-tls-key`）。

**SR-09（启动即拒绝）**：绑定地址非 loopback（127.0.0.0/8、::1、localhost）且未启用 TLS
（`-tls-cert` 与 `-tls-key` 须同时提供）时，进程在监听前直接退出。空 host（`:8080`，绑定
所有接口）同样视为非 loopback。单测正反例见 `internal/config/config_test.go` 与
`cmd/server/main_test.go`。

## 本期边界（T1.3）

- `internal/store` 仅接口 + 内存实现（`internal/store/memory`）；不引数据库驱动、零 CGO。
  SQLite/PG 及结果/事件/审计/outbox 同事务接口随 T2.3/T3.2 落地。
- `internal/gateway|scheduler|admin` 为 doc 注释骨架（对应 docs/02 §4 表），空实现占位。
- 零第三方依赖（`go.mod` 无 require），CI 见 `.github/workflows/server.yml`（go vet + go test）。
