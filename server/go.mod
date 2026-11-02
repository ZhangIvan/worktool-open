module cn.worktool.open/server

go 1.23

// 依赖钉死（T2.3 车道纪律：纯 Go 依赖仅允许 modernc.org/sqlite 与一个纯 Go
// websocket 库，版本钉死；新增依赖须先过 docs/04 门禁复核）。
// - modernc.org/sqlite v1.34.4：免 CGO 的 SQLite 驱动（ADR-02 单二进制交付）。
require modernc.org/sqlite v1.34.4

require github.com/gorilla/websocket v1.4.2

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.22.0 // indirect
	modernc.org/gc/v3 v3.0.0-20240107210532-573471604cb6 // indirect
	modernc.org/libc v1.55.3 // indirect
	modernc.org/mathutil v1.6.0 // indirect
	modernc.org/memory v1.8.0 // indirect
	modernc.org/strutil v1.2.0 // indirect
	modernc.org/token v1.1.0 // indirect
)
