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
	"onefrog/internal/secret"
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
		Action       string  `json:"action"`
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		Enabled      *bool   `json:"enabled"`
		Fingerprint  string  `json:"fingerprint"`
		SecretName   string  `json:"secret_name"`
		Mode         string  `json:"mode"`
		SystemPrompt *string `json:"system_prompt"`
		Description  *string `json:"description"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		ev.Output = "panel 参数不是有效的 JSON"
		return ev
	}
	args.Action = strings.TrimSpace(args.Action)
	args.ID = strings.TrimSpace(args.ID)
	args.Name = strings.TrimSpace(args.Name)
	args.Fingerprint = strings.TrimSpace(args.Fingerprint)
	args.SecretName = strings.TrimSpace(args.SecretName)
	args.Mode = strings.TrimSpace(args.Mode)
	ev.Command = panelCommand(args.Action, args.ID, args.Name, args.Enabled, args.Fingerprint, args.SecretName, args.Mode)
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
	case "confirm_fingerprint":
		ev.ServerID, ev.Output, ev.OK = a.panelConfirmFingerprint(ctx, speaker.ID, args.ID, args.Fingerprint)
	case "reset_fingerprint":
		ev.ServerID, ev.Output, ev.OK = a.panelResetFingerprint(ctx, speaker.ID, args.ID)
	case "delete_server":
		ev.ServerID, ev.Output, ev.OK = a.panelDeleteServer(ctx, speaker.ID, args.ID, probeChoice(call.Function.Arguments))
	case "check_update":
		ev.Output, ev.OK = a.panelCheckUpdate(ctx)
	case "apply_update":
		ev.Output, ev.OK = a.panelApplyUpdate(ctx, speaker.ID)
	case "set_api_key":
		ev.Output, ev.OK = a.panelSetAPIKey(ctx, speaker.ID, args.ID, args.SecretName, call.Function.Arguments)
	case "set_approval_mode":
		ev.Output, ev.OK = a.panelSetApprovalMode(ctx, speaker.ID, args.Mode)
	case "set_bot_prompt":
		ev.Output, ev.OK = a.panelSetBotPrompt(ctx, speaker.ID, args.ID, args.SystemPrompt, args.Description)
	case "create_bot":
		ev.Output, ev.OK = a.panelCreateBot(ctx, speaker.ID, call.Function.Arguments, args.Name, args.SystemPrompt, args.Description)
	case "delete_bot":
		ev.Output, ev.OK = a.panelDeleteBot(ctx, speaker, args.ID)
	case "delete_chat":
		ev.Output, ev.OK = a.panelDeleteChat(ctx, speaker.ID, args.ID)
	default:
		ev.Output = "panel 的 action 只能是 overview、set_module、set_bot、test_server、confirm_fingerprint、reset_fingerprint、delete_server、check_update、apply_update、set_api_key、set_approval_mode、set_bot_prompt、create_bot、delete_bot、delete_chat"
	}
	ev.Output = clip(ev.Output, 2000)
	return ev
}

func panelCommand(action, id, name string, enabled *bool, fingerprint, secretName, mode string) string {
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
	case "confirm_fingerprint":
		return "确认 " + id + " 的主机指纹 " + fingerprint
	case "reset_fingerprint":
		return "清除 " + id + " 已保存的主机指纹"
	case "delete_server":
		return "删除服务器 " + id
	case "check_update":
		return "检查更新"
	case "apply_update":
		return "升级面板"
	case "set_api_key":
		return "更新渠道 " + id + " 的 API Key（保险箱：" + secretName + "）"
	case "set_approval_mode":
		return "把审批模式改为 " + mode
	case "set_bot_prompt":
		return "修改 Bot " + id + " 的工作指令"
	case "create_bot":
		return "创建 Bot " + name
	case "delete_bot":
		return "删除 Bot " + id
	case "delete_chat":
		return "删除对话 " + id
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
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
		Prompt      string `json:"prompt,omitempty"`
		Enabled     bool   `json:"enabled"`
	}
	botRows := make([]botRow, 0, len(bots))
	for _, item := range bots {
		enabled := true
		if writer := a.settingsWriter(); writer != nil {
			enabled = writer.BotEnabled(item.ID)
		}
		prompt := clip(oneLinePrompt(a.redact(ctx, item.SystemPrompt)), 160)
		botRows = append(botRows, botRow{
			ID: item.ID, Name: item.Name, Enabled: enabled,
			Description: clip(oneLinePrompt(a.redact(ctx, item.Description)), 160),
			Prompt:      prompt,
		})
	}
	type chatRow struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	listed, _ := a.Store.ListChatSessions(ctx)
	if len(listed) > 30 {
		listed = listed[:30]
	}
	chatRows := make([]chatRow, 0, len(listed))
	for _, item := range listed {
		title := strings.TrimSpace(a.redact(ctx, item.Title))
		if title == "" {
			title = "对话"
		}
		chatRows = append(chatRows, chatRow{ID: item.ID, Title: title})
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
		"chats":           chatRows,
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
		return rec.ID, fmt.Sprintf("%s 还不能登录：主机指纹尚未确认。指纹 %s。请调用 confirm_fingerprint，把这个指纹原样放进参数。会弹出审批，不要自己接受，也不要改指纹。", rec.Name, fp), false
	case errors.Is(err, remote.ErrHostKeyMismatch):
		rec.Status = "host_key_mismatch"
		_, _ = a.Store.UpdateServer(ctx, rec)
		a.notePanel(ctx, botID, rec.ID, "测试 "+rec.Name+"：指纹不一致", "failed")
		return rec.ID, fmt.Sprintf("%s 的主机指纹和已保存的不一致，已停止连接，没有覆盖旧指纹。出示的指纹 %s。如需更换，先调用 reset_fingerprint，审批通过后再 confirm_fingerprint。", rec.Name, fp), false
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

func (a *Agent) approveHighRisk(ctx context.Context, botID string, rec store.ServerRec, what string, reasons []string) (bool, string) {
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = "panel"
	}
	if strings.TrimSpace(rec.Name) == "" {
		rec.Name = "面板"
	}
	review := a.askOperator(ctx, botID, rec, what, a.approvalMode(ctx), riskGate{Level: "high", Reasons: reasons})
	if review.Allow {
		return true, ""
	}
	if strings.TrimSpace(review.Reason) == "" {
		return false, "未批准"
	}
	return false, review.Reason
}

func cleanFingerprint(raw string) (string, bool) {
	fp := strings.TrimSpace(raw)
	if fp == "" || len(fp) > 180 || strings.ContainsAny(fp, "\r\n\t") {
		return "", false
	}
	if strings.Count(fp, " ") > 1 {
		return "", false
	}
	keyType, sum := remote.SplitPin(fp)
	if !strings.HasPrefix(sum, "SHA256:") || len(sum) < len("SHA256:")+3 {
		return "", false
	}
	for _, r := range sum[len("SHA256:"):] {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/', r == '=', r == '-', r == '_':
		default:
			return "", false
		}
	}
	if keyType != "" && strings.Contains(keyType, " ") {
		return "", false
	}
	return fp, true
}

func (a *Agent) panelConfirmFingerprint(ctx context.Context, botID, id, fingerprint string) (string, string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return "", msg, false
	}
	if a.SSH == nil {
		return "", "没有 SSH 执行器", false
	}
	fp, ok := cleanFingerprint(fingerprint)
	if !ok {
		return "", "confirm_fingerprint 需要 test_server 汇报的主机指纹，形如 ssh-ed25519 SHA256:… 或 SHA256:…", false
	}
	rec, err := a.oneServer(ctx, id)
	if err != nil {
		return "", err.Error(), false
	}
	if rec.HostKey != "" && rec.HostKey != fp {
		return rec.ID, fmt.Sprintf("%s 已保存的指纹和要确认的不一致，没有覆盖。已保存：%s。这次：%s。如需更换，先 reset_fingerprint。", rec.Name, rec.HostKey, fp), false
	}
	if rec.HostKey == fp {
		return rec.ID, rec.Name + " 的主机指纹已经是这个，没有改动。", true
	}
	what := fmt.Sprintf("确认 %s（%s）的 SSH 主机指纹为 %s。只有用这个指纹钉住并且登录成功后才会保存。", rec.Name, rec.Host, fp)
	allow, why := a.approveHighRisk(ctx, botID, rec, what, []string{"首次信任这台机器的 SSH 主机指纹，请对照 ssh-keygen -lf 的结果"})
	if !allow {
		return rec.ID, "没有保存指纹：" + why, false
	}
	plain := ""
	if rec.EncryptedKey != "" {
		if a.Box == nil {
			return rec.ID, "登录密钥无法解密，没有保存指纹", false
		}
		plain, err = a.Box.Decrypt(rec.EncryptedKey)
		if err != nil {
			return rec.ID, "登录密钥无法解密，没有保存指纹", false
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(a.execution().CommandTimeoutSeconds)*time.Second)
	defer cancel()
	_, seen, err := a.SSH.Run(runCtx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, fp, "echo ok")
	if errors.Is(err, remote.ErrHostKeyUnknown) || errors.Is(err, remote.ErrHostKeyMismatch) || (err == nil && seen != "" && seen != fp) {
		shown := seen
		if shown == "" {
			shown = "未知"
		}
		return rec.ID, fmt.Sprintf("%s 出示的指纹和要确认的不一致，没有保存。要确认的是 %s，出示的是 %s。", rec.Name, fp, shown), false
	}
	if err != nil {
		rec.Status = "offline"
		_, _ = a.Store.UpdateServer(ctx, rec)
		a.notePanel(ctx, botID, rec.ID, "确认 "+rec.Name+" 的指纹失败", "failed")
		return rec.ID, fmt.Sprintf("%s 没有连上，指纹未保存：%s", rec.Name, clip(err.Error(), 300)), false
	}
	rec.HostKey = fp
	rec.Status = "online"
	if _, err = a.Store.UpdateServer(ctx, rec); err != nil {
		return rec.ID, "指纹没有写成：" + err.Error(), false
	}
	a.notePanel(ctx, botID, rec.ID, "已确认 "+rec.Name+" 的主机指纹", "host_key_confirmed")
	return rec.ID, fmt.Sprintf("已确认 %s 的主机指纹 %s。", rec.Name, fp), true
}

func (a *Agent) panelResetFingerprint(ctx context.Context, botID, id string) (string, string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return "", msg, false
	}
	rec, err := a.oneServer(ctx, id)
	if err != nil {
		return "", err.Error(), false
	}
	if strings.TrimSpace(rec.HostKey) == "" {
		return rec.ID, rec.Name + " 没有已保存的主机指纹。", false
	}
	old := rec.HostKey
	what := fmt.Sprintf("清除 %s（%s）已保存的 SSH 主机指纹 %s。清除后不能登录，直到重新确认；不会自动信任新指纹。", rec.Name, rec.Host, old)
	allow, why := a.approveHighRisk(ctx, botID, rec, what, []string{"清除后，下次连接必须重新核对主机指纹"})
	if !allow {
		return rec.ID, "没有清除指纹：" + why, false
	}
	rec.HostKey = ""
	rec.ClearHostKey = true
	rec.Status = "unknown"
	if _, err = a.Store.UpdateServer(ctx, rec); err != nil {
		return rec.ID, "指纹没有清除：" + err.Error(), false
	}
	a.notePanel(ctx, botID, rec.ID, "已清除 "+rec.Name+" 的主机指纹", "host_key_reset")
	return rec.ID, fmt.Sprintf("已清除 %s 的主机指纹。请再 test_server，然后 confirm_fingerprint。", rec.Name), true
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

func panelHasRawSecret(raw string) bool {
	var bag map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &bag) != nil {
		return false
	}
	for _, key := range []string{"api_key", "apikey", "key", "token", "password", "secret", "value", "plaintext", "credential"} {
		encoded, ok := bag[key]
		if !ok {
			continue
		}
		s := strings.TrimSpace(string(encoded))
		if s != "" && s != "null" && s != `""` {
			return true
		}
	}
	return false
}

func vaultName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{{secret:") && strings.HasSuffix(raw, "}}") {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "{{secret:"), "}}")
	}
	return secret.ValidName(raw)
}

func (a *Agent) findProvider(idOrName string) (preferences.Provider, error) {
	writer := a.settingsWriter()
	if writer == nil {
		return preferences.Provider{}, errors.New("没有设置存储")
	}
	idOrName = strings.TrimSpace(idOrName)
	if idOrName == "" {
		return preferences.Provider{}, errors.New("需要渠道 id 或名称")
	}
	var matches []preferences.Provider
	for _, item := range writer.Snapshot().Providers {
		if item.ID == idOrName || item.Name == idOrName {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return preferences.Provider{}, errors.New("没有这个渠道")
	}
	if len(matches) > 1 {
		return preferences.Provider{}, errors.New("有多个同名渠道，请用 id")
	}
	return matches[0], nil
}

func (a *Agent) panelSetAPIKey(ctx context.Context, botID, id, secretName, raw string) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	if panelHasRawSecret(raw) {
		return "不要在参数或附件里写 API Key 明文。先用 request_secret 放进保险箱，再把 secret_name 设成那个名称。", false
	}
	p, err := a.findProvider(id)
	if err != nil {
		return err.Error(), false
	}
	if strings.HasPrefix(p.AuthKind, "oauth-") {
		return "OAuth 渠道不使用手动 API Key", false
	}
	name, err := vaultName(secretName)
	if err != nil {
		return "set_api_key 需要 secret_name，填保险箱里的名称，不要填明文。", false
	}
	item, err := a.Store.SecretByName(ctx, name)
	if err != nil {
		return "保险箱里没有密钥「" + name + "」。先 request_secret，等用户在密码卡片里保存后再调用。不要让用户把密钥打进对话。", false
	}
	what := fmt.Sprintf("把保险箱密钥「%s」写入渠道「%s」（id=%s）的 API Key。审批卡片不显示密钥明文。", name, p.Name, p.ID)
	allow, why := a.approveHighRisk(ctx, botID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, what, []string{"会替换这个渠道保存的 API Key"})
	if !allow {
		return "没有更新 API Key：" + why, false
	}
	if a.Box == nil {
		return "保险箱未配置，没有更新 API Key", false
	}
	plain, err := a.Box.Decrypt(item.EncryptedValue)
	if err != nil || secret.ValidValue(plain) != nil {
		return "保险箱里的这项密钥无法使用，没有更新 API Key", false
	}
	saved, err := a.settingsWriter().SetProviderKey(p.ID, plain)
	if err != nil {
		return "API Key 没有保存：" + err.Error(), false
	}
	a.notePanel(ctx, botID, "", "已更新渠道 "+saved.Name+" 的 API Key", "success")
	return fmt.Sprintf("已更新渠道 %s（id=%s）的 API Key。明文没有进入对话。", saved.Name, saved.ID), true
}

func approvalModeLabel(mode string) string {
	switch mode {
	case preferences.ModeOff:
		return "关闭审批"
	case preferences.ModeSmart:
		return "智能审批"
	case preferences.ModeManual:
		return "逐条确认"
	case preferences.ModeModel:
		return "替我审批"
	case preferences.ModePlan:
		return "仅规划"
	default:
		return "尚未选择（按智能审批）"
	}
}

func approvalModeEffect(mode string) string {
	switch mode {
	case preferences.ModeOff:
		return "关闭后，命令不再弹出审批卡片，只有极少数灾难性命令会被拒绝。"
	case preferences.ModePlan:
		return "之后的对话只规划，不执行远程命令。"
	case preferences.ModeManual:
		return "之后每条命令都要确认。"
	case preferences.ModeModel:
		return "之后由独立审批模型决定是否执行。"
	default:
		return "之后只读命令直接执行，有风险的命令仍会审批。"
	}
}

func (a *Agent) panelSetApprovalMode(ctx context.Context, botID, mode string) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	next, ok := preferences.NormalizeMode(mode)
	if !ok || next == preferences.ModeUnset {
		return "审批模式只能是 off、smart、manual、model、plan", false
	}
	writer := a.settingsWriter()
	if writer == nil {
		return "没有设置存储，不能改审批模式", false
	}
	stored := writer.Snapshot().Approval.Mode
	if stored == next {
		return "审批模式已经是" + approvalModeLabel(next) + "。", true
	}
	if next == preferences.ModeModel {
		route := writer.Snapshot().Approval.Route
		if route.ProviderID == "" || strings.TrimSpace(route.Model) == "" {
			return "改成「替我审批」前要先在设置里选好独立审批模型。这次没有改。", false
		}
	}
	what := fmt.Sprintf("把审批模式从「%s」改为「%s」。%s这一轮对话仍按原来的模式执行，下一轮才生效。", approvalModeLabel(stored), approvalModeLabel(next), approvalModeEffect(next))
	allow, why := a.approveHighRisk(ctx, botID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, what, []string{"审批模式决定之后的命令还要不要你确认"})
	if !allow {
		return "没有更改审批模式：" + why, false
	}
	if _, err := writer.SetApprovalMode(next); err != nil {
		return "审批模式没有保存：" + err.Error(), false
	}
	a.notePanel(ctx, botID, "", "审批模式改为 "+approvalModeLabel(next), "success")
	return fmt.Sprintf("已把审批模式从「%s」改为「%s」。这一轮仍按原来的模式，下一轮生效。", approvalModeLabel(stored), approvalModeLabel(next)), true
}

func panelRejectsSecretFields(raw string) bool {
	var bag map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &bag) != nil {
		return true
	}
	for key := range bag {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "api_key", "apikey", "api_key_secret_id", "base_url", "secret", "password", "private_key", "token", "value", "master_key":
			return true
		}
	}
	return false
}

func cleanBotName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || strings.ContainsAny(name, "\r\n\t") {
		return "", false
	}
	return name, true
}

func (a *Agent) panelCreateBot(ctx context.Context, botID, raw, name string, systemPrompt, description *string) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	if panelRejectsSecretFields(raw) {
		return "create_bot 不接受 API Key、私钥或保险箱明文。新 Bot 跟随默认模型，密钥只按名称调用。", false
	}
	name, ok := cleanBotName(name)
	if !ok {
		return "create_bot 需要 1–60 字的 name，不能包含换行", false
	}
	existing, err := a.Store.ListBots(ctx)
	if err != nil {
		return "Bot 列表读不出来：" + err.Error(), false
	}
	for _, item := range existing {
		if item.Name == name {
			return "已经有同名 Bot。请换一个名称，或用 set_bot_prompt 改它的工作指令。", false
		}
	}
	next := model.Bot{Name: name}
	if description != nil {
		next.Description = clip(a.redact(ctx, strings.TrimSpace(*description)), 400)
	}
	if systemPrompt != nil {
		next.SystemPrompt = clip(a.redact(ctx, *systemPrompt), 4000)
	}
	saved, err := a.Store.CreateBot(ctx, next)
	if err != nil {
		return "Bot 没有创建：" + err.Error(), false
	}
	a.notePanel(ctx, botID, "", "已创建 Bot "+saved.Name, "success")
	return fmt.Sprintf("已创建 Bot %s（id=%s）。已启用，跟随默认模型，没有单独的密钥。内置安全规则仍在工作指令前面。", saved.Name, saved.ID), true
}

func (a *Agent) panelDeleteBot(ctx context.Context, speaker model.Bot, id string) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	target, err := a.findBot(ctx, id)
	if err != nil {
		return "没有这个 Bot", false
	}
	if target.ID == speaker.ID {
		return "不能删除当前正在使用的主控", false
	}
	what := fmt.Sprintf("删除 Bot %s（id=%s）。它的记忆会一起去掉，已有对话还在。不会读取密钥。", target.Name, target.ID)
	allow, why := a.approveHighRisk(ctx, speaker.ID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, what, []string{"会删掉这个 Bot 和它的记忆"})
	if !allow {
		return "没有删除 Bot：" + why, false
	}
	if err = a.Store.DeleteBot(ctx, target.ID); err != nil {
		return "Bot 没有删除：" + err.Error(), false
	}
	if writer := a.settingsWriter(); writer != nil {
		_ = writer.SetBotEnabled(target.ID, true)
	}
	a.notePanel(ctx, speaker.ID, "", "已删除 Bot "+target.Name, "success")
	return fmt.Sprintf("已删除 Bot %s（id=%s）。", target.Name, target.ID), true
}

var errAmbiguousChat = errors.New("ambiguous chat")

func (a *Agent) findChat(ctx context.Context, idOrTitle string) (model.Session, error) {
	idOrTitle = strings.TrimSpace(idOrTitle)
	if idOrTitle == "" {
		return model.Session{}, fmt.Errorf("empty")
	}
	if item, err := a.Store.GetChatSession(ctx, idOrTitle); err == nil {
		return item, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return model.Session{}, err
	}
	listed, err := a.Store.ListChatSessions(ctx)
	if err != nil {
		return model.Session{}, err
	}
	var matched []model.Session
	for _, item := range listed {
		if item.Title == idOrTitle {
			matched = append(matched, item.Session)
		}
	}
	if len(matched) == 1 {
		return matched[0], nil
	}
	if len(matched) > 1 {
		return model.Session{}, errAmbiguousChat
	}
	return model.Session{}, store.ErrNotFound
}

func (a *Agent) panelDeleteChat(ctx context.Context, botID, id string) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	if strings.TrimSpace(id) == "" {
		return "delete_chat 需要对话 id 或标题", false
	}
	sess, err := a.findChat(ctx, id)
	if err != nil {
		if errors.Is(err, errAmbiguousChat) {
			return "有多条同名对话，请用 overview 里的 id", false
		}
		return "没有这条对话", false
	}
	title := strings.TrimSpace(a.redact(ctx, sess.Title))
	if title == "" {
		title = "对话"
	}
	what := fmt.Sprintf("删除对话「%s」（id=%s）。只删除这条对话和里面的消息，不删除登录、密钥或服务器。", title, sess.ID)
	allow, why := a.approveHighRisk(ctx, botID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, what, []string{"会删掉这一条对话记录"})
	if !allow {
		return "没有删除对话：" + why, false
	}
	if err = a.Store.DeleteChatSession(ctx, sess.ID); err != nil {
		return "对话没有删除：" + err.Error(), false
	}
	a.notePanel(ctx, botID, "", "已删除对话 "+title, "success")
	return fmt.Sprintf("已删除对话「%s」。", title), true
}

func (a *Agent) panelSetBotPrompt(ctx context.Context, botID, id string, systemPrompt, description *string) (string, bool) {
	if msg := a.panelPlanBlocked(ctx); msg != "" {
		return msg, false
	}
	if systemPrompt == nil && description == nil {
		return "set_bot_prompt 需要 system_prompt 或 description", false
	}
	target, err := a.findBot(ctx, id)
	if err != nil {
		return "没有这个 Bot", false
	}
	next := target
	shown := []string{}
	if description != nil {
		next.Description = clip(a.redact(ctx, strings.TrimSpace(*description)), 400)
		shown = append(shown, "简介："+next.Description)
	}
	if systemPrompt != nil {
		next.SystemPrompt = clip(a.redact(ctx, *systemPrompt), 4000)
		shown = append(shown, "工作指令："+clip(oneLinePrompt(next.SystemPrompt), 800))
	}
	what := fmt.Sprintf("修改 Bot %s（id=%s）。内置安全规则仍然在前面，工作指令只能补充，不能放宽。%s", next.Name, next.ID, strings.Join(shown, " "))
	allow, why := a.approveHighRisk(ctx, botID, store.ServerRec{Server: model.Server{ID: "panel", Name: "面板"}}, what, []string{"会改变这个 Bot 之后怎么工作"})
	if !allow {
		return "没有修改 Bot：" + why, false
	}
	if _, err = a.Store.UpdateBot(ctx, next); err != nil {
		return "Bot 没有保存：" + err.Error(), false
	}
	a.notePanel(ctx, botID, "", "已修改 Bot "+next.Name+" 的工作指令", "success")
	return fmt.Sprintf("已修改 Bot %s（id=%s）的工作指令。内置规则没有变。", next.Name, next.ID), true
}

func oneLinePrompt(s string) string {
	return strings.Join(strings.Fields(s), " ")
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
				"action":          map[string]any{"type": "string", "enum": []string{"overview", "set_module", "set_bot", "test_server", "confirm_fingerprint", "reset_fingerprint", "delete_server", "check_update", "apply_update", "set_api_key", "set_approval_mode", "set_bot_prompt", "create_bot", "delete_bot", "delete_chat"}},
				"id":              map[string]string{"type": "string", "description": "模块、Bot、服务器、渠道或对话的 id 或名称。delete_chat 也接受标题"},
				"name":            map[string]string{"type": "string", "description": "仅 create_bot。新 Bot 的名称，1 到 60 字"},
				"enabled":         map[string]string{"type": "boolean"},
				"fingerprint":     map[string]string{"type": "string", "description": "仅 confirm_fingerprint。test_server 汇报的主机指纹，原样填写"},
				"secret_name":     map[string]string{"type": "string", "description": "仅 set_api_key。保险箱里的名称，不要填明文"},
				"mode":            map[string]string{"type": "string", "description": "仅 set_approval_mode。off、smart、manual、model 或 plan"},
				"system_prompt":   map[string]string{"type": "string", "description": "create_bot 或 set_bot_prompt。工作指令，只能补充内置规则，不要写密钥明文"},
				"description":     map[string]string{"type": "string", "description": "create_bot 或 set_bot_prompt。Bot 简介"},
				"uninstall_probe": map[string]string{"type": "boolean", "description": "仅 delete_server。省略或 true 时尝试卸载节点探针；用户要求保留探针时为 false"},
			},
			"required": []string{"action"},
		},
	},
}
