package bot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"onefrog/internal/model"
	"onefrog/internal/store"
)

// Limits for ask_user choice cards.
const (
	maxChoicePrompt  = 300
	maxChoiceLabel   = 40
	maxChoiceValue   = 120
	maxChoiceCustom  = 500
	minChoiceOptions = 2
	maxChoiceOptions = 6
)

// ChoiceReply answers a choice card; it is sent instead of free text to
// resume the conversation.
type ChoiceReply struct {
	MessageID string   `json:"message_id"`
	Values    []string `json:"values,omitempty"`
	Custom    string   `json:"custom,omitempty"`
}

// ErrChoiceAnswered is returned when a card already has a different answer.
var ErrChoiceAnswered = errors.New("这个问题已经回答过了")

// ErrChoiceStale is returned for a card the conversation has moved past.
var ErrChoiceStale = errors.New("对话已经继续，这个问题不再等待回答，请直接输入")

func cleanText(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return clip(strings.TrimSpace(s), limit)
}

// ParseChoice validates the arguments of an ask_user call.
func ParseChoice(arguments string) (model.Choice, error) {
	var args struct {
		Prompt  string `json:"prompt"`
		Options []struct {
			Label string `json:"label"`
			Value string `json:"value"`
		} `json:"options"`
		MultiSelect bool `json:"multi_select"`
		AllowCustom bool `json:"allow_custom"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return model.Choice{}, errors.New("ask_user 参数不是有效的 JSON")
	}
	c := model.Choice{Prompt: cleanText(args.Prompt, maxChoicePrompt), MultiSelect: args.MultiSelect, AllowCustom: args.AllowCustom}
	if c.Prompt == "" {
		return model.Choice{}, errors.New("ask_user 需要 prompt")
	}
	if len(args.Options) < minChoiceOptions || len(args.Options) > maxChoiceOptions {
		return model.Choice{}, errors.New("ask_user 需要 2–6 个选项")
	}
	seen := map[string]bool{}
	for _, o := range args.Options {
		label := cleanText(o.Label, maxChoiceLabel)
		value := cleanText(o.Value, maxChoiceValue)
		if value == "" {
			value = label
		}
		if label == "" {
			return model.Choice{}, errors.New("ask_user 的选项不能为空")
		}
		if seen[value] {
			return model.Choice{}, errors.New("ask_user 的选项不能重复")
		}
		seen[value] = true
		c.Options = append(c.Options, model.ChoiceOption{Label: label, Value: value})
	}
	return c, nil
}

// normalizeAnswer checks a reply against its card and returns the stored answer.
func normalizeAnswer(c model.Choice, r ChoiceReply) (model.ChoiceAnswer, error) {
	ans := model.ChoiceAnswer{Custom: strings.TrimSpace(r.Custom)}
	if ans.Custom != "" && !c.AllowCustom {
		return ans, errors.New("这个问题不接受自定义回答")
	}
	if len([]rune(ans.Custom)) > maxChoiceCustom {
		return ans, errors.New("自定义回答最多 500 字")
	}
	seen := map[string]bool{}
	for _, opt := range c.Options {
		for _, v := range r.Values {
			if v == opt.Value && !seen[v] {
				seen[v] = true
				ans.Values = append(ans.Values, v)
			}
		}
	}
	if len(ans.Values) != len(r.Values) {
		return ans, errors.New("选项无效，请刷新后重试")
	}
	if !c.MultiSelect && len(ans.Values) > 1 {
		return ans, errors.New("这个问题只能选一项")
	}
	if len(ans.Values) == 0 && ans.Custom == "" {
		return ans, errors.New("请选择一个选项")
	}
	return ans, nil
}

func sameAnswer(a, b model.ChoiceAnswer) bool {
	if a.Custom != b.Custom || len(a.Values) != len(b.Values) {
		return false
	}
	for i := range a.Values {
		if a.Values[i] != b.Values[i] {
			return false
		}
	}
	return true
}

// answerText is what the operator "said" by answering the card.
func answerText(c model.Choice, ans model.ChoiceAnswer) string {
	labels := []string{}
	for _, v := range ans.Values {
		for _, o := range c.Options {
			if o.Value == v {
				labels = append(labels, o.Label)
			}
		}
	}
	text := ""
	if len(labels) > 0 {
		text = "我的选择：" + strings.Join(labels, "、")
	}
	if ans.Custom != "" {
		if text != "" {
			text += "\n补充："
		}
		text += ans.Custom
	}
	return text
}

// choiceContext describes a card in the model's history.
func choiceContext(c *model.Choice) string {
	labels := make([]string, len(c.Options))
	for i, o := range c.Options {
		labels[i] = o.Label
	}
	return "\n（已用选项卡片询问用户：" + c.Prompt + "；选项：" + strings.Join(labels, " / ") + "）"
}

// answerChoice records the reply and returns the user message text. done is
// true when the same answer was already recorded (a retried request), in
// which case no new turn should run.
func (a *Agent) answerChoice(ctx context.Context, sessionID string, r ChoiceReply) (text string, done bool, err error) {
	if sessionID == "" || r.MessageID == "" {
		return "", false, errors.New("缺少要回答的问题")
	}
	msgs, err := a.Store.ListMessages(ctx, sessionID)
	if err != nil {
		return "", false, err
	}
	var card *model.Choice
	latest := true
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.ID == r.MessageID && m.Role == "assistant" && m.Choice != nil {
			card = m.Choice
			break
		}
		if m.Role == "user" {
			latest = false
		}
	}
	if card == nil {
		return "", false, errors.New("问题不存在")
	}
	ans, err := normalizeAnswer(*card, r)
	if err != nil {
		return "", false, err
	}
	if !latest {
		if card.Answer != nil && sameAnswer(*card.Answer, ans) {
			return answerText(*card, ans), true, nil
		}
		if card.Answer != nil {
			return "", false, ErrChoiceAnswered
		}
		return "", false, ErrChoiceStale
	}
	ans.AnsweredAt = time.Now().UTC()
	stored, err := a.Store.AnswerChoice(ctx, sessionID, r.MessageID, ans)
	if errors.Is(err, store.ErrConflict) {
		if stored.Choice != nil && stored.Choice.Answer != nil && sameAnswer(*stored.Choice.Answer, ans) {
			return answerText(*card, ans), true, nil
		}
		return "", false, ErrChoiceAnswered
	}
	if err != nil {
		return "", false, err
	}
	return answerText(*card, ans), false, nil
}

func isWorker(ctx context.Context) bool {
	actor, ok := ctx.Value(usageKey{}).(usageActor)
	return ok && actor.Purpose == "worker"
}

var askUserTool = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        "ask_user",
		"description": toolAskUserDesc,
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]string{"type": "string", "description": "要用户决定的问题，一句话"},
				"options": map[string]any{
					"type": "array", "minItems": minChoiceOptions, "maxItems": maxChoiceOptions,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label": map[string]string{"type": "string", "description": "按钮文字，简短"},
							"value": map[string]string{"type": "string", "description": "可选，选项的取值，默认同 label"},
						},
						"required": []string{"label"},
					},
				},
				"multi_select": map[string]any{"type": "boolean", "description": "允许多选"},
				"allow_custom": map[string]any{"type": "boolean", "description": "允许用户另外输入文字"},
			},
			"required": []string{"prompt", "options"},
		},
	},
}
