package bot

import (
	"context"
	"encoding/json"
	"fmt"

	"onefrog/internal/model"
	"onefrog/internal/preferences"
)

// RedactServiceOutput applies the same vault/provider masking as command output.
func (a *Agent) RedactServiceOutput(ctx context.Context, raw string) string {
	return a.redact(ctx, raw)
}

func (a *Agent) inspectServices(ctx context.Context, speaker model.Bot, fallback string, call toolCall, tr *trace) string {
	var args struct {
		Action   string `json:"action"`
		ServerID string `json:"server_id"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return "参数无效"
	}
	if a.Services == nil {
		return "服务检测器未配置"
	}
	if args.Action == "list" {
		hosts, err := a.Store.ListServers(ctx)
		if err != nil {
			return "读取服务器列表失败"
		}
		ids := []string{}
		for _, h := range hosts {
			if !options(ctx).RestrictServers || h.ID == fallback || serverGranted(ctx, h.ID) {
				ids = append(ids, h.ID)
			}
		}
		raw, _ := json.Marshal(a.Services.Views(ids))
		addUntrusted(ctx, string(raw))
		return fmt.Sprintf(untrustedOpen, "服务检测快照") + a.redact(ctx, string(raw)) + untrustedClose
	}
	if args.Action != "scan" {
		return "action 只能是 list 或 scan"
	}
	if args.ServerID == "" {
		args.ServerID = fallback
	}
	rec, err := a.serverByName(ctx, args.ServerID)
	if err != nil {
		return "找不到服务器，请选择要检测的服务器"
	}
	unselected := options(ctx).RestrictServers && rec.ID != fallback && !serverGranted(ctx, rec.ID)
	mode := a.approvalMode(ctx)
	if mode == preferences.ModePlan {
		return "仅规划模式，不执行远程检测；可用 list 查看已保存的检测快照"
	}
	if unselected || mode == preferences.ModeManual {
		gate := riskGate{Ask: true, Level: "low", Reasons: []string{"只读检测 systemd、Docker 与监听端口"}}
		if unselected {
			gate.Level = "high"
			gate.Reasons = append(gate.Reasons, "这台服务器不是本轮手动选中的")
		}
		review := a.askOperator(ctx, speaker.ID, rec, "只读检测服务器服务、容器与监听端口", mode, gate)
		if !review.Allow {
			return "没有执行检测：" + review.Reason
		}
		if unselected {
			grantServer(ctx, rec.ID)
		}
	}
	v, err := a.Services.Scan(ctx, rec.ID)
	if err != nil {
		return "服务检测失败"
	}
	raw, _ := json.Marshal(v)
	ev := ToolEvent{Name: "inspect_services", Command: "只读检测服务、容器与监听端口", ServerID: rec.ID, OK: v.Status != "failed", Output: fmt.Sprintf("%d 项；状态 %s", len(v.Items), v.Status)}
	if tr != nil {
		tr.noteTool(speaker.Name, ev)
	}
	status := "success"
	if !ev.OK {
		status = "failed"
	}
	a.notePanel(ctx, speaker.ID, rec.ID, ev.Command, status)
	if len(v.Items) > 0 {
		addUntrusted(ctx, string(raw))
	}
	return fmt.Sprintf(untrustedOpen, a.safe(ctx, rec.Name)) + a.redact(ctx, string(raw)) + untrustedClose
}

var inspectServicesTool = map[string]any{
	"type": "function", "function": map[string]any{
		"name": "inspect_services", "description": "查询官方服务监控模块的检测快照或只读检测指定服务器上的 systemd 服务、Docker 容器、监听端口。不会读命令参数、环境变量、配置或密码。实时检测结果同步到面板。",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{
			"action":    map[string]any{"type": "string", "enum": []string{"list", "scan"}},
			"server_id": map[string]string{"type": "string", "description": "服务器 ID 或名称；scan 省略时使用当前选中服务器"},
		}, "required": []string{"action"}},
	}}
