package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"onefrog/internal/model"
	"onefrog/internal/remote"
	"onefrog/internal/store"
)

type grantKey struct{}

type grants struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func withGrants(ctx context.Context) context.Context {
	return context.WithValue(ctx, grantKey{}, &grants{ids: map[string]struct{}{}})
}

func grantServer(ctx context.Context, id string) {
	g, _ := ctx.Value(grantKey{}).(*grants)
	if g == nil || id == "" {
		return
	}
	g.mu.Lock()
	g.ids[id] = struct{}{}
	g.mu.Unlock()
}

func serverGranted(ctx context.Context, id string) bool {
	g, _ := ctx.Value(grantKey{}).(*grants)
	if g == nil || id == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.ids[id]
	return ok
}

type flexInt int

func (f *flexInt) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return nil
	}
	if text[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		text = strings.TrimSpace(s)
		if text == "" {
			return nil
		}
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		return err
	}
	*f = flexInt(n)
	return nil
}

var (
	hostName = regexp.MustCompile("^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,252}[A-Za-z0-9])?$")
	userName = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_.@-]{0,31}$")
)

func (a *Agent) connectServer(ctx context.Context, botID string, call toolCall) ToolEvent {
	ev := ToolEvent{Name: "connect_server"}
	var args struct {
		Name     string  `json:"name"`
		Host     string  `json:"host"`
		Port     flexInt `json:"port"`
		Username string  `json:"username"`
		AuthType string  `json:"auth_type"`
		Secret   string  `json:"secret"`
		Role     string  `json:"role"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		ev.Output = "连接信息不完整"
		return ev
	}
	host := strings.TrimSuffix(strings.TrimSpace(args.Host), ".")
	user := strings.TrimSpace(args.Username)
	if user == "" {
		user = "root"
	}
	port := int(args.Port)
	if port == 0 {
		port = 22
	}
	ev.Command = fmt.Sprintf("接入 %s@%s:%d", user, host, port)
	task, _ := ctx.Value(taskKey{}).(string)
	if !validEndpoint(host, port, user) {
		ev.Output = "地址、端口或用户名无效"
		return ev
	}
	if blockedHost(host) {
		ev.Output = "这个地址不能接入"
		return ev
	}
	if !tokenMentioned(task, host) {
		ev.Output = "地址不在用户这条消息里，拒绝接入"
		return ev
	}
	if user != "root" && !tokenMentioned(task, user) {
		ev.Output = "用户名不在用户这条消息里，拒绝接入"
		return ev
	}
	plain, auth, err := a.loginMaterial(ctx, task, args.Secret, args.AuthType)
	if err != nil {
		ev.Output = err.Error()
		return ev
	}
	if a.Box == nil {
		ev.Output = "不能保存登录信息"
		return ev
	}
	enc, err := a.Box.Encrypt(plain)
	if err != nil {
		ev.Output = "登录信息无法加密"
		return ev
	}
	name := oneLine(clip(strings.TrimSpace(args.Name), 80))
	if name == "" {
		name = host
	}
	role := oneLine(clip(strings.TrimSpace(args.Role), 40))
	rec, existed, err := a.upsertServer(ctx, host, port, user, auth, enc, name, role)
	if err != nil {
		ev.Output = "写入服务器页面失败"
		return ev
	}
	ev.ServerID = rec.ID
	if existed {
		ev.Command = fmt.Sprintf("更新接入 %s@%s:%d", user, host, port)
	}
	if a.SSH == nil {
		ev.Output = scrub(a.redact(ctx, fmt.Sprintf("已写入服务器页面，但没有 SSH 执行器。id=%s name=%s host=%s port=%d user=%s status=%s", rec.ID, rec.Name, rec.Host, rec.Port, rec.Username, rec.Status)), plain)
		_, _ = a.Store.AddHistory(ctx, store.HistoryRec{History: model.History{BotID: botID, ServerID: rec.ID, Action: ev.Command, Command: ev.Command, Status: "failed"}})
		return ev
	}
	out, _, runErr := a.probeSSH(ctx, &rec, plain)
	if runErr != nil {
		if errors.Is(runErr, remote.ErrHostKeyMismatch) {
			rec.Status = "host_key_mismatch"
		} else {
			rec.Status = "offline"
		}
		if strings.TrimSpace(out) == "" {
			out = runErr.Error()
		}
	} else {
		rec.Status = "online"
		ev.OK = true
		grantServer(ctx, rec.ID)
	}
	if saved, e := a.Store.UpdateServer(ctx, rec); e == nil {
		rec.Server = saved
	}
	verb := "已接入并写入服务器页面"
	if existed {
		verb = "已更新并写入服务器页面"
	}
	if !ev.OK {
		verb = "已写入服务器页面，但 SSH 未成功"
	}
	summary := fmt.Sprintf("%s。id=%s name=%s host=%s port=%d user=%s status=%s", verb, rec.ID, rec.Name, rec.Host, rec.Port, rec.Username, rec.Status)
	if strings.TrimSpace(out) != "" {
		summary += "\n" + clip(out, 1500)
	}
	ev.Output = clip(scrub(a.redact(ctx, summary), plain), 2000)
	hist := "success"
	if !ev.OK {
		hist = "failed"
	}
	_, _ = a.Store.AddHistory(ctx, store.HistoryRec{History: model.History{BotID: botID, ServerID: rec.ID, Action: ev.Command, Command: ev.Command, Status: hist}})
	return ev
}

func (a *Agent) probeSSH(ctx context.Context, rec *store.ServerRec, plain string) (string, string, error) {
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(a.execution().CommandTimeoutSeconds)*time.Second)
	defer cancel()
	out, fp, err := a.SSH.Run(runCtx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, "printf onefrog-ok")
	if errors.Is(err, remote.ErrHostKeyUnknown) && fp != "" && rec.HostKey == "" {
		rec.HostKey = fp
		var seen string
		out, seen, err = a.SSH.Run(runCtx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, "printf onefrog-ok")
		if seen != "" {
			fp = seen
			rec.HostKey = seen
		}
		return out, fp, err
	}
	if err == nil && fp != "" {
		rec.HostKey = fp
	}
	return out, fp, err
}

func (a *Agent) upsertServer(ctx context.Context, host string, port int, user, auth, enc, name, role string) (store.ServerRec, bool, error) {
	servers, err := a.Store.ListServers(ctx)
	if err != nil {
		return store.ServerRec{}, false, err
	}
	for _, item := range servers {
		if item.Port == port && strings.EqualFold(item.Host, host) && item.Username == user {
			rec, e := a.Store.GetServer(ctx, item.ID)
			if e != nil {
				return store.ServerRec{}, false, e
			}
			rec.Host = host
			rec.Port = port
			rec.Username = user
			rec.AuthType = auth
			rec.EncryptedKey = enc
			if name != "" {
				rec.Name = name
			}
			if role != "" {
				rec.Role = role
			}
			saved, e := a.Store.UpdateServer(ctx, rec)
			if e != nil {
				return store.ServerRec{}, false, e
			}
			rec.Server = saved
			return rec, true, nil
		}
	}
	rec := store.ServerRec{Server: model.Server{Name: name, Host: host, Port: port, Username: user, AuthType: auth, Status: "unknown", Role: role}, EncryptedKey: enc}
	saved, err := a.Store.CreateServer(ctx, rec)
	if err != nil {
		return store.ServerRec{}, false, err
	}
	rec.Server = saved
	return rec, false, nil
}

func (a *Agent) loginMaterial(ctx context.Context, task, secret, authType string) (string, string, error) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return "", "", errors.New("缺少登录密码或私钥")
	}
	if len(secret) > 64*1024 {
		return "", "", errors.New("登录信息过长")
	}
	var plain string
	if name, ok := vaultRef(secret); ok {
		placeholder := "{{secret:" + name + "}}"
		if !tokenMentioned(task, name) && !strings.Contains(task, placeholder) {
			return "", "", errors.New("密钥名称不在用户这条消息里，拒绝接入")
		}
		expanded, err := a.expand(ctx, placeholder)
		if err != nil {
			return "", "", errors.New("找不到这个密钥")
		}
		plain = expanded
	} else {
		if !secretMentioned(task, secret) {
			return "", "", errors.New("登录信息不在用户这条消息里，拒绝接入")
		}
		plain = secret
	}
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return "", "", errors.New("登录信息是空的")
	}
	if !strings.Contains(plain, "PRIVATE KEY") && utf8.RuneCountInString(plain) < 4 {
		return "", "", errors.New("密码太短")
	}
	auth := strings.TrimSpace(authType)
	if strings.Contains(plain, "PRIVATE KEY") {
		auth = "private_key"
	}
	if auth == "" {
		auth = "password"
	}
	if auth != "password" && auth != "private_key" {
		return "", "", errors.New("登录方式只能是密码或私钥")
	}
	return plain, auth, nil
}

func validEndpoint(host string, port int, user string) bool {
	if port < 1 || port > 65535 || len(host) > 253 || strings.Contains(host, "..") {
		return false
	}
	return hostName.MatchString(host) && userName.MatchString(user)
}

func blockedHost(host string) bool {
	switch strings.ToLower(host) {
	case "169.254.169.254", "metadata.google.internal", "metadata.internal":
		return true
	default:
		return false
	}
}

func vaultRef(secret string) (string, bool) {
	secret = strings.TrimSpace(secret)
	prefix := "{{secret:"
	if !strings.HasPrefix(secret, prefix) || !strings.HasSuffix(secret, "}}") {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(secret, prefix), "}}"))
	if name == "" || strings.ContainsAny(name, "{}\n") {
		return "", false
	}
	return name, true
}

func secretMentioned(task, secret string) bool {
	task = normNewlines(task)
	secret = normNewlines(strings.TrimSpace(secret))
	if secret == "" {
		return false
	}
	if strings.Contains(task, secret) {
		return true
	}
	if strings.Contains(secret, "PRIVATE KEY") && strings.Contains(task, "PRIVATE KEY") {
		for _, line := range strings.Split(secret, "\n") {
			line = strings.TrimSpace(line)
			if len(line) >= 40 && strings.Contains(task, line) {
				return true
			}
		}
	}
	return false
}

func tokenMentioned(text, token string) bool {
	if token == "" {
		return false
	}
	hay := strings.ToLower(text)
	needle := strings.ToLower(token)
	from := 0
	for from <= len(hay)-len(needle) {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(needle)
		leftOK := i == 0 || !isEndpointChar(hay[i-1])
		rightOK := end == len(hay) || !isEndpointChar(hay[end])
		if leftOK && rightOK {
			return true
		}
		from = i + 1
	}
	return false
}

func isEndpointChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '_'
}

func normNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func scrub(text, secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" || secret == "***" {
		return text
	}
	return strings.ReplaceAll(text, secret, "***")
}
