package bot

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/store"
	"onefrog/internal/task"
	"onefrog/internal/workspace"
)

// Native background jobs inside a local workspace. These are real OS
// processes: start, poll output/exit code, cancel. A task that waits on one
// parks as waiting_job and resumes when the job actually finishes — the wait
// result is the job's real outcome, never an invented one.

var jobStartTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "job_start",
		"description": "在绑定的本地工作区后台启动一个长时间命令（构建、测试、数据迁移等）。立即返回作业 id；用 job_status 查看输出和退出码，或 job_wait 等它结束。需要审批。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]string{"type": "string", "description": "要在后台运行的完整 shell 命令，在工作区根目录执行"},
			},
			"required": []string{"command"},
		},
	},
}

var jobStatusTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "job_status",
		"description": "查看后台作业的状态、输出和退出码。只读。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_id": map[string]string{"type": "string", "description": "job_start 返回的作业 id"},
			},
			"required": []string{"job_id"},
		},
	},
}

var jobCancelTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "job_cancel",
		"description": "取消一个还在运行的后台作业。需要审批。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_id": map[string]string{"type": "string", "description": "要取消的作业 id"},
			},
			"required": []string{"job_id"},
		},
	},
}

var jobWaitTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "job_wait",
		"description": "等待一个后台作业结束后继续任务。任务会挂起并按间隔检查作业的真实退出码，不会凭空等待。job_ref 填作业 id。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"job_id": map[string]string{"type": "string", "description": "要等待的作业 id"},
			},
			"required": []string{"job_id"},
		},
	},
}

// jobToolDefs are appended when the task's workspace is a native local root.
func jobToolDefs() []map[string]any {
	return []map[string]any{jobStartTool, jobStatusTool, jobCancelTool, jobWaitTool}
}

// jobToolsActive reports whether this turn's options bind a local workspace,
// which is the only place native background jobs exist.
func jobToolsActive(ctx context.Context) bool {
	t := options(ctx).Workspace
	return t != nil && t.Kind == "local"
}

// jobLocalRequest decodes a LocalRequest-shaped argument body for run-type
// calls issued through the workspace tool with a job subaction.
func (a *Agent) jobStartCall(ctx context.Context, speaker model.Bot, call toolCall, tr *trace) string {
	if a.LocalWorkspace == nil {
		return "本地工作区未启用"
	}
	target := options(ctx).Workspace
	if target == nil || target.Kind != "local" {
		return "job_start 只在本地工作区可用"
	}
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return "job_start 参数无效"
	}
	if strings.TrimSpace(args.Command) == "" {
		return "job_start 需要一条命令"
	}
	if readsVault(args.Command) {
		return "不能读取 OneFrog 的密钥和凭据文件"
	}
	// The handler refuses without the exact central grant: speaker, tool,
	// call id and argument hash must match what the shared hook authorized.
	if !grantForCall(ctx, speaker.ID, "local", call) {
		return "该调用没有通过授权（或参数已变更），未启动"
	}
	// Jobs are owned by (speaker, task): another task cannot see or cancel
	// them. A non-task caller gets nothing.
	st := taskFrom(ctx)
	if st == nil {
		return "job_start 只能在持久任务里使用"
	}
	job, err := a.Jobs.Start(ctx, a.LocalWorkspace, target.Root, args.Command, speaker.ID, st.id)
	if err != nil {
		return "后台作业没有启动：" + err.Error()
	}
	raw, _ := json.Marshal(map[string]any{
		"job_id": job.ID, "pid": job.PID, "status": job.Status,
		"message": "作业已启动。用 job_status 看输出，job_wait 等它结束。",
	})
	return string(raw)
}

func (a *Agent) jobStatusCall(ctx context.Context, speaker model.Bot, call toolCall, tr *trace) string {
	if a.Jobs == nil {
		return "后台作业不可用"
	}
	var args struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return "job_status 参数无效"
	}
	st := taskFrom(ctx)
	if st == nil {
		return "job_status 只能在持久任务里使用"
	}
	job, err := a.Jobs.Status(strings.TrimSpace(args.JobID), speaker.ID, st.id)
	if err != nil {
		return err.Error()
	}
	raw, _ := json.Marshal(job)
	return string(raw)
}

func (a *Agent) jobCancelCall(ctx context.Context, speaker model.Bot, call toolCall, tr *trace) string {
	if a.Jobs == nil {
		return "后台作业不可用"
	}
	var args struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return "job_cancel 参数无效"
	}
	// Cancelling another task's job (or an ungranted cancel) is refused.
	if !grantForCall(ctx, speaker.ID, "local", call) {
		return "该调用没有通过授权（或参数已变更），未取消"
	}
	st := taskFrom(ctx)
	if st == nil {
		return "job_cancel 只能在持久任务里使用"
	}
	job, err := a.Jobs.Cancel(strings.TrimSpace(args.JobID), speaker.ID, st.id)
	if err != nil {
		return err.Error()
	}
	raw, _ := json.Marshal(map[string]any{"job_id": job.ID, "status": job.Status, "exit_code": job.ExitCode})
	return string(raw)
}

// jobWaitCall parks the task on a real background job. The task resumes when
// the job finishes; the wake path (consumeJobWait) injects the job's actual
// outcome as the tool result.
func (a *Agent) jobWaitCall(ctx context.Context, st *taskState, speaker model.Bot, call toolCall, tr *trace) (string, bool) {
	if st == nil || a.Jobs == nil {
		return "job_wait 只能在持久任务里使用，且需要本地工作区", false
	}
	target := options(ctx).Workspace
	if target == nil || target.Kind != "local" {
		return "job_wait 只在本地工作区可用", false
	}
	var args struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return "job_wait 参数无效", false
	}
	jobID := strings.TrimSpace(args.JobID)
	job, err := a.Jobs.Status(jobID, speaker.ID, st.id)
	if err != nil {
		return err.Error(), false
	}
	if job.Finished() {
		// Already finished: report the real outcome immediately.
		return jobOutcomeText(job), false
	}
	// Park the task on this job with a short first poll so a fast job does
	// not stall the task.
	out := "已挂起，等待后台作业 " + jobID + " 结束。任务会自动检查作业的真实退出码后继续，不用等用户回复。"
	_, err = st.book.Update(st.id, func(t *task.Task) error {
		if err := taskRunnable(t); err != nil {
			return err
		}
		t.Status = task.StatusWaitingJob
		t.Pending = &task.Pending{
			Kind: task.PendingJob, JobRef: "job:" + jobID,
			CallID: call.ID, Name: call.Function.Name, ArgsHash: argsHash(call),
			WakeAt: time.Now().UTC().Add(5 * time.Second), Turn: st.turn,
		}
		// Close the started entry in this same write instead of appending a
		// second row: the call is parked, and its result is the park notice.
		closeOpenEntry(t, st.turn, call.ID, argsHash(call), task.JournalDone, out, true, task.OutcomeDone)
		return nil
	})
	if err := st.taskToolErr(err); err != nil {
		return err.Error(), true
	}
	// The task is parked on the job: the started journal entry is closed by
	// the park update below, not by execute()'s closeOpenEntry (which would
	// flip a waiting_job task back to running for a delegated-child wake).
	st.jobParked = true
	st.halted = true
	return out, true
}

// authorizeJobStart gates starting a native background job: same rules as a
// workspace run (explicit opt-in, OS-account authority, independent review).
func (a *Agent) authorizeJobStart(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	target := options(ctx).Workspace
	if target == nil || target.Kind != "local" || targetServer != "local" {
		return ctx, "job_start 只在本地工作区可用，未执行"
	}
	if a.LocalWorkspace == nil || a.Jobs == nil {
		return ctx, "本地工作区或后台作业未启用，未执行"
	}
	if _, err := a.LocalWorkspace.Roots().Pick(target.Root); err != nil {
		return ctx, "工作区根目录不在允许列表内，未执行"
	}
	if !a.LocalWorkspace.Roots().CommandExecution {
		return ctx, "本地命令执行未启用，未执行"
	}
	var args struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "job_start 参数无效，未执行"
	}
	label := "后台启动本地命令"
	v := callVerdict{Level: "medium", Reasons: []string{"命令以 OneFrog 进程的系统账号权限后台执行（不是沙箱），需要确认"}, Args: call.Function.Arguments}
	findings, level, untrusted := a.evidenceFor(ctx, args.Command)
	v.Level = level
	v.Findings = findings
	v.Untrusted = untrusted
	for _, f := range findings {
		v.Reasons = append(v.Reasons, "规则："+f.Label+"："+f.Detail)
	}
	if untrusted != "" {
		v.Reasons = append(v.Reasons, untrusted)
	}
	allow, pending, why := a.decideCall(ctx, speaker.ID, store.ServerRec{Server: model.Server{ID: "local", Name: "本地工作区 " + target.Root}}, label, v, a.approvalMode(ctx), true, args.Command)
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "未执行：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, "local", call, label))), ""
}

// authorizeJobCancel gates stopping a running job.
func (a *Agent) authorizeJobCancel(ctx context.Context, speaker model.Bot, targetServer string, call toolCall, mark func(context.Context) context.Context) (context.Context, string) {
	target := options(ctx).Workspace
	if target == nil || target.Kind != "local" || targetServer != "local" {
		return ctx, "job_cancel 只在本地工作区可用，未执行"
	}
	if a.Jobs == nil {
		return ctx, "后台作业未启用，未执行"
	}
	var args struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return ctx, "job_cancel 参数无效，未执行"
	}
	label := "取消后台作业 " + strings.TrimSpace(args.JobID)
	v := callVerdict{Level: "low", Reasons: []string{"会终止一个还在运行的后台命令"}, Args: call.Function.Arguments}
	allow, pending, why := a.decideCall(ctx, speaker.ID, store.ServerRec{Server: model.Server{ID: "local", Name: "本地工作区 " + target.Root}}, label, v, a.approvalMode(ctx), a.approvalMode(ctx) == preferences.ModeManual, "")
	if pending {
		return mark(ctx), PendingNotice
	}
	if !allow {
		return mark(ctx), "没有取消：" + why
	}
	return mark(withCallGrant(ctx, newCallGrant(speaker.ID, "local", call, label))), ""
}

// jobOutcomeText renders a finished job's real outcome for the model.
func jobOutcomeText(job workspace.Job) string {
	var b strings.Builder
	b.WriteString("后台作业 " + job.ID + " 已结束，状态 " + job.Status)
	if job.ExitCode != nil {
		b.WriteString("，退出码 " + itoaJob(*job.ExitCode))
	}
	log := strings.TrimSpace(job.Log)
	if log != "" {
		b.WriteString("\n输出：\n" + clip(log, 4000))
	}
	return b.String()
}

func itoaJob(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// consumeJobWait is called when a waiting_job task parked on a native job is
// woken: it checks the job's real state. Finished → resume with the outcome;
// still running → re-park with the next sparse poll.
func (r *Runner) consumeJobWait(ctx context.Context, it task.Task) (resume bool, result string) {
	if it.Pending == nil || it.Pending.Kind != task.PendingJob {
		return true, ""
	}
	ref := it.Pending.JobRef
	if !strings.HasPrefix(ref, "job:") {
		// Child-task and plain waits keep their existing behavior.
		return true, ""
	}
	jobID := strings.TrimPrefix(ref, "job:")
	a := r.Agent
	if a.Jobs == nil {
		return true, "后台作业服务不可用"
	}
	// Ownership check: the parked task reads only its own job.
	job, err := a.Jobs.Status(jobID, it.BotID, it.ID)
	if err != nil {
		// The job table is process-local. A restart loses jobs; the task must
		// not invent an outcome — it reports the loss and resumes so the model
		// can re-run a read-only check or restart the work with approval.
		return true, "后台作业 " + jobID + " 的状态已不可查询（服务可能重启过，作业结果未知，未重放）。请先用只读方式核对工作区现状，再决定是否需要重新执行。"
	}
	if !job.Finished() {
		// Still running: re-park with the next sparse poll.
		_, _ = r.Book.Update(it.ID, func(t *task.Task) error {
			if t.Status != task.StatusWaitingJob || t.Pending == nil || t.Pending.JobRef != ref {
				return nil
			}
			t.Pending.JobPolls++
			t.Pending.WakeAt = time.Now().UTC().Add(task.NextJobPoll(t.Pending.JobPolls))
			return nil
		})
		return false, ""
	}
	return true, jobOutcomeText(job)
}
