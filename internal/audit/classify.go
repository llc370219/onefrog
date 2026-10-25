package audit

import (
	"path"
	"regexp"
	"strings"
)

// Risk categories reported by Classify. Labels are shown to the operator.
const (
	CatDestructive = "destructive"
	CatDisk        = "disk"
	CatSystem      = "system"
	CatNetwork     = "network"
	CatAccount     = "account"
	CatPackage     = "package"
	CatDatabase    = "database"
	CatContainer   = "container"
	CatSchedule    = "schedule"
	CatRemoteCode  = "remote_code"
	CatObfuscation = "obfuscation"
	CatCritical    = "critical_path"
	CatSecretRead  = "secret_read"
	CatModify      = "modify"
	CatUnknown     = "unknown"
)

var categoryLabels = map[string]string{
	CatDestructive: "删除或覆盖数据",
	CatDisk:        "磁盘与文件系统",
	CatSystem:      "服务与系统状态",
	CatNetwork:     "网络与访问控制",
	CatAccount:     "账号与权限",
	CatPackage:     "软件包变更",
	CatDatabase:    "数据库变更",
	CatContainer:   "容器与编排",
	CatSchedule:    "计划任务",
	CatRemoteCode:  "下载或执行代码",
	CatObfuscation: "混淆或无法解析",
	CatCritical:    "涉及根目录或关键路径",
	CatSecretRead:  "读取敏感凭据",
	CatModify:      "修改文件或配置",
	CatUnknown:     "未识别的命令",
}

// CategoryLabel returns the Chinese label of a category.
func CategoryLabel(cat string) string { return categoryLabels[cat] }

// Finding is one reason a command needs confirmation.
type Finding struct {
	Category string `json:"category"`
	Label    string `json:"label"`
	Detail   string `json:"detail"`
}

// Assessment is the result of Classify. Risky commands must be confirmed by a
// person in smart approval mode; the rest are read-only and may run directly.
type Assessment struct {
	Risky    bool      `json:"risky"`
	Level    string    `json:"level"` // low, medium or high
	Findings []Finding `json:"findings,omitempty"`
}

var highCategories = map[string]bool{CatDestructive: true, CatDisk: true, CatRemoteCode: true, CatObfuscation: true, CatCritical: true, CatSecretRead: true, CatAccount: true, CatDatabase: true}

const maxDepth = 6

// Classify decides whether a shell command is read-only. It parses pipes,
// lists, subshells, command substitution, quoting, sudo/env style wrappers and
// "sh -c" strings, and checks every simple command it finds. A command is safe
// only when every part is on the read-only list; anything unknown or
// unparseable is risky.
func Classify(command string) Assessment {
	c := &classifier{seen: map[string]bool{}}
	c.check(command, 0)
	a := Assessment{Level: "low", Findings: c.findings}
	if len(c.findings) > 0 {
		a.Risky = true
		a.Level = "medium"
		for _, f := range c.findings {
			if highCategories[f.Category] {
				a.Level = "high"
			}
		}
	}
	return a
}

type classifier struct {
	findings []Finding
	seen     map[string]bool
}

func (c *classifier) add(cat, detail string) {
	detail = strings.TrimSpace(detail)
	if len(detail) > 160 {
		detail = detail[:160] + "…"
	}
	key := cat + "\x00" + detail
	if c.seen[key] || len(c.findings) >= 8 {
		return
	}
	c.seen[key] = true
	c.findings = append(c.findings, Finding{Category: cat, Label: categoryLabels[cat], Detail: detail})
}

var forkBomb = regexp.MustCompile(`:\s*\(\s*\)\s*\{`)

func (c *classifier) check(command string, depth int) {
	if depth > maxDepth {
		c.add(CatObfuscation, "嵌套层数过多，无法可靠解析")
		return
	}
	if strings.TrimSpace(command) == "" {
		if depth == 0 {
			c.add(CatUnknown, "空命令")
		}
		return
	}
	if len(command) > 8000 {
		c.add(CatObfuscation, "命令过长，无法可靠审查")
		return
	}
	for _, r := range command {
		if r == 0 || (r < 32 && r != '\n' && r != '\t' && r != '\r') || r == 0x7f {
			c.add(CatObfuscation, "包含控制字符")
			return
		}
	}
	if forkBomb.MatchString(command) {
		c.add(CatSystem, "fork 炸弹")
		return
	}
	if strings.Contains(command, "IFS") && (strings.Contains(command, "$IFS") || strings.Contains(command, "${IFS")) {
		c.add(CatObfuscation, "使用 $IFS 拼接命令")
	}
	cmds, err := parse(command)
	if err == errFunction {
		c.add(CatObfuscation, "定义 shell 函数")
		return
	}
	if err != nil {
		c.add(CatObfuscation, "无法解析的 shell 语法（引号、括号或 case 语句）")
		return
	}
	if len(cmds) == 0 {
		c.add(CatObfuscation, "没有可识别的命令")
		return
	}
	for i := range cmds {
		c.simple(cmds, i, depth)
	}
}

var safeSinks = map[string]bool{"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true, "/dev/tty": true, "-": true}

// scratchPath reports paths under /tmp or /var/tmp, where writes are harmless.
func scratchPath(p string) bool {
	if strings.Contains(p, "..") || strings.ContainsAny(p, "$`*?") {
		return false
	}
	return strings.HasPrefix(p, "/tmp/") || strings.HasPrefix(p, "/var/tmp/")
}

var criticalExact = map[string]bool{"/": true, "/*": true, "~": true, "~/": true, "$HOME": true, ".": false, "/etc": true, "/boot": true, "/bin": true, "/sbin": true, "/lib": true, "/lib64": true, "/usr": true, "/var": true, "/home": true, "/root": true, "/opt": true, "/srv": true, "/dev": true, "/proc": true, "/sys": true, "/var/lib": true, "/var/log": true, "/usr/bin": true, "/usr/sbin": true, "/usr/lib": true, "/usr/local": true, "/var/www": true, "/data": true, "/mnt": true}
var criticalPrefixes = []string{"/etc/passwd", "/etc/shadow", "/etc/group", "/etc/gshadow", "/etc/sudoers", "/etc/ssh", "/etc/fstab", "/etc/pam.d", "/etc/systemd", "/etc/crontab", "/etc/cron.", "/etc/hosts", "/etc/resolv.conf", "/etc/security", "/etc/ld.so", "/etc/profile", "/etc/bash", "/etc/environment", "/etc/nginx", "/etc/apache2", "/etc/httpd", "/etc/mysql", "/etc/postgresql", "/etc/network", "/etc/netplan", "/etc/sysctl", "/etc/iptables", "/etc/ufw", "/etc/selinux", "/boot/", "/bin/", "/sbin/", "/lib/", "/lib64/", "/usr/bin/", "/usr/sbin/", "/usr/lib/", "/var/lib/mysql", "/var/lib/postgresql", "/var/lib/docker", "/var/lib/redis", "/var/lib/mongodb", "/var/spool/cron", "/root/", "/proc/sys", "/sys/", "/dev/sd", "/dev/nvme", "/dev/vd", "/dev/xvd", "/dev/hd", "/dev/mapper", "/dev/md", "/dev/dm-", "/dev/mem", "/dev/kmem", "/dev/disk"}

// criticalPath reports the root, system directories and key system files.
func criticalPath(p string) bool {
	if p == "" {
		return false
	}
	if strings.Contains(p, "authorized_keys") || strings.Contains(p, "/.ssh") || strings.HasPrefix(p, ".ssh") || strings.HasPrefix(p, "~/.ssh") {
		return true
	}
	clean := p
	if strings.HasPrefix(p, "/") {
		clean = path.Clean(p)
		if strings.HasSuffix(p, "/*") {
			clean = path.Clean(strings.TrimSuffix(p, "*"))
			if clean == "/" || criticalExact[clean] {
				return true
			}
		}
	}
	if criticalExact[clean] || criticalExact[p] {
		return true
	}
	// Globs at the top level such as /e* or /??? can match system directories.
	if strings.HasPrefix(clean, "/") && strings.Count(clean, "/") == 1 && strings.ContainsAny(clean, "*?[") {
		return true
	}
	for _, prefix := range criticalPrefixes {
		if strings.HasPrefix(clean, prefix) || strings.HasPrefix(clean+"/", prefix) {
			return true
		}
	}
	return false
}

var sensitiveRe = regexp.MustCompile(`(^|/)(shadow|gshadow)(-)?$|/\.ssh/id_|(^|/)id_(rsa|dsa|ecdsa|ed25519)($|[^.]|\.[^p])|ssh_host_[a-z0-9]+_key$|/etc/ssl/private|/proc/[^/]+/environ|\.aws/credentials|\.docker/config\.json|\.kube/config|\.git-credentials|(^|/)\.netrc$|(^|/)\.pgpass$|(^|/)\.my\.cnf$|/etc/sudoers|\.vault-token|/etc/onefrog|private[_-]?key|\.key$`)

func sensitivePath(p string) bool { return sensitiveRe.MatchString(p) }

func (c *classifier) simple(cmds []simple, idx, depth int) {
	sc := cmds[idx]
	for _, w := range sc.Words {
		for _, sub := range w.Subs {
			c.check(sub, depth+1)
		}
	}
	for _, r := range sc.Redirects {
		for _, sub := range r.Target.Subs {
			c.check(sub, depth+1)
		}
		switch r.Op {
		case ">", ">>", ">|", "&>", "&>>", "<>", ">&":
			t := r.Target.Text
			if r.Op == ">&" && (isDigits(t) || t == "-") {
				continue
			}
			if r.Target.Dynamic || r.Target.Glob {
				c.add(CatModify, "重定向写入的目标由变量或通配符决定："+t)
				continue
			}
			if safeSinks[t] || scratchPath(t) {
				continue
			}
			if strings.HasPrefix(t, "/dev/tcp/") || strings.HasPrefix(t, "/dev/udp/") {
				c.add(CatNetwork, "通过 /dev/tcp 建立网络连接（常见于反弹 shell）："+t)
				continue
			}
			if criticalPath(t) {
				c.add(CatCritical, "写入关键文件："+t)
				c.add(CatDestructive, "重定向覆盖或追加文件："+t)
				continue
			}
			c.add(CatModify, "重定向写入文件："+t)
		case "<", "<&":
			if strings.HasPrefix(r.Target.Text, "/dev/tcp/") || strings.HasPrefix(r.Target.Text, "/dev/udp/") {
				c.add(CatNetwork, "通过 /dev/tcp 建立网络连接："+r.Target.Text)
			}
			if sensitivePath(r.Target.Text) {
				c.add(CatSecretRead, "读取敏感文件："+r.Target.Text)
			}
		}
	}
	words := sc.Words
	// Leading assignments: FOO=bar cmd
	for len(words) > 0 && isAssignment(words[0]) {
		c.assignment(words[0])
		words = words[1:]
	}
	c.command(cmds, idx, words, depth)
}

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\[[^]]*\])?\+?=`)

func isAssignment(w word) bool {
	return assignRe.MatchString(w.Text) && !strings.HasPrefix(w.Text, "=")
}

var dangerousEnv = map[string]bool{"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "LD_AUDIT": true, "PATH": true, "BASH_ENV": true, "ENV": true, "IFS": true, "PROMPT_COMMAND": true, "SHELLOPTS": true, "BASHOPTS": true, "PS4": true, "GCONV_PATH": true, "PERL5OPT": true, "PERL5LIB": true, "PYTHONPATH": true, "PYTHONSTARTUP": true, "NODE_OPTIONS": true, "RUBYOPT": true, "GIT_SSH_COMMAND": true, "GIT_EXEC_PATH": true, "EDITOR": false}

func (c *classifier) assignment(w word) {
	name := w.Text[:strings.IndexAny(w.Text, "+=[")]
	if dangerousEnv[name] {
		c.add(CatObfuscation, "修改会改变命令行为的环境变量 "+name)
	}
}

func basename(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i < len(s)-1 {
		return s[i+1:]
	}
	return s
}

func texts(ws []word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Text
	}
	return out
}

func joined(ws []word) string { return strings.Join(texts(ws), " ") }

var shells = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true, "ash": true, "mksh": true, "fish": true, "rbash": true, "tcsh": true, "csh": true}
var interpreterRe = regexp.MustCompile(`^(python[0-9.]*|pypy[0-9.]*|perl[0-9.]*|ruby[0-9.]*|node|nodejs|deno|bun|php[0-9.]*|lua[0-9.]*|luajit|tclsh[0-9.]*|wish|rscript|Rscript|R|pwsh|powershell|osascript|groovy|jshell|java|irb|ghci|julia|expect|guile|scheme|racket|elixir|erl|escript)$`)
var decoders = map[string]bool{"base64": true, "base32": true, "basenc": true, "xxd": true, "openssl": true, "gunzip": true, "zcat": true, "rev": true, "tr": true, "printf": true, "uudecode": true, "gzip": true, "xz": true, "bzip2": true, "zstd": true, "od": true}

func (c *classifier) piped(cmds []simple, idx int) (bool, string) {
	sc := cmds[idx]
	if !sc.Piped && !sc.Stdin {
		return false, ""
	}
	decoded := ""
	for j := idx - 1; j >= 0 && cmds[j].Pipeline == sc.Pipeline; j-- {
		if len(cmds[j].Words) > 0 {
			name := basename(cmds[j].Words[0].Text)
			if decoders[name] {
				decoded = name
			}
			if name == "curl" || name == "wget" || name == "fetch" {
				return true, "下载内容直接交给解释器执行"
			}
		}
	}
	if decoded != "" {
		return true, "解码（" + decoded + "）后交给解释器执行"
	}
	if sc.Stdin {
		return true, "把内联或文件内容交给解释器执行"
	}
	return true, "把管道内容交给解释器执行"
}

func (c *classifier) command(cmds []simple, idx int, words []word, depth int) {
	for guard := 0; guard < 16; guard++ {
		if len(words) == 0 {
			return
		}
		head := words[0]
		if head.AnsiC || head.Dynamic || head.Glob || strings.ContainsAny(head.Text, "{}") && head.Text != "{" && head.Text != "}" {
			detail := "命令名来自变量、命令替换或通配符：" + head.Text
			remote := false
			for _, sub := range head.Subs {
				l := strings.ToLower(sub)
				if strings.Contains(l, "curl") || strings.Contains(l, "wget") {
					remote = true
				}
			}
			if remote {
				c.add(CatRemoteCode, "执行下载得到的命令")
			}
			c.add(CatObfuscation, detail)
			return
		}
		name := head.Text
		if strings.Contains(name, "/") {
			name = basename(name)
		}
		rest := words[1:]
		args := texts(rest)
		full := clipArgs(head.Text, args)
		// Global check: reading credential files.
		for _, a := range args {
			if sensitivePath(a) && !readMetadataOnly[name] {
				c.add(CatSecretRead, "访问敏感凭据文件："+a)
			}
		}
		switch name {
		case "if", "then", "elif", "else", "do", "while", "until", "!", "{", "time", "coproc":
			words = rest
			continue
		case "fi", "done", "esac", "}", "for", "select", "in":
			return
		case "case", "function":
			c.add(CatObfuscation, "复杂 shell 结构："+name)
			return
		case "sudo", "doas":
			next, ok := c.sudo(rest)
			if !ok {
				return
			}
			words = next
			continue
		case "su", "runuser":
			if script := optValue(args, "-c", "--command"); script != "" {
				c.check(script, depth+1)
				return
			}
			c.add(CatAccount, "切换用户或打开 root shell："+full)
			return
		case "env":
			next, ok := c.env(rest)
			if !ok {
				return
			}
			words = next
			continue
		case "nohup", "setsid", "unbuffer", "chronic", "nocache", "eatmydata", "builtin", "exec", "catchsegv":
			words = rest
			continue
		case "command":
			if len(args) > 0 && (args[0] == "-v" || args[0] == "-V") {
				return
			}
			words = skipFlags(rest, nil)
			continue
		case "nice":
			words = skipFlags(rest, map[string]bool{"-n": true, "--adjustment": true})
			continue
		case "ionice":
			words = skipFlags(rest, map[string]bool{"-c": true, "-n": true, "-p": true, "-P": true, "-u": true, "--class": true, "--classdata": true})
			continue
		case "stdbuf":
			words = skipFlags(rest, map[string]bool{"-i": true, "-o": true, "-e": true})
			continue
		case "timeout":
			words = skipFlags(rest, map[string]bool{"-s": true, "-k": true, "--signal": true, "--kill-after": true})
			if len(words) > 0 {
				words = words[1:]
			}
			continue
		case "taskset":
			words = skipFlags(rest, nil)
			if len(words) > 0 && len(args) > 0 && !strings.HasPrefix(args[0], "-c") {
				words = words[1:]
			} else if len(words) > 0 {
				words = words[1:]
			}
			continue
		case "chrt":
			words = skipFlags(rest, nil)
			if len(words) > 0 {
				words = words[1:]
			}
			continue
		case "flock":
			if script := optValue(args, "-c", "--command"); script != "" {
				c.check(script, depth+1)
				return
			}
			words = skipFlags(rest, map[string]bool{"-w": true, "--timeout": true, "-E": true})
			if len(words) > 0 {
				words = words[1:]
			}
			continue
		case "watch":
			inner := skipFlags(rest, map[string]bool{"-n": true, "--interval": true, "-d": false, "-q": true})
			c.check(joined(inner), depth+1)
			return
		case "xargs":
			inner := skipFlags(rest, map[string]bool{"-n": true, "-I": true, "-i": false, "-P": true, "-d": true, "-a": true, "-L": true, "-l": false, "-s": true, "-E": true, "-e": false, "--max-args": true, "--max-procs": true, "--delimiter": true, "--arg-file": true, "--replace": false, "--max-lines": true, "--eof": false})
			if len(inner) == 0 {
				return
			}
			words = inner
			continue
		case "busybox", "toybox":
			if len(rest) > 0 && strings.HasPrefix(args[0], "-") {
				return
			}
			words = rest
			continue
		}
		if shells[name] {
			c.shell(cmds, idx, name, rest, depth)
			return
		}
		if interpreterRe.MatchString(name) {
			c.interpreter(cmds, idx, name, args, full)
			return
		}
		if h, ok := handlers[name]; ok {
			h(c, name, args, full, cmds[idx], depth)
			return
		}
		if readOnly[name] {
			return
		}
		for prefix, cat := range riskyPrefixes {
			if strings.HasPrefix(name, prefix) {
				c.add(cat, full)
				return
			}
		}
		if cat, ok := riskyCommands[name]; ok {
			c.add(cat, full)
			c.criticalTargets(args)
			return
		}
		c.add(CatUnknown, "不在只读清单中："+full)
		return
	}
	c.add(CatObfuscation, "包装层数过多")
}

func clipArgs(head string, args []string) string {
	s := strings.TrimSpace(head + " " + strings.Join(args, " "))
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func (c *classifier) criticalTargets(args []string) {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && criticalPath(a) {
			c.add(CatCritical, "目标是根目录或关键路径："+a)
		}
	}
}

// skipFlags drops leading options; withValue lists options that consume the
// next word.
func skipFlags(ws []word, withValue map[string]bool) []word {
	for len(ws) > 0 {
		t := ws[0].Text
		if t == "--" {
			return ws[1:]
		}
		if !strings.HasPrefix(t, "-") || t == "-" {
			return ws
		}
		if withValue[t] && !strings.Contains(t, "=") {
			if len(ws) < 2 {
				return nil
			}
			ws = ws[2:]
			continue
		}
		ws = ws[1:]
	}
	return ws
}

// optValue returns the value of a short or long option (-c X, -cX, --x=X).
func optValue(args []string, short, long string) string {
	for i, a := range args {
		if a == short || (long != "" && a == long) {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if long != "" && strings.HasPrefix(a, long+"=") {
			return strings.TrimPrefix(a, long+"=")
		}
		if len(short) == 2 && strings.HasPrefix(a, short) && len(a) > 2 && !strings.HasPrefix(a, "--") {
			return a[2:]
		}
	}
	return ""
}

func hasArg(args []string, opts ...string) bool {
	for _, a := range args {
		for _, o := range opts {
			if a == o || strings.HasPrefix(o, "--") && strings.HasPrefix(a, o+"=") {
				return true
			}
		}
	}
	return false
}

// hasShort reports whether any short-option cluster (like -rf) contains ch.
func hasShort(args []string, ch byte) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.IndexByte(a[1:], ch) >= 0 {
			return true
		}
	}
	return false
}

func positionals(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--" {
			return append(out, args[i+1:]...)
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			out = append(out, a)
		}
	}
	return out
}

func (c *classifier) sudo(ws []word) ([]word, bool) {
	valued := map[string]bool{"-u": true, "-g": true, "-C": true, "-D": true, "-h": true, "-p": true, "-r": true, "-t": true, "-U": true, "-T": true, "-R": true, "--user": true, "--group": true, "--prompt": true, "--host": true, "--chdir": true, "--close-from": true, "--role": true, "--type": true, "--other-user": true, "--command-timeout": true}
	shellFlag := false
	for len(ws) > 0 {
		t := ws[0].Text
		if t == "--" {
			ws = ws[1:]
			break
		}
		if !strings.HasPrefix(t, "-") {
			break
		}
		switch {
		case t == "-l" || t == "--list" || t == "-v" || t == "--validate" || t == "-k" || t == "-K" || t == "-V" || t == "--version" || t == "-h" && len(ws) == 1:
			return nil, false
		case t == "-e" || t == "--edit":
			c.add(CatModify, "sudoedit 编辑文件")
			return nil, false
		case t == "-i" || t == "-s" || t == "--login" || t == "--shell":
			shellFlag = true
		case valued[t]:
			if len(ws) > 1 {
				ws = ws[1:]
			}
		case !strings.HasPrefix(t, "--") && len(t) > 2:
			// Clusters such as -iu or -Eu root.
			last := t[len(t)-1:]
			if strings.ContainsAny(t[1:], "is") {
				shellFlag = true
			}
			if valued["-"+last] && len(ws) > 1 {
				ws = ws[1:]
			}
		}
		ws = ws[1:]
	}
	if len(ws) == 0 {
		if shellFlag {
			c.add(CatAccount, "打开 root shell")
		}
		return nil, false
	}
	return ws, true
}

func (c *classifier) env(ws []word) ([]word, bool) {
	for len(ws) > 0 {
		t := ws[0].Text
		switch {
		case t == "--":
			ws = ws[1:]
			continue
		case t == "-S" || strings.HasPrefix(t, "--split-string") || strings.HasPrefix(t, "-S"):
			c.add(CatObfuscation, "env -S 拆分字符串执行")
			return nil, false
		case t == "-u" || t == "--unset" || t == "-C" || t == "--chdir":
			if len(ws) > 1 {
				ws = ws[1:]
			}
			ws = ws[1:]
			continue
		case strings.HasPrefix(t, "-"):
			ws = ws[1:]
			continue
		case isAssignment(ws[0]):
			c.assignment(ws[0])
			ws = ws[1:]
			continue
		}
		break
	}
	if len(ws) == 0 {
		return nil, false
	}
	return ws, true
}

func (c *classifier) shell(cmds []simple, idx int, name string, ws []word, depth int) {
	args := texts(ws)
	for i := 0; i < len(ws); i++ {
		t := ws[i].Text
		if t == "--version" || t == "--help" {
			return
		}
		if t == "-n" && len(ws) > i+1 {
			return // syntax check only
		}
		if t == "-o" || t == "+o" || t == "-O" || t == "+O" || t == "--rcfile" || t == "--init-file" {
			i++
			continue
		}
		if strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--") && strings.ContainsRune(t, 'c') {
			if i+1 >= len(ws) {
				c.add(CatObfuscation, name+" -c 缺少脚本")
				return
			}
			script := ws[i+1]
			if script.Dynamic {
				remote := false
				for _, sub := range script.Subs {
					l := strings.ToLower(sub)
					if strings.Contains(l, "curl") || strings.Contains(l, "wget") || strings.Contains(l, "fetch") {
						remote = true
					}
				}
				if remote {
					c.add(CatRemoteCode, name+" -c 执行下载得到的脚本")
				} else {
					c.add(CatObfuscation, name+" -c 执行运行时生成的脚本")
				}
			}
			c.check(script.Text, depth+1)
			return
		}
		if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+") {
			continue
		}
		c.add(CatRemoteCode, "执行脚本文件（内容未经审查）："+clipArgs(name, args[i:]))
		return
	}
	if ok, why := c.piped(cmds, idx); ok {
		c.add(CatRemoteCode, why+"："+name)
		if strings.Contains(why, "解码") {
			c.add(CatObfuscation, "先解码再执行，内容无法直接审查")
		}
		return
	}
	c.add(CatUnknown, "打开交互式 shell："+name)
}

var safeModules = map[string]bool{"json.tool": true, "site": false, "platform": true, "sysconfig": true}

func (c *classifier) interpreter(cmds []simple, idx int, name string, args []string, full string) {
	if len(args) == 1 && (args[0] == "-V" || args[0] == "--version" || args[0] == "-v" || args[0] == "-version" || args[0] == "version") {
		return
	}
	if (strings.HasPrefix(name, "python") || strings.HasPrefix(name, "pypy")) && len(args) >= 2 && args[0] == "-m" {
		if safeModules[args[1]] {
			return
		}
		if args[1] == "pip" {
			pkgManager(c, "pip", args[2:], full, simple{}, 0)
			return
		}
	}
	if hasArg(args, "-c", "-e", "-E", "-r", "--eval", "-p", "--print", "-x") || hasShort(args, 'c') && strings.HasPrefix(name, "python") || hasShort(args, 'e') && (strings.HasPrefix(name, "perl") || strings.HasPrefix(name, "ruby")) {
		c.add(CatRemoteCode, "执行内联代码（内容未经逐行审查）："+full)
		return
	}
	if ok, why := c.piped(cmds, idx); ok && len(positionals(args)) == 0 {
		c.add(CatRemoteCode, why+"："+name)
		if strings.Contains(why, "解码") {
			c.add(CatObfuscation, "先解码再执行，内容无法直接审查")
		}
		return
	}
	c.add(CatRemoteCode, "运行脚本或解释器（内容未经审查）："+full)
}

// readMetadataOnly commands look at a path without reading its contents.
var readMetadataOnly = map[string]bool{"ls": true, "stat": true, "file": true, "test": true, "[": true, "[[": true, "du": true, "namei": true, "realpath": true, "readlink": true, "lsattr": true, "getfacl": true, "basename": true, "dirname": true, "ssh-keygen": true, "find": true, "echo": true, "printf": true}

var readOnly = set("ls", "dir", "vdir", "tree", "cat", "tac", "nl", "head", "tail", "less", "more", "grep", "egrep", "fgrep", "rg", "ag", "ack", "zgrep", "zegrep", "zfgrep", "zless", "zmore", "zcat", "bzcat", "xzcat", "zstdcat", "lz4cat", "wc", "cut", "paste", "join", "tr", "uniq", "column", "fold", "fmt", "expand", "unexpand", "rev", "comm", "diff", "diff3", "sdiff", "cmp", "colordiff", "stat", "file", "readlink", "realpath", "basename", "dirname", "pwd", "cd", "pushd", "popd", "dirs", "echo", "printf", "true", "false", "test", "[", "[[", "]]", "]", "sleep", "wait", "which", "whereis", "type", "locate", "plocate", "mlocate", "md5sum", "sha1sum", "sha224sum", "sha256sum", "sha384sum", "sha512sum", "b2sum", "cksum", "sum", "jq", "yq", "gojq", "base64", "base32", "basenc", "xxd", "hexdump", "hd", "od", "strings", "cal", "ncal", "printenv", "locale", "getconf", "ulimit", "umask", "set", "unset", "declare", "typeset", "local", "readonly", "shopt", "history", "seq", "yes", "expr", "bc", "shuf", "tsort", "numfmt", "factor", "lsattr", "getfacl", "namei", "findmnt", "lsblk", "blkid", "uptime", "w", "who", "whoami", "id", "groups", "hostid", "last", "lastb", "lastlog", "users", "uname", "arch", "nproc", "lscpu", "lsmem", "lspci", "lsusb", "lsmod", "lshw", "dmidecode", "lsof", "lsns", "lslocks", "lsipc", "ipcs", "ps", "pstree", "pgrep", "pidof", "top", "htop", "atop", "btop", "iotop", "iftop", "nethogs", "vmstat", "iostat", "mpstat", "pidstat", "sar", "free", "df", "du", "ss", "netstat", "nstat", "ping", "ping6", "traceroute", "traceroute6", "tracepath", "mtr", "dig", "nslookup", "host", "drill", "whois", "getent", "sensors", "systemd-cgls", "systemd-cgtop", "systemd-detect-virt", "systemd-delta", "getenforce", "sestatus", "aa-status", "apparmor_status", "ausearch", "aureport", "ntpstat", "ldd", "nm", "objdump", "readelf", "size", "tty", "clear", "exit", "return", "break", "continue", ":", "export", "alias", "iptables-save", "ip6tables-save", "ssh-keyscan", "mysqldump", "pg_dumpall", "pg_isready", "free", "dpkg-query", "apt-config", "lsinitramfs", "xmllint", "iconv", "cmp", "look", "whatis", "apropos", "man", "info", "help", "date", "env", "dmesg", "journalctl", "hostname", "mount", "swapon", "losetup", "dmsetup", "pvs", "vgs", "lvs", "pvdisplay", "vgdisplay", "lvdisplay", "lvmdiskscan", "conntrack", "nvidia-smi", "sort", "sed", "awk", "gawk", "mawk", "nawk", "tee", "find", "curl", "wget")

func set(items ...string) map[string]bool {
	m := map[string]bool{}
	for _, i := range items {
		m[i] = true
	}
	return m
}

// riskyCommands are recognised commands that always change state.
var riskyCommands = map[string]string{
	"rm": CatDestructive, "rmdir": CatDestructive, "unlink": CatDestructive, "shred": CatDestructive, "srm": CatDestructive, "truncate": CatDestructive, "wipe": CatDestructive,
	"mv": CatModify, "ln": CatModify, "install": CatModify, "chattr": CatModify, "setfacl": CatModify, "patch": CatModify, "rename": CatModify, "split": CatModify, "csplit": CatModify, "vi": CatModify, "vim": CatModify, "nvim": CatModify, "nano": CatModify, "emacs": CatModify, "ed": CatModify, "ex": CatModify, "dos2unix": CatModify, "unix2dos": CatModify, "updatedb": CatModify, "ldconfig": CatSystem, "update-alternatives": CatSystem, "update-grub": CatSystem, "grub-install": CatDisk, "update-initramfs": CatSystem, "dracut": CatSystem,
	"chmod": CatModify, "chown": CatModify, "chgrp": CatModify, "chcon": CatModify, "restorecon": CatModify, "setenforce": CatSystem, "setsebool": CatSystem,
	"dd": CatDisk, "wipefs": CatDisk, "fdisk": CatDisk, "sfdisk": CatDisk, "gdisk": CatDisk, "sgdisk": CatDisk, "cfdisk": CatDisk, "parted": CatDisk, "partprobe": CatDisk, "mkswap": CatDisk, "swapoff": CatDisk, "umount": CatDisk, "blkdiscard": CatDisk, "badblocks": CatDisk, "resize2fs": CatDisk, "xfs_growfs": CatDisk, "e2fsck": CatDisk, "fsck": CatDisk, "xfs_repair": CatDisk, "tune2fs": CatDisk, "e2label": CatDisk, "pvcreate": CatDisk, "vgcreate": CatDisk, "lvcreate": CatDisk, "lvextend": CatDisk, "lvreduce": CatDisk, "lvresize": CatDisk, "lvremove": CatDisk, "vgremove": CatDisk, "pvremove": CatDisk, "vgextend": CatDisk, "vgreduce": CatDisk, "pvmove": CatDisk, "lvchange": CatDisk, "vgchange": CatDisk, "mdadm": CatDisk, "cryptsetup": CatDisk, "zpool": CatDisk, "zfs": CatDisk, "btrfs": CatDisk, "mkinitrd": CatSystem, "fstrim": CatDisk, "hdparm": CatDisk, "sync": CatSystem,
	"reboot": CatSystem, "shutdown": CatSystem, "poweroff": CatSystem, "halt": CatSystem, "init": CatSystem, "telinit": CatSystem, "kexec": CatSystem, "systemd-run": CatSystem, "kill": CatSystem, "pkill": CatSystem, "killall": CatSystem, "killall5": CatSystem, "skill": CatSystem, "renice": CatSystem, "modprobe": CatSystem, "insmod": CatSystem, "rmmod": CatSystem, "ipcrm": CatSystem, "sysctl": CatSystem, "swapon": CatDisk,
	"passwd": CatAccount, "chpasswd": CatAccount, "usermod": CatAccount, "useradd": CatAccount, "adduser": CatAccount, "userdel": CatAccount, "deluser": CatAccount, "groupadd": CatAccount, "groupdel": CatAccount, "groupmod": CatAccount, "addgroup": CatAccount, "delgroup": CatAccount, "gpasswd": CatAccount, "chsh": CatAccount, "chfn": CatAccount, "newusers": CatAccount, "vipw": CatAccount, "vigr": CatAccount, "sudoedit": CatAccount, "pwck": CatAccount, "grpck": CatAccount, "pam-auth-update": CatAccount, "authconfig": CatAccount,
	"ssh": CatNetwork, "scp": CatNetwork, "sftp": CatNetwork, "sshpass": CatNetwork, "telnet": CatNetwork, "ftp": CatNetwork, "lftp": CatNetwork, "smbclient": CatNetwork, "mosh": CatNetwork, "socat": CatNetwork, "ifup": CatNetwork, "ifdown": CatNetwork, "iptables-restore": CatNetwork, "ip6tables-restore": CatNetwork, "ipset": CatNetwork, "brctl": CatNetwork, "wg": CatNetwork, "wg-quick": CatNetwork, "openvpn": CatNetwork, "tcpdump": CatNetwork, "nmap": CatNetwork, "masscan": CatNetwork, "aria2c": CatModify, "axel": CatModify,
	"crontab": CatSchedule, "at": CatSchedule, "batch": CatSchedule, "atrm": CatSchedule,
	"eval": CatRemoteCode, "source": CatRemoteCode, ".": CatRemoteCode, "make": CatRemoteCode, "cmake": CatRemoteCode, "gcc": CatRemoteCode, "cc": CatRemoteCode, "go": CatRemoteCode, "cargo": CatRemoteCode, "dc": CatRemoteCode, "gdb": CatRemoteCode, "strace": CatRemoteCode, "ltrace": CatRemoteCode, "chroot": CatSystem, "nsenter": CatSystem, "unshare": CatSystem, "ansible": CatRemoteCode, "ansible-playbook": CatRemoteCode, "terraform": CatContainer, "screen": CatUnknown, "tmux": CatUnknown,
	"pg_restore": CatDatabase, "mysqlimport": CatDatabase, "mongorestore": CatDatabase, "dropdb": CatDatabase, "createdb": CatDatabase, "dropuser": CatDatabase, "createuser": CatDatabase, "mongodump": CatModify, "redis-server": CatDatabase, "mysqld": CatDatabase, "postgres": CatDatabase, "pg_ctl": CatDatabase, "pg_ctlcluster": CatDatabase, "mysql_upgrade": CatDatabase, "mysql_secure_installation": CatDatabase,
	"docker-compose": CatContainer, "podman-compose": CatContainer, "minikube": CatContainer, "k3s": CatContainer, "kubeadm": CatContainer, "ctr": CatContainer,
	"touch": CatModify, "mkdir": CatModify, "cp": CatModify, "rsync": CatModify, "zip": CatModify, "unzip": CatModify, "7z": CatModify, "gzip": CatModify, "gunzip": CatModify, "bzip2": CatModify, "bunzip2": CatModify, "xz": CatModify, "unxz": CatModify, "zstd": CatModify, "unzstd": CatModify, "lz4": CatModify, "tar": CatModify,
}

var riskyPrefixes = map[string]string{"mkfs": CatDisk, "mke2fs": CatDisk, "mkfs.": CatDisk, "fsck.": CatDisk}
