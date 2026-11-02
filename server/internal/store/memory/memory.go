// Package memory 提供 store.Store 的内存实现（T1.3：本期不引数据库驱动、避免 CGO；
// 仅用于骨架与单测，进程重启即失忆——生产持久化随 T2.3 SQLite 实现落地）。
package memory

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"cn.worktool.open/server/internal/store"
)

var _ store.Store = (*Store)(nil)

// ErrEmptyRobotID 由 RegisterDevice 在 RobotID 为空时返回。
var ErrEmptyRobotID = errors.New("memory: RobotID 不能为空")

// Store 为 store.Store 的并发安全内存实现。
type Store struct {
	mu      sync.RWMutex
	devices map[string]store.Device
}

// New 创建空内存存储。
func New() *Store {
	return &Store{devices: make(map[string]store.Device)}
}

// RegisterDevice 注册或更新设备（RobotID 唯一，upsert 语义；RobotID 为空返回 ErrEmptyRobotID）。
func (s *Store) RegisterDevice(_ context.Context, d store.Device) error {
	if d.RobotID == "" {
		return ErrEmptyRobotID
	}
	if d.PairedAt.IsZero() {
		d.PairedAt = time.Now().UTC()
	}
	if d.LastSeenAt.IsZero() {
		d.LastSeenAt = d.PairedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[d.RobotID] = d
	return nil
}

// GetDevice 按 RobotID 查询设备；不存在返回 ok=false。
func (s *Store) GetDevice(_ context.Context, robotID string) (store.Device, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[robotID]
	return d, ok, nil
}

// ListDevices 按 RobotID 排序返回全部设备。
func (s *Store) ListDevices(_ context.Context) ([]store.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RobotID < out[j].RobotID })
	return out, nil
}

// Close 为 no-op（满足 store.Store 契约）。
func (s *Store) Close() error { return nil }
