package onefrog

import (
	"context"
	"fmt"
	"sync"
)

// MemStore 是内存版 Store，适合演示和测试。Onefrog 里换成 Postgres。
type MemStore struct {
	mu        sync.Mutex
	tasks     map[string]Task
	events    map[string][]Event
	approvals []ApprovalRecord
}

func NewMemStore() *MemStore {
	return &MemStore{tasks: map[string]Task{}, events: map[string][]Event{}}
}

func (s *MemStore) SaveTask(_ context.Context, t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = t
	return nil
}

func (s *MemStore) LoadTask(_ context.Context, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return t, fmt.Errorf("没有这个任务: %s", id)
	}
	return t, nil
}

func (s *MemStore) SaveEvent(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[e.TaskID] = append(s.events[e.TaskID], e)
	return nil
}

func (s *MemStore) Events(_ context.Context, taskID string, after int64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for _, e := range s.events[taskID] {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *MemStore) SaveApproval(_ context.Context, r ApprovalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approvals = append(s.approvals, r)
	return nil
}

// Hub 把事件实时分发给订阅者（SSE 连接、监工等）。订阅 "" 表示所有任务。
// 慢的订阅者会丢事件，客户端应按 Seq 从 Store 补齐（断线重连同理）。
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan Event]struct{}{}} }

func (h *Hub) Emit(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, key := range []string{e.TaskID, ""} {
		for ch := range h.subs[key] {
			select {
			case ch <- e:
			default:
			}
		}
	}
}

func (h *Hub) Subscribe(taskID string) (<-chan Event, func()) {
	ch := make(chan Event, 256)
	h.mu.Lock()
	if h.subs[taskID] == nil {
		h.subs[taskID] = map[chan Event]struct{}{}
	}
	h.subs[taskID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs[taskID], ch)
		h.mu.Unlock()
	}
}
