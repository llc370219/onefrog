package bot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMediaCapabilityFallback(t *testing.T) {
	t.Run("text with image retries without media", func(t *testing.T) {
		messages := userMedia("这段日志什么意思", true, true)
		var bodies []string
		agent := mediaAgent(func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(raw))
			if len(bodies) > 2 {
				return nil, errors.New("retried more than once")
			}
			if len(bodies) == 1 {
				return mediaObject(400, map[string]any{"error": map[string]any{"message": "The public model does not support the requested input capability"}}), nil
			}
			return mediaObject(200, map[string]any{
				"choices": []any{map[string]any{"message": map[string]any{"content": "日志里没有异常。"}}},
				"usage":   map[string]any{"prompt_tokens": 4, "completion_tokens": 6, "total_tokens": 10},
			}), nil
		})
		msg, err := agent.chat(context.Background(), "http://models.example/v1", "text-only", "k", messages, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(bodies) != 2 {
			t.Fatalf("calls = %d, want 2", len(bodies))
		}
		if !strings.Contains(bodies[0], "image_url") || !strings.Contains(bodies[0], "file_data") {
			t.Fatalf("first request did not include the attachments: %s", bodies[0])
		}
		if strings.Contains(bodies[1], "image_url") || strings.Contains(bodies[1], "file_data") {
			t.Fatalf("retry still sent media: %s", bodies[1])
		}
		if !strings.HasPrefix(msg.Content, mediaRetryNote+"\n") || !strings.Contains(msg.Content, "日志里没有异常。") {
			t.Fatalf("content = %q", msg.Content)
		}
		kept, _ := json.Marshal(messages)
		if strings.Contains(string(kept), "image_url") || strings.Contains(string(kept), "file_data") {
			t.Fatalf("caller messages still contain media: %s", kept)
		}
	})

	t.Run("image only does not retry", func(t *testing.T) {
		for _, text := range []string{mediaOnlyPrompt, "  ", ""} {
			calls := 0
			agent := mediaAgent(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					return nil, errors.New("image-only turn was retried")
				}
				return mediaObject(400, map[string]any{"error": map[string]any{"code": "AI_PUBLIC_MODEL_CAPABILITY_UNSUPPORTED", "message": "The public model does not support the requested input capability"}}), nil
			})
			_, err := agent.chat(context.Background(), "http://models.example/v1", "text-only", "k", userMedia(text, true, false), nil)
			if err == nil || err.Error() != mediaOnlyError {
				t.Fatalf("text %q error = %v", text, err)
			}
			if calls != 1 {
				t.Fatalf("text %q calls = %d", text, calls)
			}
		}
	})

	t.Run("plain error shows message", func(t *testing.T) {
		calls := 0
		agent := mediaAgent(func(r *http.Request) (*http.Response, error) {
			calls++
			return mediaObject(400, map[string]any{"error": map[string]any{"type": "invalid_request", "code": "model_not_found", "message": "model is offline"}}), nil
		})
		_, err := agent.chat(context.Background(), "http://models.example/v1", "text-only", "k", userMedia("看看这台机器", true, false), nil)
		if err == nil {
			t.Fatal("expected an error")
		}
		if err.Error() != "模型接口返回 400：model is offline" {
			t.Fatal(err)
		}
		if strings.Contains(err.Error(), "{") || strings.Contains(err.Error(), "model_not_found") {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d", calls)
		}
	})

	t.Run("text only capability error does not loop", func(t *testing.T) {
		calls := 0
		agent := mediaAgent(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				return nil, errors.New("text-only request looped")
			}
			return mediaObject(400, map[string]any{"error": map[string]any{"code": "AI_PUBLIC_MODEL_CAPABILITY_UNSUPPORTED", "message": "The public model does not support the requested input capability"}}), nil
		})
		_, err := agent.chat(context.Background(), "http://models.example/v1", "text-only", "k", []map[string]any{{"role": "user", "content": "请看这段日志"}}, nil)
		if err == nil || err.Error() != "模型接口返回 400：The public model does not support the requested input capability" {
			t.Fatal(err)
		}
		if strings.Contains(err.Error(), "请换一个支持图片的模型") || strings.Contains(err.Error(), "{") {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("calls = %d", calls)
		}
	})
}

func userMedia(text string, image, file bool) []map[string]any {
	parts := []map[string]any{{"type": "text", "text": text}}
	if image {
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:image/png;base64,aaaa", "detail": "auto"}})
	}
	if file {
		parts = append(parts, map[string]any{"type": "file", "file": map[string]string{"filename": "notes.pdf", "file_data": "data:application/pdf;base64,bbbb"}})
	}
	return []map[string]any{{"role": "user", "content": parts}}
}

func mediaAgent(rt func(*http.Request) (*http.Response, error)) *Agent {
	return &Agent{HTTP: &http.Client{Transport: roundTripFunc(rt)}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mediaResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func mediaObject(status int, body map[string]any) *http.Response {
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return mediaResponse(status, string(raw))
}
