package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	frog "onefrog/internal/frogbot/onefrog"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
)

// FrogProvider runs a Frog Bot's chat layer and background agents with the
// model route, usage accounting and account-scoped OAuth configured for that
// bot in OneFrog. OpenAI-compatible Chat Completions channels stream text and
// reasoning token by token; Responses and OAuth channels answer in one piece.
type FrogProvider struct {
	Agent *Agent
	Owner string
	BotID string // OneFrog bot; empty = the leader (主控)
	// BotIDFunc, when set, names the bot at call time (the default runtime
	// follows whichever bot is currently the leader).
	BotIDFunc func() string
	Worker    bool // background agent: uses the bot's collaboration route
}

func (p FrogProvider) Chat(ctx context.Context, req frog.ChatRequest, onText func(string)) (frog.Message, error) {
	a := p.Agent
	botID := p.BotID
	if p.BotIDFunc != nil {
		botID = p.BotIDFunc()
	}
	b, err := a.pickBot(ctx, botID)
	if err != nil && botID != "" {
		b, err = a.pickBot(ctx, "")
	}
	if err != nil {
		return frog.Message{}, errors.New("没有可用的 Bot，请先在「Bot」页启用一个")
	}
	ctx = context.WithValue(ctx, optionsKey{}, Options{OwnerID: p.Owner, RestrictServers: true})
	_, creds, err := a.credentialsFor(ctx, b, p.Worker)
	if err != nil {
		return frog.Message{}, errors.New("模型配置无效，请检查「设置 → 模型」")
	}
	if creds.base == "" || creds.model == "" {
		return frog.Message{}, errors.New("还没有配置模型：请在「设置 → 模型」里添加 API 渠道并设好主控")
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
			if m.Content == "" {
				row["content"] = nil
			}
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
	prov := a.inferenceProvider(ctx)
	if prov.Protocol != preferences.ProtocolResponses && !strings.HasPrefix(prov.AuthKind, "oauth-") {
		return a.frogStream(ctx, b, creds, messages, tools, onText, req.OnReasoning)
	}
	reply, err := a.chat(ctx, creds.base, creds.model, creds.key, messages, tools)
	if err != nil {
		if ctx.Err() != nil {
			return frog.Message{}, ctx.Err()
		}
		return frog.Message{}, errors.New(a.safe(ctx, err.Error()))
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

// frogStream calls an OpenAI-compatible Chat Completions endpoint with
// stream=true and forwards text and reasoning as they arrive. Streamed text
// is redacted per chunk; the final message is redacted as a whole.
func (a *Agent) frogStream(ctx context.Context, b model.Bot, creds credentials, messages, tools []map[string]any,
	onText, onReason func(string)) (msg frog.Message, resultErr error) {
	var usage *frog.Usage
	defer func() {
		if a.Usage == nil {
			return
		}
		actor, _ := ctx.Value(usageKey{}).(usageActor)
		var in, out, total int64
		if usage != nil {
			in, out, total = usage.Input, usage.Output, usage.Total
		}
		if err := a.Usage.Record(b.ID, actor.Purpose, creds.provider, creds.model, in, out, total, usage != nil, resultErr != nil); err != nil {
			log.Print("Token 用量保存失败")
		}
	}()
	timeout := time.Duration(a.execution().TimeoutSeconds) * time.Second
	if timeout < 2*time.Minute {
		timeout = 2 * time.Minute // a stream stays open while the model writes
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body := map[string]any{"model": creds.model, "messages": messages, "stream": true,
		"stream_options": map[string]any{"include_usage": true}}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	res, err := a.postStream(callCtx, creds, body)
	if err == nil && (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnprocessableEntity) {
		// Some compatible servers reject stream_options; retry once without it.
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		if !bytes.Contains(payload, []byte("stream_options")) && !bytes.Contains(payload, []byte("include_usage")) {
			return frog.Message{}, errors.New(a.safe(ctx, providerStatusError(res.StatusCode, payload).Error()))
		}
		delete(body, "stream_options")
		res, err = a.postStream(callCtx, creds, body)
	}
	if err != nil {
		if ctx.Err() != nil {
			return frog.Message{}, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return frog.Message{}, errors.New("模型响应超时")
		}
		return frog.Message{}, fmt.Errorf("连不上模型接口：%s", a.safe(ctx, err.Error()))
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return frog.Message{}, errors.New(a.safe(ctx, providerStatusError(res.StatusCode, payload).Error()))
	}
	redactChunk := func(fn func(string)) func(string) {
		if fn == nil {
			return nil
		}
		return func(s string) { fn(a.redact(ctx, s)) }
	}
	var reader io.Reader = res.Body
	if !strings.Contains(res.Header.Get("Content-Type"), "event-stream") {
		// A server that ignores stream=true answers with one JSON body.
		payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		if err != nil {
			return frog.Message{}, errors.New("读取模型响应失败")
		}
		return a.frogFromJSON(ctx, payload, redactChunk(onText), &usage)
	}
	m, u, err := frog.ParseStream(callCtx, reader, redactChunk(onText), redactChunk(onReason))
	usage = u
	if err != nil {
		if ctx.Err() != nil {
			return frog.Message{}, ctx.Err()
		}
		return frog.Message{}, errors.New(a.safe(ctx, err.Error()))
	}
	m.Content = a.redact(ctx, m.Content)
	for i := range m.ToolCalls {
		args := json.RawMessage(a.redact(ctx, string(m.ToolCalls[i].Args)))
		if !json.Valid(args) {
			args = json.RawMessage("null")
		}
		m.ToolCalls[i].Args = args
	}
	return m, nil
}

func (a *Agent) postStream(ctx context.Context, creds credentials, body map[string]any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(creds.base, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if creds.key != "" {
		req.Header.Set("Authorization", "Bearer "+creds.key)
	}
	client := &http.Client{}
	if a.HTTP != nil {
		copy := *a.HTTP
		client = &copy
	}
	client.Timeout = 0 // bounded by ctx; a stream may legitimately run for minutes
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}

func (a *Agent) frogFromJSON(ctx context.Context, payload []byte, onText func(string), usage **frog.Usage) (frog.Message, error) {
	var parsed struct {
		Usage *struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
			Total      int64 `json:"total_tokens"`
		} `json:"usage"`
		Choices []struct {
			Message struct {
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return frog.Message{}, errors.New("模型响应格式不正确")
	}
	if u := parsed.Usage; u != nil {
		*usage = &frog.Usage{Input: u.Prompt, Output: u.Completion, Total: u.Total}
	}
	if len(parsed.Choices) == 0 {
		return frog.Message{}, errors.New("模型没有返回内容")
	}
	out := frog.Message{Role: "assistant", Content: a.redact(ctx, parsed.Choices[0].Message.Content)}
	for _, raw := range parsed.Choices[0].Message.ToolCalls {
		call, err := normalizeCall(raw)
		if err != nil {
			return frog.Message{}, err
		}
		args := json.RawMessage(a.redact(ctx, call.Function.Arguments))
		if !json.Valid(args) {
			args = json.RawMessage("null")
		}
		out.ToolCalls = append(out.ToolCalls, frog.ToolCall{ID: call.ID, Name: call.Function.Name, Args: args})
	}
	if onText != nil && out.Content != "" {
		onText(out.Content)
	}
	return out, nil
}
