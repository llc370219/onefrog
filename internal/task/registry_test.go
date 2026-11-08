package task

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onefrog/internal/secret"
)

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func put(t *testing.T, b *Book, id string, mutate func(*Task)) Task {
	t.Helper()
	now := time.Now().UTC()
	it := Task{ID: id, OwnerID: "u1", BotID: "main", SessionID: "s1", Goal: "检查磁盘", Status: StatusQueued, CreatedAt: now, UpdatedAt: now}
	if mutate != nil {
		mutate(&it)
	}
	created, err := b.Create(it)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestRegistryPersistsEncryptedAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	box := testBox(t)
	b, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	put(t, b, "t1", func(it *Task) { it.ProjectID, it.ServerID = "s1", "web" })

	raw, err := readFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "检查磁盘") {
		t.Fatal("task file is not encrypted")
	}
	again, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Get("t1")
	if err != nil || got.Goal != "检查磁盘" || got.ProjectID != "s1" || got.Status != StatusQueued {
		t.Fatalf("reload %+v %v", got, err)
	}
	if _, err := New(path, testBoxOther(t)); err == nil {
		t.Fatal("wrong key accepted")
	}
}

func testBoxOther(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func readFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	return string(raw), err
}

func TestUpdateFailureRollsBack(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	addJournal(t, b, "t1", "c1", JournalDone, "ok")
	before, _ := b.Get("t1")
	if _, err := b.Update("t1", func(it *Task) error {
		it.Status = StatusCompleted
		return ErrInvalid
	}); err == nil {
		t.Fatal("expected update error")
	}
	after, _ := b.Get("t1")
	if after.Status != before.Status || len(after.Journal) != len(before.Journal) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("failed update left changes: %+v", after)
	}
	if _, err := b.Update("t1", func(it *Task) error { it.Status = "bogus"; return nil }); err == nil {
		t.Fatal("invalid status accepted")
	}
}

func TestPlaintextSecretAnswerRejected(t *testing.T) {
	b, _ := New("", testBox(t))
	if _, err := b.Create(Task{ID: "t1", OwnerID: "u1", BotID: "main", Goal: "x", OptionsJSON: `{"secret_answer":true}`}); err == nil {
		t.Fatal("secret answer persisted")
	}
}

func TestRetentionDropsOldestFinished(t *testing.T) {
	b, _ := New("", testBox(t))
	for i := 0; i < MaxTasks; i++ {
		put(t, b, "old-"+string(rune('a'+i%26))+string(rune('a'+i/26)), func(it *Task) { it.Status = StatusCompleted })
	}
	put(t, b, "live", nil)
	put(t, b, "live2", nil)
	if len(b.List("")) > MaxTasks {
		t.Fatalf("retention failed: %d", len(b.List("")))
	}
	if _, err := b.Get("live2"); err != nil {
		t.Fatal("new task was dropped")
	}
}

func TestRunningTaskRecoversAsInterruptedNotReplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	box := testBox(t)
	b, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	put(t, b, "t1", func(it *Task) { it.Status = StatusRunning })
	addJournal(t, b, "t1", "c1", JournalDone, "df output")
	addJournal(t, b, "t1", "c2", JournalStarted, "")
	if _, err = b.Update("t1", func(it *Task) error {
		it.Pending = &Pending{CallID: "c2", Name: "run_command", Kind: PendingTool}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	again, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Get("t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusPaused || got.Pending != nil {
		t.Fatalf("not paused for reconcile: %+v", got)
	}
	if got.Journal[1].State != JournalInterrupted {
		t.Fatalf("in-flight call not interrupted: %+v", got.Journal)
	}
	if !strings.Contains(got.LastError, "结果未知") {
		t.Fatalf("missing reconcile note: %q", got.LastError)
	}
	if count := countState(got, JournalDone); count != 1 {
		t.Fatalf("finished entry lost or replayed: %d", count)
	}
}

func TestRunningTaskWithoutInFlightCallResumesAutomatically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	box := testBox(t)
	b, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	put(t, b, "t1", func(it *Task) { it.Status = StatusRunning })
	addJournal(t, b, "t1", "c1", JournalDone, "df output")
	again, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Get("t1")
	if got.Status != StatusQueued {
		t.Fatalf("safe task should continue automatically, got %q (%s)", got.Status, got.LastError)
	}
	if got.Journal[0].State != JournalDone {
		t.Fatal("finished work was lost")
	}
}

func TestMissingBoxWithPathIsAnError(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "tasks.enc"), nil); err == nil {
		t.Fatal("path without key accepted")
	}
}

func TestGetAndListReturnDeepCopies(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", func(it *Task) {
		it.Approvals = []Approval{{ID: "a1", Reasons: []string{"理由"}}}
		it.Checkpoint.Messages = []map[string]any{{"role": "user", "content": "原始目标"}}
	})
	got, _ := b.Get("t1")
	got.Approvals[0].Reasons[0] = "被改"
	got.Checkpoint.Messages[0]["content"] = "被改"
	rows := b.List("")
	rows[0].Checkpoint.Messages[0]["content"] = "列表改动"

	again, _ := b.Get("t1")
	if again.Approvals[0].Reasons[0] != "理由" || again.Checkpoint.Messages[0]["content"] != "原始目标" {
		t.Fatalf("store mutated through returned value: %+v", again)
	}
	created := put(t, b, "t2", func(it *Task) { it.Checkpoint.Messages = []map[string]any{{"role": "user", "content": "x"}} })
	created.Plan = append(created.Plan, "y")
	if listed := b.List(""); len(listed[0].Checkpoint.Messages) > 1 {
		t.Fatal("caller input shared with store")
	}
}

func TestResumeInterruptedRollsBackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.enc")
	box := testBox(t)
	b, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	put(t, b, "t1", func(it *Task) { it.Status = StatusRunning })
	addJournal(t, b, "t1", "c1", JournalStarted, "")
	// Make the atomic replace fail by pointing the registry at a path whose
	// parent is a file.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.path = filepath.Join(blocked, "tasks.enc")
	if n, err := b.ResumeInterrupted(time.Now().UTC()); err == nil || n != 0 {
		t.Fatalf("expected write failure, got %d %v", n, err)
	}
	got, _ := b.Get("t1")
	if got.Status != StatusRunning || got.Journal[0].State != JournalStarted {
		t.Fatalf("failed write polluted memory: %+v", got)
	}
}

func TestCheckpointCompressionKeepsTurnsPaired(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	_, err := b.Update("t1", func(it *Task) error {
		msgs := []map[string]any{
			{"role": "system", "content": "系统"},
			{"role": "user", "content": "原始目标"},
		}
		for i := 0; i < 60; i++ {
			id := "c" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
			msgs = append(msgs,
				map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": id, "type": "function"}}},
				map[string]any{"role": "tool", "tool_call_id": id, "content": strings.Repeat("y", 2000)},
			)
		}
		it.Checkpoint.Messages = msgs
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := b.Get("t1")
	msgs := got.Checkpoint.Messages
	if msgs[0]["role"] != "system" || msgs[1]["content"] != "原始目标" {
		t.Fatalf("goal not preserved: %+v", msgs[:2])
	}
	if len(msgs) >= 120 {
		t.Fatal("checkpoint not compressed")
	}
	if got.Checkpoint.Compressions == 0 {
		t.Fatal("compression not recorded")
	}
	// Every tool result must still follow its assistant call.
	open := map[string]bool{}
	for _, m := range msgs {
		switch m["role"] {
		case "assistant":
			open = map[string]bool{}
			if calls, ok := m["tool_calls"].([]any); ok {
				for _, c := range calls {
					open[c.(map[string]any)["id"].(string)] = true
				}
			}
		case "tool":
			if !open[m["tool_call_id"].(string)] {
				t.Fatalf("orphan tool result in checkpoint: %v", m["tool_call_id"])
			}
		}
	}
}

func TestFileVersionAndSizeValidation(t *testing.T) {
	box := testBox(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.enc")
	enc, _ := box.Encrypt(`{"version":99,"items":[]}`)
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, box); err == nil {
		t.Fatal("unknown version accepted")
	}
	enc, _ = box.Encrypt(`{"version":1,"items":[]}`)
	if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, box); err != nil {
		t.Fatal(err)
	}
}

func TestResumeInterruptedIsIdempotent(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", func(it *Task) { it.Status = StatusRunning })
	addJournal(t, b, "t1", "c1", JournalStarted, "")
	n, err := b.ResumeInterrupted(time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("first resume %d %v", n, err)
	}
	n, err = b.ResumeInterrupted(time.Now().UTC())
	if err != nil || n != 0 {
		t.Fatalf("second resume changed tasks again: %d %v", n, err)
	}
}

func TestWaitingJobKeepsParkedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	box := testBox(t)
	b, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	wake := time.Now().UTC().Add(5 * time.Minute)
	put(t, b, "job", func(it *Task) {
		it.Status = StatusWaitingJob
		it.Pending = &Pending{Kind: PendingJob, JobRef: "deploy window", JobPolls: 2, WakeAt: wake}
	})
	again, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Get("job")
	if err != nil || got.Status != StatusWaitingJob || got.Pending == nil || got.Pending.JobRef != "deploy window" {
		t.Fatalf("waiting_job not restored: %+v %v", got, err)
	}
	if !got.Pending.WakeAt.Equal(wake) {
		t.Fatalf("wake time lost: %v", got.Pending.WakeAt)
	}
	if len(again.DueJobTasks(wake.Add(-time.Minute))) != 0 {
		t.Fatal("polled before wake time")
	}
	if len(again.DueJobTasks(wake)) != 1 {
		t.Fatal("not polled at wake time")
	}
}

func TestWaitingJobRequiresPendingJob(t *testing.T) {
	b, _ := New("", testBox(t))
	if _, err := b.Create(Task{ID: "t1", OwnerID: "u1", BotID: "main", Goal: "x", Status: StatusWaitingJob}); err == nil {
		t.Fatal("waiting_job without pending accepted")
	}
}

func TestJobPollBacksOff(t *testing.T) {
	if NextJobPoll(0) >= NextJobPoll(3) || NextJobPoll(99) != NextJobPoll(98) {
		t.Fatalf("unexpected schedule %v %v %v", NextJobPoll(0), NextJobPoll(3), NextJobPoll(99))
	}
}

func TestUpdateDoesNotMutateCheckpointMessagesOnFailure(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	if _, err := b.Update("t1", func(it *Task) error {
		it.Checkpoint.Messages = []map[string]any{
			{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "c1", "type": "function"}}},
			{"role": "tool", "tool_call_id": "c1", "content": "before"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err := b.Update("t1", func(it *Task) error {
		it.Checkpoint.Messages[1]["content"] = "after"
		return ErrInvalid
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	got, _ := b.Get("t1")
	if got.Checkpoint.Messages[1]["content"] != "before" {
		t.Fatalf("failed update mutated memory: %+v", got.Checkpoint.Messages)
	}
}

func TestListOwnerAndOrdering(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	put(t, b, "t2", func(it *Task) { it.OwnerID = "u2"; it.UpdatedAt = time.Now().UTC().Add(time.Minute) })
	rows := b.List("u1")
	if len(rows) != 1 || rows[0].ID != "t1" {
		t.Fatalf("owner filter %+v", rows)
	}
	if len(b.List("")) != 2 {
		t.Fatal("background list should see all")
	}
}

func TestJournalAndCheckpointAreBounded(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	_, err := b.Update("t1", func(it *Task) error {
		for i := 0; i < MaxJournal+20; i++ {
			it.Journal = append(it.Journal, JournalEntry{Seq: i, CallID: "c", Name: "run_command", State: JournalDone})
		}
		for i := 0; i < MaxCheckpointMessages+20; i++ {
			it.Checkpoint.Messages = append(it.Checkpoint.Messages, map[string]any{"role": "user", "content": strings.Repeat("x", 1000)})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := b.Get("t1")
	if len(got.Journal) != MaxJournal || len(got.Checkpoint.Messages) > MaxCheckpointMessages {
		t.Fatalf("not bounded: %d %d", len(got.Journal), len(got.Checkpoint.Messages))
	}
}

func addJournal(t *testing.T, b *Book, id, callID, state, result string) {
	t.Helper()
	if _, err := b.Update(id, func(it *Task) error {
		it.Journal = append(it.Journal, JournalEntry{Seq: len(it.Journal) + 1, CallID: callID, Name: "run_command", ArgsHash: "h-" + callID, State: state, Result: result})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func countState(it Task, state string) int {
	n := 0
	for _, e := range it.Journal {
		if e.State == state {
			n++
		}
	}
	return n
}

func TestOversizedGoalAndOptionsAreRefusedNotTruncated(t *testing.T) {
	b, _ := New("", testBox(t))
	long := strings.Repeat("目", MaxGoal+1)
	base := Task{ID: "t1", OwnerID: "u1", BotID: "main", Goal: "x"}

	if _, err := b.Create(Task{ID: "t1", OwnerID: "u1", BotID: "main", Goal: long}); err == nil {
		t.Fatal("oversized goal accepted")
	}
	if _, err := b.Create(Task{ID: "t1", OwnerID: "u1", BotID: "main", Goal: "x", OptionsJSON: `{"note":"` + strings.Repeat("x", MaxOptionsJSON) + `"}`}); err == nil {
		t.Fatal("oversized options accepted")
	}
	if _, err := b.Create(Task{ID: "t1", OwnerID: "u1", BotID: "main", Goal: "x", OptionsJSON: `{"mode":`}); err == nil {
		t.Fatal("invalid options JSON accepted")
	}

	created, err := b.Create(base)
	if err != nil {
		t.Fatal(err)
	}
	// Goal and authorization options are immutable: an update that tries to
	// change them is refused outright, never truncated or silently dropped.
	if _, err := b.Update(created.ID, func(it *Task) error {
		it.Goal = long
		return nil
	}); err == nil {
		t.Fatal("update changing goal accepted")
	}
	if _, err := b.Update(created.ID, func(it *Task) error {
		it.OptionsJSON = `{"note":"` + strings.Repeat("y", MaxOptionsJSON) + `"}`
		return nil
	}); err == nil {
		t.Fatal("update changing authorization options accepted")
	}
	got, _ := b.Get(created.ID)
	if got.Goal != "x" || got.OptionsJSON != "" {
		t.Fatalf("failed update changed stored value: %+v", got)
	}
}

func TestSecretAnswerCheckIsStructuralNotSubstring(t *testing.T) {
	b, _ := New("", testBox(t))
	// Prose that merely mentions the field name must not be rejected.
	for i, opts := range []string{
		`{"note":"关于 secret_answer 字段的说明"}`,
		`{"workspace":{"root":"/srv/secret_answer"}}`,
	} {
		if _, err := b.Create(Task{ID: "ok-" + string(rune('a'+i)), OwnerID: "u1", BotID: "main", Goal: "x", OptionsJSON: opts}); err != nil {
			t.Fatalf("legit options rejected: %v", err)
		}
	}
	// A real plaintext secret answer, top-level or nested, must be refused.
	for i, opts := range []string{
		`{"secret_answer":{"value":"明文"}}`,
		`{"config":{"secret_answer":"明文"}}`,
		`{"secret_answer":null}`,
	} {
		if _, err := b.Create(Task{ID: "bad-" + string(rune('a'+i)), OwnerID: "u1", BotID: "main", Goal: "x", OptionsJSON: opts}); err == nil {
			t.Fatalf("plaintext secret answer accepted: %s", opts)
		}
	}
}

func TestCheckpointTurnPairingInvariants(t *testing.T) {
	asst := func(ids ...string) map[string]any {
		calls := make([]any, 0, len(ids))
		for _, id := range ids {
			calls = append(calls, map[string]any{"id": id, "type": "function"})
		}
		return map[string]any{"role": "assistant", "content": "", "tool_calls": calls}
	}
	tool := func(id string) map[string]any {
		return map[string]any{"role": "tool", "tool_call_id": id, "content": "ok"}
	}
	user := map[string]any{"role": "user", "content": "目标"}
	system := map[string]any{"role": "system", "content": "系统"}

	cases := []struct {
		name string
		msgs []map[string]any
		ok   bool
	}{
		{"paired", []map[string]any{system, user, asst("c1"), tool("c1")}, true},
		{"pending final batch", []map[string]any{system, user, asst("c1", "c2")}, true},
		{"partial final results", []map[string]any{system, user, asst("c1", "c2"), tool("c1")}, true},
		{"orphan result", []map[string]any{system, user, tool("c1")}, false},
		{"duplicate result", []map[string]any{system, user, asst("c1"), tool("c1"), tool("c1")}, false},
		{"duplicate call id", []map[string]any{system, user, asst("c1", "c1")}, false},
		{"mid mismatch", []map[string]any{system, user, asst("c1", "c2"), tool("c1"), asst("c3")}, false},
	}
	for _, tc := range cases {
		err := checkTurns(tc.msgs)
		if tc.ok && err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: mismatch accepted", tc.name)
		}
	}

	// The same rules must apply through the registry, which normalizes typed
	// tool-call structs too.
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	if _, err := b.Update("t1", func(it *Task) error {
		it.Checkpoint.Messages = []map[string]any{system, user, asst("c1"), tool("c1"), tool("c1")}
		return nil
	}); err == nil {
		t.Fatal("duplicate tool result stored")
	}
}

func TestCompressionProtectsGoalAndPendingBatch(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	_, err := b.Update("t1", func(it *Task) error {
		msgs := []map[string]any{
			{"role": "system", "content": "系统"},
			{"role": "user", "content": "原始目标"},
		}
		for i := 0; i < 60; i++ {
			id := "done-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			msgs = append(msgs,
				map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": id, "type": "function"}}},
				map[string]any{"role": "tool", "tool_call_id": id, "content": strings.Repeat("y", 2000)},
			)
		}
		// The final batch is still waiting for one of its results.
		msgs = append(msgs,
			map[string]any{"role": "assistant", "content": "", "tool_calls": []any{
				map[string]any{"id": "pending-1", "type": "function"},
				map[string]any{"id": "pending-2", "type": "function"},
			}},
			map[string]any{"role": "tool", "tool_call_id": "pending-1", "content": "first"},
		)
		it.Checkpoint.Messages = msgs
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := b.Get("t1")
	msgs := got.Checkpoint.Messages
	if len(msgs) < 3 || msgs[0]["role"] != "system" || msgs[1]["content"] != "原始目标" {
		t.Fatalf("protected prefix lost: %+v", msgs[:minInt(2, len(msgs))])
	}
	last := msgs[len(msgs)-1]
	if last["role"] != "tool" || last["tool_call_id"] != "pending-1" {
		t.Fatalf("pending batch dropped: %+v", last)
	}
	before := msgs[len(msgs)-2]
	if before["role"] != "assistant" {
		t.Fatalf("pending assistant batch dropped: %+v", before)
	}
	if got.Checkpoint.Compressions == 0 {
		t.Fatal("compression not recorded")
	}
}

func TestCompressionRefusesWhenPendingBatchCannotFit(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	before, _ := b.Get("t1")
	big := strings.Repeat("x", MaxCheckpointBytes+1000)
	_, err := b.Update("t1", func(it *Task) error {
		it.Checkpoint.Messages = []map[string]any{
			{"role": "system", "content": "系统"},
			{"role": "user", "content": big},
			{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"id": "c1", "type": "function"}}},
		}
		return nil
	})
	if err == nil {
		t.Fatal("uncompressible checkpoint accepted")
	}
	got, _ := b.Get("t1")
	if len(got.Checkpoint.Messages) != len(before.Checkpoint.Messages) {
		t.Fatal("failed update left a truncated checkpoint")
	}
}

func TestDeepCopyIsolatesTypedToolCalls(t *testing.T) {
	type typedCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	b, _ := New("", testBox(t))
	calls := []typedCall{{ID: "c1", Type: "function"}}
	calls[0].Function.Name = "run_command"
	created := put(t, b, "t1", func(it *Task) {
		it.Checkpoint.Messages = []map[string]any{
			{"role": "user", "content": "目标"},
			{"role": "assistant", "content": "", "tool_calls": calls},
		}
	})
	if created.Checkpoint.Messages[1]["tool_calls"].([]typedCall)[0].ID != "c1" {
		t.Fatal("created copy lost typed tool calls")
	}
	// Mutating the caller's slice must not reach the store.
	calls[0].ID = "hijacked"
	got, _ := b.Get("t1")
	stored := got.Checkpoint.Messages[1]["tool_calls"].([]typedCall)
	if stored[0].ID != "c1" || stored[0].Function.Name != "run_command" {
		t.Fatalf("store shared caller's typed slice: %+v", stored)
	}
	// Mutating a returned copy must not reach the store either.
	stored[0].Function.Name = "changed"
	again, _ := b.Get("t1")
	if again.Checkpoint.Messages[1]["tool_calls"].([]typedCall)[0].Function.Name != "run_command" {
		t.Fatal("store mutated through returned typed slice")
	}
}

func TestUnmarshalableCheckpointIsRejectedNotNilled(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	_, err := b.Update("t1", func(it *Task) error {
		it.Checkpoint.Messages = []map[string]any{
			{"role": "user", "content": make(chan int)},
		}
		return nil
	})
	if err == nil {
		t.Fatal("unmarshalable checkpoint accepted")
	}
	got, _ := b.Get("t1")
	if len(got.Checkpoint.Messages) != 0 {
		t.Fatalf("rejected checkpoint stored anyway: %+v", got.Checkpoint.Messages)
	}
}

func TestNewRejectsCorruptFiles(t *testing.T) {
	box := testBox(t)
	dir := t.TempDir()
	write := func(t *testing.T, plain string) string {
		t.Helper()
		path := filepath.Join(dir, "tasks.enc")
		enc, err := box.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	valid := `{"id":"t1","owner_id":"u1","bot_id":"main","goal":"x","status":"queued"}`
	cases := map[string]string{
		"duplicate id":       `{"version":1,"items":[` + valid + `,` + valid + `]}`,
		"empty id":           `{"version":1,"items":[{"owner_id":"u1","bot_id":"main","goal":"x","status":"queued"}]}`,
		"trailing object":    `{"version":1,"items":[]}{"version":1,"items":[]}`,
		"trailing brace":     `{"version":1,"items":[]}{}`,
		"trailing bracket":   `{"version":1,"items":[]}]`,
		"trailing garbage":   `{"version":1,"items":[]}nope`,
		"unknown field":      `{"version":1,"items":[],"extra":true}`,
		"truncated document": `{"version":1,"items":[`,
	}
	for name, plain := range cases {
		if _, err := New(write(t, plain), box); err == nil {
			t.Fatalf("%s: corrupt file accepted", name)
		}
	}
	if _, err := New(write(t, `{"version":1,"items":[`+valid+`]}`), box); err != nil {
		t.Fatalf("valid file rejected: %v", err)
	}
	// Trailing whitespace is not trailing content.
	if _, err := New(write(t, "{\"version\":1,\"items\":[]}\n\t \n"), box); err != nil {
		t.Fatalf("legal whitespace rejected: %v", err)
	}

	// A file larger than the limit must be refused before parsing.
	path := filepath.Join(dir, "big.enc")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, MaxFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, box); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestCreateRollsBackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := New("", testBox(t))
	if err != nil {
		t.Fatal(err)
	}
	put(t, b, "keep", nil)
	b.path = filepath.Join(blocked, "tasks.enc")
	if _, err := b.Create(Task{ID: "t2", OwnerID: "u1", BotID: "main", Goal: "x"}); err == nil {
		t.Fatal("expected write failure")
	}
	if _, err := b.Get("t2"); err == nil {
		t.Fatal("failed create left the task in memory")
	}
	if _, err := b.Get("keep"); err != nil {
		t.Fatal("failed create disturbed existing tasks")
	}
}

func TestUpdateKeepsIdentityAndAuthorization(t *testing.T) {
	b, _ := New("", testBox(t))
	created := put(t, b, "t1", func(it *Task) {
		it.SessionID, it.ProjectID, it.ServerID = "s1", "p1", "web"
		it.Goal = "检查磁盘"
		it.OptionsJSON = `{"mode":"direct"}`
	})
	got, err := b.Update(created.ID, func(it *Task) error {
		it.OwnerID, it.BotID, it.SessionID = "attacker", "other", "s2"
		it.ProjectID, it.ServerID = "p2", "db"
		it.Goal = "删除数据库"
		it.OptionsJSON = `{"mode":"auto","approval_mode":"off"}`
		it.Status = StatusPaused
		return nil
	})
	if err == nil {
		t.Fatalf("identity change accepted: %+v", got)
	}
	stored, _ := b.Get(created.ID)
	if stored.OwnerID != "u1" || stored.BotID != "main" || stored.SessionID != "s1" ||
		stored.ProjectID != "p1" || stored.ServerID != "web" || stored.Goal != "检查磁盘" ||
		stored.OptionsJSON != `{"mode":"direct"}` || stored.Status != StatusQueued {
		t.Fatalf("rejected update changed stored task: %+v", stored)
	}
	// A legitimate progress update still works.
	updated, err := b.Update(created.ID, func(it *Task) error {
		it.Status = StatusPaused
		return nil
	})
	if err != nil || updated.Status != StatusPaused {
		t.Fatalf("legitimate status update failed: %+v %v", updated, err)
	}
}

func TestJournalTrimKeepsPendingCall(t *testing.T) {
	b, _ := New("", testBox(t))
	put(t, b, "t1", nil)
	_, err := b.Update("t1", func(it *Task) error {
		for i := 0; i < MaxJournal+5; i++ {
			it.Journal = append(it.Journal, JournalEntry{Seq: i + 1, CallID: "old-" + string(rune('a'+i%26)), State: JournalDone})
		}
		it.Journal = append(it.Journal, JournalEntry{Seq: len(it.Journal) + 1, CallID: "live", State: JournalStarted})
		it.Pending = &Pending{Kind: PendingTool, CallID: "live"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := b.Get("t1")
	if len(got.Journal) != MaxJournal {
		t.Fatalf("journal not bounded: %d", len(got.Journal))
	}
	if got.Journal[len(got.Journal)-1].CallID != "live" || got.Pending == nil || got.Pending.CallID != "live" {
		t.Fatalf("pending journal entry trimmed: %+v", got.Journal[len(got.Journal)-1])
	}
}

func TestPausedStartedCallRecoversInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.enc")
	box := testBox(t)
	b, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	put(t, b, "t1", func(it *Task) { it.Status = StatusRunning })
	addJournal(t, b, "t1", "c1", JournalStarted, "")
	if _, err := b.Update("t1", func(it *Task) error {
		it.Status = StatusPaused
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	again, err := New(path, box)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Get("t1")
	if got.Status != StatusPaused || got.Journal[0].State != JournalInterrupted {
		t.Fatalf("paused in-flight call not reconciled: %+v", got)
	}
	if !strings.Contains(got.LastError, "结果未知") {
		t.Fatalf("missing reconcile note: %q", got.LastError)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = json.Valid
