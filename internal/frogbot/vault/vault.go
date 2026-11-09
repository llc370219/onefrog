// Package vault 是 frogbot 的密钥库：AES-256-GCM 加密存储，模型永远看不到明文。
//
// 文件：
//   - data/secrets.json  加密后的记录（名字、密文、描述、使用范围、时间）
//   - data/master.key    自动生成的主密钥（0600），或用环境变量 FROGBOT_MASTER_KEY 提供
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var nameRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// 这些名字会和 shell 的常用环境变量冲突，不允许用作密钥名。
var reserved = map[string]bool{"PATH": true, "HOME": true, "USER": true, "SHELL": true, "PWD": true,
	"LANG": true, "TERM": true, "TMPDIR": true, "IFS": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true}

// ValidName 检查密钥名（UPPER_SNAKE）。
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("密钥名要用大写字母、数字和下划线（例如 GITHUB_TOKEN）")
	}
	if reserved[name] {
		return fmt.Errorf("%s 是系统环境变量名，换一个名字", name)
	}
	return nil
}

// Scope 限制密钥能被谁用；都为空表示不限制（仍然需要审批）。
type Scope struct {
	Tools   []string `json:"tools,omitempty"`   // 允许的工具名，如 shell
	Domains []string `json:"domains,omitempty"` // 允许出现在参数里的域名，如 api.github.com
}

// Info 是可以给模型和前端看的信息（没有值）。
type Info struct {
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Scope       Scope      `json:"scope"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	LastUsed    *time.Time `json:"last_used,omitempty"`
}

type record struct {
	Info
	Cipher string `json:"cipher"` // base64(nonce || ciphertext)
}

type fileFormat struct {
	Version int       `json:"version"`
	Secrets []*record `json:"secrets"`
}

// Vault 是线程安全的加密密钥库。
type Vault struct {
	path string
	aead cipher.AEAD
	Now  func() time.Time

	mu    sync.Mutex
	recs  map[string]*record
	plain map[string]string // 解密后的明文只在内存里，用于替换和脱敏
	repl  *strings.Replacer
}

// LoadKey 读取主密钥：优先环境变量（64 位 hex / base64 的 32 字节；其他字符串会做 SHA-256），
// 否则读 dir/master.key，没有就生成一个（权限 0600）。generated 表示刚刚新建。
func LoadKey(envVal, dir string) (key []byte, generated bool, err error) {
	if envVal = strings.TrimSpace(envVal); envVal != "" {
		return parseKey(envVal), false, nil
	}
	p := filepath.Join(dir, "master.key")
	if b, err := os.ReadFile(p); err == nil {
		return parseKey(strings.TrimSpace(string(b))), false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(p, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	return key, true, nil
}

func parseKey(s string) []byte {
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b
		}
	}
	h := sha256.Sum256([]byte(s)) // 口令形式：可用，但不如随机 32 字节
	return h[:]
}

// Open 打开（或新建）dir/secrets.json。
func Open(dir string, key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, errors.New("主密钥必须是 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	v := &Vault{path: filepath.Join(dir, "secrets.json"), aead: aead, recs: map[string]*record{}, plain: map[string]string{}}
	b, err := os.ReadFile(v.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(b) > 0 {
		var f fileFormat
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("secrets.json 损坏: %w", err)
		}
		for _, r := range f.Secrets {
			pt, err := v.decrypt(r.Name, r.Cipher)
			if err != nil {
				return nil, fmt.Errorf("无法解密密钥 %s（主密钥不对？）", r.Name)
			}
			v.recs[r.Name] = r
			v.plain[r.Name] = pt
		}
	}
	v.rebuild()
	return v, nil
}

func (v *Vault) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// The API credential is not part of the tool-accessible secret catalog.
func (v *Vault) SealAPIKey(key string) (string, error) {
	return v.encrypt("settings:api-key", key)
}

func (v *Vault) OpenAPIKey(sealed string) (string, error) {
	return v.decrypt("settings:api-key", sealed)
}

func (v *Vault) encrypt(name, plain string) (string, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := v.aead.Seal(nonce, nonce, []byte(plain), []byte("frogbot:"+name)) // 名字作为附加数据，密文不能挪给别的名字用
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (v *Vault) decrypt(name, c string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(c)
	if err != nil || len(b) < v.aead.NonceSize() {
		return "", errors.New("密文格式不对")
	}
	ns := v.aead.NonceSize()
	pt, err := v.aead.Open(nil, b[:ns], b[ns:], []byte("frogbot:"+name))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// save 写盘（先写临时文件再改名），调用方持锁。
func (v *Vault) save() error {
	f := fileFormat{Version: 1}
	for _, r := range v.recs {
		f.Secrets = append(f.Secrets, r)
	}
	sort.Slice(f.Secrets, func(i, j int) bool { return f.Secrets[i].Name < f.Secrets[j].Name })
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.path), 0o700); err != nil {
		return err
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	_ = os.Remove(v.path)
	return os.Rename(tmp, v.path)
}

// Set 新建或更新一个密钥。value 不会出现在任何错误信息里。
func (v *Vault) Set(name, value, desc string, scope *Scope) error {
	if err := ValidName(name); err != nil {
		return err
	}
	if value == "" {
		return errors.New("密钥值不能为空")
	}
	if len(value) > 16<<10 {
		return errors.New("密钥值太长（最多 16KB）")
	}
	c, err := v.encrypt(name, value)
	if err != nil {
		return errors.New("加密失败")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	r := v.recs[name]
	if r == nil {
		r = &record{Info: Info{Name: name, CreatedAt: now}}
	}
	r.Cipher, r.UpdatedAt = c, now
	if desc != "" {
		r.Description = desc
	}
	if scope != nil {
		r.Scope = *scope
	}
	v.recs[name] = r
	old, had := v.plain[name]
	v.plain[name] = value
	if err := v.save(); err != nil {
		if had {
			v.plain[name] = old
		} else {
			delete(v.plain, name)
			delete(v.recs, name)
		}
		return fmt.Errorf("保存失败: %v", err)
	}
	v.rebuild()
	return nil
}

// Delete 删除一个密钥。
func (v *Vault) Delete(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.recs[name] == nil {
		return fmt.Errorf("没有叫 %s 的密钥", name)
	}
	delete(v.recs, name)
	delete(v.plain, name) // 删掉后旧值不再脱敏：它已经不会被注入任何工具
	if err := v.save(); err != nil {
		return err
	}
	v.rebuild()
	return nil
}

// List 返回所有密钥的信息（不含值）。
func (v *Vault) List() []Info {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]Info, 0, len(v.recs))
	for _, r := range v.recs {
		out = append(out, r.Info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Has 判断密钥是否存在。
func (v *Vault) Has(name string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.recs[name] != nil
}

var urlHost = regexp.MustCompile(`(?i)\b(?:https?|wss?|ftp)://([a-z0-9.\-]+)`)

// Allowed 实现 onefrog.Secrets：密钥存在 + 工具/域名在允许范围内。
func (v *Vault) Allowed(tool string, args json.RawMessage, names []string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, n := range names {
		r := v.recs[n]
		if r == nil {
			return fmt.Errorf("密钥 %s 不存在，先用 request_secret 让用户保存", n)
		}
		if len(r.Scope.Tools) > 0 && !contains(r.Scope.Tools, tool) {
			return fmt.Errorf("密钥 %s 只允许这些工具使用: %s", n, strings.Join(r.Scope.Tools, ", "))
		}
		if len(r.Scope.Domains) > 0 {
			for _, m := range urlHost.FindAllStringSubmatch(string(args), -1) {
				if !domainOK(strings.ToLower(m[1]), r.Scope.Domains) {
					return fmt.Errorf("密钥 %s 不允许发往 %s（允许: %s）", n, m[1], strings.Join(r.Scope.Domains, ", "))
				}
			}
		}
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func domainOK(host string, allowed []string) bool {
	for _, d := range allowed {
		d = strings.ToLower(strings.TrimPrefix(d, "*."))
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Values 实现 onefrog.Secrets：返回明文并更新 last_used。只在工具执行层调用。
func (v *Vault) Values(names []string) (map[string]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := map[string]string{}
	now := v.now()
	for _, n := range names {
		p, ok := v.plain[n]
		if !ok {
			return nil, fmt.Errorf("密钥 %s 不存在", n)
		}
		out[n] = p
		t := now
		v.recs[n].LastUsed = &t
	}
	_ = v.save()
	return out, nil
}

// Redact 实现 onefrog.Secrets：把所有已知密钥值及其常见编码替换成 [secret:NAME]。
func (v *Vault) Redact(s string) string {
	v.mu.Lock()
	r := v.repl
	v.mu.Unlock()
	if r == nil || s == "" {
		return s
	}
	return r.Replace(s)
}

// RedactBytes 同上，作用于字节（例如写盘前的 JSON）。
func (v *Vault) RedactBytes(b []byte) []byte {
	v.mu.Lock()
	r := v.repl
	v.mu.Unlock()
	if r == nil {
		return b
	}
	return []byte(r.Replace(string(b)))
}

// rebuild 重新生成脱敏替换表，调用方持锁。
func (v *Vault) rebuild() { v.repl = NewReplacer(v.plain) }

// NewReplacer 为一组密钥（名字 → 明文）生成脱敏替换表：原文及常见编码都替换成 [secret:NAME]。
// 没有可替换内容时返回 nil。嵌入宿主用它对接宿主自己的保险箱。
func NewReplacer(plain map[string]string) *strings.Replacer {
	type pair struct{ from, to string }
	var pairs []pair
	seen := map[string]bool{}
	for name, val := range plain {
		tag := "[secret:" + name + "]"
		for _, f := range Variants(val) {
			if !seen[f] {
				seen[f] = true
				pairs = append(pairs, pair{f, tag})
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	// 长的先匹配，避免短变体把长变体切碎
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].from) != len(pairs[j].from) {
			return len(pairs[i].from) > len(pairs[j].from)
		}
		return pairs[i].from < pairs[j].from
	})
	args := make([]string, 0, len(pairs)*2)
	for _, p := range pairs {
		args = append(args, p.from, p.to)
	}
	return strings.NewReplacer(args...)
}

// Variants 返回一个密钥值需要被抹掉的各种形式：原文、JSON 转义、URL 编码、hex、
// base64（标准/URL 安全，带不带 = 都算），以及它出现在更长 base64 串中间时
// 一定会出现的那段字符（例如 Basic auth 里 base64("user:"+token)）。
func Variants(val string) []string {
	if len(val) < 4 {
		return nil
	}
	set := map[string]bool{val: true}
	add := func(s string, minLen int) {
		if len(s) >= minLen {
			set[s] = true
		}
	}
	if j, err := json.Marshal(val); err == nil {
		add(string(j[1:len(j)-1]), 4)
	}
	if len(val) >= 6 {
		add(url.QueryEscape(val), 6)
		add(url.PathEscape(val), 6)
		add(hex.EncodeToString([]byte(val)), 12)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
			add(enc.EncodeToString([]byte(val)), 8)
		}
		for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			for k := 0; k < 3; k++ {
				buf := append(make([]byte, k), val...)
				e := enc.EncodeToString(buf)
				start := (8*k + 5) / 6          // 第一个完全由密钥字节决定的字符
				end := (8 * (k + len(val))) / 6 // 最后一个完全由密钥字节决定的字符之后
				if end > len(e) {
					end = len(e)
				}
				if end-start >= 8 {
					add(e[start:end], 8)
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}
