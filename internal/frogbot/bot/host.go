package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/state"
	"onefrog/internal/frogbot/vault"
)

// Host 是嵌入宿主（OneFrog）提供的能力。设置后 Frog Bot 不再使用自己的
// 登录、模型设置、本地保险箱和定时器，界面也只显示宿主的那一套。
type Host struct {
	Vault       Vault           // 宿主保险箱
	Automations HostAutomations // 宿主自动化（定时任务）
}

// Vault 是 Frog Bot 需要的密钥库能力。本地模式由 vault.Vault 实现，嵌入模式由宿主实现。
type Vault interface {
	onefrog.Secrets
	RedactBytes([]byte) []byte
	List() []vault.Info
	Has(name string) bool
	Set(name, value, desc string, scope *vault.Scope) error
	Delete(name string) error
	SealAPIKey(key string) (string, error)
	OpenAPIKey(sealed string) (string, error)
}

// HostAutomation 是宿主自动化给聊天层看的摘要（没有密钥）。
type HostAutomation struct {
	ID         string
	Title      string
	Prompt     string
	Schedule   string // 人能读懂的节奏描述
	Enabled    bool
	NextRun    string
	LastStatus string
}

// AutomationDraft 是聊天层提出的定时计划。
type AutomationDraft struct {
	Title    string
	Prompt   string
	Kind     string // once | daily | weekly | interval
	AtLocal  string // once：2006-01-02 15:04
	Hour     int
	Minute   int
	Weekday  int // weekly：0=周日 … 6=周六
	Interval int // interval：分钟
}

// HostAutomations 由宿主实现；所有变更都已经过 Frog Bot 的审批卡片。
type HostAutomations interface {
	List() ([]HostAutomation, error)
	Create(d AutomationDraft) (HostAutomation, error)
	SetEnabled(id string, enabled bool) (HostAutomation, error)
	Delete(id string) error
	RunNow(id string) error
}

// Embedded 表示运行在宿主里。
func (b *Bot) Embedded() bool { return b.cfg.Host != nil }

func (b *Bot) writeSealedMemory(path, text, mode string) error {
	var content string
	if mode != "replace" {
		old, err := state.ReadFile(b.cfg.Codec, path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		content = strings.TrimRight(string(old), "\n") + "\n- " + strings.TrimSpace(text) + "\n"
	} else {
		content = strings.TrimSpace(text) + "\n"
	}
	if len(content) > 1<<20 {
		return errors.New("记忆文件最多 1 MiB")
	}
	content = b.vault.Redact(content)
	if b.cfg.Redact != nil {
		content = b.cfg.Redact(content)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return state.WriteFile(b.cfg.Codec, path, []byte(content))
}

// planTools 返回定时计划工具：本地模式用内置调度器，嵌入模式改用宿主自动化（工具名不变）。
func (b *Bot) planTools() []onefrog.Tool {
	if b.cfg.Host == nil {
		return b.scheduleTools()
	}
	if b.cfg.Host.Automations == nil {
		return nil
	}
	return b.hostScheduleTools(b.cfg.Host.Automations)
}

func (b *Bot) findAutomation(h HostAutomations, id string) (HostAutomation, error) {
	id = strings.TrimSpace(id)
	list, err := h.List()
	if err != nil {
		return HostAutomation{}, err
	}
	var hits []HostAutomation
	for _, a := range list {
		if a.ID == id {
			return a, nil
		}
		if id != "" && (strings.HasPrefix(a.ID, id) || a.Title == id) {
			hits = append(hits, a)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return HostAutomation{}, fmt.Errorf("没有找到计划 %q，先用 list_schedules 看看", id)
}

func describeAutomations(list []HostAutomation) string {
	if len(list) == 0 {
		return "还没有交给 Frog Bot 的定时计划（OneFrog「自动化」）。"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "共 %d 个定时计划（OneFrog「自动化」，执行者 Frog Bot）：\n", len(list))
	for _, a := range list {
		en := "暂停"
		if a.Enabled {
			en = "启用"
		}
		next := a.NextRun
		if next == "" {
			next = "无下次"
		}
		status := a.LastStatus
		if status == "" {
			status = "未运行"
		}
		fmt.Fprintf(&sb, "- %s [%s] 「%s」%s；下次 %s；上次状态 %s\n", a.ID, en, a.Title, a.Schedule, next, status)
	}
	return sb.String()
}

func (b *Bot) hostScheduleTools(h HostAutomations) []onefrog.Tool {
	tool := func(name, desc, schema string, fn func(json.RawMessage) (string, error)) onefrog.Tool {
		return onefrog.PassSecrets{FuncTool: onefrog.FuncTool{ToolName: name, Desc: desc, JSONSchema: schema, RiskFn: safe,
			Fn: func(_ context.Context, _ onefrog.ToolEnv, a json.RawMessage) (string, error) { return fn(a) }}}
	}
	idSchema := `{"type":"object","properties":{"schedule_id":{"type":"string"}},"required":["schedule_id"]}`
	type idArgs struct {
		ID string `json:"schedule_id"`
	}
	toggle := func(op string, on bool) func(json.RawMessage) (string, error) {
		return func(a json.RawMessage) (string, error) {
			v, _ := readArgs[idArgs](a)
			it, err := b.findAutomation(h, v.ID)
			if err != nil {
				return "", err
			}
			return b.scheduleMutate(op, a, func() (string, error) {
				it2, err := h.SetEnabled(it.ID, on)
				if err != nil {
					return "", err
				}
				word := map[bool]string{true: "已恢复", false: "已暂停"}[on]
				return fmt.Sprintf("%s计划 %s「%s」", word, it2.ID, it2.Title), nil
			})
		}
	}
	return []onefrog.Tool{
		tool("list_schedules", "列出交给你执行的定时计划（保存在 OneFrog「自动化」里）：ID、启用状态、节奏、下次触发、上次状态。",
			`{"type":"object","properties":{}}`,
			func(json.RawMessage) (string, error) {
				list, err := h.List()
				if err != nil {
					return "", err
				}
				return describeAutomations(list), nil
			}),
		tool("create_schedule", "创建定时计划，保存到 OneFrog「自动化」，到点由你在后台执行。会弹出审批卡片，用户确认后才生效。"+
			"kind=once 用 at_local（2006-01-02 15:04）；daily 用 hour/minute；weekly 用 hour/minute + weekday（0=周日…6=周六，只能选一天）；interval 用 interval_minutes（15 分钟到 7 天）。时间按 OneFrog 服务器的时区。",
			`{"type":"object","properties":{"title":{"type":"string"},"prompt":{"type":"string"},"kind":{"type":"string","enum":["once","daily","weekly","interval"]},"at_local":{"type":"string"},"hour":{"type":"integer"},"minute":{"type":"integer"},"weekday":{"type":"integer"},"interval_minutes":{"type":"integer"}},"required":["title","prompt","kind"]}`,
			func(a json.RawMessage) (string, error) {
				v, err := readArgs[struct {
					Title    string `json:"title"`
					Prompt   string `json:"prompt"`
					Kind     string `json:"kind"`
					AtLocal  string `json:"at_local"`
					Hour     int    `json:"hour"`
					Minute   int    `json:"minute"`
					Weekday  int    `json:"weekday"`
					Interval int    `json:"interval_minutes"`
				}](a)
				if err != nil {
					return "", err
				}
				d := AutomationDraft{Title: strings.TrimSpace(v.Title), Prompt: strings.TrimSpace(v.Prompt), Kind: v.Kind,
					AtLocal: v.AtLocal, Hour: v.Hour, Minute: v.Minute, Weekday: v.Weekday, Interval: v.Interval}
				if d.Title == "" || d.Prompt == "" || d.Kind == "" {
					return "", errors.New("需要 title、prompt、kind")
				}
				return b.scheduleMutate("create", a, func() (string, error) {
					it, err := h.Create(d)
					if err != nil {
						return "", err
					}
					next := it.NextRun
					if next == "" {
						next = "无"
					}
					return fmt.Sprintf("已创建计划 %s「%s」%s；下次 %s。用户可以在 OneFrog「自动化」里查看和修改。", it.ID, it.Title, it.Schedule, next), nil
				})
			}),
		tool("pause_schedule", "暂停一个定时计划（仍保留，不会到点触发）。需要用户确认。", idSchema, toggle("pause", false)),
		tool("resume_schedule", "恢复一个已暂停的定时计划。需要用户确认。", idSchema, toggle("resume", true)),
		tool("delete_schedule", "删除一个定时计划。需要用户确认。", idSchema,
			func(a json.RawMessage) (string, error) {
				v, _ := readArgs[idArgs](a)
				it, err := b.findAutomation(h, v.ID)
				if err != nil {
					return "", err
				}
				return b.scheduleMutate("delete", a, func() (string, error) {
					if err := h.Delete(it.ID); err != nil {
						return "", err
					}
					return fmt.Sprintf("已删除计划 %s「%s」", it.ID, it.Title), nil
				})
			}),
		tool("run_schedule_now", "立刻按某个计划的内容运行一次（仍走任务队列与审批）。", idSchema,
			func(a json.RawMessage) (string, error) {
				v, _ := readArgs[idArgs](a)
				it, err := b.findAutomation(h, v.ID)
				if err != nil {
					return "", err
				}
				if err := h.RunNow(it.ID); err != nil {
					return "", err
				}
				return fmt.Sprintf("已让计划 %s「%s」立刻运行一次，完成后会通知你。", it.ID, it.Title), nil
			}),
	}
}

// ---------- 给宿主自动化用的任务接口 ----------

// SubmitTask 按固定 ID 排一个后台任务；同一个 ID 重复提交返回已有任务（幂等）。
func (b *Bot) SubmitTask(id, title, prompt string) (string, error) {
	if id == "" {
		return "", errors.New("任务 ID 为空")
	}
	if _, ok := b.store.Task(id); ok {
		return id, nil
	}
	return b.CreateTask(prompt, title, TaskOpts{ID: id})
}

// TaskState 是宿主观察任务用的摘要。
type TaskState struct {
	Found   bool
	Status  string // queued | running | paused | completed | failed | cancelled
	Waiting bool   // 正在等用户审批或回答
	Summary string
	Error   string
}

func (b *Bot) TaskState(id string) TaskState {
	t, ok := b.store.Task(id)
	if !ok {
		return TaskState{}
	}
	st := TaskState{Found: true, Status: t.Status, Summary: t.Summary, Error: t.Error}
	for _, a := range b.store.PendingApprovals() {
		if a.TaskID == id {
			st.Waiting = true
		}
	}
	return st
}
