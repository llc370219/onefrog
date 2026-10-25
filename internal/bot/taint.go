package bot

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

// Prompt-injection guard. Command output, attachment text and other content
// the model reads during a turn is untrusted. If a command the model wants to
// run is copied from that content (a whole segment or a URL) and was not part
// of what the user asked for, smart approval asks the operator first.

type taintKey struct{}

type taintSet struct {
	mu    sync.Mutex
	texts []string
	ran   []string // commands already run or approved this turn
	size  int
}

const maxTaint = 4 << 20

func withTaint(ctx context.Context) context.Context {
	if _, ok := ctx.Value(taintKey{}).(*taintSet); ok {
		return ctx
	}
	return context.WithValue(ctx, taintKey{}, &taintSet{})
}

// addUntrusted records content the model has seen but the user did not write.
func addUntrusted(ctx context.Context, text string) {
	t, _ := ctx.Value(taintKey{}).(*taintSet)
	if t == nil || strings.TrimSpace(text) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.size+len(text) > maxTaint {
		return
	}
	t.size += len(text)
	t.texts = append(t.texts, text)
}

// markRan records a command that already ran (or was approved) so that output
// echoing it back, such as ps or systemctl status, does not taint a repeat.
func markRan(ctx context.Context, command string) {
	t, _ := ctx.Value(taintKey{}).(*taintSet)
	if t == nil {
		return
	}
	t.mu.Lock()
	t.ran = append(t.ran, strings.Join(strings.Fields(command), " "))
	t.mu.Unlock()
}

var (
	segmentSplit = regexp.MustCompile(`&&|\|\||[;|\n]`)
	urlPattern   = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s'"<>()]+`)
)

const minSegment = 8

// untrustedSource returns a reason when the command appears to be driven by
// untrusted content, or "" otherwise.
func untrustedSource(ctx context.Context, command string) string {
	t, _ := ctx.Value(taintKey{}).(*taintSet)
	if t == nil {
		return ""
	}
	t.mu.Lock()
	texts := append([]string(nil), t.texts...)
	ran := append([]string(nil), t.ran...)
	t.mu.Unlock()
	if len(texts) == 0 {
		return ""
	}
	request, _ := ctx.Value(taskKey{}).(string)
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	request = norm(request)
	seen := func(piece string) bool {
		if request != "" && strings.Contains(request, piece) {
			return false
		}
		for _, prev := range ran {
			if strings.Contains(prev, piece) {
				return false
			}
		}
		for _, text := range texts {
			if strings.Contains(norm(text), piece) {
				return true
			}
		}
		return false
	}
	for _, u := range urlPattern.FindAllString(command, -1) {
		u = strings.TrimRight(u, ".,;:")
		if seen(u) {
			return "命令里的网址来自服务器输出或附件，不是你直接提供的"
		}
	}
	pieces := append([]string{command}, segmentSplit.Split(command, -1)...)
	for _, piece := range pieces {
		piece = norm(piece)
		if len(piece) < minSegment {
			continue
		}
		if seen(piece) {
			return "命令内容出现在服务器输出或附件里，不是你直接要求的，可能是提示注入"
		}
	}
	return ""
}
