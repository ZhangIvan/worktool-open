package scheduler

import (
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		t.Fatalf("解析时间 %q 失败: %v", s, err)
	}
	return tm.UTC()
}

func TestParseCronErrors(t *testing.T) {
	cases := []string{
		"",             // 空表达式
		"* * * *",      // 4 字段
		"* * * * * *",  // 6 字段
		"60 * * * *",   // 分钟越界
		"* 24 * * *",   // 小时越界
		"* * 32 * *",   // dom 越界
		"* * * 13 *",   // 月份越界
		"* * * * 8",    // dow 越界
		"*/0 * * * *",  // 步长 0
		"a * * * *",    // 非数值
		"1-60 * * * *", // 范围越界
		"5-1 * * * *",  // 范围倒置
	}
	for _, expr := range cases {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("表达式 %q 应解析失败，实际通过", expr)
		}
	}
}

func TestCronNextBasic(t *testing.T) {
	cases := []struct {
		expr  string
		after string
		want  string
	}{
		// 每分钟
		{"* * * * *", "2026-11-02 10:00:30", "2026-11-02 10:01:00"},
		{"* * * * *", "2026-11-02 10:00:00", "2026-11-02 10:01:00"}, // 严格大于
		// 固定分钟
		{"5 * * * *", "2026-11-02 10:00:00", "2026-11-02 10:05:00"},
		{"5 * * * *", "2026-11-02 10:05:00", "2026-11-02 11:05:00"},
		// 范围
		{"10-12 * * * *", "2026-11-02 10:12:00", "2026-11-02 11:10:00"},
		// 步长
		{"*/15 * * * *", "2026-11-02 10:00:00", "2026-11-02 10:15:00"},
		{"*/15 * * * *", "2026-11-02 10:16:00", "2026-11-02 10:30:00"},
		{"10-50/10 * * * *", "2026-11-02 10:10:00", "2026-11-02 10:20:00"},
		// 列表
		{"0,30 * * * *", "2026-11-02 10:00:00", "2026-11-02 10:30:00"},
		{"0,30 9 * * *", "2026-11-02 09:30:00", "2026-11-03 09:00:00"},
		// 时+分
		{"30 9 * * *", "2026-11-02 09:30:00", "2026-11-03 09:30:00"},
		// 日+月（跨年）
		{"0 0 1 1 *", "2026-11-02 10:00:00", "2027-01-01 00:00:00"},
		// 月内日
		{"0 0 15 * *", "2026-11-02 10:00:00", "2026-11-15 00:00:00"},
	}
	for _, tc := range cases {
		expr, err := ParseCron(tc.expr)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", tc.expr, err)
		}
		got, ok := expr.Next(at(t, tc.after))
		if !ok {
			t.Fatalf("表达式 %q 无后续触发时刻", tc.expr)
		}
		if got != at(t, tc.want) {
			t.Errorf("表达式 %q：Next(%s)=%s，期望 %s", tc.expr, tc.after, got.Format("2006-01-02 15:04:05"), tc.want)
		}
	}
}

func TestCronNextWeekday(t *testing.T) {
	// 2026-11-02 是周一。dow=5（周五）
	expr, err := ParseCron("0 9 * * 5")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got, _ := expr.Next(at(t, "2026-11-02 10:00:00"))
	if want := at(t, "2026-11-06 09:00:00"); got != want {
		t.Errorf("周五表达式：got %s want %s", got, want)
	}
	// dow 名称解析（sun=0；7 也是周日）
	for _, dow := range []string{"0", "7", "sun"} {
		e, err := ParseCron("0 9 * * " + dow)
		if err != nil {
			t.Fatalf("解析 dow=%s 失败: %v", dow, err)
		}
		// 2026-11-02 周一 → 下一个周日 = 2026-11-08
		got, _ := e.Next(at(t, "2026-11-02 10:00:00"))
		if want := at(t, "2026-11-08 09:00:00"); got != want {
			t.Errorf("dow=%s：got %s want %s", dow, got, want)
		}
	}
}

func TestCronDomDowUnionSemantics(t *testing.T) {
	// Vixie 语义：dom 与 dow 同时受限 → 并集（任一命中）。
	// "0 0 13 * 5"：每月 13 日 或 每个周五 的 00:00。
	// 2026-11-02（周一）之后：2026-11-06（周五）先于 2026-11-13。
	expr, err := ParseCron("0 0 13 * 5")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got, _ := expr.Next(at(t, "2026-11-02 10:00:00"))
	if want := at(t, "2026-11-06 00:00:00"); got != want {
		t.Errorf("dom/dow 并集：got %s want %s（周五 11-06 应先于 13 日）", got, want)
	}
	// 仅 dow 受限（dom=*）：正常交集语义（日期不参与）
	expr2, _ := ParseCron("0 0 * * 1")
	got2, _ := expr2.Next(at(t, "2026-11-02 10:00:00"))
	if want := at(t, "2026-11-09 00:00:00"); got2 != want {
		t.Errorf("仅 dow：got %s want %s", got2, want)
	}
}

func TestCronNextNoFireWithinHorizon(t *testing.T) {
	// 2 月 30 日永不存在 → 4 年内无可触发时刻
	expr, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if _, ok := expr.Next(at(t, "2026-11-02 10:00:00")); ok {
		t.Error("2 月 30 日表达式应无可触发时刻")
	}
}
