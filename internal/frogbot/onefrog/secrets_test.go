package onefrog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type testSecrets map[string]string

func (s testSecrets) Allowed(_ string, _ json.RawMessage, names []string) error {
	for _, n := range names {
		if _, ok := s[n]; !ok {
			return errors.New("missing " + n)
		}
	}
	return nil
}
func (s testSecrets) Values(names []string) (map[string]string, error) {
	out := map[string]string{}
	for _, n := range names {
		out[n] = s[n]
	}
	return out, nil
}
func (s testSecrets) Redact(t string) string {
	for n, v := range s {
		t = strings.ReplaceAll(t, v, "[secret:"+n+"]")
	}
	return t
}

func TestSecretRefsAndSubstitute(t *testing.T) {
	args := json.RawMessage(`{"command":"curl -H \"Bearer {{secret:TOK}}\" x && echo {{secret:TOK}} {{secret:B}}"}`)
	if r := SecretRefs(args); strings.Join(r, ",") != "B,TOK" {
		t.Fatalf("refs %v", r)
	}
	vals := map[string]string{"TOK": "s3cr\"et", "B": "bee"}
	out, env, err := SubstituteSecrets("shell", args, vals)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "s3cr") || !strings.Contains(string(out), "${TOK}") || env["TOK"] != "s3cr\"et" {
		t.Errorf("shell substitution: %s %v", out, env)
	}
	out, env, _ = SubstituteSecrets("write_file", json.RawMessage(`{"path":"a","content":"k={{secret:TOK}}"}`), vals)
	var m map[string]string
	_ = json.Unmarshal(out, &m)
	if m["content"] != "k=s3cr\"et" || len(env) != 0 {
		t.Errorf("file substitution: %v", m)
	}
}

func TestSecretUseAlwaysNeedsApproval(t *testing.T) {
	g, _ := NewGuard(Rules{}, nil, nil)
	g.Secrets = testSecrets{"TOK": "hunter2-secret"}
	sh := NewRegistry(DefaultTools()...).Get("shell")
	args := json.RawMessage(`{"command":"echo {{secret:TOK}}"}`)
	for _, p := range []Policy{PolicyOnRequest, PolicyAuto, PolicyUntrusted, PolicyNever} {
		v, _ := g.Classify(Task{Workspace: t.TempDir(), Policy: p}, sh, args)
		if v.Decision != AskUser {
			t.Errorf("policy %s: want ask, got %v", p, v.Decision)
		}
	}
	// never 模式问不了用户 => 拒绝
	v := g.Check(context.Background(), Task{ID: "t", Workspace: t.TempDir(), Policy: PolicyNever}, sh, args, func(EventType, any) {})
	if v.Decision != Deny {
		t.Error("never policy must deny secret use")
	}
	// 不存在的密钥直接拒绝
	v, _ = g.Classify(Task{Workspace: t.TempDir(), Policy: PolicyOnRequest}, sh, json.RawMessage(`{"command":"echo {{secret:NOPE}}"}`))
	if v.Decision != Deny {
		t.Error("missing secret must be denied")
	}
	// 没有占位符的只读命令照常放行
	v, _ = g.Classify(Task{Workspace: t.TempDir(), Policy: PolicyOnRequest}, sh, json.RawMessage(`{"command":"echo hi"}`))
	if v.Decision != Allow {
		t.Error("plain echo should be allowed")
	}
}

func TestExecuteInjectsEnvAndRedacts(t *testing.T) {
	g, _ := NewGuard(Rules{}, nil, nil)
	g.Secrets = testSecrets{"TOK": "hunter2-secret"}
	out, err := g.Execute(context.Background(), NewRegistry(DefaultTools()...).Get("shell"), ToolEnv{Workspace: t.TempDir()},
		json.RawMessage(`{"command":"echo got:{{secret:TOK}}; echo len:${#TOK}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "got:[secret:TOK]") || !strings.Contains(out, "len:14") {
		t.Errorf("output: %q", out)
	}
}

func TestThinkingStream(t *testing.T) {
	stream := `data: {"choices":[{"delta":{"reasoning_content":"先想"}}]}
data: {"choices":[{"delta":{"reasoning":"一想。"}}]}
data: {"choices":[{"delta":{"content":"<thi"}}]}
data: {"choices":[{"delta":{"content":"nk>标签里"}}]}
data: {"choices":[{"delta":{"content":"的思考</th"}}]}
data: {"choices":[{"delta":{"content":"ink>答案<b>粗</b>"}}]}
data: [DONE]
`
	var text, think strings.Builder
	m, err := parseStreamR(context.Background(), strings.NewReader(stream),
		func(s string) { text.WriteString(s) }, func(s string) { think.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if think.String() != "先想一想。标签里的思考" {
		t.Errorf("think %q", think.String())
	}
	if text.String() != "答案<b>粗</b>" || m.Content != "答案<b>粗</b>" {
		t.Errorf("text %q content %q", text.String(), m.Content)
	}
}
