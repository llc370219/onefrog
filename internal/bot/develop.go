package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"onefrog/internal/devmodule"
	"onefrog/internal/model"
	"onefrog/internal/module"
	"onefrog/internal/store"
)

// developsModules reports whether item is the stock 自定义模块 bot.
func (a *Agent) developsModules(item model.Bot) bool {
	return item.ID == devmodule.BotID || item.Name == devmodule.BotName || item.Name == devmodule.LegacyBotName
}

func (a *Agent) developPrompt(ctx context.Context, bot model.Bot) string {
	var b strings.Builder
	b.WriteString(promptDevelop)
	b.WriteString(promptPriorityReply)
	a.writeOperationsModeOnly(&b, ctx)
	if !isWorker(ctx) {
		b.WriteString(promptAskUser)
		if isDirect(ctx) {
			a.writeHelpers(&b, ctx, bot.ID)
		} else {
			a.writePeers(&b, ctx, bot.ID)
			a.writePeerMemory(&b, ctx, bot.ID)
		}
	}
	writeExtra(&b, bot)
	b.WriteString(promptMemory)
	a.writeMailbox(&b, ctx, bot, isWorker(ctx))
	a.writeOwnMemory(&b, ctx, bot)
	a.writeServers(&b, ctx, "")
	a.writeAssets(&b, bot)
	a.writeDevModules(&b)
	return b.String()
}

func (a *Agent) writeOperationsModeOnly(b *strings.Builder, ctx context.Context) {
	mode := a.approvalMode(ctx)
	desc, ok := promptModes[mode]
	if !ok {
		desc = promptModes["smart"]
	}
	fmt.Fprintf(b, "\n当前审批模式：%s\n", desc)
}

func (a *Agent) writeDevModules(b *strings.Builder) {
	if a.DevModules == nil {
		return
	}
	rows := a.DevModules.List()
	if len(rows) == 0 {
		b.WriteString("\n当前还没有自定义模块。\n")
		return
	}
	b.WriteString("\n当前已安装的自定义模块：\n")
	for _, row := range rows {
		fmt.Fprintf(b, "- id=%s name=%s 记录=%d条", row.ID, row.Name, row.RecordCount)
		if row.Description != "" {
			b.WriteString(" 说明=" + row.Description)
		}
		b.WriteString("\n")
	}
}

func (a *Agent) writeAllModulesSummary(b *strings.Builder, ctx context.Context) {
	var stored []model.Module
	if a.Store != nil {
		stored, _ = a.Store.ListModules(ctx)
	}
	byID := map[string]model.Module{}
	for _, item := range stored {
		byID[item.ID] = item
	}
	b.WriteString("\n当前面板模块列表：\n")
	for _, item := range module.Catalog() {
		en := item.Enabled
		if s, ok := byID[item.ID]; ok {
			en = s.Enabled
		}
		fmt.Fprintf(b, "- [builtin] id=%s name=%s enabled=%v %s\n", item.ID, item.Name, en, item.Description)
	}
	if a.DevModules != nil {
		for _, custom := range a.DevModules.List() {
			en := true
			if s, ok := byID[custom.ID]; ok {
				en = s.Enabled
			}
			fmt.Fprintf(b, "- [custom] id=%s name=%s enabled=%v records=%d %s\n", custom.ID, custom.Name, en, custom.RecordCount, custom.Description)
		}
	}
}

func (a *Agent) developModuleCall(ctx context.Context, speaker model.Bot, call toolCall, tr *trace) string {
	ev := ToolEvent{Name: "develop_module"}
	defer func() {
		if tr != nil {
			tr.noteTool(speaker.Name, ev)
		}
	}()
	if a.DevModules == nil {
		ev.Output = "自定义模块未就绪"
		return ev.Output
	}
	var args struct {
		Action      string  `json:"action"`
		ID          string  `json:"id"`
		Name        *string `json:"name"`
		Description *string `json:"description"`
		HTML        *string `json:"html"`
		Enabled     *bool   `json:"enabled"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		ev.Output = "develop_module 参数不是有效的 JSON"
		return ev.Output
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		action = "list"
	}
	target := strings.TrimSpace(args.ID)
	if target == "" && args.Name != nil {
		target = strings.TrimSpace(*args.Name)
	}
	ev.Command = strings.TrimSpace(action + " " + target)
	if action != "list" && action != "inspect" {
		if msg := a.panelPlanBlocked(ctx); msg != "" {
			ev.Output = msg
			return ev.Output
		}
		if panelRejectsSecretFields(call.Function.Arguments) {
			ev.Output = "develop_module 不接受密码、私钥、API Key 等敏感字段，自定义模块不能保存密钥"
			return ev.Output
		}
	}

	switch action {
	case "list":
		rows := a.DevModules.List()
		if len(rows) == 0 {
			ev.OK, ev.Output = true, "目前还没有自定义模块。"
			return ev.Output
		}
		var b strings.Builder
		for _, r := range rows {
			en := true
			if a.Store != nil {
				if m, err := a.Store.GetModule(ctx, r.ID); err == nil {
					en = m.Enabled
				}
			}
			fmt.Fprintf(&b, "- id=%s name=%s enabled=%v records=%d updated=%s %s\n",
				r.ID, r.Name, en, r.RecordCount, r.UpdatedAt.Format(time.RFC3339), r.Description)
		}
		ev.OK, ev.Output = true, a.redact(ctx, b.String())
		return ev.Output

	case "inspect":
		item, err := a.DevModules.Get(target)
		if err != nil {
			ev.Output = err.Error()
			return ev.Output
		}
		en := true
		if a.Store != nil {
			if m, e := a.Store.GetModule(ctx, item.ID); e == nil {
				en = m.Enabled
			}
		}
		sample := item.Records
		if len(sample) > 5 {
			sample = sample[len(sample)-5:]
		}
		raw, _ := json.Marshal(map[string]any{
			"id":            item.ID,
			"name":          item.Name,
			"description":   item.Description,
			"enabled":       en,
			"record_count":  len(item.Records),
			"recent_sample": sample,
			"state":         item.State,
			"html":          item.HTML,
		})
		ev.OK, ev.Output = true, a.redact(ctx, string(raw))
		return ev.Output

	case "create":
		name := ""
		if args.Name != nil {
			name = a.redact(ctx, *args.Name)
		}
		desc := ""
		if args.Description != nil {
			desc = a.redact(ctx, *args.Description)
		}
		html := ""
		if args.HTML != nil {
			html = a.redact(ctx, *args.HTML)
		}
		if strings.Contains(name+desc+html, "敏感内容检查失败") {
			ev.Output = "敏感内容检查失败，模块未创建"
			return ev.Output
		}
		created, err := a.DevModules.Create(devmodule.Input{
			Name:        name,
			Description: desc,
			HTML:        html,
		})
		if err != nil {
			ev.Output = "模块没有创建：" + err.Error()
			return ev.Output
		}
		en := true
		if args.Enabled != nil {
			en = *args.Enabled
		}
		if a.Store != nil {
			_, _ = a.Store.SaveModule(ctx, model.Module{
				ID:          created.ID,
				Name:        created.Name,
				Version:     "1.0.0",
				Type:        "custom",
				Description: created.Description,
				Enabled:     en,
				CreatedAt:   created.CreatedAt,
			})
		}
		a.notePanel(ctx, speaker.ID, "", "新建自定义模块 "+created.Name, "success")
		ev.OK = true
		ev.Output = fmt.Sprintf("已创建自定义模块「%s」（id=%s），已挂载到「模块 → %s」和「自定义模块」工作台，面板显示=%v。", created.Name, created.ID, created.Name, en)
		return ev.Output

	case "update":
		old, err := a.DevModules.Get(strings.TrimSpace(args.ID))
		if err != nil && args.Name != nil {
			old, err = a.DevModules.Get(strings.TrimSpace(*args.Name))
		}
		if err != nil {
			ev.Output = err.Error()
			return ev.Output
		}
		what := fmt.Sprintf("修改自定义模块「%s」（id=%s）的界面或配置，保留已保存的状态与 %d 条记录。", old.Name, old.ID, len(old.Records))
		allow, why := a.approveHighRisk(ctx, speaker.ID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, what, []string{"会更新该自定义模块的前端界面代码"})
		if !allow {
			ev.Output = "没有修改模块：" + why
			return ev.Output
		}
		var patch devmodule.Patch
		if args.Name != nil {
			v := a.redact(ctx, *args.Name)
			patch.Name = &v
		}
		if args.Description != nil {
			v := a.redact(ctx, *args.Description)
			patch.Description = &v
		}
		if args.HTML != nil {
			v := a.redact(ctx, *args.HTML)
			patch.HTML = &v
		}
		updated, err := a.DevModules.Update(old.ID, patch)
		if err != nil {
			ev.Output = "模块没有更新：" + err.Error()
			return ev.Output
		}
		en := true
		if a.Store != nil {
			if m, e := a.Store.GetModule(ctx, updated.ID); e == nil {
				en = m.Enabled
			}
			if args.Enabled != nil {
				en = *args.Enabled
			}
			_, _ = a.Store.SaveModule(ctx, model.Module{
				ID:          updated.ID,
				Name:        updated.Name,
				Version:     "1.0.0",
				Type:        "custom",
				Description: updated.Description,
				Enabled:     en,
				CreatedAt:   updated.CreatedAt,
			})
		}
		a.notePanel(ctx, speaker.ID, "", "更新自定义模块 "+updated.Name, "success")
		ev.OK = true
		ev.Output = fmt.Sprintf("已更新自定义模块「%s」（id=%s），原有 %d 条记录已保留。", updated.Name, updated.ID, len(updated.Records))
		return ev.Output

	case "delete":
		old, err := a.DevModules.Get(target)
		if err != nil {
			ev.Output = err.Error()
			return ev.Output
		}
		what := fmt.Sprintf("删除自定义模块「%s」（id=%s）以及其中的状态和 %d 条记录。", old.Name, old.ID, len(old.Records))
		allow, why := a.approveHighRisk(ctx, speaker.ID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, what, []string{"会永久删除该自定义模块和它的记录"})
		if !allow {
			ev.Output = "没有删除模块：" + why
			return ev.Output
		}
		if _, err = a.DevModules.Delete(old.ID); err != nil {
			ev.Output = "模块没有删除：" + err.Error()
			return ev.Output
		}
		if a.Store != nil {
			_ = a.Store.DeleteModule(ctx, old.ID)
		}
		a.notePanel(ctx, speaker.ID, "", "删除自定义模块 "+old.Name, "success")
		ev.OK, ev.Output = true, fmt.Sprintf("已删除自定义模块「%s」（id=%s）。", old.Name, old.ID)
		return ev.Output

	default:
		ev.Output = "develop_module 的 action 只能是 list、inspect、create、update、delete"
		return ev.Output
	}
}

// manageModulesCall lets 模块管理 Bot list/enable/disable panel modules and manage records of custom modules.
func (a *Agent) manageModulesCall(ctx context.Context, speaker model.Bot, call toolCall, tr *trace) string {
	ev := ToolEvent{Name: "manage_modules"}
	defer func() {
		if tr != nil {
			tr.noteTool(speaker.Name, ev)
		}
	}()
	var args struct {
		Action   string         `json:"action"`
		ID       string         `json:"id"`
		RecordID string         `json:"record_id"`
		Data     map[string]any `json:"data"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		ev.Output = "manage_modules 参数不是有效的 JSON"
		return ev.Output
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		action = "list"
	}
	ev.Command = strings.TrimSpace(action + " " + args.ID)
	if action != "list" && action != "list_records" {
		if msg := a.panelPlanBlocked(ctx); msg != "" {
			ev.Output = msg
			return ev.Output
		}
	}
	switch action {
	case "list":
		var b strings.Builder
		a.writeAllModulesSummary(&b, ctx)
		ev.OK, ev.Output = true, strings.TrimSpace(a.redact(ctx, b.String()))
		return ev.Output
	case "enable", "disable":
		en := action == "enable"
		out, ok := a.panelSetModule(ctx, speaker.ID, strings.TrimSpace(args.ID), &en)
		ev.OK, ev.Output = ok, out
		return out
	case "list_records":
		if a.DevModules == nil {
			ev.Output = "自定义模块未就绪"
			return ev.Output
		}
		payload, err := a.DevModules.GetData(args.ID)
		if err != nil {
			ev.Output = err.Error()
			return ev.Output
		}
		recs := payload.Records
		if len(recs) > 30 {
			recs = recs[:30]
		}
		raw, _ := json.Marshal(map[string]any{"state": payload.State, "records": recs, "total_records": len(payload.Records)})
		ev.OK, ev.Output = true, a.redact(ctx, string(raw))
		return ev.Output
	case "add_record":
		if a.DevModules == nil {
			ev.Output = "自定义模块未就绪"
			return ev.Output
		}
		payload, err := a.DevModules.AddRecord(args.ID, args.Data)
		if err != nil {
			ev.Output = err.Error()
			return ev.Output
		}
		ev.OK, ev.Output = true, fmt.Sprintf("已新增记录，当前共 %d 条。", len(payload.Records))
		return ev.Output
	case "delete_record":
		if a.DevModules == nil {
			ev.Output = "自定义模块未就绪"
			return ev.Output
		}
		payload, err := a.DevModules.DeleteRecord(args.ID, args.RecordID)
		if err != nil {
			ev.Output = err.Error()
			return ev.Output
		}
		ev.OK, ev.Output = true, fmt.Sprintf("已删除记录，剩余 %d 条。", len(payload.Records))
		return ev.Output
	default:
		ev.Output = "manage_modules 的 action 只能是 list、enable、disable、list_records、add_record、delete_record"
		return ev.Output
	}
}

func (a *Agent) findAnyModule(ctx context.Context, idOrName string) (model.Module, bool) {
	q := strings.TrimSpace(idOrName)
	if q == "" {
		return model.Module{}, false
	}
	if item, ok := module.Find(q); ok {
		if a.Store != nil {
			if saved, err := a.Store.GetModule(ctx, item.ID); err == nil {
				return saved, true
			}
		}
		return item, true
	}
	if a.Store != nil {
		stored, err := a.Store.ListModules(ctx)
		if err == nil {
			for _, item := range stored {
				if item.ID == q || item.Name == q {
					return item, true
				}
			}
		}
	}
	if a.DevModules != nil {
		if cm, err := a.DevModules.Get(q); err == nil {
			return model.Module{
				ID:          cm.ID,
				Name:        cm.Name,
				Version:     "1.0.0",
				Type:        "custom",
				Description: cm.Description,
				Enabled:     true,
				CreatedAt:   cm.CreatedAt,
			}, true
		}
	}
	return model.Module{}, false
}

var _ = errors.Is

var developModuleTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "develop_module",
		"description": toolDevelopModuleDesc,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":      map[string]any{"type": "string", "enum": []string{"list", "inspect", "create", "update", "delete"}},
				"id":          map[string]string{"type": "string", "description": "inspect、update、delete 时填写自定义模块的 id 或名称"},
				"name":        map[string]string{"type": "string", "description": "create 或 update：模块名称，1–40 字，例如 打卡"},
				"description": map[string]string{"type": "string", "description": "create 或 update：一句话说明模块功能"},
				"html":        map[string]string{"type": "string", "description": "create 或 update：完整的交互界面 HTML+CSS+JS 代码片段，使用 window.onefrog 持久化状态和记录"},
				"enabled":     map[string]string{"type": "boolean", "description": "是否在面板首页显示，默认 true"},
			},
			"required": []string{"action"},
		},
	},
}

var manageModulesTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "manage_modules",
		"description": "查看和开关面板模块（含内置模块与自定义模块），以及读写自定义模块里的记录。不要用它新建界面，新建界面交给自定义模块。",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":    map[string]any{"type": "string", "enum": []string{"list", "enable", "disable", "list_records", "add_record", "delete_record"}},
				"id":        map[string]string{"type": "string", "description": "模块 id 或名称"},
				"record_id": map[string]string{"type": "string", "description": "仅 delete_record：要删除的记录 id"},
				"data":      map[string]any{"type": "object", "description": "仅 add_record：要新增的记录字段对象，例如 {\"note\":\"早班打卡\"}"},
			},
			"required": []string{"action"},
		},
	},
}
