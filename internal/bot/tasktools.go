package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/task"
)

// planEqual reports whether two plans have identical steps.
func planEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Tools that only exist for durable tasks: plan, checkpoint, wait and finish.
// A long task therefore has an explicit state instead of ending with a polite
// paragraph, and "waiting for approval", "failed" or "out of steps" is never
// recorded as success.

var taskPlanTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "plan",
		"description": "把任务拆成几步，覆盖后保存为当前计划。任务可以中途修改计划。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"steps": map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "maxItems": 40},
			},
			"required": []string{"steps"},
		},
	},
}

var taskCheckpointTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "checkpoint",
		"description": "记录当前进度和下一步。完成一步、拿到关键结论或准备暂停前调用。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"progress": map[string]string{"type": "string", "description": "这一阶段完成了什么"},
				"next":     map[string]string{"type": "string", "description": "下一步做什么"},
				"facts":    map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "需要保留的关键事实"},
			},
			"required": []string{"progress"},
		},
	},
}

var taskPlanProgressTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "plan_progress",
		"description": "在一计划步骤真正做完并通过检查后，把完成的前缀步数记录下来（从当前计划第 1 步起连续）。卡片上的进度条按它显示。不要凭感觉或步骤预算估计进度。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"completed": map[string]string{"type": "integer", "description": "已连续完成的计划步骤数（0 到总步数）"},
			},
			"required": []string{"completed"},
		},
	},
}

var taskWaitTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "wait",
		"description": "任务需要等外部条件（例如一段时间的观察窗口、别人的操作）时调用。任务会保存状态并按间隔重新检查，不会占用对话。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_ref":       map[string]string{"type": "string", "description": "要等待或之后复查的事情，写清楚怎么判断可以继续"},
				"after_minutes": map[string]string{"type": "integer", "description": "多少分钟后回来检查，5-120"},
			},
			"required": []string{"job_ref"},
		},
	},
}

var taskFinishTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "finish",
		"description": "任务真正完成时调用。必须写清结论和验证方式；需要用户批准、遇到失败或没做完时不要调用，改为说明情况。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"summary":      map[string]string{"type": "string", "description": "完成了什么，一两句话"},
				"verification": map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "minItems": 1, "description": "怎么确认已经生效，例如命令与结果"},
			},
			"required": []string{"summary", "verification"},
		},
	},
}

// planProgressInvalid marks a plan_progress argument that failed validation
// inside the guarded update. Returning it from the Book.Update callback makes
// the registry refuse the write entirely: no count, no journal entry and no
// timestamp move. The caller distinguishes it from ownership errors
// (paused/cancelled) and from persistence errors, because a validation
// failure is a readable tool result the model can correct, never a window
// stop.
type planProgressInvalid struct{ msg string }

func (e planProgressInvalid) Error() string { return e.msg }

// taskToolDefs are appended to the ordinary tool list for a durable task.
func taskToolDefs() []map[string]any {
	return []map[string]any{taskPlanTool, taskPlanProgressTool, taskCheckpointTool, taskWaitTool, taskFinishTool}
}

// taskRunnable rejects a pure task-tool write when the task is no longer
// running. The check runs inside the same registry Update that applies the
// change, so a concurrent pause, cancel, park or finish cannot be overwritten
// by a tool call that was already in flight.
func taskRunnable(t *task.Task) error {
	switch {
	case t.Status == task.StatusCancelled:
		return errCancelled
	case t.Status == task.StatusPaused:
		return errPaused
	case t.Terminal(), t.Waiting():
		return errFinalBlocked
	}
	// Only a running task belongs to the window that dispatched it. A task the
	// operator paused and immediately resumed is already queued while this
	// window unwinds: it must not plan, park or finish behind their back.
	if !windowOwns(t) {
		return errPaused
	}
	return nil
}

// taskToolErr turns a guarded write's error into the stop signal the window
// loop understands, and records that this window must not continue.
func (st *taskState) taskToolErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errCancelled):
		st.finished = true
		return errCancelled
	case errors.Is(err, errPaused), errors.Is(err, errFinalBlocked):
		st.halted = true
		return errSuspended
	}
	st.halted = true
	return err
}

// runTaskTool handles the task-only tools. handled is false for every other
// call, which then goes through the normal tool dispatch.
func (st *taskState) runTaskTool(ctx context.Context, a *Agent, call toolCall) (out string, handled bool, err error) {
	switch call.Function.Name {
	case "plan":
		// Pure state tools are journaled like any other call, but with their
		// state change in the SAME write. A crash between the change and the
		// result would otherwise replay the tool and apply it twice; and
		// because the effect is idempotent (the plan is replaced, not
		// appended), replaying after a successful write simply reuses the
		// stored result.
		if prior := st.priorTaskTool(call); prior != nil {
			return plainResult(prior.Result), true, nil
		}
		var args struct {
			Steps []string `json:"steps"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			return "plan 参数不是有效的 JSON", true, nil
		}
		steps := make([]string, 0, len(args.Steps))
		for _, s := range args.Steps {
			if s = strings.TrimSpace(a.redact(ctx, s)); s != "" {
				steps = append(steps, clip(s, task.MaxLine))
			}
		}
		if len(steps) == 0 {
			return "计划不能为空", true, nil
		}
		out = "计划已保存。"
		st.track.note("plan", "更新计划（"+clip(strings.Join(steps, "；"), 80)+"）")
		_, err = st.book.Update(st.id, func(t *task.Task) error {
			if err := taskRunnable(t); err != nil {
				return err
			}
			samePlan := planEqual(t.Plan, steps)
			t.Plan = steps
			if !samePlan {
				// A new plan invalidates stale completion: the count only
				// survives windows and restarts, never plan rewrites.
				t.PlanCompleted = 0
			} else if t.PlanCompleted > len(steps) {
				t.PlanCompleted = len(steps)
			}
			appendTaskToolEntry(t, st.turn, call, out)
			return nil
		})
		if err := st.taskToolErr(err); err != nil {
			return "", true, err
		}
		return out, true, nil
	case "plan_progress":
		// The model marks leading plan steps done after the work and checks
		// actually happened. The count is a persisted completion prefix; it is
		// never inferred from turns, tool calls, budget or time.
		// Replay first: a stored journal entry proves the count and the entry
		// were applied exactly once in one write, so a repeated call reuses
		// the stored result without touching state again.
		if prior := st.priorTaskTool(call); prior != nil {
			return plainResult(prior.Result), true, nil
		}
		var args struct {
			Completed *int `json:"completed"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			return "plan_progress 参数不是有效的 JSON", true, nil
		}
		if args.Completed == nil {
			// Missing or null is a malformed call, not a window error: answer
			// the model and leave the task completely untouched.
			return "plan_progress 需要一个整数 completed，表示已连续完成的计划步骤数", true, nil
		}
		var out string
		_, err = st.book.Update(st.id, func(t *task.Task) error {
			if err := taskRunnable(t); err != nil {
				return err
			}
			total := len(t.Plan)
			if total == 0 {
				// The sentinel makes Book.Update refuse the whole write, so a
				// rejected count can never move UpdatedAt or the journal.
				return planProgressInvalid{"还没有计划：先用 plan 生成计划，再标记进度"}
			}
			if *args.Completed < 0 || *args.Completed > total {
				return planProgressInvalid{fmt.Sprintf("完成数必须在 0 到 %d 之间", total)}
			}
			// One durable write holds both the new count and the journal
			// entry: a crash before it replays cleanly, a crash after it
			// leaves both persisted.
			t.PlanCompleted = *args.Completed
			out = fmt.Sprintf("已记录完成 %d / %d 步。", *args.Completed, total)
			appendTaskToolEntry(t, st.turn, call, out)
			return nil
		})
		var inv planProgressInvalid
		if errors.As(err, &inv) {
			// Validation failure: the update was refused, nothing was
			// persisted and the window must NOT halt. The model reads the
			// reason and can retry with a corrected count.
			return inv.msg, true, nil
		}
		if err := st.taskToolErr(err); err != nil {
			return "", true, err
		}
		// The write succeeded; only now record the card activity.
		st.track.note("plan_progress", "计划进度 "+out)
		return out, true, nil
	case "checkpoint":
		if prior := st.priorTaskTool(call); prior != nil {
			return plainResult(prior.Result), true, nil
		}
		var args struct {
			Progress string   `json:"progress"`
			Next     string   `json:"next"`
			Facts    []string `json:"facts"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			return "checkpoint 参数不是有效的 JSON", true, nil
		}
		progress := strings.TrimSpace(a.redact(ctx, args.Progress))
		if progress == "" {
			return "checkpoint 需要写清完成了什么", true, nil
		}
		out = "进度已保存。"
		_, err = st.book.Update(st.id, func(t *task.Task) error {
			if err := taskRunnable(t); err != nil {
				return err
			}
			t.Progress = append(t.Progress, clip(progress, task.MaxLine))
			if n := strings.TrimSpace(a.redact(ctx, args.Next)); n != "" {
				t.Next = clip(n, task.MaxLine)
				t.Checkpoint.Next = t.Next
			}
			for _, f := range args.Facts {
				if f = strings.TrimSpace(a.redact(ctx, f)); f != "" {
					t.Checkpoint.Facts = append(t.Checkpoint.Facts, clip(f, task.MaxLine))
				}
			}
			appendTaskToolEntry(t, st.turn, call, out)
			return nil
		})
		if err := st.taskToolErr(err); err != nil {
			return "", true, err
		}
		return out, true, nil
	case "wait":
		if prior := st.priorTaskTool(call); prior != nil {
			// The park is already recorded; do not extend the wake time.
			st.halted = true
			return plainResult(prior.Result), true, nil
		}
		var args struct {
			JobRef       string `json:"job_ref"`
			AfterMinutes int    `json:"after_minutes"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			return "wait 参数不是有效的 JSON", true, nil
		}
		ref := strings.TrimSpace(a.redact(ctx, args.JobRef))
		if ref == "" {
			return "wait 需要说明在等什么", true, nil
		}
		minutes := args.AfterMinutes
		if minutes <= 0 {
			minutes = 10
		}
		if minutes < 5 {
			minutes = 5
		}
		if minutes > 120 {
			minutes = 120
		}
		wake := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
		out = "已保存为等待中，稍后自动重新检查；不用等用户回复。"
		_, err = st.book.Update(st.id, func(t *task.Task) error {
			if err := taskRunnable(t); err != nil {
				return err
			}
			t.Status = task.StatusWaitingJob
			t.Pending = &task.Pending{Kind: task.PendingJob, JobRef: clip(ref, task.MaxLine), WakeAt: wake}
			appendTaskToolEntry(t, st.turn, call, out)
			return nil
		})
		if err := st.taskToolErr(err); err != nil {
			return "", true, err
		}
		st.halted = true
		return out, true, nil
	case "finish":
		if prior := st.priorTaskTool(call); prior != nil {
			st.finished = true
			st.result = plainResult(prior.Result)
			return plainResult(prior.Result), true, nil
		}
		if st.unknown && !st.checked {
			// A mutating call started and never reported. Its effect may or may
			// not exist, so "it worked" is not a verified outcome: the model
			// has to inspect the actual state first and say so.
			return "有一步操作在执行中断，结果未知。不能直接宣称完成：先用只读方式核对它是否生效，再在 finish 的 verification 里写明核对结果。", true, nil
		}
		var args struct {
			Summary      string   `json:"summary"`
			Verification []string `json:"verification"`
		}
		if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
			return "finish 参数不是有效的 JSON", true, nil
		}
		summary := strings.TrimSpace(a.redact(ctx, args.Summary))
		verified := make([]string, 0, len(args.Verification))
		for _, v := range args.Verification {
			if v = strings.TrimSpace(a.redact(ctx, v)); v != "" {
				verified = append(verified, clip(v, task.MaxLine))
			}
		}
		if summary == "" || len(verified) == 0 {
			return "只有在确认完成后才能调用 finish，并且必须写明验证方式。", true, nil
		}
		// Verification evidence must be a real tool observation made after the
		// last mutation. Prose alone is not evidence: a task that changed
		// anything needs a later read-only check (journal Seq comparison, so
		// evidence survives windows and restarts and is invalidated by a later
		// mutation).
		if st.lastMutationSeq > 0 && st.verifiedSeq <= st.lastMutationSeq {
			return "还没有验证证据：最后一次修改之后，需要先用只读方式（workspace 的 read/list/search/status/diff 或 job_status）核对真实结果，再调用 finish 并写明验证方式。", true, nil
		}
		text := summary + "\n\n验证：\n- " + strings.Join(verified, "\n- ")
		st.track.note("verify", "记录验证证据（"+clip(summary, 60)+"）")
		// One durable write holds everything that makes the answer real: the
		// journal entry, the verification evidence, the final text, the
		// transcript message id and the terminal status. A crash before it
		// leaves the task running, so finish is simply replayed (idempotent);
		// a crash after it leaves a completed task that can always write or
		// repair its closing message, and whose journal already records the
		// call so the batch can never run another side effect.
		messageID := st.finalMessageID()
		_, err = st.book.Update(st.id, func(t *task.Task) error {
			// An operator action wins: never turn a paused or cancelled task
			// into a completed one.
			if err := taskRunnable(t); err != nil {
				return err
			}
			// Result and ResultMessageID are part of this same write: a task
			// must never be completed without the outbox that lets a restart
			// write (or repair) its closing message.
			if hasCorrection(*t) {
				return errCorrectionPending
			}
			t.Result = clip(text, task.MaxResult)
			t.ResultMessageID = messageID
			t.Verified = verified
			t.Progress = append(t.Progress, clip(summary, task.MaxLine))
			// The task is really finishing with recorded verification: the
			// plan is complete. Only this path may set the full count; a
			// failed, paused or cancelled task keeps its prefix.
			t.PlanCompleted = len(t.Plan)
			t.Status = task.StatusCompleted
			t.FinishedAt = time.Now().UTC()
			t.Pending = nil
			appendTaskToolEntry(t, st.turn, call, text)
			return nil
		})
		if errors.Is(err, errCorrectionPending) {
			return correctionSkipped, true, nil
		}
		if err := st.taskToolErr(err); err != nil {
			return "", true, err
		}
		// Only the transcript write is left; it is idempotent and repaired on a
		// later scan if it fails here.
		if err := st.writeFinal(ctx, a, text, messageID); err != nil {
			return "", true, err
		}
		st.finished = true
		st.result = text
		return text, true, nil
	case "request_secret", "ask_user":
		return "", false, nil
	}
	return "", false, nil
}

// priorTaskTool returns the stored result of a pure task tool already executed
// in this turn. Its state change and its journal entry share one write, so a
// stored entry proves the change was applied exactly once.
func (st *taskState) priorTaskTool(call toolCall) *task.JournalEntry {
	it, err := st.book.Get(st.id)
	if err != nil {
		return nil
	}
	return findEntry(it.Journal, st.turn, call.ID, argsHash(call))
}

// appendTaskToolEntry records a pure task tool in the journal. It runs inside
// the same Update that applies the tool's state change.
func appendTaskToolEntry(t *task.Task, turn int, call toolCall, out string) {
	t.Journal = append(t.Journal, task.JournalEntry{
		At: time.Now().UTC(), Seq: len(t.Journal) + 1, Turn: turn,
		CallID: call.ID, Name: call.Function.Name, ArgsHash: argsHash(call),
		State: task.JournalDone, Result: out,
	})
}

// parkCard stores a waiting_user state bound to the exact chat card message
// the task is suspended on. An answer is accepted only for that message, so a
// card left over from an earlier task or turn can never resume this one.
func (st *taskState) parkCard(kind, botID, sessionID, question string, card *model.Choice, secretReq *model.SecretRequest, messageID string) error {
	if messageID == "" {
		// Without a durable card id the transport cannot prove which card an
		// answer belongs to. Refuse rather than accept a guessed card.
		st.halted = true
		return errors.New("卡片消息没有保存成功，任务已暂停")
	}
	callID := ""
	hash := ""
	if st.current != nil {
		callID, hash = st.current.ID, argsHash(*st.current)
	} else if len(st.outcome.Text) > 0 {
	}
	_, err := st.book.Update(st.id, func(t *task.Task) error {
		// A park is still a window outcome: if the operator paused, cancelled,
		// resumed or another park already moved the task, that state wins. The
		// card message was already written, but the task must not be revived.
		if !windowOwns(t) {
			return errFinalBlocked
		}
		t.Status = task.StatusWaitingUser
		t.Pending = &task.Pending{
			Kind: task.PendingUser, CallID: callID, ArgsHash: hash, Turn: st.turn,
			JobRef: clip(question, task.MaxLine), CardMessageID: messageID,
		}
		// The journal entry keeps the card id too, so a restart can prove the
		// binding without relying on the session transcript.
		for i := len(t.Journal) - 1; i >= 0; i-- {
			if t.Journal[i].Turn == st.turn && t.Journal[i].CallID == callID {
				t.Journal[i].CardMessageID = messageID
				break
			}
		}
		return nil
	})
	if err != nil {
		st.halted = true
	}
	return err
}

// AnswerUser resumes a task waiting on a card. The reply is the tool result;
// a secret answer arrives as a vault acknowledgement, never as plaintext.
func (r *Runner) AnswerUser(ctx context.Context, ownerID, id, answer string) (task.Task, error) {
	it, err := r.Book.Update(id, func(t *task.Task) error {
		if t.OwnerID != ownerID {
			return task.ErrNotFound
		}
		if t.Status != task.StatusWaitingUser && t.Status != task.StatusWaitingJob {
			return errors.New("任务不在等待回答")
		}
		if t.Pending == nil {
			return errors.New("任务没有等待中的卡片")
		}
		// The answer is the operator's next turn, not a second result for the
		// call that already reported "card shown". It is stored on Pending and
		// injected as a user message when the window resumes.
		t.Pending.Answer = clip(answer, task.MaxResult)
		t.Status = task.StatusQueued
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	r.Wake()
	return it, nil
}

var _ = errors.New
