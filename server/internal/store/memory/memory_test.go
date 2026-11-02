package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"cn.worktool.open/server/internal/store"
)

func TestRegisterGetList(t *testing.T) {
	ctx := context.Background()
	s := New()
	defer func() { _ = s.Close() }()

	if err := s.RegisterDevice(ctx, store.Device{RobotID: "robot-b"}); err != nil {
		t.Fatalf("RegisterDevice(robot-b): %v", err)
	}
	if err := s.RegisterDevice(ctx, store.Device{RobotID: "robot-a"}); err != nil {
		t.Fatalf("RegisterDevice(robot-a): %v", err)
	}

	got, ok, err := s.GetDevice(ctx, "robot-a")
	if err != nil || !ok {
		t.Fatalf("GetDevice(robot-a) = ok:%v err:%v, want ok:true err:nil", ok, err)
	}
	if got.PairedAt.IsZero() || got.LastSeenAt.IsZero() {
		t.Fatalf("GetDevice(robot-a) 时间戳未补默认值: %+v", got)
	}

	if _, ok, _ := s.GetDevice(ctx, "missing"); ok {
		t.Fatal("GetDevice(missing) 应返回 ok=false")
	}

	list, err := s.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(list) != 2 || list[0].RobotID != "robot-a" || list[1].RobotID != "robot-b" {
		t.Fatalf("ListDevices 应按 RobotID 排序返回 2 台，实际 %+v", list)
	}

	// upsert 语义：同 RobotID 重复注册为覆盖。
	updated := store.Device{RobotID: "robot-a", PairedAt: time.Now().UTC(), LastSeenAt: time.Now().UTC().Add(time.Second)}
	if err := s.RegisterDevice(ctx, updated); err != nil {
		t.Fatalf("RegisterDevice(upsert): %v", err)
	}
	got, _, _ = s.GetDevice(ctx, "robot-a")
	if !got.LastSeenAt.Equal(updated.LastSeenAt) {
		t.Fatalf("upsert 未覆盖，LastSeenAt = %v, want %v", got.LastSeenAt, updated.LastSeenAt)
	}
}

func TestRegisterDeviceRejectsEmptyRobotID(t *testing.T) {
	s := New()
	if err := s.RegisterDevice(context.Background(), store.Device{RobotID: ""}); !errors.Is(err, ErrEmptyRobotID) {
		t.Fatalf("空 RobotID 应返回 ErrEmptyRobotID，实际: %v", err)
	}
}
