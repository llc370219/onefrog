package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"onefrog/internal/model"
	"onefrog/internal/secret"
	"onefrog/internal/store"
)

// generateSecret creates a new vault entry from crypto/rand. The plaintext is
// encrypted immediately and never returned to the model, the trace or the
// conversation; the model only learns the name and the {{secret:名称}} form.
// Existing names are never overwritten.
func (a *Agent) generateSecret(ctx context.Context, speaker model.Bot, call toolCall, tr *trace) string {
	var args struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Length      int    `json:"length"`
		Charset     string `json:"charset"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "generate_secret 参数不是有效的 JSON"
	}
	name, err := secret.ValidName(args.Name)
	if err != nil {
		return err.Error()
	}
	if name == "model-key" {
		return "「model-key」是模型渠道专用名称，换一个名称。"
	}
	spec, err := secret.NormalizeSpec(secret.Spec{Length: args.Length, Charset: args.Charset})
	if err != nil {
		return err.Error()
	}
	if a.Box == nil {
		return "保险箱未配置"
	}
	exists := fmt.Sprintf("保险箱里已经有密钥「%s」，不会覆盖。命令里直接写 {{secret:%s}}；需要换新值时请用户在密钥保险箱里轮换。", name, name)
	if _, err = a.Store.SecretByName(ctx, name); err == nil {
		return exists
	} else if !errors.Is(err, store.ErrNotFound) {
		return "读取保险箱失败"
	}
	value, err := secret.Generate(spec)
	if err != nil {
		return err.Error()
	}
	enc, err := a.Box.Encrypt(value)
	value = ""
	if err != nil {
		return "加密失败"
	}
	_, err = a.Store.CreateSecret(ctx, store.SecretRec{Secret: model.Secret{Name: name, Description: args.Description}, EncryptedValue: enc})
	switch {
	case errors.Is(err, store.ErrConflict):
		return exists
	case errors.Is(err, store.ErrLimit):
		return "保险箱已有 100 个密钥，不能再添加。请让用户在密钥保险箱里删除不用的项。"
	case err != nil:
		return "保存到保险箱失败"
	}
	summary := fmt.Sprintf("%d 位，%s", spec.Length, secret.CharsetLabel(spec.Charset))
	if tr != nil {
		tr.noteTool(speaker.Name, ToolEvent{Name: "generate_secret", Command: name, OK: true, Output: "已随机生成并加密保存（" + summary + "），明文不经过模型"})
	}
	return fmt.Sprintf("已随机生成密钥「%s」（%s）并加密存进保险箱。你看不到明文，也不要设法读取或打印它。命令里写 {{secret:%s}}，执行前由服务端替换，输出里的明文会被遮盖。", name, summary, name)
}

var generateSecretTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "generate_secret",
		"description": toolGenerateSecretDesc,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]string{"type": "string", "description": "保险箱里的名称，命令写成 {{secret:名称}}。可用中文，不能有空格、换行或大括号；已存在时不会覆盖"},
				"description": map[string]string{"type": "string", "description": "可选，说明这个密钥的用途，例如「MySQL root 密码」"},
				"length":      map[string]any{"type": "integer", "minimum": secret.MinGenLength, "maximum": secret.MaxGenLength, "description": "长度，默认 32"},
				"charset":     map[string]any{"type": "string", "enum": secret.Charsets(), "description": "alnum=大小写字母和数字（默认）；symbols=再加 -_.,:+=@%^；hex=十六进制；base64url；digits=纯数字"},
			},
			"required": []string{"name"},
		},
	},
}
