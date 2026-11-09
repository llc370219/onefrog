package onefrog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAI 实现 OpenAI 兼容的 Chat Completions 接口（流式 + 工具调用）。
// 适用于 OpenAI、xAI(https://api.x.ai/v1)、DeepSeek、OpenRouter 等。
type OpenAI struct {
	BaseURL    string // 例如 https://api.x.ai/v1
	APIKey     string
	Client     *http.Client
	MaxRetries int               // 429/5xx 重试次数，默认 2
	Headers    map[string]string // 额外请求头（例如 OpenRouter 的 HTTP-Referer）
}

func NewOpenAI(baseURL, apiKey string) *OpenAI {
	return &OpenAI{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Client: &http.Client{}, MaxRetries: 2}
}

type oaFunc struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Arguments   string          `json:"arguments,omitempty"`
}

type oaToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function oaFunc `json:"function"`
}

type oaMsg struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

func (o *OpenAI) Chat(ctx context.Context, req ChatRequest, onText func(string)) (Message, error) {
	body := map[string]any{"model": req.Model, "stream": true, "messages": toOA(req.Messages)}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{"type": "function",
				"function": oaFunc{Name: t.Name, Description: t.Description, Parameters: t.Schema}})
		}
		body["tools"] = tools
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Message{}, err
	}
	resp, err := o.post(ctx, payload)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	return parseStreamR(ctx, resp.Body, onText, req.OnReasoning)
}

func (o *OpenAI) post(ctx context.Context, payload []byte) (*http.Response, error) {
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	for attempt := 0; ; attempt++ {
		hr, err := http.NewRequestWithContext(ctx, "POST", o.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Content-Type", "application/json")
		hr.Header.Set("Authorization", "Bearer "+o.APIKey)
		for k, v := range o.Headers {
			hr.Header.Set(k, v)
		}
		resp, err := client.Do(hr)
		retry := err != nil || resp.StatusCode == 429 || resp.StatusCode >= 500
		if err == nil && resp.StatusCode == 200 {
			return resp, nil
		}
		if err == nil {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			err = fmt.Errorf("模型接口返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		}
		if !retry || attempt >= o.MaxRetries || ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-time.After(time.Duration(attempt+1) * time.Second):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func toOA(msgs []Message) []oaMsg {
	out := make([]oaMsg, 0, len(msgs))
	for _, m := range msgs {
		c := m.Content
		om := oaMsg{Role: m.Role, Content: &c, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, oaToolCall{ID: tc.ID, Type: "function",
				Function: oaFunc{Name: tc.Name, Arguments: string(tc.Args)}})
		}
		if len(om.ToolCalls) > 0 && c == "" {
			om.Content = nil
		}
		out = append(out, om)
	}
	return out
}

// parseStream 解析 SSE 流：累积文字和按 index 拼接的工具调用参数。
func parseStream(ctx context.Context, r io.Reader, onText func(string)) (Message, error) {
	return parseStreamR(ctx, r, onText, nil)
}

// parseStreamR 同上，另外把 reasoning_content / reasoning 字段和 <think>…</think> 里的内容交给 onReason。
func parseStreamR(ctx context.Context, r io.Reader, onText, onReason func(string)) (Message, error) {
	var split thinkSplitter
	var text strings.Builder
	emitText := func(s string) {
		if s == "" {
			return
		}
		text.WriteString(s)
		if onText != nil {
			onText(s)
		}
	}
	emitReason := func(s string) {
		if s != "" && onReason != nil {
			onReason(s)
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	var calls []*oaToolCall
	byIndex := map[int]*oaToolCall{}
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string       `json:"content"`
					ReasoningContent string       `json:"reasoning_content"`
					Reasoning        string       `json:"reasoning"`
					ToolCalls        []oaToolCall `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Error != nil {
			return Message{}, fmt.Errorf("模型接口错误: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		emitReason(d.ReasoningContent)
		emitReason(d.Reasoning)
		if d.Content != "" {
			t, th := split.feed(d.Content)
			emitReason(th)
			emitText(t)
		}
		for i, tc := range d.ToolCalls {
			idx := i
			if tc.Index != nil {
				idx = *tc.Index
			}
			c := byIndex[idx]
			if c == nil {
				c = &oaToolCall{}
				byIndex[idx] = c
				calls = append(calls, c)
			}
			if tc.ID != "" {
				c.ID = tc.ID
			}
			c.Function.Name += tc.Function.Name
			c.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return Message{}, ctx.Err()
		}
		return Message{}, err
	}
	if ctx.Err() != nil {
		return Message{}, ctx.Err()
	}
	t, th := split.flush()
	emitReason(th)
	emitText(t)
	msg := Message{Role: "assistant", Content: text.String()}
	for i, c := range calls {
		args := strings.TrimSpace(c.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		raw := json.RawMessage(args)
		if !json.Valid(raw) { // 保证事件能被 JSON 序列化，工具会收到参数错误
			raw, _ = json.Marshal(map[string]string{"_invalid_json": args})
		}
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Args: raw})
	}
	return msg, nil
}

// thinkSplitter 把流式文字里的 <think>…</think> 分离出来；标签可能被切在两个分片之间。
type thinkSplitter struct {
	in  bool
	buf string
}

func (s *thinkSplitter) feed(chunk string) (text, think string) {
	s.buf += chunk
	var tb, kb strings.Builder
	for {
		tag := "<think>"
		if s.in {
			tag = "</think>"
		}
		if i := strings.Index(s.buf, tag); i >= 0 {
			if s.in {
				kb.WriteString(s.buf[:i])
			} else {
				tb.WriteString(s.buf[:i])
			}
			s.buf = s.buf[i+len(tag):]
			s.in = !s.in
			continue
		}
		// 末尾可能是半个标签，先留着
		keep := 0
		for n := min(len(tag)-1, len(s.buf)); n > 0; n-- {
			if strings.HasSuffix(s.buf, tag[:n]) {
				keep = n
				break
			}
		}
		out := s.buf[:len(s.buf)-keep]
		s.buf = s.buf[len(s.buf)-keep:]
		if s.in {
			kb.WriteString(out)
		} else {
			tb.WriteString(out)
		}
		return tb.String(), kb.String()
	}
}

func (s *thinkSplitter) flush() (text, think string) {
	b := s.buf
	s.buf = ""
	if s.in {
		return "", b
	}
	return b, ""
}
