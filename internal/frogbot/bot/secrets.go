package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"onefrog/internal/frogbot/onefrog"
	"onefrog/internal/frogbot/vault"
)

// 密钥：模型只知道名字，值由用户在密钥卡片里填，直接 POST 到 /api/secrets，
// 加密保存在 data/secrets.json；只在工具执行前一刻替换进参数，输出里再抹掉。

// RequestSecret 发一张密钥卡片（立刻返回）。
func (b *Bot) RequestSecret(name, desc string) (string, error) {
	name = strings.TrimSpace(name)
	if err := vault.ValidName(name); err != nil {
		return "", err
	}
	for _, c := range b.store.PendingSecretCards() {
		if c.Name == name {
			return fmt.Sprintf("已经有一张等待填写 %s 的卡片了，等用户填写即可。", name), nil
		}
	}
	req := &SecretReq{ID: onefrog.NewID("sec"), Name: name, Description: strings.TrimSpace(desc), Status: "pending", Exists: b.vault.Has(name)}
	b.store.Append("message", Message{ID: onefrog.NewID("msg"), Role: "assistant", Secret: req, CreatedAt: time.Now()}, false)
	return fmt.Sprintf("密钥卡片已显示给用户（%s）。用户保存后你会收到系统通知，你永远看不到值。现在用一句话请用户在卡片里填写，提醒不要贴在聊天里，然后结束本轮。", name), nil
}

// SaveSecret 由 HTTP 接口调用：保存用户在卡片里填的值。value 绝不写日志、事件或返回值。
func (b *Bot) SaveSecret(reqID, name, value, desc string) (string, error) {
	var card *SecretReq
	if reqID != "" {
		c, ok := b.store.SecretCard(reqID)
		if !ok {
			return "", errors.New("没有这张密钥卡片")
		}
		if c.Status != "pending" {
			return "", errors.New("这张卡片已经处理过了")
		}
		card, name = &c, c.Name
		if desc == "" {
			desc = c.Description
		}
	}
	if err := b.vault.Set(name, value, desc, nil); err != nil {
		return "", err
	}
	if card != nil {
		card.Status = "saved"
		b.store.Append("secret.card", card, false)
	}
	b.notify(fmt.Sprintf("密钥 %s 已由用户通过安全卡片保存（你看不到值）。之后用 {{secret:%s}} 引用它；如果之前的事在等这个密钥，现在可以继续了。", name, name))
	return name, nil
}

// CancelSecret 用户取消卡片 = 拒绝提供。
func (b *Bot) CancelSecret(reqID string) error {
	c, ok := b.store.SecretCard(reqID)
	if !ok {
		return errors.New("没有这张密钥卡片")
	}
	if c.Status != "pending" {
		return errors.New("这张卡片已经处理过了")
	}
	c.Status = "cancelled"
	b.store.Append("secret.card", c, false)
	b.notify(fmt.Sprintf("用户取消了密钥卡片 %s，视为拒绝提供。不要再索要，除非用户主动提起；需要它的事先别做。", c.Name))
	return nil
}

func (b *Bot) listSecrets() string {
	list := b.vault.List()
	if len(list) == 0 {
		return "密钥库是空的。"
	}
	var sb strings.Builder
	for _, s := range list {
		fmt.Fprintf(&sb, "- %s", s.Name)
		if s.Description != "" {
			fmt.Fprintf(&sb, "：%s", s.Description)
		}
		if len(s.Scope.Tools) > 0 {
			fmt.Fprintf(&sb, "（仅限工具 %s）", strings.Join(s.Scope.Tools, ","))
		}
		if len(s.Scope.Domains) > 0 {
			fmt.Fprintf(&sb, "（仅限域名 %s）", strings.Join(s.Scope.Domains, ","))
		}
		if s.LastUsed != nil {
			fmt.Fprintf(&sb, " 最近使用 %s", s.LastUsed.Format("01-02 15:04"))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("（只有名字和说明，值永远不可见。用 {{secret:NAME}} 在工具参数里引用。）")
	return sb.String()
}

// secretsBrief 给后台 agent 的说明：有哪些密钥、怎么引用。
func (b *Bot) secretsBrief() string {
	list := b.vault.List()
	if len(list) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## 可用密钥\n只能用名字引用，你看不到值：在工具参数里写 {{secret:NAME}}，执行时才会替换（shell 里会变成环境变量 ${NAME}）。每次使用都可能需要用户确认。不要尝试打印、编码或外发密钥。\n")
	for _, s := range list {
		fmt.Fprintf(&sb, "- %s %s\n", s.Name, s.Description)
	}
	return sb.String()
}

func (b *Bot) secretTools() []onefrog.Tool {
	safeTool := func(name, desc, schema string, fn func(json.RawMessage) (string, error)) onefrog.Tool {
		return onefrog.FuncTool{ToolName: name, Desc: desc, JSONSchema: schema, RiskFn: safe,
			Fn: func(_ context.Context, _ onefrog.ToolEnv, a json.RawMessage) (string, error) { return fn(a) }}
	}
	del := onefrog.FuncTool{ToolName: "delete_secret", Desc: "删除一个密钥", JSONSchema: `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`,
		RiskFn: func(json.RawMessage) onefrog.Risk { return onefrog.RiskDanger },
		Fn: func(_ context.Context, _ onefrog.ToolEnv, a json.RawMessage) (string, error) {
			v, _ := readArgs[struct{ Name string }](a)
			if err := b.vault.Delete(v.Name); err != nil {
				return "", err
			}
			return "已删除密钥 " + v.Name, nil
		}}
	return []onefrog.Tool{
		safeTool("list_secrets", "列出已保存的密钥：只有名字和说明，永远不含值。", `{"type":"object","properties":{}}`,
			func(json.RawMessage) (string, error) { return b.listSecrets(), nil }),
		safeTool("request_secret", "请用户通过安全卡片保存一个密钥（名字用 UPPER_SNAKE，如 GITHUB_TOKEN；description 说明用途）。立刻返回，值永远不会给你。",
			`{"type":"object","properties":{"name":{"type":"string"},"description":{"type":"string"}},"required":["name","description"]}`,
			func(a json.RawMessage) (string, error) {
				v, err := readArgs[struct{ Name, Description string }](a)
				if err != nil {
					return "", err
				}
				return b.RequestSecret(v.Name, v.Description)
			}),
		onefrog.PassSecrets{FuncTool: onefrog.FuncTool{ToolName: "delete_secret", Desc: "删除一个密钥（需要用户在审批卡片上确认）。", JSONSchema: del.JSONSchema,
			RiskFn: safe, Fn: func(_ context.Context, _ onefrog.ToolEnv, a json.RawMessage) (string, error) {
				v, _ := readArgs[struct{ Name string }](a)
				if !b.vault.Has(v.Name) {
					return "", fmt.Errorf("没有叫 %s 的密钥", v.Name)
				}
				return b.chatWrite(del, a)
			}}},
	}
}

// 用户在聊天里直接贴密钥：常见格式自动隐藏，并提醒聊天层改用密钥卡片。
var pastedKey = regexp.MustCompile(`\b(sk-ant-[A-Za-z0-9_\-]{20,}|sk-[A-Za-z0-9_\-]{20,}|xai-[A-Za-z0-9]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_\-]{20,}|xox[abprs]-[A-Za-z0-9\-]{10,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_\-]{35})`)

// maskPastedKeys 返回隐藏后的文本和是否发现了疑似密钥。
func maskPastedKeys(text string) (string, bool) {
	if !pastedKey.MatchString(text) {
		return text, false
	}
	return pastedKey.ReplaceAllString(text, "[疑似密钥已隐藏]"), true
}
