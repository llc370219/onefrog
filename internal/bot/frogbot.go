package bot

import (
	"context"
	"encoding/json"
	"errors"

	"onefrog/internal/audit"
	frog "onefrog/internal/frogbot/onefrog"
	"onefrog/internal/id"
)

// FrogProvider lets Frog Bot run both its chat and agent loops with the
// provider, usage accounting and account-scoped OAuth already configured in OneFrog.
type FrogProvider struct {
	Agent  *Agent
	Owner  string
	Worker bool
}

func (p FrogProvider) Chat(ctx context.Context, req frog.ChatRequest, onText func(string)) (frog.Message, error) {
	a := p.Agent
	b, err := a.pickBot(ctx, "main")
	if err != nil {
		return frog.Message{}, err
	}
	ctx = context.WithValue(ctx, optionsKey{}, Options{OwnerID: p.Owner, RestrictServers: true})
	_, creds, err := a.credentialsFor(ctx, b, p.Worker)
	if err != nil {
		return frog.Message{}, err
	}
	if creds.base == "" || creds.model == "" {
		return frog.Message{}, errors.New("请先在 OneFrog 设置中配置模型")
	}
	purpose := "frog-chat"
	if p.Worker {
		purpose = "frog-agent"
	}
	ctx = usageContext(ctx, b.ID, purpose, creds.provider)
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		row := map[string]any{"role": m.Role, "content": a.redact(ctx, m.Content)}
		if m.ToolCallID != "" {
			row["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) != 0 {
			calls := make([]map[string]any, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{
					"name": c.Name, "arguments": a.redact(ctx, string(c.Args)),
				}})
			}
			row["tool_calls"] = calls
		}
		if len(m.ProviderItems) > 0 {
			row["response_items"] = m.ProviderItems
		}
		messages = append(messages, row)
	}
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Schema,
		}})
	}
	reply, err := a.chat(ctx, creds.base, creds.model, creds.key, messages, tools)
	if err != nil {
		// A provider may include request details in an error. Keep credentials
		// and raw upstream response bodies out of Frog Bot's durable log.
		if ctx.Err() != nil {
			return frog.Message{}, ctx.Err()
		}
		return frog.Message{}, errors.New("模型请求失败，请检查 OneFrog 中的模型配置")
	}
	out := frog.Message{Role: "assistant", Content: a.redact(ctx, reply.Content), ProviderItems: reply.ResponseItems}
	for _, c := range reply.ToolCalls {
		args := json.RawMessage(a.redact(ctx, c.Function.Arguments))
		if !json.Valid(args) {
			args = json.RawMessage("null")
		}
		out.ToolCalls = append(out.ToolCalls, frog.ToolCall{ID: c.ID, Name: c.Function.Name, Args: args})
	}
	if onText != nil && out.Content != "" {
		onText(out.Content)
	}
	return out, nil
}

// RunFrogCommand is only registered behind Frog Bot's exact-argument approval
// guard. Reuse OneFrog's host-key pinning, policy, secret expansion and audit.
func (a *Agent) RunFrogCommand(ctx context.Context, owner, serverID, command string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := a.Store.GetServer(ctx, serverID); err != nil {
		return "", errors.New("服务器不存在")
	}
	// Catastrophic commands must stay blocked even if the host policy uses
	// manual confirmation. Never use approval to bypass a capability limit.
	if hard := audit.Decide(command); !hard.Allow {
		return "", errors.New(hard.Reason)
	}
	opt := Options{OwnerID: owner, RestrictServers: true, Approver: func(_ context.Context, req ApprovalRequest) (bool, string) {
		if req.ServerID != serverID || req.Command != a.redact(ctx, command) {
			return false, "Frog Bot 审批范围不匹配"
		}
		return true, "用户已在 Frog Bot 批准此命令"
	}}
	ctx = context.WithValue(ctx, optionsKey{}, opt)
	ctx = withGrants(withTaint(ctx))
	raw, _ := json.Marshal(map[string]string{"server_id": serverID, "command": command})
	var call toolCall
	call.ID, call.Type = id.New(), "function"
	call.Function.Name, call.Function.Arguments = "run_command", string(raw)
	ev := a.runTool(ctx, "main", serverID, call)
	if !ev.OK {
		return "", errors.New(ev.Output)
	}
	return ev.Output, nil
}
