package onefrog

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// TaskInfo 是任务的当前状态（给聊天层的 task_status 用）。
type TaskInfo struct {
	ID        string    `json:"id"`
	BotID     string    `json:"bot_id,omitempty"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	Result    *Result   `json:"result,omitempty"`
}

// Manager 并发运行多个任务，带全局并发上限。Start 立刻返回任务 ID，不阻塞。
type Manager struct {
	Agent *Agent
	sem   chan struct{}
	mu    sync.Mutex
	tasks map[string]*managed
}

type managed struct {
	info   TaskInfo
	cancel context.CancelFunc
	done   chan struct{}
}

func NewManager(a *Agent, maxConcurrent int) *Manager {
	if maxConcurrent <= 0 {
		maxConcurrent = 8
	}
	return &Manager{Agent: a, sem: make(chan struct{}, maxConcurrent), tasks: map[string]*managed{}}
}

func (m *Manager) Start(t Task) (string, error) {
	return m.StartWithRelease(t, func() {})
}

// StartWithRelease transfers an acquired host lease to the task.
func (m *Manager) StartWithRelease(t Task, release func()) (string, error) {
	if t.ID == "" {
		t.ID = NewID("task")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &managed{info: TaskInfo{ID: t.ID, BotID: t.BotID, Status: StatusQueued, CreatedAt: time.Now()},
		cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if old, dup := m.tasks[t.ID]; dup {
		select {
		case <-old.done: // 已经结束（例如暂停了），允许用同一个 ID 恢复
		default:
			m.mu.Unlock()
			cancel()
			release()
			return "", fmt.Errorf("任务 %s 正在运行", t.ID)
		}
	}
	m.tasks[t.ID] = e
	m.mu.Unlock()

	go func() {
		defer close(e.done)
		defer release()
		defer cancel()
		select { // 超过并发上限就排队
		case m.sem <- struct{}{}:
			defer func() { <-m.sem }()
		case <-ctx.Done():
		}
		m.update(t.ID, func(i *TaskInfo) { i.Status, i.StartedAt = StatusRunning, time.Now() })
		res, _ := m.Agent.Run(ctx, t)
		m.update(t.ID, func(i *TaskInfo) { i.Status, i.EndedAt, i.Result = res.Status, time.Now(), &res })
	}()
	return t.ID, nil
}

func (m *Manager) update(id string, f func(*TaskInfo)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.tasks[id]; e != nil {
		f(&e.info)
	}
}

func (m *Manager) get(id string) (*managed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.tasks[id]; e != nil {
		return e, nil
	}
	return nil, fmt.Errorf("没有这个任务: %s", id)
}

func (m *Manager) Steer(id, msg string) error { return m.Agent.Steer(id, msg) }

// Pause 请求任务在下一个安全点暂停（见 Agent.Pause）。
func (m *Manager) Pause(id string) error {
	if _, err := m.get(id); err != nil {
		return err
	}
	m.Agent.Pause(id)
	return nil
}

func (m *Manager) Cancel(id string) error {
	e, err := m.get(id)
	if err == nil {
		e.cancel()
	}
	return err
}

func (m *Manager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.tasks {
		e.cancel()
	}
}

func (m *Manager) WaitAll() {
	m.mu.Lock()
	wait := make([]<-chan struct{}, 0, len(m.tasks))
	for _, e := range m.tasks {
		wait = append(wait, e.done)
	}
	m.mu.Unlock()
	for _, done := range wait {
		<-done
	}
}

func (m *Manager) Status(id string) (TaskInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := m.tasks[id]; e != nil {
		return e.info, nil
	}
	return TaskInfo{}, fmt.Errorf("没有这个任务: %s", id)
}

func (m *Manager) List() []TaskInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]TaskInfo, 0, len(m.tasks))
	for _, e := range m.tasks {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Wait 等任务结束并返回结果。
func (m *Manager) Wait(ctx context.Context, id string) (Result, error) {
	e, err := m.get(id)
	if err != nil {
		return Result{}, err
	}
	select {
	case <-e.done:
		info, _ := m.Status(id)
		return *info.Result, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}
