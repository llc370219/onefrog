package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"log"
	"onefrog/internal/asset"
	"onefrog/internal/attachment"
	"onefrog/internal/audit"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/providerlogin"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"onefrog/internal/usage"
)

type Exec interface {
	Run(ctx context.Context, host string, port int, user, authType, secret, knownKey, command string) (string, string, error)
}

type Agent struct {
	OAuth    *providerlogin.Manager
	Files    *attachment.Manager
	Usage    *usage.Manager
	Assets   *asset.Ledger
	Store    store.Store
	Box      *secret.Box
	SSH      Exec
	HTTP     *http.Client
	Settings *preferences.Manager
}

type ToolEvent struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	ServerID string `json:"server_id"`
	OK       bool   `json:"ok"`
	Output   string `json:"output"`
}

type Result struct {
	SessionID string      `json:"session_id"`
	Text      string      `json:"text"`
	Tools     []ToolEvent `json:"tools"`
	Steps     []Step      `json:"steps"`
}

func (a *Agent) Reply(ctx context.Context, botID, serverID, sessionID, content string) (Result, error) {
	return a.ReplyTo(ctx, botID, serverID, sessionID, content, nil)
}

func (a *Agent) ReplyTo(ctx context.Context, botID, serverID, sessionID, content string, sink func(Step)) (Result, error) {
	return a.ReplyWithOptions(ctx, botID, serverID, sessionID, content, Options{}, sink)
}
func (a *Agent) replyTurn(ctx context.Context, botID, serverID, sessionID, content string, refs []model.Attachment, sink func(Step)) (Result, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Result{}, errors.New("空消息")
	}
	bot, err := a.pickBot(ctx, botID)
	if err != nil {
		return Result{}, err
	}
	if a.Settings != nil {
		resolved, _, e := a.credentialsFor(ctx, bot, false)
		if e != nil {
			return Result{}, e
		}
		bot = resolved
	}
	ctx = context.WithValue(ctx, taskKey{}, content)
	sess, err := a.Store.EnsureSession(ctx, sessionID, bot.ID, clip(content, 24))
	if err != nil {
		return Result{}, err
	}
	if sess.BotID != bot.ID {
		return Result{}, errors.New("切换主 Bot 时请新建对话")
	}
	savedOptions := options(ctx)
	savedOptions.AttachmentIDs = nil
	optionJSON, _ := json.Marshal(savedOptions)
	if err = a.Store.SaveChatOptions(ctx, sess.ID, optionJSON); err != nil {
		return Result{}, err
	}
	if _, err := a.Store.AddMessage(ctx, model.Message{SessionID: sess.ID, Role: "user", Content: content, Attachments: refs}); err != nil {
		return Result{}, err
	}
	if sink != nil {
		sink(Step{Kind: "session", Output: sess.ID})
	}
	if strings.TrimSpace(bot.BaseURL) == "" {
		text := "模型还没接上。接上之后，主控可以把活分给其他 Bot，命令会先过审核。"
		_, _ = a.Store.AddMessage(ctx, model.Message{SessionID: sess.ID, Role: "assistant", Content: text})
		return Result{SessionID: sess.ID, Text: text}, nil
	}
	result, err := a.lead(ctx, bot, serverID, sess.ID, sink)
	if err != nil {
		return Result{}, errors.New(a.safe(ctx, err.Error()))
	}
	return result, nil
}

func (a *Agent) pickBot(ctx context.Context, botID string) (model.Bot, error) {
	if botID != "" {
		if !a.allowedBot(ctx, botID) {
			return model.Bot{}, errors.New("该 Bot 已停用或被排除")
		}
		return a.Store.GetBot(ctx, botID)
	}
	bots, err := a.Store.ListBots(ctx)
	if err != nil {
		return model.Bot{}, err
	}
	if len(bots) == 0 {
		return model.Bot{}, errors.New("还没有 Bot")
	}
	for _, b := range bots {
		if a.allowedBot(ctx, b.ID) {
			return b, nil
		}
	}
	return model.Bot{}, errors.New("没有可用的 Bot")
}

type toolCall struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
	ID               string `json:"id"`
	Type             string `json:"type"`
	Function         struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type assistantMsg struct {
	Content   string
	ToolCalls []toolCall
}

func (a *Agent) chat(ctx context.Context, baseURL, modelName, apiKey string, messages []map[string]any, tools []map[string]any) (answer assistantMsg, resultErr error) {
	var input, output, total int64
	reported := false
	defer func() {
		actor, ok := ctx.Value(usageKey{}).(usageActor)
		if a.Usage != nil && ok {
			if err := a.Usage.Record(actor.BotID, actor.Purpose, actor.ProviderID, modelName, input, output, total, reported, resultErr != nil); err != nil {
				log.Print("Token 用量保存失败")
			}
		}
	}()
	body := map[string]any{
		"model":    modelName,
		"messages": messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(a.execution().TimeoutSeconds)*time.Second)
	defer cancel()
	raw, _ := json.Marshal(body)
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return assistantMsg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{}
	if a.HTTP != nil {
		copy := *a.HTTP
		client = &copy
	}
	client.Timeout = time.Duration(a.execution().TimeoutSeconds) * time.Second
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	var res *http.Response
	actor, hasActor := ctx.Value(usageKey{}).(usageActor)
	if hasActor && a.Settings != nil && a.OAuth != nil {
		if p, e := a.Settings.Provider(actor.ProviderID); e == nil && strings.HasPrefix(p.AuthKind, "oauth-") {
			payload, e := a.OAuth.CompleteChat(callCtx, options(ctx).OwnerID, p.AuthKind, modelName, messages, tools)
			if e != nil {
				return assistantMsg{}, e
			}
			res = &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload))}
		}
	}
	if res == nil {
		res, err = client.Do(req)
	}
	if err != nil {
		return assistantMsg{}, err
	}
	defer res.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		snippet := string(payload)
		if len(snippet) > 180 {
			snippet = snippet[:180]
		}
		return assistantMsg{}, fmt.Errorf("模型接口返回 %d：%s", res.StatusCode, snippet)
	}
	var parsed struct {
		Usage *struct {
			Prompt     *int64 `json:"prompt_tokens"`
			Completion *int64 `json:"completion_tokens"`
			Input      *int64 `json:"input_tokens"`
			Output     *int64 `json:"output_tokens"`
			Total      *int64 `json:"total_tokens"`
		} `json:"usage"`
		Choices []struct {
			Message struct {
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return assistantMsg{}, err
	}
	if u := parsed.Usage; u != nil && (u.Prompt != nil || u.Completion != nil || u.Input != nil || u.Output != nil || u.Total != nil) {
		reported = true
		if u.Prompt != nil {
			input = *u.Prompt
		} else if u.Input != nil {
			input = *u.Input
		}
		if u.Completion != nil {
			output = *u.Completion
		} else if u.Output != nil {
			output = *u.Output
		}
		if u.Total != nil {
			total = *u.Total
		} else {
			total = input + output
		}
	}
	if len(parsed.Choices) == 0 {
		return assistantMsg{}, errors.New("模型没有返回内容")
	}
	msg := assistantMsg{Content: parsed.Choices[0].Message.Content}
	for _, raw := range parsed.Choices[0].Message.ToolCalls {
		call, err := normalizeCall(raw)
		if err != nil {
			return assistantMsg{}, err
		}
		msg.ToolCalls = append(msg.ToolCalls, call)
	}
	return msg, nil
}

func (a *Agent) runTool(ctx context.Context, botID, fallbackServer string, call toolCall) ToolEvent {
	var args struct {
		ServerID string `json:"server_id"`
		Command  string `json:"command"`
	}
	_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
	if args.ServerID == "" {
		args.ServerID = fallbackServer
	}
	ev := ToolEvent{Name: "run_command", Command: a.redact(ctx, args.Command), ServerID: args.ServerID}
	if call.Function.Name != "run_command" {
		ev.Output = "未知工具"
		return ev
	}
	rec, err := a.Store.GetServer(ctx, args.ServerID)
	if err != nil {
		rec, err = a.serverByName(ctx, args.ServerID)
		if err != nil {
			ev.Output = "找不到服务器"
			return ev
		}
	}
	if options(ctx).RestrictServers && (fallbackServer == "" || rec.ID != fallbackServer) {
		ev.Output = "请先手动选择目标服务器；本轮只能操作选中的服务器"
		return ev
	}
	expanded, err := a.expand(ctx, args.Command)
	if err != nil {
		ev.Output = err.Error()
		return ev
	}
	if rec.HostKey == "" {
		ev.Output = "该服务器的 SSH 主机指纹尚未确认：请先在服务器页面点“测试连接”核对并确认指纹"
		return ev
	}
	mode := a.approvalMode(ctx)
	hard := audit.Decide(expanded)
	if !hard.Allow && mode != "manual" {
		a.recordReview(ctx, botID, rec.ID, ev.Command, Review{Allow: false, Reason: hard.Reason, Risk: "high", Mode: "rules"})
		ev.Output = "已拦截：" + hard.Reason
		_, _ = a.Store.AddHistory(ctx, store.HistoryRec{History: model.History{BotID: botID, ServerID: rec.ID, Action: ev.Command, Command: ev.Command, Status: "blocked"}})
		return ev
	}
	var decision Review
	if mode == "manual" {
		warning := ""
		if !hard.Allow {
			warning = "命中危险命令关键字：" + hard.Reason
		}
		decision = a.manualApprove(ctx, botID, rec, ev.Command, warning)
	} else {
		decision = a.approve(ctx, botID, rec.ID, args.Command)
	}
	if !decision.Allow {
		ev.Output = "已拦截：" + decision.Reason
		_, _ = a.Store.AddHistory(ctx, store.HistoryRec{History: model.History{BotID: botID, ServerID: rec.ID, Action: ev.Command, Command: ev.Command, Status: "blocked"}})
		return ev
	}
	plain := ""
	if rec.EncryptedKey != "" {
		plain, err = a.Box.Decrypt(rec.EncryptedKey)
		if err != nil {
			ev.Output = "登录密钥无法解密"
			return ev
		}
	}
	if a.SSH == nil {
		ev.Output = "没有执行器"
		return ev
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(a.execution().CommandTimeoutSeconds)*time.Second)
	defer cancel()
	// The stored host key is pinned; a mismatch fails and is never overwritten.
	out, _, err := a.SSH.Run(runCtx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, expanded)
	out = a.redact(ctx, out)
	status := "success"
	if err != nil {
		status = "failed"
		if out == "" {
			out = a.redact(ctx, err.Error())
		}
	} else {
		ev.OK = true
	}
	ev.Output = clip(out, 2000)
	_, _ = a.Store.AddHistory(ctx, store.HistoryRec{History: model.History{BotID: botID, ServerID: rec.ID, Action: ev.Command, Command: ev.Command, Status: status}})
	return ev
}

func (a *Agent) apiKey(ctx context.Context, secretID string) (string, error) {
	if secretID == "" {
		return "", nil
	}
	rec, err := a.Store.GetSecret(ctx, secretID)
	if err != nil {
		return "", err
	}
	return a.Box.Decrypt(rec.EncryptedValue)
}

func (a *Agent) expand(ctx context.Context, command string) (string, error) {
	var b strings.Builder
	rest := command
	for {
		i := strings.Index(rest, "{{secret:")
		if i < 0 {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(rest[:i])
		rest = rest[i+len("{{secret:"):]
		j := strings.Index(rest, "}}")
		if j < 0 {
			return "", errors.New("密钥占位符不完整")
		}
		rec, err := a.Store.SecretByName(ctx, rest[:j])
		if err != nil {
			return "", fmt.Errorf("找不到密钥 %s", rest[:j])
		}
		plain, err := a.Box.Decrypt(rec.EncryptedValue)
		if err != nil {
			return "", err
		}
		b.WriteString(plain)
		rest = rest[j+2:]
	}
}

func (a *Agent) redact(ctx context.Context, text string) string {
	recs, err := a.Store.AllSecrets(ctx)
	if err != nil {
		return "[敏感内容检查失败，输出已隐藏]"
	}
	if a.Settings != nil {
		text = a.Settings.Redact(text)
	}
	servers, e := a.Store.ListServers(ctx)
	if e != nil {
		return "[敏感内容检查失败，输出已隐藏]"
	}
	for _, srv := range servers {
		rec, e := a.Store.GetServer(ctx, srv.ID)
		if e != nil {
			return "[敏感内容检查失败，输出已隐藏]"
		}
		if rec.EncryptedKey != "" {
			plain, e := a.Box.Decrypt(rec.EncryptedKey)
			if e != nil {
				return "[敏感内容检查失败，输出已隐藏]"
			}
			if strings.HasPrefix(plain, "onefrog-ssh-v1:") {
				var values map[string]string
				if json.Unmarshal([]byte(strings.TrimPrefix(plain, "onefrog-ssh-v1:")), &values) == nil {
					for _, v := range values {
						if v != "" {
							text = strings.ReplaceAll(text, v, "***")
						}
					}
				}
			} else if plain != "" {
				text = strings.ReplaceAll(text, plain, "***")
			}
		}
	}
	for _, rec := range recs {
		plain, err := a.Box.Decrypt(rec.EncryptedValue)
		if err != nil || plain == "" {
			continue
		}
		text = strings.ReplaceAll(text, plain, "***")
	}
	return text
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (a *Agent) serverByName(ctx context.Context, name string) (store.ServerRec, error) {
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return store.ServerRec{}, err
	}
	for _, item := range servers {
		if item.ID == name || item.Name == name {
			return a.Store.GetServer(ctx, item.ID)
		}
	}
	return store.ServerRec{}, store.ErrNotFound
}
