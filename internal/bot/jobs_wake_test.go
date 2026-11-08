package bot

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"onefrog/internal/task"
	"onefrog/internal/workspace"
)

// TestNativeJobWakeRequiresBothAnchors 验证 waiting_job 任务挂在本地后台作业
// （JobRef "job:<id>"）上被唤醒时的回填规则：只有当 journal 里存在匹配的
// job_wait JournalDone 条目、且 checkpoint 里存在同一 call 的 tool 消息时，
// 真实结果（此处为作业状态丢失的「结果未知，未重放」）才回填并排队；任一
// 锚点缺失，任务保持 waiting_job 且内容完全不变。重复唤醒幂等。
func TestNativeJobWakeRequiresBothAnchors(t *testing.T) {
	now := time.Now().UTC()
	resultNeedle := "结果未知，未重放"

	// makeTask 构造一个 due 的 waiting_job 任务，checkpoint 里是
	// assistant(tool_calls c1) + tool(c1) 的配对批次。
	makeTask := func(withJournal, withToolMsg bool) task.Task {
		tk := task.Task{
			ID: "task-1", OwnerID: "owner-1", BotID: "bot-1", SessionID: "session-1",
			Goal: "等待后台作业结束", Status: task.StatusWaitingJob,
			Deadline: now.Add(time.Hour),
			Pending: &task.Pending{
				Kind: task.PendingJob, JobRef: "job:missing",
				WakeAt: now.Add(-time.Second), Turn: 1, CallID: "c1", ArgsHash: "h1",
			},
		}
		msgs := []map[string]any{
			{"role": "assistant", "tool_calls": []map[string]any{{"id": "c1"}}},
		}
		if withToolMsg {
			msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": "c1", "content": "已挂起"})
		}
		tk.Checkpoint = task.Checkpoint{Messages: msgs}
		if withJournal {
			tk.Journal = []task.JournalEntry{{
				Name: "job_wait", State: task.JournalDone,
				Turn: 1, CallID: "c1", ArgsHash: "h1", Result: "已挂起",
			}}
		}
		return tk
	}

	cases := []struct {
		name        string
		withJournal bool
		withToolMsg bool
	}{
		{name: "两个锚点齐全", withJournal: true, withToolMsg: true},
		{name: "缺 journal", withJournal: false, withToolMsg: true},
		{name: "缺 tool", withJournal: true, withToolMsg: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			book, err := task.New("", nil)
			if err != nil {
				t.Fatalf("task.New: %v", err)
			}
			if _, err := book.Create(makeTask(tc.withJournal, tc.withToolMsg)); err != nil {
				t.Fatalf("book.Create: %v", err)
			}
			before, err := book.Get("task-1")
			if err != nil {
				t.Fatalf("book.Get: %v", err)
			}
			runner := NewRunner(&Agent{Jobs: workspace.NewJobTable()}, book)

			runner.wakeDueJobs()

			after, err := book.Get("task-1")
			if err != nil {
				t.Fatalf("book.Get: %v", err)
			}
			complete := tc.withJournal && tc.withToolMsg
			if complete {
				if after.Status != task.StatusQueued {
					t.Fatalf("锚点齐全应排队，实际状态：%v", after.Status)
				}
				if after.Pending != nil {
					t.Fatalf("锚点齐全排队后 Pending 应为 nil")
				}
				if len(after.Journal) != 1 || !strings.Contains(after.Journal[0].Result, resultNeedle) {
					t.Fatalf("journal 回填应包含 %q，实际：%+v", resultNeedle, after.Journal)
				}
				var toolContent string
				for _, m := range after.Checkpoint.Messages {
					if r, _ := m["role"].(string); r == "tool" && m["tool_call_id"] == "c1" {
						toolContent, _ = m["content"].(string)
					}
				}
				if !strings.Contains(toolContent, resultNeedle) {
					t.Fatalf("checkpoint tool 消息回填应包含 %q，实际：%q", resultNeedle, toolContent)
				}
			} else {
				if after.Status != task.StatusWaitingJob {
					t.Fatalf("锚点缺失应保持 waiting_job，实际状态：%v", after.Status)
				}
				if !reflect.DeepEqual(before.Journal, after.Journal) {
					t.Fatalf("锚点缺失不应修改 journal\n之前：%+v\n之后：%+v", before.Journal, after.Journal)
				}
				if !reflect.DeepEqual(before.Checkpoint.Messages, after.Checkpoint.Messages) {
					t.Fatalf("锚点缺失不应修改 checkpoint 消息\n之前：%v\n之后：%v", before.Checkpoint.Messages, after.Checkpoint.Messages)
				}
				if !reflect.DeepEqual(before.Pending, after.Pending) {
					t.Fatalf("锚点缺失不应修改 Pending\n之前：%+v\n之后：%+v", before.Pending, after.Pending)
				}
			}

			// 重复唤醒必须幂等。
			runner.wakeDueJobs()
			again, err := book.Get("task-1")
			if err != nil {
				t.Fatalf("二次 Get: %v", err)
			}
			if !reflect.DeepEqual(after.Journal, again.Journal) {
				t.Fatalf("二次唤醒应幂等：journal 变化\n之前：%+v\n之后：%+v", after.Journal, again.Journal)
			}
			if !reflect.DeepEqual(after.Checkpoint.Messages, again.Checkpoint.Messages) {
				t.Fatalf("二次唤醒应幂等：checkpoint 消息变化\n之前：%v\n之后：%v", after.Checkpoint.Messages, again.Checkpoint.Messages)
			}
			if again.Status != after.Status || !reflect.DeepEqual(after.Pending, again.Pending) {
				t.Fatalf("二次唤醒应幂等：状态/Pending 变化 %v/%+v -> %v/%+v", after.Status, after.Pending, again.Status, again.Pending)
			}
		})
	}
}
