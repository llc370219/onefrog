// Package scheduler 是 frogbot 的定时唤醒器。
//
// 计算下次触发时间、持久化计划/认领记录，到点把任务交给现有任务队列。
// 本包不调用模型；后端进程不在时无法执行（前端开不开无关）。
package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// DefaultTimezone 是应用级默认时区（IANA）。Spec.Timezone 为空时使用它。
const DefaultTimezone = "Asia/Shanghai"

// Kind 是触发节奏。
type Kind string

const (
	Once   Kind = "once"   // 只跑一次
	Daily  Kind = "daily"  // 每天固定时刻
	Weekly Kind = "weekly" // 每周若干天的固定时刻
)

// Spec 描述一条计划的触发规则。
//
// 时间一律按 Timezone（空 = DefaultTimezone）的墙上时钟理解：
//   - Once 用 AtLocal（该时区的本地年月日时分秒；原 Location 会被忽略）
//   - Daily / Weekly 用 Hour、Minute（0–23 / 0–59）
//   - Weekly 额外用 Weekdays（time.Sunday=0 … time.Saturday=6），至少一天
type Spec struct {
	Kind     Kind           `json:"kind"`
	Timezone string         `json:"timezone"`
	AtLocal  time.Time      `json:"at_local,omitempty"`
	Hour     int            `json:"hour,omitempty"`
	Minute   int            `json:"minute,omitempty"`
	Weekdays []time.Weekday `json:"weekdays,omitempty"`
}

// Validate 检查字段是否合理，不计算下次时间。
func (s Spec) Validate() error {
	switch s.Kind {
	case Once, Daily, Weekly:
	default:
		return fmt.Errorf("未知节奏 %q（要用 once / daily / weekly）", s.Kind)
	}
	if _, err := LoadLocation(s.Timezone); err != nil {
		return err
	}
	switch s.Kind {
	case Once:
		if s.AtLocal.IsZero() {
			return errors.New("单次计划需要 at_local")
		}
	case Daily, Weekly:
		if s.Hour < 0 || s.Hour > 23 {
			return fmt.Errorf("小时要在 0–23，收到 %d", s.Hour)
		}
		if s.Minute < 0 || s.Minute > 59 {
			return fmt.Errorf("分钟要在 0–59，收到 %d", s.Minute)
		}
		if s.Kind == Weekly {
			if len(s.Weekdays) == 0 {
				return errors.New("每周计划至少选一天")
			}
			seen := map[time.Weekday]bool{}
			for _, d := range s.Weekdays {
				if d < time.Sunday || d > time.Saturday {
					return fmt.Errorf("星期取值要在 0–6，收到 %d", d)
				}
				if seen[d] {
					return fmt.Errorf("星期重复：%v", d)
				}
				seen[d] = true
			}
		}
	}
	return nil
}

// Next 返回严格晚于 now 的下一次触发时刻（带该时区的 Location）。
// 单次计划若已过期，返回 ok=false。
func (s Spec) Next(now time.Time) (next time.Time, ok bool, err error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, false, err
	}
	loc, err := LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, false, err
	}
	now = now.In(loc)

	switch s.Kind {
	case Once:
		at := WallIn(loc, s.AtLocal)
		if !at.After(now) {
			return time.Time{}, false, nil
		}
		return at, true, nil

	case Daily:
		cand := time.Date(now.Year(), now.Month(), now.Day(), s.Hour, s.Minute, 0, 0, loc)
		if !cand.After(now) {
			cand = cand.AddDate(0, 0, 1)
		}
		return cand, true, nil

	case Weekly:
		days := append([]time.Weekday(nil), s.Weekdays...)
		sort.Slice(days, func(i, j int) bool { return days[i] < days[j] })
		for i := 0; i < 7; i++ {
			d := now.AddDate(0, 0, i)
			if !hasWeekday(days, d.Weekday()) {
				continue
			}
			cand := time.Date(d.Year(), d.Month(), d.Day(), s.Hour, s.Minute, 0, 0, loc)
			if cand.After(now) {
				return cand, true, nil
			}
		}
		for i := 7; i < 14; i++ {
			d := now.AddDate(0, 0, i)
			if !hasWeekday(days, d.Weekday()) {
				continue
			}
			return time.Date(d.Year(), d.Month(), d.Day(), s.Hour, s.Minute, 0, 0, loc), true, nil
		}
		return time.Time{}, false, errors.New("每周计划算不出下次时间")
	}
	return time.Time{}, false, fmt.Errorf("未知节奏 %q", s.Kind)
}

// LoadLocation 解析 IANA 时区；空或 "UTC" 以外的空值使用 DefaultTimezone。
func LoadLocation(name string) (*time.Location, error) {
	if name == "" {
		name = DefaultTimezone
	}
	if name == "UTC" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("时区 %q 无效：%w", name, err)
	}
	return loc, nil
}

// WallIn 把 t 的年月日时分秒按 loc 的墙上时钟重新解释（丢掉原 Location）。
func WallIn(loc *time.Location, t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
}

func hasWeekday(days []time.Weekday, want time.Weekday) bool {
	for _, d := range days {
		if d == want {
			return true
		}
	}
	return false
}
