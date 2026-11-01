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
)

const (
	mediaOnlyPrompt = "请分析这些附件。"
	mediaOnlyError  = "当前模型不能看图片或 PDF。请换一个支持图片的模型，或把图里的文字贴过来。"
	mediaRetryNote  = "当前模型不能看图片或 PDF，这些附件没有传给它。下面只根据文字回答。"
)

// recoverUnsupportedMedia retries once when a text model rejects an image or
// PDF. An image-only turn is not sent again. A nil payload means the caller
// should show the provider error as-is.
func (a *Agent) recoverUnsupportedMedia(ctx context.Context, baseURL, modelName, apiKey string, messages []map[string]any, tools []map[string]any, payload []byte) ([]byte, string, error) {
	if !stripUnsupportedMedia(messages, payload) {
		return nil, "", nil
	}
	text := strings.TrimSpace(latestUserText(messages))
	if text == "" || text == mediaOnlyPrompt {
		return nil, "", errors.New(mediaOnlyError)
	}
	next, status, err := a.postChat(ctx, baseURL, modelName, apiKey, messages, tools)
	if err != nil {
		return nil, "", err
	}
	if status >= 300 {
		return nil, "", providerStatusError(status, next)
	}
	return next, mediaRetryNote, nil
}

func (a *Agent) postChat(ctx context.Context, baseURL, modelName, apiKey string, messages []map[string]any, tools []map[string]any) ([]byte, int, error) {
	body := map[string]any{
		"model":    modelName,
		"messages": messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	raw, _ := json.Marshal(body)
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
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
			payload, e := a.OAuth.CompleteChat(ctx, options(ctx).OwnerID, p.AuthKind, modelName, messages, tools)
			if e != nil {
				return nil, 0, e
			}
			res = &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(payload))}
		}
	}
	if res == nil {
		res, err = client.Do(req)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("连不上模型接口：%s", clip(err.Error(), 180))
	}
	defer res.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return payload, res.StatusCode, nil
}

// stripUnsupportedMedia removes image and PDF parts after the model rejects
// that input. The caller's message maps are edited in place so a later
// tool-loop call does not send the same parts again.
func stripUnsupportedMedia(messages []map[string]any, payload []byte) bool {
	if !unsupportedMedia(payload) {
		return false
	}
	stripped := false
	for _, msg := range messages {
		switch parts := msg["content"].(type) {
		case []map[string]any:
			kept := make([]map[string]any, 0, len(parts))
			changed := false
			for _, part := range parts {
				if mediaPart(part) {
					stripped = true
					changed = true
					continue
				}
				kept = append(kept, part)
			}
			if changed {
				msg["content"] = kept
			}
		case []any:
			kept := make([]any, 0, len(parts))
			changed := false
			for _, raw := range parts {
				part, ok := raw.(map[string]any)
				if ok && mediaPart(part) {
					stripped = true
					changed = true
					continue
				}
				kept = append(kept, raw)
			}
			if changed {
				msg["content"] = kept
			}
		}
	}
	return stripped
}

func mediaPart(part map[string]any) bool {
	kind, _ := part["type"].(string)
	return kind == "image_url" || kind == "file"
}

func unsupportedMedia(payload []byte) bool {
	code, message := apiErrorFields(payload)
	return code == "AI_PUBLIC_MODEL_CAPABILITY_UNSUPPORTED" || strings.Contains(message, "does not support the requested input capability")
}

func apiErrorFields(payload []byte) (string, string) {
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &parsed) != nil {
		return "", ""
	}
	return parsed.Error.Code, parsed.Error.Message
}

func providerStatusError(status int, payload []byte) error {
	_, message := apiErrorFields(payload)
	message = strings.TrimSpace(message)
	if message == "" {
		message = strings.TrimSpace(string(payload))
	}
	return fmt.Errorf("模型接口返回 %d：%s", status, clip(message, 180))
}

// peelMediaNote splits the user-facing disclaimer off a model reply so a tool
// loop does not send it back as the assistant's own words. The caller puts it
// on the final answer instead.
func peelMediaNote(content string) (string, string) {
	if content == mediaRetryNote {
		return mediaRetryNote, ""
	}
	prefix := mediaRetryNote + "\n"
	if strings.HasPrefix(content, prefix) {
		return mediaRetryNote, strings.TrimPrefix(content, prefix)
	}
	return "", content
}

func joinMediaNote(note, content string) string {
	if note == "" || strings.HasPrefix(content, note) {
		return content
	}
	if strings.TrimSpace(content) == "" {
		return note
	}
	return note + "\n" + content
}

func latestUserText(messages []map[string]any) string {
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role != "user" {
			continue
		}
		return contentText(messages[i]["content"])
	}
	return ""
}

func contentText(content any) string {
	switch parts := content.(type) {
	case string:
		return parts
	case []map[string]any:
		var b strings.Builder
		for _, part := range parts {
			if kind, _ := part["type"].(string); kind == "text" {
				if text, ok := part["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	case []any:
		var b strings.Builder
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := part["type"].(string); kind == "text" {
				if text, ok := part["text"].(string); ok {
					b.WriteString(text)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}
