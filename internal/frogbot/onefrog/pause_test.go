package onefrog

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptProvider：第 n 次调用返回 replies[n]；block 为真时一直等到 ctx 取消。
type scriptProvider struct {
	mu      sync.Mutex
	replies []Message
	reqs    [][]Message
	block   map[int]bool
	entered chan int
}

func (p *scriptProvider) Chat(ctx context.Context, req ChatRequest, _ func(string)) (Message, error) {
	p.mu.Lock()
	n := len(p.reqs)
	p.reqs = append(p.reqs, append([]Message(nil), req.Messages...))
	p.mu.Unlock()
	if p.entered != nil {
		p.entered <- n
	}
	if p.block[n] {
		<-ctx.Done()
		return Message{}, ctx.Err()
	}
	if n >= len(p.replies) {
		return Message{Role: "assistant", Content: "完成"}, nil
	}
	return p.replies[n], nil
}

func checkpointOf(s *recSink) (msgs []Message, steps int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.ev {
		if e.Type == EvCheckpoint {
			d := e.Data.(map[string]any)
			msgs, steps = d["messages"].([]Message), d["steps"].(int)
		}
	}
	return
}

// 暂停发生在命令执行期间：命令一定跑完，结果进快照，然后在下一步之前暂停；恢复后接着跑。
func TestPauseAtSafePointAndResume(t *testing.T) {
	ws := t.TempDir()
	started := make(chan struct{})
	slow := FuncTool{ToolName: "slow", JSONSchema: `{"type":"object"}`, RiskFn: func(json.RawMessage) Risk { return RiskRead },
		Fn: func(ctx context.Context, _ ToolEnv, _ json.RawMessage) (string, error) {
			close(started)
			time.Sleep(150 * time.Millisecond)
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "slow done", nil
		}}
	p := &scriptProvider{replies: []Message{call("slow", map[string]string{}), call("slow", map[string]string{})}}
	sink := &recSink{}
	a := &Agent{Provider: p, Tools: NewRegistry(slow), Guard: newGuard(t, nil), Sink: sink}
	done := make(chan Result)
	go func() { r, _ := a.Run(context.Background(), Task{ID: "p1", Input: "做事", Workspace: ws}); done <- r }()
	<-started
	a.Pause("p1") // 工具正在执行
	r := <-done
	if r.Status != StatusPaused || r.Steps != 1 {
		t.Fatalf("want paused after 1 step, got %+v", r)
	}
	msgs, steps := checkpointOf(sink)
	if steps != 1 || len(msgs) != 3 || msgs[2].Role != "tool" || msgs[2].Content != "slow done" {
		t.Fatalf("checkpoint %d %+v", steps, msgs)
	}
	if len(p.reqs) != 1 {
		t.Fatalf("must not call the model again after pause, calls=%d", len(p.reqs))
	}
	// 恢复：从快照接着跑（不重新执行第一步）
	p.replies = []Message{{}, {Role: "assistant", Content: "全部完成"}}
	p.reqs = [][]Message{{}} // 让下一次调用的序号为 1
	sink2 := &recSink{}
	a.Sink = sink2
	r2, _ := a.Run(context.Background(), Task{ID: "p1", Input: "做事", Workspace: ws, Resume: msgs, StartStep: steps})
	if r2.Status != StatusCompleted || r2.Output != "全部完成" || r2.Steps != 2 {
		t.Fatalf("resume got %+v", r2)
	}
	got := p.reqs[1]
	if got[1].Content != "做事" || got[3].Content != "slow done" || !strings.Contains(got[len(got)-1].Content, "暂停") {
		t.Errorf("resume did not continue from checkpoint: %+v", got)
	}
}

// 暂停发生在等模型回复时：模型请求被中断（没有副作用），立刻暂停。
func TestPauseInterruptsModelCall(t *testing.T) {
	p := &scriptProvider{block: map[int]bool{0: true}, entered: make(chan int, 4)}
	a := &Agent{Provider: p, Guard: newGuard(t, nil), Sink: &recSink{}}
	done := make(chan Result)
	go func() {
		r, _ := a.Run(context.Background(), Task{ID: "p2", Input: "x", Workspace: t.TempDir()})
		done <- r
	}()
	<-p.entered
	a.Pause("p2")
	select {
	case r := <-done:
		if r.Status != StatusPaused || r.Steps != 0 {
			t.Fatalf("got %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pause did not interrupt the model call")
	}
}

// 任务还没开始就请求暂停：一开始就暂停，不调用模型。
func TestPauseBeforeStart(t *testing.T) {
	p := &scriptProvider{}
	a := &Agent{Provider: p, Guard: newGuard(t, nil)}
	a.Pause("p3")
	r, _ := a.Run(context.Background(), Task{ID: "p3", Input: "x", Workspace: t.TempDir()})
	if r.Status != StatusPaused || len(p.reqs) != 0 {
		t.Fatalf("got %+v calls=%d", r, len(p.reqs))
	}
}
