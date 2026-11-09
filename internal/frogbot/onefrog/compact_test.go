package onefrog

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func longHistory(steps int) []Message {
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "原始任务：整理日志"}}
	for i := 0; i < steps; i++ {
		msgs = append(msgs,
			Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Name: "shell", Args: []byte(`{"command":"cat big.log"}`)}}},
			Message{Role: "tool", ToolCallID: "c", Content: strings.Repeat("log line ", 2000)})
	}
	return msgs
}

func TestCompactorSummarizesOldTurnsAndKeepsPairs(t *testing.T) {
	msgs := longHistory(30)
	var got []Message
	c := &AutoCompactor{MaxTokens: 20000, KeepRecent: 5, Summarize: func(_ context.Context, old []Message) (string, error) {
		got = old
		return "已经看过 25 个日志文件", nil
	}}
	out, err := c.Compact(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if EstimateTokens(out) > 20000 {
		t.Fatalf("still too long: %d tokens", EstimateTokens(out))
	}
	if out[0].Content != "sys" || out[1].Content != "原始任务：整理日志" || !strings.Contains(out[2].Content, "已经看过 25 个日志文件") {
		t.Fatalf("head/summary wrong: %+v", out[:3])
	}
	if out[3].Role == "tool" {
		t.Fatal("kept tail starts with an orphan tool result")
	}
	for i, m := range out {
		if m.Role == "tool" && (i == 0 || len(out[i-1].ToolCalls) == 0) {
			t.Fatalf("tool result at %d not preceded by its call", i)
		}
	}
	if len(got) == 0 {
		t.Fatal("summarizer not called")
	}
	// 没超过上限时原样返回
	short := longHistory(1)
	if same, _ := c.Compact(context.Background(), short); len(same) != len(short) {
		t.Fatal("short history should not be compacted")
	}
}

func TestCompactorFallsBackWithoutModel(t *testing.T) {
	c := &AutoCompactor{MaxTokens: 20000, Summarize: func(context.Context, []Message) (string, error) {
		return "", errors.New("model down")
	}}
	out, err := c.Compact(context.Background(), longHistory(30))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out[2].Content, "调用 shell") || EstimateTokens(out) > 20000 {
		t.Fatalf("mechanical summary missing or too long: %d tokens", EstimateTokens(out))
	}
}

// 默认接在 agent 循环里：长任务每一步前都会检查，模型收到的对话不会无限变长。
func TestAgentLoopUsesCompactor(t *testing.T) {
	ws := t.TempDir()
	os.WriteFile(ws+"/big.txt", []byte(strings.Repeat("x", 40000)), 0o644)
	var replies []Message
	for i := 0; i < 12; i++ {
		replies = append(replies, call("read_file", map[string]string{"path": "big.txt"}))
	}
	replies = append(replies, Message{Role: "assistant", Content: "done"})
	fp := &fakeProvider{replies: replies}
	a := &Agent{Provider: fp, Guard: newGuard(t, nil), Compactor: &AutoCompactor{MaxTokens: 30000, KeepRecent: 4}}
	res, err := a.Run(context.Background(), Task{ID: "t", Input: "读很多次", Workspace: ws, Policy: PolicyNever})
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("%+v %v", res, err)
	}
	for i, r := range fp.reqs {
		if n := EstimateTokens(r.Messages); n > 30000 {
			t.Fatalf("request %d had %d tokens", i, n)
		}
	}
	last := fp.reqs[len(fp.reqs)-1].Messages
	if !strings.HasPrefix(last[2].Content, compactedPrefix) {
		t.Fatal("expected compacted summary in later requests")
	}
}
