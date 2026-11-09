package onefrog

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
)

// 密钥占位符：模型和事件里只出现 {{secret:NAME}}，真实值只在执行工具前一刻替换进去。
var secretRef = regexp.MustCompile(`\{\{secret:([A-Z][A-Z0-9_]{0,63})\}\}`)

// Secrets 是密钥库接口（frogbot 的 vault 实现了它）。
type Secrets interface {
	// Allowed 检查这些密钥是否存在、是否允许被这个工具/这些参数使用。
	Allowed(tool string, args json.RawMessage, names []string) error
	// Values 返回明文，只在执行层调用，绝不能写进事件或返回给模型。
	Values(names []string) (map[string]string, error)
	// Redact 把文本里出现的任何已知密钥（含 base64 / URL 编码等）替换成 [secret:NAME]。
	Redact(s string) string
}

// SecretRefs 找出参数里引用的密钥名（去重、排序）。
func SecretRefs(args json.RawMessage) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range secretRef.FindAllStringSubmatch(string(args), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// SubstituteSecrets 把参数里的占位符换成真实值。
// shell 工具不把明文拼进命令，而是换成 ${NAME} 并通过环境变量传入。
func SubstituteSecrets(tool string, args json.RawMessage, vals map[string]string) (json.RawMessage, map[string]string, error) {
	var v any
	if err := json.Unmarshal(args, &v); err != nil {
		return nil, nil, errors.New("参数不是合法 JSON")
	}
	env := map[string]string{}
	repl := func(s string) string {
		return secretRef.ReplaceAllStringFunc(s, func(m string) string {
			name := secretRef.FindStringSubmatch(m)[1]
			val, ok := vals[name]
			if !ok {
				return m
			}
			if tool == "shell" {
				env[name] = val
				return "${" + name + "}"
			}
			return val
		})
	}
	v = walkStrings(v, repl)
	out, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return out, env, nil
}

func walkStrings(v any, f func(string) string) any {
	switch x := v.(type) {
	case string:
		return f(x)
	case []any:
		for i := range x {
			x[i] = walkStrings(x[i], f)
		}
	case map[string]any:
		for k := range x {
			x[k] = walkStrings(x[k], f)
		}
	}
	return v
}

// Execute 在审批通过后执行工具：替换密钥 → 执行 → 输出和错误脱敏。
// 没有配置密钥库时等同于 tool.Execute。
func (g *Guard) Execute(ctx context.Context, tool Tool, env ToolEnv, args json.RawMessage) (string, error) {
	if g == nil || g.Secrets == nil {
		return tool.Execute(ctx, env, args)
	}
	if refs := SecretRefs(args); len(refs) > 0 && !handlesSecrets(tool) {
		vals, err := g.Secrets.Values(refs)
		if err != nil {
			return "", errors.New(g.Secrets.Redact(err.Error()))
		}
		var extra map[string]string
		if args, extra, err = SubstituteSecrets(tool.Name(), args, vals); err != nil {
			return "", err
		}
		if len(extra) > 0 {
			merged := map[string]string{}
			for k, v := range env.Env {
				merged[k] = v
			}
			for k, v := range extra {
				merged[k] = v
			}
			env.Env = merged
		}
	}
	out, err := tool.Execute(ctx, env, args)
	out = g.Secrets.Redact(out)
	if err != nil {
		err = errors.New(g.Secrets.Redact(err.Error()))
	}
	return out, err
}

// RedactString 是个小工具：s 为 nil 时原样返回。
func RedactString(s Secrets, text string) string {
	if s == nil {
		return text
	}
	return s.Redact(text)
}

// SecretAware 由「自己把调用转交给另一套审批管线」的工具实现（例如 frogbot 聊天层的写文件），
// 这一层的 Guard 不替换它参数里的占位符，也不为它弹密钥审批，交给下游处理。
type SecretAware interface{ HandlesSecrets() bool }

// PassSecrets 包装一个 FuncTool，声明它自己处理密钥引用。
type PassSecrets struct{ FuncTool }

func (PassSecrets) HandlesSecrets() bool { return true }

func handlesSecrets(t Tool) bool {
	s, ok := t.(SecretAware)
	return ok && s.HandlesSecrets()
}
