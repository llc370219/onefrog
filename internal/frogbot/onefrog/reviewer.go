package onefrog

import (
	"context"
	"encoding/json"
	"strings"
)

// ReviewRequest 是交给审核者的内容：任务目标 + 打算执行的具体操作。
type ReviewRequest struct {
	TaskID    string          `json:"task_id"`
	TaskInput string          `json:"task_input"`
	ToolName  string          `json:"tool_name"`
	Args      json.RawMessage `json:"args"`
	Risk      Risk            `json:"risk"`
	Workspace string          `json:"workspace"`
}

// Reviewer 在执行前审核一个操作（类似 Codex 的自动审批）。
type Reviewer interface {
	Review(ctx context.Context, req ReviewRequest) (Verdict, error)
}

// LLMReviewer 用一个单独的（便宜的）模型做审核。
type LLMReviewer struct {
	Provider Provider
	Model    string
}

const reviewerPrompt = `你是一个安全审核员。一个编程 agent 正准备执行下面的操作。
判断它是否：符合用户的任务目标、只影响工作区、不会泄露数据或造成难以撤销的破坏。
只输出一个 JSON 对象，不要输出其他内容：
{"decision":"allow|deny|ask_user","reason":"一句话理由"}
拿不准时选 ask_user。`

func (r *LLMReviewer) Review(ctx context.Context, req ReviewRequest) (Verdict, error) {
	body, _ := json.MarshalIndent(req, "", "  ")
	msg, err := r.Provider.Chat(ctx, ChatRequest{Model: r.Model, Messages: []Message{
		{Role: "system", Content: reviewerPrompt},
		{Role: "user", Content: string(body)},
	}}, nil)
	if err != nil {
		return Verdict{}, err
	}
	return ParseVerdict(msg.Content), nil
}

// ParseVerdict 解析审核模型的输出；解析失败或结果不合法一律 ask_user。
func ParseVerdict(s string) Verdict {
	bad := Verdict{AskUser, "审核结果无法解析，交给用户决定", "reviewer"}
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j <= i {
		return bad
	}
	var v Verdict
	if json.Unmarshal([]byte(s[i:j+1]), &v) != nil {
		return bad
	}
	switch v.Decision {
	case Allow, Deny, AskUser:
		v.By = "reviewer"
		return v
	}
	return bad
}
