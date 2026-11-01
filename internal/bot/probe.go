package bot

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"onefrog/internal/onboarding"
	"onefrog/internal/preferences"
)

func nl() string { return string([]byte{10}) }

// readsOwnPanel reports a shell command that downloads or searches this panel's
// own pages. The minified frontend is not an install API.
func (a *Agent) readsOwnPanel(command string) bool {
	low := strings.ToLower(command)
	if strings.Contains(low, "/assets/index-") || strings.Contains(low, "probetoken") {
		return true
	}
	host := panelHost(a.PublicURL)
	if host == "" || !strings.Contains(low, host) {
		return false
	}
	return strings.Contains(low, ".js") || strings.Contains(low, ".html") || strings.Contains(low, "/assets/")
}

func panelHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func (a *Agent) installProbe(ctx context.Context, botID string, call toolCall) ToolEvent {
	ev := ToolEvent{Name: "install_probe", Command: "安装只读探针"}
	var args struct {
		Action      string   `json:"action"`
		ServerID    string   `json:"server_id"`
		RequestID   string   `json:"request_id"`
		Fingerprint string   `json:"fingerprint"`
		ReportBase  string   `json:"report_base"`
		Interval    flexInt  `json:"interval"`
		Targets     []string `json:"targets"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		ev.Output = "参数无效"
		return ev
	}
	action := strings.TrimSpace(args.Action)
	if action == "" {
		action = "install"
	}
	if action == "confirm" {
		ev.Command = "确认主机指纹"
	}
	if a.approvalMode(ctx) == preferences.ModePlan {
		ev.Output = "仅规划模式，不能安装探针。"
		return ev
	}
	if a.Onboarding == nil {
		ev.Output = "探针安装未配置"
		return ev
	}
	owner := strings.TrimSpace(options(ctx).OwnerID)
	if owner == "" {
		owner = "panel"
	}
	if action == "confirm" {
		v, err := a.Onboarding.Confirm(owner, strings.TrimSpace(args.RequestID), args.Fingerprint)
		if err != nil {
			ev.Output = err.Error()
			return ev
		}
		// Confirm returns before the job leaves fingerprint_required. Keep
		// waiting so a correct fingerprint actually continues the install.
		return a.finishProbe(ctx, botID, ev, a.waitOnboarding(ctx, owner, v.ID, true))
	}
	if action != "install" {
		ev.Output = "未知操作"
		return ev
	}
	id := strings.TrimSpace(args.ServerID)
	rec, err := a.Store.GetServer(ctx, id)
	if err != nil {
		rec, err = a.serverByName(ctx, id)
	}
	if err != nil {
		ev.Output = "找不到服务器"
		return ev
	}
	ev.ServerID = rec.ID
	if rec.EncryptedKey == "" {
		ev.Output = "这台服务器还没有保存登录信息。请先用 connect_server 接入，不要去下载面板页面。"
		return ev
	}
	if v, ok := a.Onboarding.ActiveForServer(owner, rec.ID); ok {
		if v.Status == "running" {
			v = a.waitOnboarding(ctx, owner, v.ID, false)
		}
		return a.finishProbe(ctx, botID, ev, v)
	}
	report := strings.TrimSpace(args.ReportBase)
	if report == "" {
		report = strings.TrimSpace(a.PublicURL)
	}
	if report == "" {
		ev.Output = "请提供节点能访问的面板地址。不要填 127.0.0.1。"
		return ev
	}
	name := strings.TrimSpace(rec.Name)
	if name == "" {
		name = rec.Host
	}
	v, err := a.Onboarding.Start(owner, onboarding.Request{
		ServerID:   rec.ID,
		Name:       name,
		Host:       rec.Host,
		Port:       rec.Port,
		Username:   rec.Username,
		AuthType:   rec.AuthType,
		Role:       rec.Role,
		Configure:  true,
		ReportBase: report,
		Interval:   int(args.Interval),
		Targets:    args.Targets,
	})
	if err != nil {
		ev.Output = err.Error()
		return ev
	}
	return a.finishProbe(ctx, botID, ev, a.waitOnboarding(ctx, owner, v.ID, false))
}

func (a *Agent) waitOnboarding(ctx context.Context, owner, id string, ignoreFingerprint bool) onboarding.View {
	deadline := time.Now().Add(3 * time.Minute)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	for {
		v, err := a.Onboarding.Get(owner, id)
		if err != nil {
			return onboarding.View{ID: id, Status: "failed", Message: "找不到安装任务"}
		}
		switch v.Status {
		case "completed", "failed", "partial", "interrupted":
			return v
		case "fingerprint_required":
			if !ignoreFingerprint {
				return v
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return v
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			if latest, e := a.Onboarding.Get(owner, id); e == nil {
				return latest
			}
			return v
		case <-timer.C:
		}
	}
}

func (a *Agent) finishProbe(ctx context.Context, botID string, ev ToolEvent, v onboarding.View) ToolEvent {
	if v.ServerID != "" {
		ev.ServerID = v.ServerID
	}
	ev.Output = formatOnboarding(v)
	ev.OK = v.Status == "completed" || v.Status == "partial" || v.Status == "fingerprint_required"
	if v.Status == "fingerprint_required" || v.Status == "running" || v.Status == "" {
		return ev
	}
	status := "failed"
	if v.Status == "completed" || v.Status == "partial" {
		status = "success"
	}
	a.notePanel(ctx, botID, ev.ServerID, "安装只读探针", status)
	return ev
}

func formatOnboarding(v onboarding.View) string {
	if v.Status == "fingerprint_required" {
		return "需要用户确认 SSH 主机指纹后才能继续。指纹：" + v.Fingerprint + nl() + "request_id=" + v.ID + nl() + "请把指纹原样告诉用户，等用户确认后再用 action=confirm 提交。不要自己确认。"
	}
	parts := []string{"状态：" + v.Status}
	if v.Message != "" {
		parts = append(parts, v.Message)
	}
	for _, step := range v.Steps {
		line := "- " + step.Label + "：" + step.Status
		if step.Message != "" {
			line += "（" + step.Message + "）"
		}
		parts = append(parts, line)
	}
	out := strings.Join(parts, nl())
	if strings.Contains(strings.ToLower(out), "token") {
		return "安装结果里含有上报凭据，已隐藏。请到面板查看探针是否在线，不要在对话里索要凭据。"
	}
	return out
}

var installProbeTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "install_probe",
		"description": toolInstallProbeDesc,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":      map[string]any{"type": "string", "enum": []string{"install", "confirm"}, "description": "install 安装或重装；confirm 提交用户确认的主机指纹"},
				"server_id":   map[string]string{"type": "string", "description": "服务器 id 或名称"},
				"request_id":  map[string]string{"type": "string", "description": "等待确认指纹时返回的任务 id"},
				"fingerprint": map[string]string{"type": "string", "description": "用户确认的主机指纹，必须与展示的一致"},
				"report_base": map[string]string{"type": "string", "description": "节点能访问的面板地址；省略时用面板公网地址"},
				"interval":    map[string]string{"type": "integer", "description": "采集间隔，10 到 300 秒"},
				"targets":     map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "description": "线路检测目标，最多 8 个"},
			},
		},
	},
}
