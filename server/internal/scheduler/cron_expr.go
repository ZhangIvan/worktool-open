// cron 表达式解析与下次触发计算（FR-38 定时任务；任务卡 T3.2）。
//
// 5 字段标准形态（min hour dom mon dow），支持 `*`、`*/n`、`a-b`、`a-b/n`、
// 逗号列表；dow 允许 0~7（0 与 7 均为周日）。dom/dow 同时受限时按 Vixie cron
// 语义取并集（任一命中即命中）。不引入第三方依赖（go.mod 依赖钉死纪律）。
//
// 时区：以传入时间的本地时区计算（服务端统一 UTC，见 Service.Now 的 .UTC() 约定）。
package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronExpr 为已解析的 cron 表达式（位图：第 i 位 = 值 i 命中）。
type CronExpr struct {
	minute uint64 // 0~59
	hour   uint64 // 0~23
	dom    uint64 // 1~31（0 位未用）
	month  uint64 // 1~12（0 位未用）
	dow    uint64 // 0~6（7 归一为 0）
	// domRestricted / dowRestricted：Vixie 语义——两字段均为 `*` 时按交集（恒真），
	// 任一受限则 dom/dow 取并集。
	domRestricted bool
	dowRestricted bool
	raw           string
}

// ParseCron 解析 5 字段 cron 表达式。
func ParseCron(expr string) (*CronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, fmt.Errorf("scheduler/cron: 表达式须为 5 字段（min hour dom mon dow），实际 %d 字段: %q", len(fields), expr)
	}
	c := &CronExpr{raw: expr}
	var err error
	if c.minute, err = parseCronField(fields[0], 0, 59, nil); err != nil {
		return nil, err
	}
	if c.hour, err = parseCronField(fields[1], 0, 23, nil); err != nil {
		return nil, err
	}
	if c.dom, err = parseCronField(fields[2], 1, 31, nil); err != nil {
		return nil, err
	}
	if c.month, err = parseCronField(fields[3], 1, 12, map[string]int{}); err != nil {
		return nil, err
	}
	if c.dow, err = parseCronField(fields[4], 0, 7, map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}); err != nil {
		return nil, err
	}
	// dow=7 归一为 0（周日）
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
		c.dow &^= 1 << 7
	}
	c.domRestricted = fields[2] != "*" && fields[2] != "*/1"
	c.dowRestricted = fields[4] != "*" && fields[4] != "*/1"
	return c, nil
}

// parseCronField 解析单字段为位图；names 为可选名称映射（如 dow 的 sun~sat）。
func parseCronField(field string, min, max int, names map[string]int) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, fmt.Errorf("scheduler/cron: 字段 %q 含空片段", field)
		}
		step := 1
		rangePart := part
		if s, after, ok := strings.Cut(part, "/"); ok {
			rangePart = s
			v, err := strconv.Atoi(after)
			if err != nil || v < 1 {
				return 0, fmt.Errorf("scheduler/cron: 步长非法: %q", part)
			}
			step = v
		}
		lo, hi := min, max
		switch {
		case rangePart == "*":
			// 全域
		default:
			if l, h, ok := strings.Cut(rangePart, "-"); ok {
				lv, err := cronValue(l, names)
				if err != nil {
					return 0, fmt.Errorf("scheduler/cron: 范围下界非法: %q", rangePart)
				}
				hv, err := cronValue(h, names)
				if err != nil {
					return 0, fmt.Errorf("scheduler/cron: 范围上界非法: %q", rangePart)
				}
				lo, hi = lv, hv
			} else {
				v, err := cronValue(rangePart, names)
				if err != nil {
					return 0, fmt.Errorf("scheduler/cron: 值非法: %q", rangePart)
				}
				lo, hi = v, v
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, fmt.Errorf("scheduler/cron: 值越界（%d~%d）: %q", min, max, part)
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	if bits == 0 {
		return 0, fmt.Errorf("scheduler/cron: 字段 %q 未命中任何值", field)
	}
	return bits, nil
}

func cronValue(s string, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func (c *CronExpr) hits(bitset uint64, v int) bool { return bitset&(1<<uint(v)) != 0 }

// dayMatches 判定某日是否命中（Vixie dom/dow 并集语义）。
func (c *CronExpr) dayMatches(t time.Time) bool {
	if !c.domRestricted && !c.dowRestricted {
		return true
	}
	if c.domRestricted && c.dowRestricted {
		return c.hits(c.dom, t.Day()) || c.hits(c.dow, int(t.Weekday()))
	}
	if c.domRestricted {
		return c.hits(c.dom, t.Day())
	}
	return c.hits(c.dow, int(t.Weekday()))
}

// Next 返回 after 之后（严格大于）的下一次触发时刻；4 年内无可触发时刻返回 ok=false。
func (c *CronExpr) Next(after time.Time) (time.Time, bool) {
	// 从下一分钟整分开始逐分搜索的上界优化版：先逐日，再在日内找 hour/minute。
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := after.Add(4 * 365 * 24 * time.Hour)
	for d := t; d.Before(limit); d = d.AddDate(0, 0, 1) {
		if !c.hits(c.month, int(d.Month())) || !c.dayMatches(d) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !c.hits(c.hour, h) {
				continue
			}
			for m := 0; m < 60; m++ {
				if !c.hits(c.minute, m) {
					continue
				}
				cand := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, d.Location())
				if cand.After(after) && !cand.Before(t.Truncate(time.Minute)) {
					return cand, true
				}
			}
		}
	}
	return time.Time{}, false
}

// String 返回原始表达式。
func (c *CronExpr) String() string { return c.raw }
