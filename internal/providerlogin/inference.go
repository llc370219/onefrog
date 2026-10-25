package providerlogin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"onefrog/internal/id"
	"sort"
	"strings"
	"time"
)

func (m *Manager) request(ctx context.Context, method, url, token, account string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		reader = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, url, reader)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OneFrog/0.1 (OAuth compatibility)")
	if account != "" {
		req.Header.Set("ChatGPT-Account-Id", account)
	}
	client := *m.HTTP
	client.Timeout = 0
	if _, ok := ctx.Deadline(); !ok {
		bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		req = req.WithContext(bounded)
	}
	res, e := client.Do(req)
	if e != nil {
		return nil, errors.New("OAuth 服务连接失败，请检查网络")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("OAuth 上游返回 %d；请检查账号权限、配额或重新登录", res.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 8*1024*1024+1))
	if len(raw) > 8*1024*1024 {
		return nil, errors.New("OAuth 服务响应过大")
	}
	return raw, e
}
func (m *Manager) Models(ctx context.Context, owner, kind string) ([]string, error) {
	c, e := m.Access(ctx, owner, kind)
	if e != nil {
		return nil, e
	}
	d, e := m.definition(kind)
	if e != nil {
		return nil, e
	}
	var raw []byte
	if kind == "oauth-openai" {
		raw, e = m.request(ctx, "GET", d.BaseURL+"/models?client_version=0.1.0", c.Access, c.AccountID, nil)
	} else {
		if c.ProjectID == "" {
			c.ProjectID, e = m.project(ctx, c.Access)
			if e != nil {
				return nil, e
			}
			m.mu.Lock()
			m.accounts[key(owner, kind)] = c
			e = m.persistLocked()
			m.mu.Unlock()
			if e != nil {
				return nil, e
			}
		}
		raw, e = m.request(ctx, "POST", d.BaseURL+"/v1internal:fetchAvailableModels", c.Access, "", map[string]string{"project": c.ProjectID})
	}
	if e != nil {
		return nil, e
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return nil, errors.New("模型目录格式无效")
	}
	set := map[string]bool{}
	for _, field := range []string{"models", "data"} {
		var list []map[string]any
		if json.Unmarshal(v[field], &list) == nil {
			for _, r := range list {
				for _, k := range []string{"slug", "id", "name"} {
					if s, ok := r[k].(string); ok && s != "" {
						set[s] = true
						break
					}
				}
			}
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(v[field], &entries) == nil {
			for name := range entries {
				set[name] = true
			}
		}
	}
	out := []string{}
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, errors.New("账号未返回可用模型，请检查订阅或官方应用初始化")
	}
	return out, nil
}
func (m *Manager) CompleteChat(ctx context.Context, owner, kind, model string, messages []map[string]any, tools []map[string]any) ([]byte, error) {
	c, e := m.Access(ctx, owner, kind)
	if e != nil {
		return nil, e
	}
	d, e := m.definition(kind)
	if e != nil {
		return nil, e
	}
	if kind == "oauth-openai" {
		body := responsesRequest(model, messages, tools)
		raw, e := m.request(ctx, "POST", d.BaseURL+"/responses", c.Access, c.AccountID, body)
		if e != nil {
			return nil, e
		}
		return normalizeResponses(raw)
	}
	if c.ProjectID == "" {
		return nil, errors.New("请先在官方 Antigravity 应用完成账号初始化并拉取模型")
	}
	body := googleRequest(model, c.ProjectID, messages, tools)
	raw, e := m.request(ctx, "POST", d.BaseURL+"/v1internal:generateContent", c.Access, "", body)
	if e != nil {
		return nil, e
	}
	return normalizeGoogle(raw)
}
func contentParts(content any) []map[string]any {
	if s, ok := content.(string); ok {
		return []map[string]any{{"type": "text", "text": s}}
	}
	raw, _ := json.Marshal(content)
	var parts []map[string]any
	_ = json.Unmarshal(raw, &parts)
	return parts
}
func arr(v any) []map[string]any {
	raw, _ := json.Marshal(v)
	var out []map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func object(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func str(v any) string { s, _ := v.(string); return s }
func responsesRequest(model string, messages, tools []map[string]any) map[string]any {
	input := []map[string]any{}
	instructions := []string{}
	for _, msg := range messages {
		role := str(msg["role"])
		if role == "system" {
			instructions = append(instructions, str(msg["content"]))
			continue
		}
		if role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": msg["tool_call_id"], "output": msg["content"]})
			continue
		}
		parts := []map[string]any{}
		for _, p := range contentParts(msg["content"]) {
			switch p["type"] {
			case "text":
				if str(p["text"]) != "" {
					t := "input_text"
					if role == "assistant" {
						t = "output_text"
					}
					parts = append(parts, map[string]any{"type": t, "text": p["text"]})
				}
			case "image_url":
				image := object(p["image_url"])
				parts = append(parts, map[string]any{"type": "input_image", "image_url": image["url"]})
			case "file":
				f := object(p["file"])
				parts = append(parts, map[string]any{"type": "input_file", "filename": f["filename"], "file_data": f["file_data"]})
			}
		}
		if len(parts) > 0 {
			input = append(input, map[string]any{"role": role, "content": parts})
		}
		for _, call := range arr(msg["tool_calls"]) {
			f := object(call["function"])
			input = append(input, map[string]any{"type": "function_call", "call_id": call["id"], "name": f["name"], "arguments": f["arguments"]})
		}
	}
	defs := []map[string]any{}
	for _, t := range tools {
		f := object(t["function"])
		defs = append(defs, map[string]any{"type": "function", "name": f["name"], "description": f["description"], "parameters": f["parameters"]})
	}
	body := map[string]any{"model": model, "instructions": strings.Join(instructions, "\n"), "input": input, "store": false, "stream": true}
	if len(defs) > 0 {
		body["tools"] = defs
	}
	return body
}
func normalizeResponses(raw []byte) ([]byte, error) {
	var response map[string]any
	if json.Unmarshal(raw, &response) != nil {
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 4096), 8*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var ev map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) != nil {
				continue
			}
			if ev["type"] == "response.failed" || ev["type"] == "error" {
				return nil, errors.New("OAuth 模型响应失败，请检查模型权限或配额")
			}
			if ev["type"] == "response.completed" {
				response = object(ev["response"])
			}
		}
		if scanner.Err() != nil {
			return nil, errors.New("OAuth 流读取失败")
		}
	}
	if response["error"] != nil || response["status"] == "failed" || response["status"] == "incomplete" {
		return nil, errors.New("OAuth 模型未完成本次请求")
	}
	texts := []string{}
	calls := []map[string]any{}
	for _, item := range arr(response["output"]) {
		if item["type"] == "function_call" {
			calls = append(calls, map[string]any{"id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": item["arguments"]}})
		}
		for _, p := range arr(item["content"]) {
			if p["type"] == "output_text" {
				texts = append(texts, str(p["text"]))
			}
		}
	}
	if response == nil || len(texts) == 0 && len(calls) == 0 {
		return nil, errors.New("OAuth 模型未返回文本或工具调用")
	}
	out := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Join(texts, "\n"), "tool_calls": calls}}}}
	if u := object(response["usage"]); len(u) > 0 {
		out["usage"] = map[string]any{"prompt_tokens": u["input_tokens"], "completion_tokens": u["output_tokens"], "total_tokens": u["total_tokens"]}
	}
	return json.Marshal(out)
}
func googleRequest(model, project string, messages, tools []map[string]any) map[string]any {
	contents := []map[string]any{}
	system := []string{}
	names := map[string]string{}
	for _, msg := range messages {
		role := str(msg["role"])
		if role == "system" {
			system = append(system, str(msg["content"]))
			continue
		}
		parts := []map[string]any{}
		if role == "tool" {
			parts = append(parts, map[string]any{"functionResponse": map[string]any{"name": names[str(msg["tool_call_id"])], "response": map[string]any{"result": msg["content"]}}})
			role = "user"
		} else {
			if role == "assistant" {
				role = "model"
			}
			for _, p := range contentParts(msg["content"]) {
				switch p["type"] {
				case "text":
					if str(p["text"]) != "" {
						parts = append(parts, map[string]any{"text": p["text"]})
					}
				case "image_url", "file":
					uri := ""
					if p["type"] == "image_url" {
						uri = str(object(p["image_url"])["url"])
					} else {
						uri = str(object(p["file"])["file_data"])
					}
					if i := strings.Index(uri, ";base64,"); strings.HasPrefix(uri, "data:") && i > 0 {
						parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": uri[5:i], "data": uri[i+8:]}})
					}
				}
			}
			for _, call := range arr(msg["tool_calls"]) {
				f := object(call["function"])
				name := str(f["name"])
				names[str(call["id"])] = name
				var args any
				_ = json.Unmarshal([]byte(str(f["arguments"])), &args)
				part := map[string]any{"functionCall": map[string]any{"name": name, "args": args}}
				if sig := str(call["thought_signature"]); sig != "" {
					part["thoughtSignature"] = sig
				}
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			contents = append(contents, map[string]any{"role": role, "parts": parts})
		}
	}
	request := map[string]any{"contents": contents, "systemInstruction": map[string]any{"parts": []any{map[string]any{"text": strings.Join(system, "\n")}}}, "generationConfig": map[string]any{"maxOutputTokens": 8192}}
	decls := []map[string]any{}
	for _, t := range tools {
		f := object(t["function"])
		decls = append(decls, map[string]any{"name": f["name"], "description": f["description"], "parameters": f["parameters"]})
	}
	if len(decls) > 0 {
		request["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}
	return map[string]any{"model": model, "project": project, "userAgent": "antigravity", "requestType": "agent", "requestId": "onefrog-" + id.New(), "request": request}
}
func normalizeGoogle(raw []byte) ([]byte, error) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return nil, errors.New("Google 模型响应格式无效")
	}
	if response, ok := v["response"].(map[string]any); ok {
		v = response
	}
	candidates := arr(v["candidates"])
	if len(candidates) == 0 {
		return nil, errors.New("Google 模型未返回候选结果，请检查账号或安全拦截信息")
	}
	texts := []string{}
	calls := []map[string]any{}
	for _, part := range arr(object(candidates[0]["content"])["parts"]) {
		if part["thought"] != true && str(part["text"]) != "" {
			texts = append(texts, str(part["text"]))
		}
		if f, ok := part["functionCall"].(map[string]any); ok {
			args, _ := json.Marshal(f["args"])
			calls = append(calls, map[string]any{"id": "call_" + id.New(), "type": "function", "thought_signature": part["thoughtSignature"], "function": map[string]any{"name": f["name"], "arguments": string(args)}})
		}
	}
	if len(texts) == 0 && len(calls) == 0 {
		return nil, errors.New("Google 模型未返回文本或工具调用")
	}
	out := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": strings.Join(texts, "\n"), "tool_calls": calls}}}}
	if u := object(v["usageMetadata"]); len(u) > 0 {
		number := func(key string) float64 { n, _ := u[key].(float64); return n }
		out["usage"] = map[string]any{"prompt_tokens": u["promptTokenCount"], "completion_tokens": number("candidatesTokenCount") + number("thoughtsTokenCount"), "total_tokens": u["totalTokenCount"]}
	}
	return json.Marshal(out)
}
