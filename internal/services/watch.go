package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

const (
	DefaultInterval = 60
	MinInterval     = 30
	MaxInterval     = 3600
	maxTargets      = 100
	maxTargetName   = 800
)

// Watch states. Unknown means the probe could not decide (SSH failure,
// unavailable source, or a truncated list that may hide the target).
const (
	StatePending = "pending"
	StateOK      = "ok"
	StateFault   = "fault"
	StateMissing = "missing"
	StateUnknown = "unknown"
)

// Target selects one inventory entry. For ports the name is the listen
// address (Item.Ports), e.g. "0.0.0.0:80".
type Target struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type WatchConfig struct {
	IntervalSeconds int                 `json:"interval_seconds"`
	Servers         map[string][]Target `json:"servers"`
}

type WatchStatus struct {
	ServerID  string     `json:"server_id"`
	Kind      string     `json:"kind"`
	Name      string     `json:"name"`
	State     string     `json:"state"`
	Detail    string     `json:"detail"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	ChangedAt *time.Time `json:"changed_at,omitempty"`
}

type watchEntry struct {
	WatchStatus
	Alerted bool `json:"alerted"`
}

type watchFile struct {
	Config  WatchConfig  `json:"config"`
	Entries []watchEntry `json:"entries"`
}

type watchKey struct{ server, kind, name string }

// Watch polls only the services the user selected and records state changes.
type Watch struct {
	path    string
	box     *secret.Box
	store   store.Store
	mu      sync.Mutex
	scanner *Scanner
	cfg     WatchConfig
	entries map[watchKey]watchEntry
	wake    chan struct{}
	now     func() time.Time
}

func NewWatch(path string, box *secret.Box, st store.Store) (*Watch, error) {
	w := &Watch{path: path, box: box, store: st, cfg: WatchConfig{IntervalSeconds: DefaultInterval, Servers: map[string][]Target{}}, entries: map[watchKey]watchEntry{}, wake: make(chan struct{}, 1), now: func() time.Time { return time.Now().UTC() }}
	if path == "" || box == nil {
		return w, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := box.Decrypt(string(raw))
	if err != nil {
		return nil, err
	}
	var data watchFile
	if err = json.Unmarshal([]byte(plain), &data); err != nil {
		return nil, err
	}
	cfg, err := normalize(data.Config)
	if err != nil {
		return nil, err
	}
	w.cfg = cfg
	wanted := cfg.keys()
	for _, e := range data.Entries {
		k := watchKey{e.ServerID, e.Kind, e.Name}
		if wanted[k] {
			w.entries[k] = e
		}
	}
	return w, nil
}

// Attach shares the inventory scanner so manual and scheduled scans reuse one cache.
func (w *Watch) Attach(s *Scanner) {
	w.mu.Lock()
	w.scanner = s
	w.mu.Unlock()
}

func (c WatchConfig) keys() map[watchKey]bool {
	out := map[watchKey]bool{}
	for id, list := range c.Servers {
		for _, t := range list {
			out[watchKey{id, t.Kind, t.Name}] = true
		}
	}
	return out
}

func cleanTarget(t Target) (Target, error) {
	t.Kind = strings.TrimSpace(t.Kind)
	t.Name = strings.TrimSpace(t.Name)
	if t.Kind != "systemd" && t.Kind != "docker" && t.Kind != "ports" {
		return t, errors.New("监控类型无效")
	}
	if t.Name == "" || len(t.Name) > maxTargetName {
		return t, errors.New("监控项名称为空或过长")
	}
	for _, r := range t.Name {
		if unicode.IsControl(r) {
			return t, errors.New("监控项名称包含控制字符")
		}
	}
	return t, nil
}

func normalize(in WatchConfig) (WatchConfig, error) {
	out := WatchConfig{IntervalSeconds: in.IntervalSeconds, Servers: map[string][]Target{}}
	if out.IntervalSeconds == 0 {
		out.IntervalSeconds = DefaultInterval
	}
	if out.IntervalSeconds < MinInterval || out.IntervalSeconds > MaxInterval {
		return out, fmt.Errorf("轮询间隔需在 %d 到 %d 秒之间", MinInterval, MaxInterval)
	}
	for id, list := range in.Servers {
		id = strings.TrimSpace(id)
		if id == "" {
			return out, errors.New("服务器 ID 不能为空")
		}
		seen := map[Target]bool{}
		clean := make([]Target, 0, len(list))
		for _, raw := range list {
			t, err := cleanTarget(raw)
			if err != nil {
				return out, err
			}
			if seen[t] {
				continue
			}
			seen[t] = true
			clean = append(clean, t)
		}
		if len(clean) > maxTargets {
			return out, fmt.Errorf("每台服务器最多监控 %d 项", maxTargets)
		}
		if len(clean) == 0 {
			continue
		}
		sort.Slice(clean, func(i, j int) bool {
			if clean[i].Kind != clean[j].Kind {
				return clean[i].Kind < clean[j].Kind
			}
			return clean[i].Name < clean[j].Name
		})
		out.Servers[id] = append(out.Servers[id], clean...)
	}
	return out, nil
}

func copyConfig(c WatchConfig) WatchConfig {
	out := WatchConfig{IntervalSeconds: c.IntervalSeconds, Servers: map[string][]Target{}}
	for id, list := range c.Servers {
		out.Servers[id] = append([]Target{}, list...)
	}
	return out
}

func copyStamp(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

// Snapshot returns the config and one status per selected target, hiding deleted servers.
func (w *Watch) Snapshot(ctx context.Context) (WatchConfig, []WatchStatus, error) {
	w.mu.Lock()
	cfg := copyConfig(w.cfg)
	entries := make(map[watchKey]watchEntry, len(w.entries))
	for k, e := range w.entries {
		entries[k] = e
	}
	w.mu.Unlock()
	out := []WatchStatus{}
	for id, list := range cfg.Servers {
		if _, err := w.store.GetServer(ctx, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				delete(cfg.Servers, id)
				continue
			}
			return cfg, nil, err
		}
		for _, t := range list {
			e, ok := entries[watchKey{id, t.Kind, t.Name}]
			st := e.WatchStatus
			if !ok {
				st = WatchStatus{ServerID: id, Kind: t.Kind, Name: t.Name, State: StatePending, Detail: "等待首次检测"}
			}
			st.CheckedAt, st.ChangedAt = copyStamp(st.CheckedAt), copyStamp(st.ChangedAt)
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.ServerID != b.ServerID {
			return a.ServerID < b.ServerID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return cfg, out, nil
}

// Set replaces the whole selection after validating every server ID.
func (w *Watch) Set(ctx context.Context, in WatchConfig) error {
	cfg, err := normalize(in)
	if err != nil {
		return err
	}
	for id := range cfg.Servers {
		if _, err := w.store.GetServer(ctx, id); err != nil {
			return err
		}
	}
	w.mu.Lock()
	oldCfg, oldEntries := w.cfg, w.entries
	w.cfg = cfg
	wanted := cfg.keys()
	w.entries = map[watchKey]watchEntry{}
	for k, e := range oldEntries {
		if wanted[k] {
			w.entries[k] = e
		}
	}
	if err = w.saveLocked(); err != nil {
		w.cfg, w.entries = oldCfg, oldEntries
	}
	w.mu.Unlock()
	if err == nil {
		w.Wake()
	}
	return err
}

// Wake triggers an immediate poll without blocking.
func (w *Watch) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Remove drops a deleted server's selection and status.
func (w *Watch) Remove(serverID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.cfg.Servers[serverID]; !ok {
		return nil
	}
	delete(w.cfg.Servers, serverID)
	for k := range w.entries {
		if k.server == serverID {
			delete(w.entries, k)
		}
	}
	return w.saveLocked()
}

// Run polls until ctx ends. Saving the selection wakes it immediately.
func (w *Watch) Run(ctx context.Context) {
	for {
		w.Poll(ctx)
		w.mu.Lock()
		every := time.Duration(w.cfg.IntervalSeconds) * time.Second
		w.mu.Unlock()
		timer := time.NewTimer(every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// Poll checks every selected server once. The scanner caps SSH concurrency.
func (w *Watch) Poll(ctx context.Context) {
	w.mu.Lock()
	scanner := w.scanner
	ids := make([]string, 0, len(w.cfg.Servers))
	for id := range w.cfg.Servers {
		ids = append(ids, id)
	}
	w.mu.Unlock()
	if scanner == nil || len(ids) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			w.pollServer(ctx, scanner, id)
		}(id)
	}
	wg.Wait()
}

func (w *Watch) pollServer(ctx context.Context, scanner *Scanner, id string) {
	rec, err := w.store.GetServer(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		_ = w.Remove(id)
		return
	}
	if err != nil {
		return
	}
	view, err := scanner.Scan(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		_ = w.Remove(id)
		return
	}
	if err != nil {
		return
	}
	now := w.now()
	var notes []model.History
	w.mu.Lock()
	dirty := false
	for _, t := range w.cfg.Servers[id] {
		k := watchKey{id, t.Kind, t.Name}
		state, detail := Evaluate(view, t)
		e, ok := w.entries[k]
		if !ok {
			e = watchEntry{WatchStatus: WatchStatus{ServerID: id, Kind: t.Kind, Name: t.Name}}
		}
		stamp := now
		e.CheckedAt = &stamp
		if e.State != state || e.Detail != detail {
			if e.State != state {
				changed := now
				e.ChangedAt = &changed
			}
			dirty = true
		}
		e.State, e.Detail = state, detail
		label := t.Kind + " " + t.Name
		switch state {
		case StateFault, StateMissing:
			if !e.Alerted {
				e.Alerted = true
				dirty = true
				notes = append(notes, model.History{ServerID: id, ServerName: rec.Name, Action: "服务监控异常：" + label + "（" + detail + "）", Command: "service-watch " + label, Status: "failed"})
			}
		case StateOK:
			if e.Alerted {
				e.Alerted = false
				dirty = true
				notes = append(notes, model.History{ServerID: id, ServerName: rec.Name, Action: "服务监控恢复：" + label, Command: "service-watch " + label, Status: "success"})
			}
		}
		w.entries[k] = e
	}
	if dirty {
		_ = w.saveLocked()
	}
	w.mu.Unlock()
	for _, h := range notes {
		_, _ = w.store.AddHistory(ctx, store.HistoryRec{History: h})
	}
}

func healthy(it Item) bool {
	switch it.State {
	case "active", "running", "listening":
		return !strings.Contains(it.Detail, "(unhealthy)")
	}
	return false
}

func describe(it Item) string {
	parts := []string{}
	for _, s := range []string{it.State, it.Detail} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	if it.Kind == "ports" && strings.TrimSpace(it.Name) != "" {
		parts = append(parts, it.Name)
	}
	return strings.Join(parts, " · ")
}

// Evaluate maps one inventory view to the state of a single target.
func Evaluate(v View, t Target) (string, string) {
	if v.Status == "failed" || v.CollectedAt == nil {
		msg := strings.TrimSpace(v.Error)
		if msg == "" {
			msg = "检测失败"
		}
		return StateUnknown, msg
	}
	source, ok := v.Sources[t.Kind]
	if !ok || source.Status != "ok" {
		return StateUnknown, "采集来源不可用"
	}
	var bad *Item
	for i := range v.Items {
		it := v.Items[i]
		if it.Kind != t.Kind {
			continue
		}
		if (t.Kind == "ports" && it.Ports != t.Name) || (t.Kind != "ports" && it.Name != t.Name) {
			continue
		}
		if healthy(it) {
			return StateOK, describe(it)
		}
		if bad == nil {
			bad = &it
		}
	}
	if bad != nil {
		return StateFault, describe(*bad)
	}
	if source.Truncated {
		return StateUnknown, "服务列表已截断，无法确认"
	}
	return StateMissing, "未找到该服务"
}

func (w *Watch) saveLocked() error {
	if w.path == "" || w.box == nil {
		return nil
	}
	data := watchFile{Config: w.cfg, Entries: make([]watchEntry, 0, len(w.entries))}
	for _, e := range w.entries {
		data.Entries = append(data.Entries, e)
	}
	sort.Slice(data.Entries, func(i, j int) bool {
		a, b := data.Entries[i], data.Entries[j]
		if a.ServerID != b.ServerID {
			return a.ServerID < b.ServerID
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	enc, err := w.box.Encrypt(string(raw))
	if err != nil {
		return err
	}
	dir := filepath.Dir(w.path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".servicewatch-*")
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
	return os.Rename(name, w.path)
}
