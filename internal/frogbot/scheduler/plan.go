package scheduler

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"
)

// Overlap 控制上一趟还没结束时，这一趟怎么处理。
const (
	OverlapSkip  = "skip"  // 跳过本趟（默认）
	OverlapQueue = "queue" // 照样入队，交给任务队列排队
)

// Plan 是一条持久化的定时计划。
type Plan struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Prompt    string    `json:"prompt"` // 到点交给后台任务的内容
	Spec      Spec      `json:"spec"`
	Enabled   bool      `json:"enabled"`
	Overlap   string    `json:"overlap"` // skip | queue
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	NextRun    *time.Time `json:"next_run,omitempty"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	LastStatus string     `json:"last_status,omitempty"` // claimed|dispatched|skipped_misfire|skipped_overlap|needs_verify|completed|failed|expired
	LastTaskID string     `json:"last_task_id,omitempty"`
	LastClaim  string     `json:"last_claim,omitempty"`
}

// Claim 是对某一次拟触发时刻的原子认领，用来重启后避免重复派发。
//
// 状态机：
//
//	claimed → dispatched → completed|failed
//	claimed/dispatched → needs_verify（重启后任务去向不明，需要人工核实，绝不自动重放危险副作用）
//	→ skipped_misfire | skipped_overlap | expired
type Claim struct {
	Key          string     `json:"key"` // planID + "@" + intended UTC RFC3339
	PlanID       string     `json:"plan_id"`
	TaskID       string     `json:"task_id"`
	IntendedAt   time.Time  `json:"intended_at"`
	ClaimedAt    time.Time  `json:"claimed_at"`
	Status       string     `json:"status"`
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Note         string     `json:"note,omitempty"`
}

// OccurrenceKey 对一次拟触发时刻生成稳定键。
func OccurrenceKey(planID string, at time.Time) string {
	return planID + "@" + at.UTC().Format(time.RFC3339)
}

// TaskIDFor 由认领键派生确定性任务 ID，重启后仍能对上。
func TaskIDFor(claimKey string) string {
	sum := sha1.Sum([]byte("frogbot-sched:" + claimKey))
	return "task_sched_" + hex.EncodeToString(sum[:12])
}

// Public 返回给前端/聊天层看的视图（不含内部实现细节以外的字段）。
func (p Plan) Public() map[string]any {
	tz := p.Spec.Timezone
	if tz == "" {
		tz = DefaultTimezone
	}
	m := map[string]any{
		"id": p.ID, "title": p.Title, "prompt": p.Prompt, "enabled": p.Enabled,
		"overlap": p.Overlap, "kind": string(p.Spec.Kind), "timezone": tz,
		"hour": p.Spec.Hour, "minute": p.Spec.Minute,
		"created_at": p.CreatedAt, "updated_at": p.UpdatedAt,
		"last_status": p.LastStatus, "last_task_id": p.LastTaskID, "last_claim": p.LastClaim,
	}
	if !p.Spec.AtLocal.IsZero() {
		m["at_local"] = WallIn(mustLoc(tz), p.Spec.AtLocal).Format("2006-01-02 15:04:05")
	}
	if len(p.Spec.Weekdays) > 0 {
		days := make([]int, len(p.Spec.Weekdays))
		for i, d := range p.Spec.Weekdays {
			days[i] = int(d)
		}
		m["weekdays"] = days
	}
	if p.NextRun != nil {
		m["next_run"] = p.NextRun.Format(time.RFC3339)
		m["next_run_local"] = p.NextRun.In(mustLoc(tz)).Format("2006-01-02 15:04:05 MST")
	}
	if p.LastRun != nil {
		m["last_run"] = p.LastRun.Format(time.RFC3339)
		m["last_run_local"] = p.LastRun.In(mustLoc(tz)).Format("2006-01-02 15:04:05 MST")
	}
	return m
}

func mustLoc(name string) *time.Location {
	loc, err := LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// DescribeSpec 给人看的一句话。
func DescribeSpec(s Spec) string {
	tz := s.Timezone
	if tz == "" {
		tz = DefaultTimezone
	}
	switch s.Kind {
	case Once:
		return fmt.Sprintf("单次 %s（%s）", WallIn(mustLoc(tz), s.AtLocal).Format("2006-01-02 15:04"), tz)
	case Daily:
		return fmt.Sprintf("每天 %02d:%02d（%s）", s.Hour, s.Minute, tz)
	case Weekly:
		names := []string{"日", "一", "二", "三", "四", "五", "六"}
		var parts []string
		for _, d := range s.Weekdays {
			parts = append(parts, "周"+names[int(d)])
		}
		return fmt.Sprintf("每%s %02d:%02d（%s）", joinCN(parts), s.Hour, s.Minute, tz)
	}
	return string(s.Kind)
}

func joinCN(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += "、" + parts[i]
	}
	return out
}
