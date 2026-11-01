package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

// SecretReply is the operator's plaintext for a password card. It is accepted
// once, encrypted into the vault, and never written into the conversation.
type SecretReply struct {
	MessageID string `json:"message_id"`
	Value     string `json:"value"`
}

// ErrSecretStale is returned when the conversation moved past an unanswered card.
var ErrSecretStale = errors.New("对话已经继续，这个密钥不再等待输入，请直接在密钥保险箱里保存")

func secretAck(name string) string {
	return "已把密钥「" + name + "」存进保险箱。模型看不到明文。"
}

// ParseSecretRequest validates a request_secret call. The model never supplies
// the secret value.
func ParseSecretRequest(arguments string) (model.SecretRequest, error) {
	var args struct {
		Name        string `json:"name"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return model.SecretRequest{}, errors.New("request_secret 参数不是有效的 JSON")
	}
	name, err := secret.ValidName(args.Name)
	if err != nil {
		return model.SecretRequest{}, err
	}
	label, err := secret.CleanLabel(args.Label)
	if err != nil {
		return model.SecretRequest{}, err
	}
	return model.SecretRequest{Name: name, Label: label, Description: secret.CleanDescription(args.Description)}, nil
}

func secretContext(s *model.SecretRequest) string {
	if s.ProvidedAt != nil {
		return "\n（已用密码卡片保存密钥「" + s.Name + "」。明文不在对话里，命令里写成 {{secret:" + s.Name + "}}）"
	}
	return "\n（已向用户展示密码卡片，等待保存密钥「" + s.Name + "」。不要再索要明文）"
}

func (a *Agent) requestSecret(ctx context.Context, call toolCall, tr *trace) string {
	if isWorker(ctx) || tr == nil {
		return "协作 Bot 不能直接向用户索取密钥。把需要的密钥名称写进结论，由主控来请求。"
	}
	if tr.choice != nil || tr.secret != nil {
		return "这一轮已经在等用户了，等用户回应后再继续。"
	}
	req, err := ParseSecretRequest(call.Function.Arguments)
	if err != nil {
		return err.Error()
	}
	if _, err = a.Store.SecretByName(ctx, req.Name); err == nil {
		return fmt.Sprintf("保险箱里已经有密钥「%s」。命令里写成 {{secret:%s}}。不要再向用户索要，也不要覆盖。", req.Name, req.Name)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "读取保险箱失败"
	}
	items, err := a.Store.ListSecrets(ctx)
	if err != nil {
		return "读取保险箱失败"
	}
	if len(items) >= secret.MaxCount {
		return "保险箱已有 100 个密钥，不能再添加。请让用户在密钥保险箱里删除不用的项。"
	}
	tr.secret = &req
	return "已把密码卡片展示给用户。本轮到此结束，等用户安全保存后再继续。你不会看到明文。"
}

// answerSecret encrypts the operator's value into the vault and returns only an
// acknowledgement. done is true when this card was already saved, in which case
// the value is ignored and no new turn should run.
func (a *Agent) answerSecret(ctx context.Context, sessionID string, r SecretReply) (text string, done bool, err error) {
	if sessionID == "" || r.MessageID == "" {
		return "", false, errors.New("缺少要保存的密钥")
	}
	msgs, err := a.Store.ListMessages(ctx, sessionID)
	if err != nil {
		return "", false, err
	}
	var card *model.SecretRequest
	latest := true
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.ID == r.MessageID && m.Role == "assistant" && m.SecretRequest != nil {
			card = m.SecretRequest
			break
		}
		if m.Role == "user" {
			latest = false
		}
	}
	if card == nil {
		return "", false, errors.New("密钥请求不存在")
	}
	ack := secretAck(card.Name)
	if card.ProvidedAt != nil {
		return ack, true, nil
	}
	if !latest {
		return "", false, ErrSecretStale
	}
	if err = secret.ValidValue(r.Value); err != nil {
		return "", false, err
	}
	if a.Box == nil {
		return "", false, errors.New("保险箱未配置")
	}
	enc, err := a.Box.Encrypt(r.Value)
	if err != nil {
		return "", false, errors.New("加密失败")
	}
	existing, err := a.Store.SecretByName(ctx, card.Name)
	if errors.Is(err, store.ErrNotFound) {
		items, e := a.Store.ListSecrets(ctx)
		if e != nil {
			return "", false, e
		}
		if len(items) >= secret.MaxCount {
			return "", false, store.ErrLimit
		}
		_, err = a.Store.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: card.Name, Description: card.Description}, EncryptedValue: enc})
	} else if err != nil {
		return "", false, err
	} else {
		_, err = a.Store.UpdateSecret(ctx, existing.ID, enc, nil)
	}
	if err != nil {
		return "", false, err
	}
	_, err = a.Store.MarkSecretProvided(ctx, sessionID, r.MessageID, time.Now().UTC())
	if errors.Is(err, store.ErrConflict) {
		return ack, true, nil
	}
	if err != nil {
		return "", false, err
	}
	return ack, false, nil
}

var requestSecretTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "request_secret",
		"description": toolRequestSecretDesc,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]string{"type": "string", "description": "保险箱里的名称，命令写成 {{secret:名称}}。可用中文，不能有空格、换行或大括号"},
				"label":       map[string]string{"type": "string", "description": "密码卡片标题，例如「数据库密码」"},
				"description": map[string]string{"type": "string", "description": "可选，向用户说明这是什么密钥"},
			},
			"required": []string{"name", "label"},
		},
	},
}
