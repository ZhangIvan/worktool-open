// Package store 定义服务端持久化接口（docs/02-ARCHITECTURE.md §4）。
//
// 计划职责：SQLite（默认）/ Postgres（可选）；结果/事件/审计/outbox 同事务写入
// （docs/06 §3 ①⑤）。T1.3 骨架仅定义最小设备注册接口并提供内存实现
// （internal/store/memory），不引入数据库驱动（避免 CGO）；
// 完整结果/事件/审计/outbox 事务接口随 T2.3/T3.2 扩展。
package store

import (
	"context"
	"time"
)

// Device 为已配对设备的注册信息（骨架字段；会话/凭据字段随 T2.3、协议 v2 随 T3.4 扩展）。
type Device struct {
	RobotID    string
	PairedAt   time.Time
	LastSeenAt time.Time
}

// Store 为服务端持久化边界。
// 内存实现见 internal/store/memory（var _ store.Store 断言契约）；SQLite/PG 实现随 T2.3 落地。
type Store interface {
	// RegisterDevice 注册或更新设备（RobotID 唯一，upsert 语义）。
	RegisterDevice(ctx context.Context, d Device) error
	// GetDevice 按 RobotID 查询设备；不存在时返回 ok=false。
	GetDevice(ctx context.Context, robotID string) (d Device, ok bool, err error)
	// ListDevices 列出全部已注册设备。
	ListDevices(ctx context.Context) ([]Device, error)
	// Close 释放底层资源（内存实现为 no-op）。
	Close() error
}
