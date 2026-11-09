package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/scheduler"
)

// botHost 把调度器接到任务队列、事件流和聊天层通知。
type botHost struct{ b *Bot }

func (h botHost) Dispatch(p scheduler.Plan, c scheduler.Claim) (string, error) {
	prompt := p.Prompt
	if !strings.Contains(prompt, "【定时任务") {
		prompt = fmt.Sprintf("【定时任务「%s」· 预定 %s】\n%s",
			p.Title, c.IntendedAt.In(schedulerMustLoc(p.Spec.Timezone)).Format("2006-01-02 15:04 MST"), p.Prompt)
	}
	id, err := h.b.CreateTask(prompt, p.Title, TaskOpts{ID: c.TaskID, ScheduleID: p.ID})
	return id, err
}

func (h botHost) Lookup(taskID string) scheduler.TaskStatus {
	t, ok := h.b.store.Task(taskID)
	if !ok {
		return scheduler.TaskMissing
	}
	switch t.Status {
	case "queued", "running", "paused":
		return scheduler.TaskOpen
	case "completed":
		return scheduler.TaskCompleted
	case "failed":
		return scheduler.TaskFailed
	case "cancelled":
		return scheduler.TaskCancelled
	default:
		return scheduler.TaskUnknown
	}
}

func (h botHost) Notify(kind, text string, _ scheduler.Plan, _ *scheduler.Claim) {
	h.b.notify(text)
}

func (h botHost) Emit(kind string, payload any) {
	h.b.store.Append(kind, payload, false)
}

func schedulerMustLoc(tz string) *time.Location {
	loc, err := scheduler.LoadLocation(tz)
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return loc
}

func (b *Bot) initScheduler() error {
	st, err := scheduler.OpenStoreWithCodec(b.cfg.DataDir, b.cfg.Codec)
	if err != nil {
		return err
	}
	b.sched = scheduler.NewEngine(st, botHost{b}, scheduler.Config{Enter: b.cfg.Enter})
	return nil
}

// Schedules 列出全部计划（给 HTTP / 聊天层）。
func (b *Bot) Schedules() []map[string]any {
	if b.sched == nil {
		return nil
	}
	list := b.sched.Store.List()
	out := make([]map[string]any, len(list))
	for i, p := range list {
		out[i] = p.Public()
	}
	return out
}

func (b *Bot) findSchedule(id string) (scheduler.Plan, error) {
	id = strings.TrimSpace(id)
	if p, ok := b.sched.Store.Get(id); ok {
		return p, nil
	}
	var hits []scheduler.Plan
	for _, p := range b.sched.Store.List() {
		if id != "" && (strings.HasSuffix(p.ID, id) || strings.HasPrefix(p.ID, id) || p.Title == id) {
			hits = append(hits, p)
		}
	}
	if len(hits) == 1 {
		return hits[0], nil
	}
	return scheduler.Plan{}, fmt.Errorf("没有找到计划 %q，先用 list_schedules 看看", id)
}

func (b *Bot) listSchedulesText() string {
	list := b.sched.Store.List()
	if len(list) == 0 {
		return "还没有定时计划。"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "共 %d 个定时计划：\n", len(list))
	for _, p := range list {
		en := "暂停"
		if p.Enabled {
			en = "启用"
		}
		next := "无下次"
		if p.NextRun != nil {
			next = p.NextRun.In(schedulerMustLoc(p.Spec.Timezone)).Format("01-02 15:04 MST")
		}
		fmt.Fprintf(&sb, "- %s [%s] 「%s」%s；下次 %s；上次状态 %s\n",
			p.ID, en, p.Title, scheduler.DescribeSpec(p.Spec), next, p.LastStatus)
	}
	return sb.String()
}

// scheduleMutate 通过与写文件相同的审批管线变更计划；聊天里说"同意"不算数。
func (b *Bot) scheduleMutate(op string, args json.RawMessage, apply func() (string, error)) (string, error) {
	tool := onefrog.FuncTool{
		ToolName: "schedule_" + op, Desc: "定时计划：" + op,
		JSONSchema: `{"type":"object"}`,
		RiskFn:     func(json.RawMessage) onefrog.Risk { return onefrog.RiskDanger },
		Fn: func(_ context.Context, _ onefrog.ToolEnv, _ json.RawMessage) (string, error) {
			return apply()
		},
	}
	task := onefrog.Task{ID: onefrog.NewID("chat"), BotID: b.cfg.Name, Input: "聊天层变更定时计划",
		Workspace: b.cfg.Workspace, Policy: b.cfg.ChatPolicy}
	v, _ := b.guard.Classify(task, tool, args)
	switch v.Decision {
	case onefrog.Deny:
		return onefrog.DeniedMessage(v), nil
	case onefrog.Allow:
		return apply()
	}
	b.goWork(func() {
		release, err := b.enter()
		if err != nil {
			return
		}
		defer release()
		emit := func(typ onefrog.EventType, data any) {
			b.store.Append("agent", onefrog.Event{TaskID: task.ID, Type: typ, Time: time.Now(), Data: data}, false)
		}
		verdict := b.guard.Check(b.ctx, task, tool, args, emit)
		if b.ctx.Err() != nil {
			return
		}
		if verdict.Decision != onefrog.Allow {
			b.notify(fmt.Sprintf("定时计划操作 %s 没有被批准（%s）。没有改动。不要重试绕过；简短告诉用户即可。", op, verdict.Reason))
			return
		}
		out, err := apply()
		if err != nil {
			b.notify(fmt.Sprintf("定时计划操作 %s 已批准但执行出错：%v", op, err))
			return
		}
		b.notify(fmt.Sprintf("定时计划操作 %s 已批准并完成：%s。用一句话告诉用户。", op, out))
	})
	return "这个定时计划变更需要用户确认，审批卡片已显示。批准后会自动执行，结果会通知你。现在用一句话请用户在卡片上确认，然后结束本轮。不要说已经改好了。", nil
}

func parseScheduleDraft(a json.RawMessage) (scheduler.Draft, error) {
	v, err := readArgs[struct {
		ID       string `json:"schedule_id"`
		Title    string
		Prompt   string
		Kind     string
		Timezone string
		AtLocal  string `json:"at_local"` // 2006-01-02 15:04 或 15:04（今天/明天由后端按 now 理解：必须带日期）
		Hour     int
		Minute   int
		Weekdays []int
		Overlap  string
		Enabled  *bool
	}](a)
	if err != nil {
		return scheduler.Draft{}, err
	}
	if v.Title == "" || v.Prompt == "" || v.Kind == "" {
		return scheduler.Draft{}, errors.New("需要 title、prompt、kind")
	}
	tz := v.Timezone
	if tz == "" {
		tz = scheduler.DefaultTimezone
	}
	spec := scheduler.Spec{Kind: scheduler.Kind(v.Kind), Timezone: tz, Hour: v.Hour, Minute: v.Minute}
	switch spec.Kind {
	case scheduler.Once:
		if v.AtLocal == "" {
			return scheduler.Draft{}, errors.New("单次计划需要 at_local（例如 2026-11-10 09:00）")
		}
		t, err := time.ParseInLocation("2006-01-02 15:04", v.AtLocal, schedulerMustLoc(tz))
		if err != nil {
			t, err = time.ParseInLocation("2006-01-02 15:04:05", v.AtLocal, schedulerMustLoc(tz))
		}
		if err != nil {
			return scheduler.Draft{}, fmt.Errorf("at_local 格式不对：%v", err)
		}
		spec.AtLocal = t
	case scheduler.Weekly:
		if len(v.Weekdays) == 0 {
			return scheduler.Draft{}, errors.New("每周计划需要 weekdays（0=周日 … 6=周六）")
		}
		for _, d := range v.Weekdays {
			spec.Weekdays = append(spec.Weekdays, time.Weekday(d))
		}
	case scheduler.Daily:
	default:
		return scheduler.Draft{}, fmt.Errorf("kind 只能是 once/daily/weekly")
	}
	en := true
	if v.Enabled != nil {
		en = *v.Enabled
	}
	return scheduler.Draft{ID: v.ID, Title: v.Title, Prompt: v.Prompt, Spec: spec, Overlap: v.Overlap, Enabled: en}, nil
}

func (b *Bot) scheduleTools() []onefrog.Tool {
	tool := func(name, desc, schema string, fn func(json.RawMessage) (string, error)) onefrog.Tool {
		return onefrog.PassSecrets{FuncTool: onefrog.FuncTool{ToolName: name, Desc: desc, JSONSchema: schema, RiskFn: safe,
			Fn: func(_ context.Context, _ onefrog.ToolEnv, a json.RawMessage) (string, error) { return fn(a) }}}
	}
	draftSchema := `{"type":"object","properties":{"schedule_id":{"type":"string"},"title":{"type":"string"},"prompt":{"type":"string"},"kind":{"type":"string","enum":["once","daily","weekly"]},"timezone":{"type":"string"},"at_local":{"type":"string"},"hour":{"type":"integer"},"minute":{"type":"integer"},"weekdays":{"type":"array","items":{"type":"integer"}},"overlap":{"type":"string","enum":["skip","queue"]},"enabled":{"type":"boolean"}},"required":["title","prompt","kind"]}`
	return []onefrog.Tool{
		tool("list_schedules", "列出全部定时计划：ID、启用状态、节奏、时区、下次触发、上次状态。",
			`{"type":"object","properties":{}}`,
			func(json.RawMessage) (string, error) { return b.listSchedulesText(), nil }),
		tool("get_schedule", "查看一个定时计划的详情。",
			`{"type":"object","properties":{"schedule_id":{"type":"string"}},"required":["schedule_id"]}`,
			func(a json.RawMessage) (string, error) {
				v, _ := readArgs[struct {
					ID string `json:"schedule_id"`
				}](a)
				p, err := b.findSchedule(v.ID)
				if err != nil {
					return "", err
				}
				raw, _ := json.MarshalIndent(p.Public(), "", "  ")
				return string(raw), nil
			}),
		tool("create_schedule", "创建定时计划（once/daily/weekly）。会弹出审批卡片，用户确认后才生效。timezone 默认 Asia/Shanghai。at_local 用于 once（2006-01-02 15:04）；daily/weekly 用 hour/minute；weekly 还要 weekdays（0=周日…6=周六）。overlap=skip（默认，上一趟未完就跳过）或 queue。",
			draftSchema,
			func(a json.RawMessage) (string, error) {
				d, err := parseScheduleDraft(a)
				if err != nil {
					return "", err
				}
				d.ID = ""
				return b.scheduleMutate("create", a, func() (string, error) {
					p, err := b.sched.Upsert(d)
					if err != nil {
						return "", err
					}
					next := "无"
					if p.NextRun != nil {
						next = p.NextRun.In(schedulerMustLoc(p.Spec.Timezone)).Format("2006-01-02 15:04 MST")
					}
					return fmt.Sprintf("已创建计划 %s「%s」%s；下次 %s", p.ID, p.Title, scheduler.DescribeSpec(p.Spec), next), nil
				})
			}),
		tool("update_schedule", "更新已有定时计划（需要 schedule_id）。会弹出审批卡片。",
			draftSchema,
			func(a json.RawMessage) (string, error) {
				d, err := parseScheduleDraft(a)
				if err != nil {
					return "", err
				}
				if d.ID == "" {
					return "", errors.New("更新需要 schedule_id")
				}
				if _, err := b.findSchedule(d.ID); err != nil {
					return "", err
				}
				return b.scheduleMutate("update", a, func() (string, error) {
					p, err := b.sched.Upsert(d)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("已更新计划 %s「%s」", p.ID, p.Title), nil
				})
			}),
		tool("pause_schedule", "暂停一个定时计划（仍保留，不会到点触发）。需要用户确认。",
			`{"type":"object","properties":{"schedule_id":{"type":"string"}},"required":["schedule_id"]}`,
			func(a json.RawMessage) (string, error) {
				v, _ := readArgs[struct {
					ID string `json:"schedule_id"`
				}](a)
				p, err := b.findSchedule(v.ID)
				if err != nil {
					return "", err
				}
				return b.scheduleMutate("pause", a, func() (string, error) {
					p2, err := b.sched.SetEnabled(p.ID, false)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("已暂停计划 %s「%s」", p2.ID, p2.Title), nil
				})
			}),
		tool("resume_schedule", "恢复一个已暂停的定时计划。需要用户确认。",
			`{"type":"object","properties":{"schedule_id":{"type":"string"}},"required":["schedule_id"]}`,
			func(a json.RawMessage) (string, error) {
				v, _ := readArgs[struct {
					ID string `json:"schedule_id"`
				}](a)
				p, err := b.findSchedule(v.ID)
				if err != nil {
					return "", err
				}
				return b.scheduleMutate("resume", a, func() (string, error) {
					p2, err := b.sched.SetEnabled(p.ID, true)
					if err != nil {
						return "", err
					}
					next := ""
					if p2.NextRun != nil {
						next = "；下次 " + p2.NextRun.In(schedulerMustLoc(p2.Spec.Timezone)).Format("2006-01-02 15:04 MST")
					}
					return fmt.Sprintf("已恢复计划 %s「%s」%s", p2.ID, p2.Title, next), nil
				})
			}),
		tool("delete_schedule", "删除一个定时计划。需要用户确认。",
			`{"type":"object","properties":{"schedule_id":{"type":"string"}},"required":["schedule_id"]}`,
			func(a json.RawMessage) (string, error) {
				v, _ := readArgs[struct {
					ID string `json:"schedule_id"`
				}](a)
				p, err := b.findSchedule(v.ID)
				if err != nil {
					return "", err
				}
				return b.scheduleMutate("delete", a, func() (string, error) {
					if err := b.sched.Remove(p.ID); err != nil {
						return "", err
					}
					return fmt.Sprintf("已删除计划 %s「%s」", p.ID, p.Title), nil
				})
			}),
		tool("run_schedule_now", "立刻按某个计划的内容派一次后台任务（不消耗正规触发点；仍走任务队列与审批）。",
			`{"type":"object","properties":{"schedule_id":{"type":"string"}},"required":["schedule_id"]}`,
			func(a json.RawMessage) (string, error) {
				v, _ := readArgs[struct {
					ID string `json:"schedule_id"`
				}](a)
				p, err := b.findSchedule(v.ID)
				if err != nil {
					return "", err
				}
				c, err := b.sched.RunNow(p.ID)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("已立刻派发任务 %s（来自计划 %s）。", c.TaskID, p.ID), nil
			}),
	}
}
