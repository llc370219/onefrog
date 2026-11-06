// Package automation runs user-defined Bot tasks on a schedule, similar to
// recurring "tasks" in chat assistants: a saved prompt plus a schedule. Each run
// talks to the chosen Bot in a dedicated session and leaves a history entry.
package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"onefrog/internal/id"
	"onefrog/internal/maintenance"
	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

const (
	MaxItems      = 50
	MaxName       = 40
	MaxPrompt     = 4000
	MinInterval   = 15          // minutes
	MaxInterval   = 7 * 24 * 60 // minutes
	RunTimeout    = 10 * time.Minute
	maxParallel   = 2
	SessionPrefix = "auto-"
	summaryLimit  = 300
)

const (
	KindInterval = "interval"
	KindDaily    = "daily"
	KindWeekly   = "weekly"
	KindOnce     = "once"
)

const (
	StatusRunning = "running"
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

var (
	ErrRunning = errors.New("任务正在运行")
	ErrFull    = fmt.Errorf("自动化任务最多 %d 个", MaxItems)
)

// ValidationError marks bad user input so the API can answer 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// Schedule describes when an automation fires. Time and Weekday use the
// server's local time zone.
type Schedule struct {
	Kind    string     `json:"kind"`
	Minutes int        `json:"minutes,omitempty"`
	Time    string     `json:"time,omitempty"`
	Weekday int        `json:"weekday,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

type Automation struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Prompt       string     `json:"prompt"`
	BotID        string     `json:"bot_id"`
	ServerID     string     `json:"server_id"`
	Schedule     Schedule   `json:"schedule"`
	ApprovalMode string     `json:"approval_mode"`
	Enabled      bool       `json:"enabled"`
	OwnerID      string     `json:"owner_id"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	LastStatus   string     `json:"last_status,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	LastSummary  string     `json:"last_summary,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// Runner performs one run and returns the session used and the Bot's reply.
type Runner func(ctx context.Context, it Automation) (sessionID, text string, err error)

// SessionID is the chat session an automation writes into.
func SessionID(it Automation) string { return SessionPrefix + it.ID + "-" + it.BotID }

// IsSession reports whether a session id belongs to an automation.
func IsSession(id string) bool { return strings.HasPrefix(id, SessionPrefix) }

type bookFile struct {
	Items []Automation `json:"items"`
}

// Book stores automations (encrypted on disk) and runs them when due.
type Book struct {
	Gate  *maintenance.Gate
	path  string
	box   *secret.Box
	store store.Store

	mu      sync.Mutex
	items   map[string]Automation
	running map[string]bool
	runner  Runner
	base    context.Context

	slots chan struct{}
	wake  chan struct{}
	wg    sync.WaitGroup
	now   func() time.Time
	loc   *time.Location
}

// New loads the book from path. An empty path or nil box keeps it in memory.
func New(path string, box *secret.Box, st store.Store) (*Book, error) {
	b := &Book{
		path: path, box: box, store: st,
		items: map[string]Automation{}, running: map[string]bool{},
		base:  context.Background(),
		slots: make(chan struct{}, maxParallel), wake: make(chan struct{}, 1),
		now: func() time.Time { return time.Now().UTC() }, loc: time.Local,
	}
	if path == "" || box == nil {
		return b, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := box.Decrypt(string(raw))
	if err != nil {
		return nil, err
	}
	var data bookFile
	if err = json.Unmarshal([]byte(plain), &data); err != nil {
		return nil, err
	}
	for _, it := range data.Items {
		if it.ID == "" {
			continue
		}
		if it.LastStatus == StatusRunning {
			it.LastStatus = StatusFailed
			it.LastError = "服务重启，运行被中断"
		}
		b.items[it.ID] = it
	}
	return b, nil
}

// SetRunner installs the function that talks to the Bot.
func (b *Book) SetRunner(r Runner) {
	b.mu.Lock()
	b.runner = r
	b.mu.Unlock()
}

// Wake makes the scheduler re-check due items without blocking.
func (b *Book) Wake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *Book) List() []Automation {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Automation, 0, len(b.items))
	for _, it := range b.items {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (b *Book) Get(id string) (Automation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return Automation{}, store.ErrNotFound
	}
	return it, nil
}

func (b *Book) Create(in Automation, ownerID string) (Automation, error) {
	now := b.now()
	it, err := clean(in, now)
	if err != nil {
		return Automation{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= MaxItems {
		return Automation{}, ErrFull
	}
	it.ID = id.New()
	it.OwnerID = ownerID
	it.CreatedAt, it.UpdatedAt = now, now
	it.LastStatus, it.LastError, it.LastSummary, it.SessionID, it.LastRunAt = "", "", "", "", nil
	it.NextRunAt = b.nextFor(it, now)
	b.items[it.ID] = it
	if err = b.saveLocked(); err != nil {
		delete(b.items, it.ID)
		return Automation{}, err
	}
	b.Wake()
	return it, nil
}

func (b *Book) Update(id string, in Automation) (Automation, error) {
	now := b.now()
	c, err := clean(in, now)
	if err != nil {
		return Automation{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.items[id]
	if !ok {
		return Automation{}, store.ErrNotFound
	}
	it := old
	it.Name, it.Prompt, it.BotID, it.ServerID = c.Name, c.Prompt, c.BotID, c.ServerID
	it.Schedule, it.ApprovalMode, it.Enabled = c.Schedule, c.ApprovalMode, c.Enabled
	it.UpdatedAt = now
	it.NextRunAt = b.nextFor(it, now)
	b.items[id] = it
	if err = b.saveLocked(); err != nil {
		b.items[id] = old
		return Automation{}, err
	}
	b.Wake()
	return it, nil
}

func (b *Book) Delete(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.items[id]
	if !ok {
		return store.ErrNotFound
	}
	delete(b.items, id)
	if err := b.saveLocked(); err != nil {
		b.items[id] = old
		return err
	}
	return nil
}

// ForgetServer detaches automations from a deleted server.
func (b *Book) ForgetServer(serverID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	dirty := false
	for k, it := range b.items {
		if it.ServerID == serverID {
			it.ServerID = ""
			b.items[k] = it
			dirty = true
		}
	}
	if !dirty {
		return nil
	}
	return b.saveLocked()
}

// RunNow starts one run immediately, regardless of the schedule.
func (b *Book) RunNow(id string) (Automation, error) {
	if b.Gate.Paused() {
		return Automation{}, maintenance.ErrPaused
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	it, ok := b.items[id]
	if !ok {
		return Automation{}, store.ErrNotFound
	}
	if b.running[id] {
		return Automation{}, ErrRunning
	}
	return b.startLocked(it, false), nil
}

// Run dispatches due automations until ctx ends, then waits for running ones.
func (b *Book) Run(ctx context.Context) {
	b.mu.Lock()
	b.base = ctx
	b.mu.Unlock()
	defer b.wg.Wait()
	for {
		wait := b.dispatchDue()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-b.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (b *Book) dispatchDue() time.Duration {
	done, err := b.Gate.Enter()
	if err != nil {
		return time.Second
	}
	defer done()
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	wait := time.Minute
	for _, it := range b.items {
		if !it.Enabled || it.NextRunAt == nil {
			continue
		}
		if !it.NextRunAt.After(now) {
			if b.running[it.ID] {
				// Still busy with the previous run: skip this slot.
				it.NextRunAt = b.nextFor(it, now)
				b.items[it.ID] = it
				_ = b.saveLocked()
			} else {
				it = b.startLocked(it, true)
			}
		}
		if it.NextRunAt != nil {
			if d := it.NextRunAt.Sub(now); d < wait {
				wait = d
			}
		}
	}
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}

func (b *Book) startLocked(it Automation, scheduled bool) Automation {
	done, err := b.Gate.Enter()
	if err != nil {
		return it
	}
	now := b.now()
	b.running[it.ID] = true
	it.LastStatus = StatusRunning
	it.LastError = ""
	it.LastRunAt = &now
	if scheduled || it.Schedule.Kind != KindOnce {
		it.NextRunAt = b.nextFor(it, now)
	}
	b.items[it.ID] = it
	if err := b.saveLocked(); err != nil {
		log.Printf("automation: save: %v", err)
	}
	b.wg.Add(1)
	go func() { defer done(); b.execute(b.base, it) }()
	return it
}

func (b *Book) execute(parent context.Context, it Automation) {
	defer b.wg.Done()
	var (
		sid, out string
		err      error
	)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("运行异常：%v", r)
		}
		b.finish(it, sid, out, err)
	}()
	select {
	case b.slots <- struct{}{}:
	case <-parent.Done():
		err = errors.New("服务正在停止")
		return
	}
	defer func() { <-b.slots }()
	b.mu.Lock()
	runner := b.runner
	b.mu.Unlock()
	if runner == nil {
		err = errors.New("Bot 服务未配置")
		return
	}
	ctx, cancel := context.WithTimeout(parent, RunTimeout)
	defer cancel()
	sid, out, err = runner(ctx, it)
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = errors.New("运行超时")
	}
}

func (b *Book) finish(ran Automation, sid, out string, runErr error) {
	status, detail := StatusSuccess, strings.TrimSpace(out)
	if runErr != nil {
		status, detail = StatusFailed, runErr.Error()
	}
	if sid == "" {
		sid = SessionID(ran)
	}
	b.mu.Lock()
	delete(b.running, ran.ID)
	it, ok := b.items[ran.ID]
	if ok {
		it.LastStatus = status
		if runErr != nil {
			it.LastError = clip(detail, summaryLimit)
		} else {
			it.LastError = ""
			it.LastSummary = clip(detail, summaryLimit)
		}
		it.SessionID = sid
		if it.Schedule.Kind == KindOnce && it.NextRunAt == nil {
			it.Enabled = false
		}
		b.items[it.ID] = it
		if err := b.saveLocked(); err != nil {
			log.Printf("automation: save: %v", err)
		}
	}
	st := b.store
	b.mu.Unlock()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := model.History{BotID: ran.BotID, ServerID: ran.ServerID, Action: "自动化 " + ran.Name, Command: "自动化任务：" + ran.Name + "\n" + clip(detail, 2000), Status: status}
	if bot, err := st.GetBot(ctx, ran.BotID); err == nil {
		h.BotName = bot.Name
	}
	if ran.ServerID != "" {
		if rec, err := st.GetServer(ctx, ran.ServerID); err == nil {
			h.ServerName = rec.Name
		}
	}
	if _, err := st.AddHistory(ctx, store.HistoryRec{History: h}); err != nil {
		log.Printf("automation: history: %v", err)
	}
}

func (b *Book) nextFor(it Automation, now time.Time) *time.Time {
	if !it.Enabled {
		return nil
	}
	return Next(it.Schedule, now, b.loc)
}

// Next returns the first fire time strictly after "after", or nil if none.
func Next(s Schedule, after time.Time, loc *time.Location) *time.Time {
	if loc == nil {
		loc = time.Local
	}
	var t time.Time
	switch s.Kind {
	case KindInterval:
		if s.Minutes <= 0 {
			return nil
		}
		t = after.Add(time.Duration(s.Minutes) * time.Minute)
	case KindDaily, KindWeekly:
		h, m, ok := parseClock(s.Time)
		if !ok {
			return nil
		}
		local := after.In(loc)
		t = time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, loc)
		step := 1
		if s.Kind == KindWeekly {
			step = 7
			t = t.AddDate(0, 0, (s.Weekday-int(t.Weekday())+7)%7)
		}
		for !t.After(after) {
			t = t.AddDate(0, 0, step)
		}
	case KindOnce:
		if s.At == nil || !s.At.After(after) {
			return nil
		}
		t = *s.At
	default:
		return nil
	}
	t = t.UTC()
	return &t
}

func parseClock(v string) (int, int, bool) {
	hs, ms, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func clean(in Automation, now time.Time) (Automation, error) {
	out := Automation{
		Name:         strings.TrimSpace(in.Name),
		Prompt:       strings.TrimSpace(in.Prompt),
		BotID:        strings.TrimSpace(in.BotID),
		ServerID:     strings.TrimSpace(in.ServerID),
		ApprovalMode: strings.TrimSpace(in.ApprovalMode),
		Enabled:      in.Enabled,
	}
	if n := utf8.RuneCountInString(out.Name); n == 0 || n > MaxName {
		return out, invalid("名称需要 1-%d 个字", MaxName)
	}
	for _, r := range out.Name {
		if unicode.IsControl(r) {
			return out, invalid("名称不能包含控制字符")
		}
	}
	if n := utf8.RuneCountInString(out.Prompt); n == 0 || n > MaxPrompt {
		return out, invalid("任务内容需要 1-%d 个字", MaxPrompt)
	}
	if out.BotID == "" {
		return out, invalid("请选择 Bot")
	}
	switch out.ApprovalMode {
	case "", "smart", "model", "plan":
	default:
		return out, invalid("审批模式无效")
	}
	s := Schedule{Kind: strings.TrimSpace(in.Schedule.Kind)}
	switch s.Kind {
	case KindInterval:
		if in.Schedule.Minutes < MinInterval || in.Schedule.Minutes > MaxInterval {
			return out, invalid("间隔需要 %d 分钟到 7 天", MinInterval)
		}
		s.Minutes = in.Schedule.Minutes
	case KindDaily, KindWeekly:
		h, m, ok := parseClock(in.Schedule.Time)
		if !ok {
			return out, invalid("时间格式应为 HH:MM")
		}
		s.Time = fmt.Sprintf("%02d:%02d", h, m)
		if s.Kind == KindWeekly {
			if in.Schedule.Weekday < 0 || in.Schedule.Weekday > 6 {
				return out, invalid("星期无效")
			}
			s.Weekday = in.Schedule.Weekday
		}
	case KindOnce:
		if in.Schedule.At == nil || in.Schedule.At.IsZero() {
			return out, invalid("请选择运行时间")
		}
		at := in.Schedule.At.UTC().Truncate(time.Minute)
		if out.Enabled && !at.After(now) {
			return out, invalid("运行时间需要晚于现在")
		}
		s.At = &at
	default:
		return out, invalid("计划类型无效")
	}
	out.Schedule = s
	return out, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func (b *Book) saveLocked() error {
	if b.path == "" || b.box == nil {
		return nil
	}
	data := bookFile{Items: make([]Automation, 0, len(b.items))}
	for _, it := range b.items {
		data.Items = append(data.Items, it)
	}
	sort.Slice(data.Items, func(i, j int) bool { return data.Items[i].ID < data.Items[j].ID })
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	enc, err := b.box.Encrypt(string(raw))
	if err != nil {
		return err
	}
	dir := filepath.Dir(b.path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".automations-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.WriteString(enc); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, b.path)
}
