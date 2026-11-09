package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"crypto/rand"
	"encoding/hex"
)

// Clock 可注入，便于单测。
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// TaskStatus 是引擎向宿主查询任务去向时用的。
type TaskStatus string

const (
	TaskUnknown   TaskStatus = ""
	TaskOpen      TaskStatus = "open" // queued/running/paused
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
	TaskMissing   TaskStatus = "missing"
)

// Host 是引擎对 frogbot 的回调：派任务、查状态、通知。
type Host interface {
	// Dispatch 创建（或确认已存在）确定性 ID 的任务。返回实际任务 ID。
	Dispatch(plan Plan, claim Claim) (taskID string, err error)
	// Lookup 查询任务当前状态。
	Lookup(taskID string) TaskStatus
	// Notify 把验证需要/失败等事件告诉聊天层（宿主决定怎么叫醒）。
	Notify(kind, text string, plan Plan, claim *Claim)
	// Emit 把计划变更推给前端（schedule.upserted / schedule.deleted / schedule.run）。
	Emit(kind string, payload any)
}

// Config 是引擎策略。
type Config struct {
	Enter        func() (func(), error) // optional host maintenance lease
	TickEvery    time.Duration          // 默认 15s
	MisfireGrace time.Duration          // 周期性：迟到超过这个就跳过本趟，默认 5m
	OneOffGrace  time.Duration          // 单次：超过预定时间但仍在宽限内可补跑，默认 24h
	MaxCatchUp   int                    // 单次 Tick 最多处理多少个过期点，默认 8
}

// Engine 是无模型的定时循环。
type Engine struct {
	Store *Store
	Host  Host
	Clock Clock
	Cfg   Config

	mu       sync.Mutex
	lastWall time.Time // 用于检测时钟回拨
	running  bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewEngine(store *Store, host Host, cfg Config) *Engine {
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = 15 * time.Second
	}
	if cfg.MisfireGrace <= 0 {
		cfg.MisfireGrace = 5 * time.Minute
	}
	if cfg.OneOffGrace <= 0 {
		cfg.OneOffGrace = 24 * time.Hour
	}
	if cfg.MaxCatchUp <= 0 {
		cfg.MaxCatchUp = 8
	}
	return &Engine{Store: store, Host: host, Clock: realClock{}, Cfg: cfg}
}

// Start 启动后台 Tick；可重复调用但只会跑一份。
func (e *Engine) Start(parent context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	e.cancel = cancel
	e.running = true
	e.lastWall = e.Clock.Now()
	e.recoverLocked()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.loop(ctx)
	}()
}

// Stop 优雅停止，等当前 Tick 结束。
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	cancel := e.cancel
	e.mu.Unlock()
	cancel()
	e.wg.Wait()
	e.mu.Lock()
	e.running = false
	e.mu.Unlock()
}

func (e *Engine) loop(ctx context.Context) {
	// 启动后立刻扫一次，再按节奏 Tick
	e.Tick()
	tk := time.NewTicker(e.Cfg.TickEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			e.Tick()
		}
	}
}

// recoverLocked 在启动时核对未完成认领：任务还在就保留；失踪就标 needs_verify，绝不自动重放。
func (e *Engine) recoverLocked() {
	for _, c := range e.Store.Claims() {
		if c.Status != "claimed" && c.Status != "dispatched" {
			continue
		}
		st := e.Host.Lookup(c.TaskID)
		switch st {
		case TaskOpen:
			if c.Status == "claimed" {
				_, _ = e.Store.UpdateClaim(c.Key, "dispatched", "重启后确认任务仍在")
			}
		case TaskCompleted, TaskFailed, TaskCancelled:
			_, _ = e.Store.UpdateClaim(c.Key, string(st), "重启后对账")
			e.refreshNext(c.PlanID)
		case TaskMissing, TaskUnknown:
			updated, _ := e.Store.UpdateClaim(c.Key, "needs_verify", "重启后找不到对应任务；请核实外部副作用，不要自动重放")
			if p, ok := e.Store.Get(c.PlanID); ok {
				e.Host.Notify("schedule.verify",
					fmt.Sprintf("定时计划「%s」有一次触发（%s）在重启后找不到任务 %s。请核实有没有实际执行过；系统不会自动重跑这次。",
						p.Title, c.IntendedAt.In(mustLoc(p.Spec.Timezone)).Format("01-02 15:04"), c.TaskID),
					p, &updated)
				e.Host.Emit("schedule.run", map[string]any{"plan": p.Public(), "claim": updated})
			}
			e.refreshNext(c.PlanID)
		}
	}
	for _, p := range e.Store.List() {
		e.refreshNext(p.ID)
	}
}

// Tick 扫描到期计划。可被测试直接调用。
func (e *Engine) Tick() {
	if e.Cfg.Enter != nil {
		release, err := e.Cfg.Enter()
		if err != nil {
			return
		}
		defer release()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.Clock.Now()
	// 时钟回拨：不触发，等到墙上时间重新超过上次观察值
	if !e.lastWall.IsZero() && now.Before(e.lastWall.Add(-time.Second)) {
		log.Printf("scheduler: 检测到时钟回拨（%s → %s），本轮不触发", e.lastWall.Format(time.RFC3339), now.Format(time.RFC3339))
		return
	}
	e.lastWall = now

	n := 0
	for _, p := range e.Store.List() {
		if !p.Enabled {
			continue
		}
		for n < e.Cfg.MaxCatchUp {
			fired, err := e.fireOne(p, now)
			if err != nil {
				log.Printf("scheduler: plan %s: %v", p.ID, err)
				break
			}
			if !fired {
				break
			}
			n++
			if np, ok := e.Store.Get(p.ID); ok {
				p = np
			} else {
				break
			}
			if !p.Enabled {
				break
			}
		}
	}
}

func (e *Engine) fireOne(p Plan, now time.Time) (bool, error) {
	// 用 Next(prev) 找"本该发生"的点：取 next(lastProcessed) ≤ now 的那个。
	// 简化：以 NextRun 为准；若为空则重算。
	var due time.Time
	if p.NextRun != nil {
		due = *p.NextRun
	} else {
		next, ok, err := p.Spec.Next(now.Add(-time.Millisecond))
		if err != nil || !ok {
			_ = e.Store.SetNext(p.ID, nil)
			return false, err
		}
		due = next
		_ = e.Store.SetNext(p.ID, &due)
	}
	if due.After(now) {
		return false, nil
	}

	late := now.Sub(due)
	key := OccurrenceKey(p.ID, due)
	taskID := TaskIDFor(key)

	// 迟到策略
	switch p.Spec.Kind {
	case Once:
		if late > e.Cfg.OneOffGrace {
			c := Claim{Key: key, PlanID: p.ID, TaskID: taskID, IntendedAt: due, ClaimedAt: now.UTC(), Status: "expired",
				Note: fmt.Sprintf("超过单次宽限 %s", e.Cfg.OneOffGrace)}
			if _, created, err := e.Store.TryClaim(c); err != nil {
				return false, err
			} else if created {
				e.Host.Notify("schedule.expired",
					fmt.Sprintf("单次计划「%s」预定 %s 已超过宽限，未执行。", p.Title, due.In(mustLoc(p.Spec.Timezone)).Format("01-02 15:04 MST")),
					p, &c)
				e.Host.Emit("schedule.run", map[string]any{"plan": p.Public(), "claim": c})
			}
			_ = e.Store.SetNext(p.ID, nil)
			// 过期后自动停用，避免反复扫
			p.Enabled = false
			p.UpdatedAt = now.UTC()
			_ = e.Store.Put(&p)
			e.Host.Emit("schedule.upserted", p.Public())
			return true, nil
		}
	default:
		if late > e.Cfg.MisfireGrace {
			c := Claim{Key: key, PlanID: p.ID, TaskID: taskID, IntendedAt: due, ClaimedAt: now.UTC(), Status: "skipped_misfire",
				Note: fmt.Sprintf("迟到 %s > 宽限 %s，跳过本趟", late.Round(time.Second), e.Cfg.MisfireGrace)}
			if _, created, err := e.Store.TryClaim(c); err != nil {
				return false, err
			} else if created {
				e.Host.Emit("schedule.run", map[string]any{"plan": p.Public(), "claim": c})
			}
			// 推进到 due 之后的下一次
			next, ok, err := p.Spec.Next(due)
			if err != nil {
				return false, err
			}
			if ok {
				_ = e.Store.SetNext(p.ID, &next)
			} else {
				_ = e.Store.SetNext(p.ID, nil)
			}
			e.emitPlan(p.ID)
			return true, nil
		}
	}

	// 重叠：上一趟还开着
	if p.Overlap == "" {
		p.Overlap = OverlapSkip
	}
	if p.Overlap == OverlapSkip {
		for _, oc := range e.Store.OpenClaimsFor(p.ID) {
			if oc.Key == key {
				continue
			}
			st := e.Host.Lookup(oc.TaskID)
			if st == TaskOpen || st == TaskUnknown {
				c := Claim{Key: key, PlanID: p.ID, TaskID: taskID, IntendedAt: due, ClaimedAt: now.UTC(), Status: "skipped_overlap",
					Note: "上一趟任务还在进行，按 overlap=skip 跳过"}
				if _, created, err := e.Store.TryClaim(c); err != nil {
					return false, err
				} else if created {
					e.Host.Emit("schedule.run", map[string]any{"plan": p.Public(), "claim": c})
				}
				e.advanceAfter(p, due)
				return true, nil
			}
		}
	}

	claim := Claim{Key: key, PlanID: p.ID, TaskID: taskID, IntendedAt: due, ClaimedAt: now.UTC(), Status: "claimed"}
	got, created, err := e.Store.TryClaim(claim)
	if err != nil {
		return false, err
	}
	if !created {
		// 已经认领过：推进 next，避免卡死
		e.advanceAfter(p, due)
		return got.Status == "claimed" || got.Status == "dispatched", nil
	}

	id, err := e.Host.Dispatch(p, got)
	if err != nil {
		_, _ = e.Store.UpdateClaim(got.Key, "failed", err.Error())
		e.Host.Notify("schedule.failed",
			fmt.Sprintf("定时计划「%s」到点了，但创建任务失败：%v", p.Title, err), p, &got)
		e.Host.Emit("schedule.run", map[string]any{"plan": p.Public(), "claim": got, "error": err.Error()})
		e.advanceAfter(p, due)
		return true, nil
	}
	got.TaskID = id
	updated, _ := e.Store.UpdateClaim(got.Key, "dispatched", "")
	e.Host.Emit("schedule.run", map[string]any{"plan": p.Public(), "claim": updated})
	e.advanceAfter(p, due)
	return true, nil
}

func (e *Engine) advanceAfter(p Plan, due time.Time) {
	switch p.Spec.Kind {
	case Once:
		_ = e.Store.SetNext(p.ID, nil)
		p.Enabled = false
		p.UpdatedAt = e.Clock.Now().UTC()
		if cur, ok := e.Store.Get(p.ID); ok {
			cur.Enabled = false
			cur.NextRun = nil
			cur.UpdatedAt = p.UpdatedAt
			_ = e.Store.Put(&cur)
			e.Host.Emit("schedule.upserted", cur.Public())
		}
	default:
		next, ok, err := p.Spec.Next(due)
		if err != nil || !ok {
			_ = e.Store.SetNext(p.ID, nil)
		} else {
			_ = e.Store.SetNext(p.ID, &next)
		}
		e.emitPlan(p.ID)
	}
}

func (e *Engine) refreshNext(id string) {
	p, ok := e.Store.Get(id)
	if !ok {
		return
	}
	if !p.Enabled {
		_ = e.Store.SetNext(id, nil)
		e.emitPlan(id)
		return
	}
	next, okNext, err := p.Spec.Next(e.Clock.Now())
	if err != nil || !okNext {
		_ = e.Store.SetNext(id, nil)
	} else {
		_ = e.Store.SetNext(id, &next)
	}
	e.emitPlan(id)
}

func (e *Engine) emitPlan(id string) {
	if p, ok := e.Store.Get(id); ok {
		e.Host.Emit("schedule.upserted", p.Public())
	}
}

// ---------- 计划 CRUD（供 bot 层调用；审批由 bot 层做） ----------

type Draft struct {
	Title   string
	Prompt  string
	Spec    Spec
	Overlap string
	Enabled bool
	ID      string // 更新时必填；创建时可空
}

func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "sch_" + hex.EncodeToString(b[:])
}

func (e *Engine) Upsert(d Draft) (Plan, error) {
	if d.Title == "" {
		return Plan{}, fmt.Errorf("需要标题")
	}
	if d.Prompt == "" {
		return Plan{}, fmt.Errorf("需要执行内容 prompt")
	}
	if d.Spec.Timezone == "" {
		d.Spec.Timezone = DefaultTimezone
	}
	if err := d.Spec.Validate(); err != nil {
		return Plan{}, err
	}
	if d.Overlap == "" {
		d.Overlap = OverlapSkip
	}
	if d.Overlap != OverlapSkip && d.Overlap != OverlapQueue {
		return Plan{}, fmt.Errorf("overlap 只能是 skip 或 queue")
	}
	now := e.Clock.Now().UTC()
	var p Plan
	if d.ID != "" {
		old, ok := e.Store.Get(d.ID)
		if !ok {
			return Plan{}, fmt.Errorf("没有计划 %s", d.ID)
		}
		p = old
		p.Title, p.Prompt, p.Spec, p.Overlap, p.Enabled = d.Title, d.Prompt, d.Spec, d.Overlap, d.Enabled
		p.UpdatedAt = now
	} else {
		p = Plan{
			ID: NewID(), Title: d.Title, Prompt: d.Prompt, Spec: d.Spec,
			Enabled: d.Enabled, Overlap: d.Overlap, CreatedAt: now, UpdatedAt: now,
		}
	}
	if p.Enabled {
		if next, ok, err := p.Spec.Next(e.Clock.Now()); err != nil {
			return Plan{}, err
		} else if ok {
			p.NextRun = &next
		} else {
			p.NextRun = nil
		}
	} else {
		p.NextRun = nil
	}
	if err := e.Store.Put(&p); err != nil {
		return Plan{}, err
	}
	e.Host.Emit("schedule.upserted", p.Public())
	return p, nil
}

func (e *Engine) SetEnabled(id string, enabled bool) (Plan, error) {
	p, ok := e.Store.Get(id)
	if !ok {
		return Plan{}, fmt.Errorf("没有计划 %s", id)
	}
	p.Enabled = enabled
	p.UpdatedAt = e.Clock.Now().UTC()
	if enabled {
		if next, ok, err := p.Spec.Next(e.Clock.Now()); err != nil {
			return Plan{}, err
		} else if ok {
			p.NextRun = &next
		} else {
			return Plan{}, fmt.Errorf("这个计划已经没有下一次了（单次已过期？）")
		}
	} else {
		p.NextRun = nil
	}
	if err := e.Store.Put(&p); err != nil {
		return Plan{}, err
	}
	e.Host.Emit("schedule.upserted", p.Public())
	return p, nil
}

func (e *Engine) Remove(id string) error {
	p, ok := e.Store.Get(id)
	if !ok {
		return fmt.Errorf("没有计划 %s", id)
	}
	if err := e.Store.Delete(id); err != nil {
		return err
	}
	e.Host.Emit("schedule.deleted", map[string]any{"id": id, "title": p.Title})
	return nil
}

// RunNow 立刻认领一次"手动触发"并派发（不消耗计划的正规 occurrence）。
func (e *Engine) RunNow(id string) (Claim, error) {
	p, ok := e.Store.Get(id)
	if !ok {
		return Claim{}, fmt.Errorf("没有计划 %s", id)
	}
	now := e.Clock.Now().UTC()
	key := OccurrenceKey(p.ID, now) + "/manual/" + NewID()
	taskID := TaskIDFor(key)
	c := Claim{Key: key, PlanID: p.ID, TaskID: taskID, IntendedAt: now, ClaimedAt: now, Status: "claimed", Note: "手动立即执行"}
	got, _, err := e.Store.TryClaim(c)
	if err != nil {
		return Claim{}, err
	}
	id2, err := e.Host.Dispatch(p, got)
	if err != nil {
		_, _ = e.Store.UpdateClaim(got.Key, "failed", err.Error())
		return got, err
	}
	got.TaskID = id2
	updated, _ := e.Store.UpdateClaim(got.Key, "dispatched", "manual")
	e.Host.Emit("schedule.run", map[string]any{"plan": p.Public(), "claim": updated, "manual": true})
	return updated, nil
}

// MarkTaskFinished 任务结束时由宿主调用，更新认领状态。
func (e *Engine) MarkTaskFinished(taskID, status string) {
	for _, c := range e.Store.Claims() {
		if c.TaskID != taskID {
			continue
		}
		if c.Status == "claimed" || c.Status == "dispatched" {
			_, _ = e.Store.UpdateClaim(c.Key, status, "")
			e.emitPlan(c.PlanID)
		}
	}
}
