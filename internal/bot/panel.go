package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/module"
	"onefrog/internal/onboarding"
	"onefrog/internal/preferences"
	"onefrog/internal/remote"
	"onefrog/internal/store"
	"onefrog/internal/update"
)

// PanelUpdates is the release checker the assistant can read. Apply only
// writes a version tag for the host helper; it does not talk to Docker.
type PanelUpdates interface {
	Status() update.Status
	CheckNow(context.Context) (update.Status, error)
	Apply() (string, error)
}

func (a *Agent) settingsWriter() *preferences.Manager {
	if a.SettingsLive != nil {
		return a.SettingsLive
	}
	return a.Settings
}

func (a *Agent) panelAct(ctx context.Context, speaker model.Bot, call toolCall) ToolEvent {
	ev := ToolEvent{Name: "panel"}
	var args struct {
		Action  string `json:"action"`
		ID      string `json:"id"`
		Enabled *bool  `json:"enabled"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		ev.Output = "panel 参数不是有效的 JSON"
		return ev
	}
	args.Action = strings.TrimSpace(args.Action)
	args.ID = strings.TrimSpace(args.ID)
	ev.Command = panelCommand(args.Action, args.ID, args.Enabled)
	switch args.Action {
	case "overview":
		ev.OK = true
		ev.Output = a.panelOverview(ctx, speaker)
	case "set_module":
		ev.Output, ev.OK = a.panelSetModule(ctx, speaker.ID, args.ID, args.Enabled)
	case "set_bot":
		ev.Output, ev.OK = a.panelSetBot(ctx, speaker, args.ID, args.Enabled)
	case "test_server":
		ev.ServerID, ev.Output, ev.OK = a.panelTestServer(ctx, speaker.ID, args.ID)
	case "delete_server":
		ev.ServerID, ev.Output, ev.OK = a.panelDeleteServer(ctx, speaker.ID, args.ID, probeChoice(call.Function.Arguments))
	case "check_update":
		ev.Output, ev.OK = a.panelCheckUpdate(ctx)
	case "apply_update":
		ev.Output, ev.OK = a.panelApplyUpdate(ctx, speaker.ID)
	default:
		ev.Output = "panel 的 action 只能是 overview、set_module、set_bot、test_server、delete_server、check_update、apply_update"
	}
	ev.Output = clip(ev.Output, 2000)
	return ev
}

func panelCommand(action, id string, enabled *bool) string {
	switch action {
	case "overview":
		return "查看面板"
	case "set_module":
		if enabled != nil && *enabled {
			return "启用模块 " + id
		}
		return "关闭模块 " + id
	case "set_bot":
		if enabled != nil && *enabled {
			return "启用 Bot " + id
		}
		return "停用 Bot " + id
	case "test_server":
		return "测试服务器 " + id
	case "delete_server":
		return "删除服务器 " + id
	case "check_update":
		return "检查更新"
	case "apply_update":
		return "升级面板"
	default:
		return action
	}
}

func (a *Agent) panelPlanBlocked(ctx context.Context) string {
	if a.approvalMode(ctx) == preferences.ModePlan {
		return "仅规划模式，不能改面板，也不能测试连接。可以查看总览和检查更新。"
	}
	return ""
}

func (a *Agent) panelOverview(ctx context.Context, speaker model.Bot) string {
	connected, modelLine := a.modelLine(ctx, speaker)
	servers, _ := a.Store.ListServers(ctx)
	type serverRow struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Host   string `json:"host"`
		Port   int    `json:"port"`
		Status string `json:"status"`
		Role   string `json:"role,omitempty"`
	}
	rows := make([]serverRow, 0, len(servers))
	for _, item := range servers {
		rows = append(rows, serverRow{ID: item.ID, Name: item.Name, Host: item.Host, Port: item.Port, Status: item.Status, Role: item.Role})
	}
	stored, _ := a.Store.ListModules(ctx)
	byID := map[string]model.Module{}
	for _, item := range stored {
		byID[item.ID] = item
	}
	type moduleRow struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	mods := make([]moduleRow, 0, len(module.Catalog()))
	for _, item := range module.Catalog() {
		enabled := item.Enabled
		if saved, ok := byID[item.ID]; ok {
			enabled = saved.Enabled
		}
		mods = append(mods, moduleRow{ID: item.ID, Name: item.Name, Enabled: enabled})
	}
	bots, _ := a.Store.ListBots(ctx)
	type botRow struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	botRows := make([]botRow, 0, len(bots))
	for _, item := range bots {
		enabled := true
		if writer := a.settingsWriter(); writer != nil {
			enabled = writer.BotEnabled(item.ID)
		}
		botRows = append(botRows, botRow{ID: item.ID, Name: item.Name, Enabled: enabled})
	}
	var upd any
	if a.Updates != nil {
		st := a.Updates.Status()
		upd = map[string]any{
			"current": st.Current, "latest": st.Latest, "available": st.Available,
			"can_apply": st.CanApply, "error": st.Error,
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"model_connected": connected,
		"model":           modelLine,
		"servers":         rows,
		"modules":         mods,
		"bots":            botRows,
		"update":          upd,
	})
	return modelLine + "\n" + string(raw)
}

func (a *Agent) modelLine(ctx context.Context, speaker model.Bot) (bool, string) {
	if a.Settings == nil {
		if strings.TrimSpace(speaker.BaseURL) == "" || strings.TrimSpace(speaker.ModelName) == "" {
			return false, "模型还没接上。请在设置里添加渠道、拉取模型并保存。"
		}
		return true, "模型已接上：" + speaker.ModelName
	}
	route := a.Settings.RouteFor(speaker, false)
	if !isWorker(ctx) && options(ctx).ModelRoute != nil {
		route = *options(ctx).ModelRoute
	}
	if route.ProviderID == "" || strings.TrimSpace(route.Model) == "" {
		return false, "模型还没接上。请在设置里添加渠道、拉取模型并保存。"
	}
	p, err := a.Settings.Provider(route.ProviderID)
	if err != nil {
		return false, "模型渠道不可用：" + err.Error()
	}
	label := p.Name + " / " + route.Model
	if strings.HasPrefix(p.AuthKind, "oauth-") {
		if a.OAuth == nil {
			return false, "渠道 " + label + " 用的是账号登录，但登录通道没有接到助理。"
		}
		status := "disconnected"
		for _, ac := range a.OAuth.Accounts(options(ctx).OwnerID) {
			if ac.Kind == p.AuthKind {
				status = ac.Status
			}
		}
		if status != "connected" {
			return false, "渠道 " + label + " 的账号还没登录成功（" + status + "）。请在设置里完成登录并保存。"
		}
		return true, "模型已接上：" + label
	}
	if p.AuthRequired && !p.HasAPIKey {
		return false, "渠道 " + label + " 还没有 API Key。"
	}
	return true, "模型已接上：" + label
}

func (a *Agent) panelSetModule(ctx context.Context, botID, id string, enabled *bool) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	if enabled == nil {
		return "set_module 需要 enabled", false
	}
	item, ok := module.Find(id)
	if !ok {
		return "没有这个模块", false
	}
	if _, err := a.Store.GetModule(ctx, item.ID); err != nil {
		item.Enabled = *enabled
		if _, err := a.Store.SaveModule(ctx, item); err != nil {
			return "模块没有写成：" + err.Error(), false
		}
	} else if _, err := a.Store.SetModuleEnabled(ctx, item.ID, *enabled); err != nil {
		return "模块没有改成：" + err.Error(), false
	}
	verb := "已关闭"
	if *enabled {
		verb = "已启用"
	}
	a.notePanel(ctx, botID, "", verb+"模块 "+item.Name, "success")
	return fmt.Sprintf("%s模块 %s（id=%s）。面板会在这一轮结束后刷新。", verb, item.Name, item.ID), true
}

func (a *Agent) panelSetBot(ctx context.Context, speaker model.Bot, id string, enabled *bool) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	if enabled == nil {
		return "set_bot 需要 enabled", false
	}
	target, err := a.findBot(ctx, id)
	if err != nil {
		return "没有这个 Bot", false
	}
	if target.ID == speaker.ID && !*enabled {
		return "不能停用当前正在使用的主控", false
	}
	writer := a.settingsWriter()
	if writer == nil {
		return "没有设置存储，不能改 Bot 开关", false
	}
	if err := writer.SetBotEnabled(target.ID, *enabled); err != nil {
		return "Bot 开关没有保存：" + err.Error(), false
	}
	verb := "已停用"
	if *enabled {
		verb = "已启用"
	}
	a.notePanel(ctx, speaker.ID, "", verb+" Bot "+target.Name, "success")
	return fmt.Sprintf("%s Bot %s（id=%s）。", verb, target.Name, target.ID), true
}

func (a *Agent) panelTestServer(ctx context.Context, botID, id string) (string, string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return "", msg, false
	}
	if a.SSH == nil {
		return "", "没有 SSH 执行器", false
	}
	rec, err := a.serverByName(ctx, id)
	if err != nil {
		return "", "没有这台服务器", false
	}
	plain := ""
	if rec.EncryptedKey != "" {
		if a.Box == nil {
			return rec.ID, "登录密钥无法解密", false
		}
		plain, err = a.Box.Decrypt(rec.EncryptedKey)
		if err != nil {
			return rec.ID, "登录密钥无法解密", false
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(a.execution().CommandTimeoutSeconds)*time.Second)
	defer cancel()
	out, fp, err := a.SSH.Run(runCtx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, "echo ok")
	out = clip(a.redact(ctx, out), 500)
	switch {
	case errors.Is(err, remote.ErrHostKeyUnknown):
		return rec.ID, fmt.Sprintf("%s 还不能登录：主机指纹尚未确认。指纹 %s。请用户在服务器页面核对后再确认，不要自行接受这个指纹。", rec.Name, fp), false
	case errors.Is(err, remote.ErrHostKeyMismatch):
		rec.Status = "host_key_mismatch"
		_, _ = a.Store.UpdateServer(ctx, rec)
		a.notePanel(ctx, botID, rec.ID, "测试 "+rec.Name+"：指纹不一致", "failed")
		return rec.ID, fmt.Sprintf("%s 的主机指纹和已保存的不一致，已停止连接。不要重置指纹，请用户在服务器页面核对。", rec.Name), false
	case err != nil:
		rec.Status = "offline"
		_, _ = a.Store.UpdateServer(ctx, rec)
		a.notePanel(ctx, botID, rec.ID, "测试 "+rec.Name+"：连不上", "failed")
		return rec.ID, fmt.Sprintf("%s 连不上：%s", rec.Name, clip(err.Error(), 300)), false
	}
	rec.Status = "online"
	_, _ = a.Store.UpdateServer(ctx, rec)
	a.notePanel(ctx, botID, rec.ID, "测试 "+rec.Name+"：在线", "success")
	if strings.TrimSpace(out) == "" {
		out = "ok"
	}
	return rec.ID, rec.Name + " SSH 正常。" + out, true
}

func (a *Agent) panelCheckUpdate(ctx context.Context) (string, bool) {
	if a.Updates == nil {
		return "这个安装没有更新检查。", false
	}
	st, err := a.Updates.CheckNow(ctx)
	if errors.Is(err, update.ErrTooSoon) {
		st = a.Updates.Status()
		return a.formatUpdate(st) + "刚刚检查过，用的是刚才的结果。", true
	}
	if err != nil && st.Error == "" {
		st.Error = err.Error()
	}
	return a.formatUpdate(st), st.Error == ""
}

func (a *Agent) formatUpdate(st update.Status) string {
	line := "当前版本 " + st.Current
	if st.Latest != "" {
		line += "，最新 " + st.Latest
	}
	if st.Available {
		line += "，有新版本"
		if st.CanApply {
			line += "，可以申请升级"
		} else {
			line += "，这次安装还不能在面板里升级，需要先重新运行安装命令"
		}
	} else if st.Error == "" {
		line += "，没有更新的正式版"
	}
	if st.Error != "" {
		line += "。检查失败：" + clip(st.Error, 300)
	}
	return line + "。"
}

func probeChoice(raw string) *bool {
	var bag map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &bag) != nil {
		return nil
	}
	encoded, ok := bag["uninstall_probe"]
	if !ok || string(encoded) == "null" {
		return nil
	}
	var drop bool
	if json.Unmarshal(encoded, &drop) != nil {
		return nil
	}
	return &drop
}

func (a *Agent) panelDeleteServer(ctx context.Context, botID, id string, uninstall *bool) (string, string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return "", msg, false
	}
	rec, err := a.oneServer(ctx, id)
	if err != nil {
		return "", err.Error(), false
	}
	dropProbe := uninstall == nil || *uninstall
	what := "删除服务器记录 " + rec.Name + "（" + rec.Host + "）。面板里不再显示这台机器，保存的登录信息会一并清除"
	if dropProbe {
		what += "，并尝试卸载节点上的只读探针"
	} else {
		what += "，不卸载节点上的探针"
	}
	review := a.askOperator(ctx, botID, rec, what, a.approvalMode(ctx), riskGate{
		Level:   "high",
		Reasons: []string{"删除后这台机器从面板消失，登录信息一并清除"},
	})
	if !review.Allow {
		reason := review.Reason
		if reason == "" {
			reason = "未批准"
		}
		return rec.ID, "没有删除：" + reason, false
	}
	probe := onboarding.ProbeRemoval{Status: "skipped", Detail: "未选择卸载探针"}
	if dropProbe {
		if a.Onboarding != nil {
			probe = a.Onboarding.RemoveProbe(ctx, rec)
		} else {
			probe = onboarding.ProbeRemoval{Status: "skipped", Detail: "SSH 执行器不可用"}
		}
	}
	if err := a.Store.DeleteServer(ctx, rec.ID); err != nil {
		return rec.ID, "服务器记录没有删除：" + err.Error(), false
	}
	if a.Probes != nil {
		_ = a.Probes.Remove(rec.ID)
	}
	a.notePanel(ctx, botID, rec.ID, "删除服务器 "+rec.Name, "server_deleted")
	return rec.ID, fmt.Sprintf("已删除服务器 %s（%s）。%s。面板会在这一轮结束后刷新。", rec.Name, rec.Host, probeRemovalLine(dropProbe, probe)), true
}

func (a *Agent) oneServer(ctx context.Context, idOrName string) (store.ServerRec, error) {
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		return store.ServerRec{}, errors.New("delete_server 需要服务器名称或 id")
	}
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return store.ServerRec{}, err
	}
	var matches []model.Server
	for _, item := range servers {
		if item.ID == idOrName || item.Name == idOrName {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return store.ServerRec{}, errors.New("没有这台服务器")
	}
	if len(matches) > 1 {
		var b strings.Builder
		b.WriteString("有多台匹配的服务器，没有删除。请用 id 指定：")
		for _, item := range matches {
			fmt.Fprintf(&b, " %s（id=%s，%s）", item.Name, item.ID, item.Host)
		}
		return store.ServerRec{}, errors.New(b.String())
	}
	return a.Store.GetServer(ctx, matches[0].ID)
}

func probeRemovalLine(drop bool, probe onboarding.ProbeRemoval) string {
	if !drop {
		return "未卸载探针"
	}
	switch probe.Status {
	case "removed":
		return "节点探针已卸载"
	case "absent":
		return "节点上没有探针"
	case "failed":
		return "探针卸载失败：" + probe.Detail
	default:
		if strings.TrimSpace(probe.Detail) == "" {
			return "未卸载探针"
		}
		return "未卸载探针：" + probe.Detail
	}
}

func (a *Agent) panelApplyUpdate(ctx context.Context, botID string) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	if a.Updates == nil {
		return "这个安装不能从面板升级。", false
	}
	st := a.Updates.Status()
	if !st.Available {
		return "当前没有可安装的新版本。先 check_update。", false
	}
	review := a.askOperator(ctx, botID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, "升级 OneFrog 到 "+st.Latest, a.approvalMode(ctx), riskGate{
		Level:   "high",
		Reasons: []string{"升级会重启面板，进行中的回复会中断；账号、设置和数据保留"},
	})
	if !review.Allow {
		reason := review.Reason
		if reason == "" {
			reason = "未批准"
		}
		return "没有升级：" + reason, false
	}
	tag, err := a.Updates.Apply()
	if err != nil {
		return "升级没有发出去：" + err.Error(), false
	}
	a.notePanel(ctx, botID, "", "申请升级到 "+tag, "success")
	return "已请求升级到 " + tag + "。容器会重启，请稍等后刷新。账号和数据保留。", true
}

func (a *Agent) notePanel(ctx context.Context, botID, serverID, action, status string) {
	_, _ = a.Store.AddHistory(ctx, store.HistoryRec{History: model.History{BotID: botID, ServerID: serverID, Action: action, Command: action, Status: status}})
}

var panelTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "panel",
		"description": toolPanelDesc,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":          map[string]any{"type": "string", "enum": []string{"overview", "set_module", "set_bot", "test_server", "delete_server", "check_update", "apply_update"}},
				"id":              map[string]string{"type": "string", "description": "模块 id 或名称、Bot id 或名称、服务器 id 或名称"},
				"enabled":         map[string]string{"type": "boolean"},
				"uninstall_probe": map[string]string{"type": "boolean", "description": "仅 delete_server。省略或 true 时尝试卸载节点探针；用户要求保留探针时为 false"},
			},
			"required": []string{"action"},
		},
	},
}
