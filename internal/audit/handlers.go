package audit

import (
	"regexp"
	"strings"
)

type handler func(c *classifier, name string, args []string, full string, sc simple, depth int)

var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		"rm":            rmHandler,
		"find":          findHandler,
		"sed":           sedHandler,
		"awk":           awkHandler,
		"gawk":          awkHandler,
		"mawk":          awkHandler,
		"nawk":          awkHandler,
		"tee":           teeHandler,
		"sort":          outputFlag("-o", "--output"),
		"iconv":         outputFlag("-o", "--output"),
		"iptables-save": outputFlag("-f", "--file"),
		"mysqldump":     outputFlag("-r", "--result-file"),
		"pg_dump":       outputFlag("-f", "--file"),
		"pg_dumpall":    outputFlag("-f", "--file"),
		"cp":            copyHandler,
		"mkdir":         scratchOnly(CatModify),
		"touch":         scratchOnly(CatModify),
		"mktemp":        func(*classifier, string, []string, string, simple, int) {},
		"export":        exportHandler,
		"alias":         aliasHandler,
		"date":          dateHandler,
		"hwclock":       allowIf(CatSystem, "-r", "--show", "--get", "-V", "--version"),
		"env":           func(*classifier, string, []string, string, simple, int) {},
		"dmesg":         denyIf(CatSystem, "-C", "--clear", "-c", "--read-clear", "-D", "--console-off", "-E", "--console-on", "-n", "--console-level"),
		"journalctl":    journalHandler,
		"hostname":      hostnameHandler,
		"mount":         noPositional(CatDisk),
		"swapon":        allowIf(CatDisk, "--show", "-s", "--summary"),
		"losetup":       allowIf(CatDisk, "-a", "--all", "-l", "--list", "-j"),
		"dmsetup":       subAllow(CatDisk, "ls", "info", "status", "table", "deps", "version"),
		"conntrack":     allowIf(CatNetwork, "-L", "--dump", "-S", "--stats", "-C", "--count", "-G", "--get"),
		"nvidia-smi":    denyIf(CatSystem, "-r", "--gpu-reset", "-pm", "--persistence-mode", "-pl", "--power-limit", "-ac", "-rac", "-e", "-c", "--compute-mode", "-lgc", "-rgc", "-mig", "-am", "-caa"),
		"systemctl":     systemctlHandler,
		"service":       serviceHandler,
		"docker":        dockerHandler,
		"podman":        dockerHandler,
		"nerdctl":       dockerHandler,
		"kubectl":       kubectlHandler,
		"oc":            kubectlHandler,
		"k3s": func(c *classifier, n string, a []string, f string, sc simple, d int) {
			if len(a) > 0 && a[0] == "kubectl" {
				kubectlHandler(c, n, a[1:], f, sc, d)
			} else {
				c.add(CatContainer, f)
			}
		},
		"helm":            subAllow(CatContainer, "list", "ls", "status", "get", "history", "show", "search", "version", "template", "lint", "env"),
		"crictl":          subAllow(CatContainer, "ps", "images", "img", "logs", "inspect", "inspecti", "inspectp", "info", "version", "pods", "stats", "statsp"),
		"git":             gitHandler,
		"apt":             aptHandler,
		"apt-get":         aptHandler,
		"apt-cache":       func(*classifier, string, []string, string, simple, int) {},
		"apt-mark":        subAllow(CatPackage, "showauto", "showmanual", "showhold", "showinstall", "showremove", "showpurge"),
		"aptitude":        subAllow(CatPackage, "search", "show", "versions", "why", "why-not"),
		"dpkg":            dpkgHandler,
		"yum":             subAllow(CatPackage, "list", "info", "search", "provides", "whatprovides", "repolist", "check-update", "deplist", "repoquery", "history", "--version"),
		"dnf":             subAllow(CatPackage, "list", "info", "search", "provides", "whatprovides", "repolist", "check-update", "deplist", "repoquery", "history", "--version"),
		"microdnf":        subAllow(CatPackage, "repolist", "--version"),
		"rpm":             rpmHandler,
		"apk":             subAllow(CatPackage, "info", "list", "search", "policy", "version", "stats", "audit", "dot", "--version"),
		"pacman":          pacmanHandler,
		"zypper":          subAllow(CatPackage, "search", "se", "info", "if", "list-updates", "lu", "lp", "list-patches", "patches", "packages", "pa", "repos", "lr", "products", "pd", "--version"),
		"snap":            subAllow(CatPackage, "list", "info", "find", "version", "changes", "services", "connections", "interfaces", "logs", "known", "warnings", "--version"),
		"flatpak":         subAllow(CatPackage, "list", "info", "search", "remotes", "history", "--version"),
		"pip":             pipHandler,
		"pip3":            pipHandler,
		"pipx":            subAllow(CatPackage, "list", "--version", "environment"),
		"npm":             subAllow(CatPackage, "ls", "list", "ll", "la", "view", "info", "show", "outdated", "-v", "--version", "version", "doctor", "why", "explain", "prefix", "root", "bin", "help", "config", "audit"),
		"pnpm":            subAllow(CatPackage, "ls", "list", "outdated", "-v", "--version", "why", "root", "bin", "audit"),
		"yarn":            subAllow(CatPackage, "list", "info", "outdated", "-v", "--version", "why", "config", "audit"),
		"gem":             subAllow(CatPackage, "list", "info", "search", "environment", "-v", "--version", "contents", "outdated", "specification"),
		"composer":        subAllow(CatPackage, "show", "outdated", "licenses", "depends", "why", "why-not", "diagnose", "--version", "validate", "info"),
		"mysql":           sqlClient,
		"mariadb":         sqlClient,
		"psql":            sqlClient,
		"sqlite3":         sqliteHandler,
		"redis-cli":       redisHandler,
		"mongo":           mongoHandler,
		"mongosh":         mongoHandler,
		"mysqladmin":      subAllow(CatDatabase, "status", "ping", "version", "processlist", "variables", "extended-status", "--version"),
		"crontab":         crontabHandler,
		"atq":             func(*classifier, string, []string, string, simple, int) {},
		"iptables":        iptablesHandler,
		"ip6tables":       iptablesHandler,
		"arptables":       iptablesHandler,
		"ebtables":        iptablesHandler,
		"nft":             nftHandler,
		"ufw":             subAllow(CatNetwork, "status", "app", "version", "show", "--version"),
		"firewall-cmd":    firewallCmdHandler,
		"fail2ban-client": subAllow(CatNetwork, "status", "ping", "version", "get", "-V", "--version", "banned"),
		"ip":              ipHandler,
		"ifconfig":        ifconfigHandler,
		"route":           noPositional(CatNetwork),
		"arp":             denyIf(CatNetwork, "-d", "--delete", "-s", "--set", "-f", "--file"),
		"nmcli":           nmcliHandler,
		"ethtool":         ethtoolHandler,
		"tc":              tcHandler,
		"resolvectl":      subAllow(CatNetwork, "status", "query", "statistics", "show-cache", "show-server-state", "--version"),
		"hostnamectl":     subAllow(CatSystem, "status", "--static", "--transient", "--pretty", "--json", "hostname", "icon-name", "chassis", "deployment", "location"),
		"timedatectl":     subAllow(CatSystem, "status", "show", "list-timezones", "timesync-status", "show-timesync", "--version"),
		"localectl":       subAllow(CatSystem, "status", "list-locales", "list-keymaps", "list-x11-keymap-models", "list-x11-keymap-layouts"),
		"loginctl":        subAllow(CatSystem, "list-sessions", "session-status", "show-session", "list-users", "user-status", "show-user", "list-seats", "seat-status", "show-seat"),
		"systemd-analyze": subAllow(CatSystem, "time", "blame", "critical-chain", "verify", "security", "plot", "dot", "dump", "calendar", "timespan", "timestamp", "unit-paths", "cat-config", "syscall-filter", "condition", "log-level"),
		"chronyc":         subAllow(CatSystem, "tracking", "sources", "sourcestats", "activity", "ntpdata", "clients", "serverstats", "-n", "-c"),
		"ntpq":            func(*classifier, string, []string, string, simple, int) {},
		"auditctl":        allowIf(CatSystem, "-l", "-s", "-v"),
		"faillock":        denyIf(CatAccount, "--reset"),
		"faillog":         denyIf(CatAccount, "-r", "--reset", "-l", "-m", "--lock-secs", "--maximum"),
		"chage":           allowIf(CatAccount, "-l", "--list"),
		"visudo":          allowIf(CatAccount, "-c", "--check"),
		"sysctl":          sysctlHandler,
		"curl":            curlHandler,
		"wget":            wgetHandler,
		"openssl":         opensslHandler,
		"nc":              ncHandler,
		"ncat":            ncHandler,
		"netcat":          ncHandler,
		"ssh": func(c *classifier, n string, a []string, f string, sc simple, d int) {
			if len(a) == 1 && a[0] == "-V" {
				return
			}
			c.add(CatNetwork, "连接其他主机执行操作："+f)
		},
		"ssh-keygen": allowIf(CatModify, "-l", "-lf", "-F", "-B", "-L"),
		"tar":        tarHandler,
		"gzip":       compressHandler,
		"gunzip":     compressHandler,
		"bzip2":      compressHandler,
		"bunzip2":    compressHandler,
		"xz":         compressHandler,
		"unxz":       compressHandler,
		"zstd":       compressHandler,
		"unzstd":     compressHandler,
		"lz4":        compressHandler,
		"unzip":      allowIf(CatModify, "-l", "-v", "-t", "-Z", "-p", "-z", "-lv"),
		"7z":         subAllow(CatModify, "l", "t", "i"),
		"zip": func(c *classifier, n string, a []string, f string, sc simple, d int) {
			if p := positionals(a); len(p) > 0 && scratchPath(p[0]) {
				return
			}
			c.add(CatModify, f)
		},
		"nginx":           allowIf(CatSystem, "-t", "-T", "-v", "-V", "-h", "-?"),
		"apachectl":       subAllow(CatSystem, "-t", "configtest", "-S", "-M", "-v", "-V", "-l", "-L", "status", "fullstatus", "-t -D DUMP_VHOSTS"),
		"apache2ctl":      subAllow(CatSystem, "-t", "configtest", "-S", "-M", "-v", "-V", "-l", "-L", "status", "fullstatus"),
		"httpd":           allowIf(CatSystem, "-t", "-S", "-M", "-v", "-V", "-l", "-L"),
		"haproxy":         allowIf(CatSystem, "-c", "-v", "-vv"),
		"sshd":            allowIf(CatSystem, "-t", "-T", "-V"),
		"named-checkconf": func(*classifier, string, []string, string, simple, int) {},
		"named-checkzone": func(*classifier, string, []string, string, simple, int) {},
		"caddy":           subAllow(CatSystem, "validate", "version", "adapt", "list-modules", "build-info", "environ"),
		"certbot":         subAllow(CatSystem, "certificates", "--version", "plugins", "show_account"),
		"supervisorctl":   subAllow(CatSystem, "status", "avail", "pid", "version", "tail", "help"),
		"pm2":             subAllow(CatSystem, "list", "ls", "l", "status", "show", "describe", "info", "logs", "monit", "jlist", "prettylist", "env", "--version", "-v"),
		"virsh":           subAllow(CatSystem, "list", "dominfo", "domstate", "nodeinfo", "version", "net-list", "pool-list", "vol-list", "domifaddr", "dumpxml", "capabilities", "hostname", "domblklist", "domiflist", "net-info", "pool-info", "sysinfo"),
		"smartctl":        smartctlHandler,
		"hdparm":          allowIf(CatDisk, "-I", "-i", "-C", "-g", "-t", "-T", "-tT"),
		"fdisk":           allowIf(CatDisk, "-l", "--list"),
		"sfdisk":          allowIf(CatDisk, "-l", "--list", "-d", "--dump", "-s", "--show-size", "-V", "--verify"),
		"parted":          partedHandler,
		"tune2fs":         allowIf(CatDisk, "-l"),
		"dumpe2fs":        func(*classifier, string, []string, string, simple, int) {},
		"xfs_info":        func(*classifier, string, []string, string, simple, int) {},
		"mdadm":           allowIf(CatDisk, "--detail", "-D", "--examine", "-E", "--query", "-Q"),
		"cryptsetup":      subAllow(CatDisk, "status", "luksDump", "isLuks", "--version"),
		"zpool":           subAllow(CatDisk, "list", "status", "iostat", "get", "history", "version", "events"),
		"zfs":             subAllow(CatDisk, "list", "get", "version", "holds", "diff"),
		"btrfs":           btrfsHandler,
		"dd":              ddHandler,
		"fuser":           denyIf(CatSystem, "-k", "--kill"),
		"kill": func(c *classifier, n string, a []string, f string, sc simple, d int) {
			if hasArg(a, "-l", "-L", "--list") || len(a) == 2 && a[0] == "-0" {
				return
			}
			c.add(CatSystem, "终止进程："+f)
		},
		"screen": allowIf(CatUnknown, "-ls", "-list"),
		"tmux":   subAllow(CatUnknown, "ls", "list-sessions", "list-windows", "list-panes", "-V"),
		"go":     subAllow(CatRemoteCode, "version", "env", "list", "doc"),
		"cargo":  subAllow(CatRemoteCode, "--version", "-V", "tree", "metadata", "search"),
		"logger": func(*classifier, string, []string, string, simple, int) {},
	}
}

func rmHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	recursive := hasShort(args, 'r') || hasShort(args, 'R') || hasArg(args, "--recursive")
	force := hasShort(args, 'f') || hasArg(args, "--force")
	detail := "删除文件：" + full
	if recursive && force {
		detail = "递归强制删除：" + full
	}
	c.add(CatDestructive, detail)
	if hasArg(args, "--no-preserve-root") {
		c.add(CatCritical, "关闭根目录保护：--no-preserve-root")
	}
	for _, p := range positionals(args) {
		if criticalPath(p) || p == "*" || p == ".*" {
			c.add(CatCritical, "删除目标是根目录、关键路径或通配全部："+p)
		}
	}
}

func findHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	root := ""
	if p := positionals(args); len(p) > 0 && !strings.HasPrefix(p[0], "(") && !strings.HasPrefix(p[0], "!") {
		root = p[0]
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-delete":
			c.add(CatDestructive, "find -delete 批量删除："+full)
			if root == "" || criticalPath(root) {
				c.add(CatCritical, "在根目录或关键路径下批量删除："+root)
			}
		case "-exec", "-execdir", "-ok", "-okdir":
			j := i + 1
			for j < len(args) && args[j] != ";" && args[j] != "+" && args[j] != "\\;" {
				j++
			}
			inner := args[i+1 : j]
			for k := range inner {
				inner[k] = strings.ReplaceAll(inner[k], "{}", "FILE")
			}
			if len(inner) == 0 {
				c.add(CatObfuscation, "find -exec 缺少命令")
				return
			}
			before := len(c.findings)
			c.check(quote(inner), depth+1)
			if len(c.findings) > before && (root == "" || criticalPath(root)) {
				c.add(CatCritical, "对根目录或关键路径下的每个文件执行："+strings.Join(inner, " "))
			}
			i = j
		case "-fprint", "-fprint0", "-fprintf", "-fls":
			if i+1 < len(args) && !scratchPath(args[i+1]) && !safeSinks[args[i+1]] {
				c.add(CatModify, "find 写入文件："+args[i+1])
			}
		}
	}
}

var sedExec = regexp.MustCompile(`(^|[;}\n]\s*)[0-9,$/!]*\s*[ew](\s|$)|/[gpIi0-9]*[ew]\s*($|[;}\s])`)

func sedHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		if a == "--in-place" || strings.HasPrefix(a, "--in-place=") || (len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsRune(a[1:], 'i')) {
			c.add(CatModify, "sed -i 原地修改文件："+full)
			c.criticalTargets(positionals(args)[1:])
			return
		}
	}
	for _, a := range positionals(args) {
		if sedExec.MatchString(a) {
			c.add(CatRemoteCode, "sed 脚本包含执行(e)或写文件(w)命令："+a)
			return
		}
		break
	}
}

var awkRisk = regexp.MustCompile(`system\s*\(|\|\s*getline|\|&|print[f]?[^;}]*[^<>=!]>{1,2}\s*["$a-zA-Z(]|print[f]?[^;}]*\|\s*"`)

func awkHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-f", "--file") || hasShort(args, 'f') && !hasArg(args, "-F") {
		c.add(CatRemoteCode, "awk 运行脚本文件："+full)
		return
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-F" || a == "-v" || a == "--assign" || a == "--field-separator" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		if awkRisk.MatchString(a) {
			c.add(CatRemoteCode, "awk 程序会执行命令或写文件："+a)
		}
		return
	}
}

func teeHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, p := range positionals(args) {
		if safeSinks[p] || scratchPath(p) {
			continue
		}
		if criticalPath(p) {
			c.add(CatCritical, "写入关键文件："+p)
		}
		c.add(CatModify, "tee 写入文件："+p)
	}
}

func outputFlag(short, long string) handler {
	return func(c *classifier, name string, args []string, full string, sc simple, depth int) {
		v := optValue(args, short, long)
		if v == "" || safeSinks[v] || scratchPath(v) {
			return
		}
		if criticalPath(v) {
			c.add(CatCritical, "写入关键文件："+v)
		}
		c.add(CatModify, name+" 写入文件："+v)
	}
}

func copyHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	p := positionals(args)
	if t := optValue(args, "-t", "--target-directory"); t != "" {
		p = append(p, t)
	}
	if len(p) >= 2 && scratchPath(p[len(p)-1]) {
		return
	}
	c.add(CatModify, "复制并可能覆盖文件："+full)
	if len(p) > 0 && criticalPath(p[len(p)-1]) {
		c.add(CatCritical, "复制目标是关键路径："+p[len(p)-1])
	}
}

func scratchOnly(cat string) handler {
	return func(c *classifier, name string, args []string, full string, sc simple, depth int) {
		p := positionals(args)
		ok := len(p) > 0
		for _, x := range p {
			if !scratchPath(x) {
				ok = false
			}
		}
		if !ok {
			c.add(cat, full)
		}
	}
}

func exportHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		if i := strings.IndexByte(a, '='); i > 0 && dangerousEnv[a[:i]] {
			c.add(CatObfuscation, "修改会改变命令行为的环境变量 "+a[:i])
		}
	}
}

func aliasHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		if strings.Contains(a, "=") {
			c.add(CatObfuscation, "定义别名可能改变后续命令："+a)
		}
	}
}

func dateHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-s", "--set") || hasShort(args, 's') {
		c.add(CatSystem, "修改系统时间："+full)
		return
	}
	for i, a := range args {
		if a == "-d" || a == "--date" || a == "-r" || a == "--reference" || a == "-f" || a == "--file" || a == "-I" {
			_ = i
			return
		}
	}
	for _, p := range positionals(args) {
		if !strings.HasPrefix(p, "+") {
			c.add(CatSystem, "修改系统时间："+full)
			return
		}
	}
}

func journalHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		for _, bad := range []string{"--vacuum-size", "--vacuum-time", "--vacuum-files", "--rotate", "--flush", "--sync", "--relinquish-var", "--smart-relinquish-var", "--setup-keys", "--update-catalog"} {
			if a == bad || strings.HasPrefix(a, bad+"=") {
				c.add(CatDestructive, "清理或轮转系统日志："+full)
				return
			}
		}
	}
}

func hostnameHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if len(positionals(args)) > 0 || hasArg(args, "-F", "--file", "-b", "--boot") {
		c.add(CatSystem, "修改主机名："+full)
	}
}

// allowIf is safe only when one of the listed read-only flags is present and
// there are no other subcommands beyond operands.
func allowIf(cat string, safe ...string) handler {
	return func(c *classifier, name string, args []string, full string, sc simple, depth int) {
		for _, a := range args {
			for _, s := range safe {
				if a == s {
					return
				}
			}
		}
		c.add(cat, full)
	}
}

func denyIf(cat string, bad ...string) handler {
	return func(c *classifier, name string, args []string, full string, sc simple, depth int) {
		for _, a := range args {
			for _, b := range bad {
				if a == b || strings.HasPrefix(b, "--") && strings.HasPrefix(a, b+"=") {
					c.add(cat, full)
					return
				}
			}
		}
	}
}

func noPositional(cat string) handler {
	return func(c *classifier, name string, args []string, full string, sc simple, depth int) {
		valued := map[string]bool{"-t": true, "-O": true}
		for i := 0; i < len(args); i++ {
			if valued[args[i]] {
				i++
				continue
			}
			if !strings.HasPrefix(args[i], "-") {
				c.add(cat, full)
				return
			}
		}
	}
}

// firstSub returns the first positional word, skipping options (and values of
// options listed in valued).
func firstSub(args []string, valued map[string]bool) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1], args[i+2:]
			}
			return "", nil
		}
		if strings.HasPrefix(a, "-") {
			if valued[a] {
				i++
			}
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

// bareSafe commands only print status when run without arguments.
var bareSafe = set("pm2", "supervisorctl", "hostnamectl", "timedatectl", "localectl", "loginctl", "ufw", "resolvectl", "fail2ban-client", "apt-mark", "snap", "flatpak", "npm", "yarn", "pnpm", "gem", "composer", "go", "cargo", "mysqladmin", "dmsetup", "zfs", "systemd-analyze", "cryptsetup", "7z", "apachectl", "apache2ctl", "caddy", "certbot")

// subAllow allows the listed subcommands; with no subcommand a leading
// read-only flag (also listed) is accepted.
func subAllow(cat string, safe ...string) handler {
	ok := set(safe...)
	return func(c *classifier, name string, args []string, full string, sc simple, depth int) {
		if len(args) == 0 {
			if !bareSafe[name] {
				c.add(cat, full)
			}
			return
		}
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				if ok[a] {
					return
				}
				continue
			}
			if ok[a] {
				if name == "npm" && a == "audit" && hasArg(args, "fix") || a == "config" && hasArg(args, "set", "delete", "edit", "unset", "add", "remove") {
					c.add(cat, full)
				}
				return
			}
			c.add(cat, full)
			return
		}
		c.add(cat, full)
	}
}

var systemctlRead = set("status", "show", "cat", "list-units", "list-unit-files", "list-sockets", "list-timers", "list-jobs", "list-dependencies", "list-machines", "list-automounts", "list-paths", "is-active", "is-enabled", "is-failed", "is-system-running", "get-default", "show-environment", "help", "--version", "-h", "--help")
var systemctlHard = set("reboot", "poweroff", "halt", "kexec", "emergency", "rescue", "isolate", "default", "suspend", "hibernate", "hybrid-sleep", "soft-reboot", "switch-root")

func systemctlHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-H", "--host", "-M", "--machine") {
		c.add(CatNetwork, "操作其他主机或容器上的服务："+full)
	}
	sub, _ := firstSub(args, map[string]bool{"-t": true, "--type": true, "-n": true, "--lines": true, "-o": true, "--output": true, "-p": true, "--property": true, "-s": true, "--signal": true, "--state": true, "-H": true, "--host": true, "-M": true, "--machine": true, "--root": true, "--kill-whom": true, "--job-mode": true})
	if sub == "" {
		for _, a := range args {
			if a == "--version" || a == "-h" || a == "--help" {
				return
			}
		}
		return // bare systemctl lists units
	}
	if systemctlRead[sub] {
		return
	}
	if systemctlHard[sub] {
		c.add(CatSystem, "关机、重启或切换系统运行级别："+full)
		return
	}
	c.add(CatSystem, "改变服务或系统状态："+full)
	if sub == "stop" || sub == "disable" || sub == "mask" || sub == "kill" || sub == "restart" {
		for _, p := range positionals(args)[1:] {
			l := strings.ToLower(p)
			if strings.HasPrefix(l, "ssh") || strings.Contains(l, "network") || strings.HasPrefix(l, "systemd-") || strings.Contains(l, "firewall") || strings.Contains(l, "docker") {
				c.add(CatCritical, "影响远程访问或核心服务："+p)
			}
		}
	}
}

func serviceHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "--status-all", "--version", "-h", "--help") {
		return
	}
	p := positionals(args)
	if len(p) >= 2 && (p[1] == "status") {
		return
	}
	c.add(CatSystem, "改变服务状态："+full)
	if len(p) > 0 && strings.HasPrefix(strings.ToLower(p[0]), "ssh") {
		c.add(CatCritical, "影响远程访问："+p[0])
	}
}

var dockerRead = set("ps", "images", "logs", "inspect", "stats", "info", "version", "top", "port", "diff", "history", "events", "search", "--version", "-v")
var dockerMgmtRead = set("ls", "list", "ps", "inspect", "logs", "df", "top", "port", "diff", "history", "stats", "version", "images", "show", "events", "info", "config", "cat")
var dockerMgmt = set("container", "image", "network", "volume", "system", "context", "node", "service", "stack", "secret", "config", "plugin", "buildx", "manifest", "swarm", "compose", "builder", "trust")

func dockerHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	valued := map[string]bool{"-H": true, "--host": true, "--context": true, "-c": true, "--config": true, "-l": true, "--log-level": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true}
	sub, rest := firstSub(args, valued)
	if sub == "" {
		if hasArg(args, "--version", "-v", "--help", "-h") || len(args) == 0 {
			return
		}
		c.add(CatContainer, full)
		return
	}
	if dockerRead[sub] {
		return
	}
	if sub == "exec" {
		dockerExec(c, name, rest, full, depth)
		return
	}
	if dockerMgmt[sub] {
		cvalued := map[string]bool{"-f": true, "--file": true, "-p": true, "--project-name": true, "--project-directory": true, "--profile": true, "--env-file": true, "--filter": true, "--format": true}
		sub2, rest2 := firstSub(rest, cvalued)
		if sub == "compose" && sub2 == "exec" {
			// docker compose exec [opts] SERVICE CMD...
			dockerExec(c, name, rest2, full, depth)
			return
		}
		if sub == "container" && sub2 == "exec" {
			dockerExec(c, name, rest2, full, depth)
			return
		}
		if dockerMgmtRead[sub2] && !(sub == "config" && sub2 != "ls" && sub2 != "inspect") {
			return
		}
		cat := CatContainer
		if sub2 == "rm" || sub2 == "prune" || sub2 == "down" && hasArg(rest2, "-v", "--volumes", "--rmi") {
			cat = CatDestructive
		}
		c.add(cat, "容器或编排变更："+full)
		return
	}
	cat := CatContainer
	switch sub {
	case "rm", "rmi", "kill":
		cat = CatDestructive
	case "run", "create":
		if hasArg(args, "--privileged", "--pid=host", "--net=host", "--network=host") || strings.Contains(full, "-v /:") || strings.Contains(full, "docker.sock") {
			c.add(CatCritical, "特权容器或挂载宿主机关键路径："+full)
		}
	case "cp":
		cat = CatModify
	}
	c.add(cat, "容器或编排变更："+full)
}

func dockerExec(c *classifier, name string, rest []string, full string, depth int) {
	valued := map[string]bool{"-u": true, "--user": true, "-w": true, "--workdir": true, "-e": true, "--env": true, "--env-file": true, "--detach-keys": true, "--index": true}
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--privileged" {
			c.add(CatCritical, "以特权方式进入容器："+full)
		}
		if !strings.HasPrefix(a, "-") {
			break
		}
		if valued[a] {
			i++
		}
		i++
	}
	if i+1 >= len(rest) {
		c.add(CatContainer, "进入容器交互会话："+full)
		return
	}
	inner := rest[i+1:]
	c.check(quote(inner), depth+1)
}

var kubectlRead = set("get", "describe", "logs", "top", "version", "explain", "api-resources", "api-versions", "cluster-info", "events", "diff", "wait")

func kubectlHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	valued := map[string]bool{"-n": true, "--namespace": true, "--context": true, "--kubeconfig": true, "-o": true, "--output": true, "-l": true, "--selector": true, "-c": true, "--container": true, "--cluster": true, "--user": true, "-s": true, "--server": true, "--field-selector": true, "--since": true, "--tail": true}
	sub, rest := firstSub(args, valued)
	if sub == "" {
		return
	}
	if kubectlRead[sub] {
		return
	}
	switch sub {
	case "config":
		s2, _ := firstSub(rest, nil)
		if s2 == "view" || s2 == "get-contexts" || s2 == "current-context" || s2 == "get-clusters" || s2 == "get-users" {
			return
		}
	case "auth":
		if s2, _ := firstSub(rest, nil); s2 == "can-i" || s2 == "whoami" {
			return
		}
	case "rollout":
		if s2, _ := firstSub(rest, nil); s2 == "status" || s2 == "history" {
			return
		}
	case "exec":
		for i, a := range rest {
			if a == "--" && i+1 < len(rest) {
				c.check(quote(rest[i+1:]), depth+1)
				return
			}
		}
		c.add(CatContainer, "进入 Pod 交互会话："+full)
		return
	}
	cat := CatContainer
	if sub == "delete" || sub == "drain" {
		cat = CatDestructive
	}
	c.add(cat, "Kubernetes 变更："+full)
}

var gitRead = set("status", "log", "diff", "show", "rev-parse", "ls-files", "ls-tree", "ls-remote", "describe", "blame", "shortlog", "cat-file", "grep", "version", "help", "count-objects", "whatchanged", "rev-list", "name-rev", "for-each-ref", "show-ref", "check-ignore", "merge-base", "var", "--version")

func gitHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for i, a := range args {
		if a == "-c" || strings.HasPrefix(a, "--config-env") || strings.HasPrefix(a, "--exec-path") {
			c.add(CatObfuscation, "git -c 可以改写执行的程序："+full)
			return
		}
		if !strings.HasPrefix(a, "-") && (i == 0 || args[i-1] != "-C") {
			break
		}
	}
	sub, rest := firstSub(args, map[string]bool{"-C": true, "--git-dir": true, "--work-tree": true, "--namespace": true})
	if sub == "" || gitRead[sub] {
		return
	}
	switch sub {
	case "branch", "tag":
		if !hasArg(rest, "-d", "-D", "--delete", "-m", "-M", "--move", "-c", "-C", "--copy", "-f", "--force", "-u", "--set-upstream-to", "--unset-upstream", "-a", "-s", "--annotate", "--sign") && (sub == "branch" || len(positionals(rest)) == 0 || hasArg(rest, "-l", "--list")) {
			return
		}
	case "remote":
		if p := positionals(rest); len(p) == 0 || p[0] == "show" || p[0] == "get-url" {
			return
		}
	case "config":
		if hasArg(rest, "--get", "--get-all", "--list", "-l", "--get-regexp", "--show-origin", "--get-urlmatch") {
			return
		}
	case "stash":
		if p := positionals(rest); len(p) > 0 && (p[0] == "list" || p[0] == "show") {
			return
		}
	case "worktree":
		if p := positionals(rest); len(p) > 0 && p[0] == "list" {
			return
		}
	case "reflog":
		if p := positionals(rest); len(p) == 0 || p[0] == "show" {
			return
		}
	}
	cat := CatModify
	if sub == "clean" || sub == "reset" && hasArg(rest, "--hard") || sub == "push" && (hasArg(rest, "-f", "--force", "--force-with-lease", "--delete", "--mirror") || hasShort(rest, 'f')) || sub == "checkout" && hasArg(rest, "--", ".", "-f", "--force") {
		cat = CatDestructive
	}
	if sub == "clone" || sub == "fetch" || sub == "pull" || sub == "push" || sub == "submodule" {
		c.add(CatNetwork, "git 访问远程仓库："+full)
	}
	c.add(cat, "git 变更："+full)
}

var aptRead = set("list", "show", "search", "policy", "depends", "rdepends", "showsrc", "changelog", "madison", "--version", "-v", "help", "check")

func aptHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-s", "--simulate", "--dry-run", "--just-print", "--no-act", "--recon") {
		return
	}
	sub, _ := firstSub(args, map[string]bool{"-o": true, "-c": true, "-t": true, "--target-release": true})
	if sub == "" {
		if hasArg(args, "--version", "-v", "-h", "--help") {
			return
		}
	}
	if aptRead[sub] {
		return
	}
	c.add(CatPackage, "软件包变更（安装、删除、升级或刷新索引）："+full)
	if sub == "remove" || sub == "purge" || sub == "autoremove" {
		for _, p := range positionals(args)[1:] {
			l := strings.ToLower(p)
			if strings.Contains(l, "ssh") || strings.Contains(l, "sudo") || strings.Contains(l, "systemd") || strings.HasPrefix(l, "libc") || strings.Contains(l, "kernel") || strings.Contains(l, "linux-image") || l == "*" {
				c.add(CatCritical, "删除关键软件包："+p)
			}
		}
	}
}

func dpkgHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	safe := set("-l", "-L", "-s", "-S", "-p", "--list", "--listfiles", "--status", "--search", "--print-avail", "--print-architecture", "--print-foreign-architectures", "--get-selections", "-c", "--contents", "-I", "--info", "--audit", "-C", "--verify", "-V", "--compare-versions", "--version", "--help", "-x")
	for _, a := range args {
		if safe[a] {
			if a == "-x" {
				break
			}
			return
		}
	}
	c.add(CatPackage, "dpkg 安装或删除软件包："+full)
}

func rpmHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		if strings.HasPrefix(a, "-q") || a == "--query" || a == "-V" || a == "--verify" || a == "--querytags" || a == "--version" || a == "--showrc" || a == "--eval" || a == "-K" || a == "--checksig" {
			return
		}
	}
	c.add(CatPackage, "rpm 安装或删除软件包："+full)
}

func pacmanHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		if strings.HasPrefix(a, "-Q") || a == "-Ss" || a == "-Si" || a == "-Sl" || a == "-Sg" || a == "-Sii" || strings.HasPrefix(a, "-F") && !strings.Contains(a, "y") || a == "--version" || a == "-V" {
			return
		}
	}
	c.add(CatPackage, "pacman 安装、删除或升级软件包："+full)
}

func pipHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	pkgManager(c, name, args, full, sc, depth)
}

func pkgManager(c *classifier, name string, args []string, full string, sc simple, depth int) {
	sub, rest := firstSub(args, nil)
	switch sub {
	case "list", "show", "freeze", "check", "search", "index", "inspect", "debug", "help", "download":
		if sub == "download" {
			break
		}
		return
	case "config":
		if p := positionals(rest); len(p) > 0 && (p[0] == "list" || p[0] == "get" || p[0] == "debug") {
			return
		}
	case "":
		if hasArg(args, "--version", "-V", "--help", "-h") {
			return
		}
	}
	c.add(CatPackage, "安装、删除或升级 Python 包："+full)
}

var sqlWrite = regexp.MustCompile(`(?i)\b(insert|update|delete|drop|truncate|alter|create|grant|revoke|replace|merge|rename|call|exec|execute|load|copy|vacuum|reindex|cluster|lock|into|outfile|dumpfile|shutdown|kill|flush|reset|purge|handler|install|uninstall|import|attach|detach|pragma|refresh|comment|discard|reassign|security|savepoint|release|rollback|commit|begin|start|prepare|deallocate|listen|notify|unlisten|set\s+global|set\s+persist|pg_terminate_backend|pg_cancel_backend|pg_reload_conf|pg_read_file|pg_read_binary_file|pg_ls_dir|lo_import|lo_export|dblink|load_file|sys_exec|sys_eval|xp_cmdshell)\b`)
var sqlReadStart = regexp.MustCompile(`(?i)^\s*(select|show|describe|desc|explain|with|values|table|help|status|\\[dlnpz][a-zA-Z+]*|\\conninfo|\\x|\\timing|\\q)\b`)

// readOnlySQL accepts statements that only read: SELECT, SHOW, DESCRIBE,
// EXPLAIN (without ANALYZE of writes) and psql describe meta-commands.
func readOnlySQL(q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	if strings.Contains(q, `\!`) || strings.Contains(q, `\o`) || strings.Contains(q, `\w`) || strings.Contains(q, `\i`) || strings.Contains(q, `\g`) || strings.Contains(q, `\copy`) || strings.Contains(q, `\set`) || strings.Contains(q, "system ") {
		return false
	}
	if sqlWrite.MatchString(q) {
		return false
	}
	if regexp.MustCompile(`(?i)for\s+(update|share)|explain\s+analy[sz]e`).MatchString(q) {
		return false
	}
	for _, stmt := range strings.Split(q, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if !sqlReadStart.MatchString(stmt) {
			return false
		}
	}
	return true
}

func sqlClient(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "--version", "-V", "--help", "-l", "--list", "-?") && !hasArg(args, "-c", "-e", "--command", "--execute") {
		return
	}
	var queries []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-e" || a == "-c" || a == "--execute" || a == "--command":
			if i+1 < len(args) {
				queries = append(queries, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--execute=") || strings.HasPrefix(a, "--command="):
			queries = append(queries, a[strings.IndexByte(a, '=')+1:])
		case a == "-f" || a == "--file" || strings.HasPrefix(a, "--file=") || a == "--init-command" || strings.HasPrefix(a, "--init-command="):
			c.add(CatDatabase, "执行 SQL 文件或初始化命令（内容未经审查）："+full)
			return
		case (name == "mysql" || name == "mariadb") && strings.HasPrefix(a, "-e") && len(a) > 2:
			queries = append(queries, a[2:])
		}
	}
	if len(queries) == 0 {
		if sc.Stdin || sc.Piped {
			c.add(CatDatabase, "通过标准输入执行 SQL（内容未经审查）："+full)
		} else {
			c.add(CatDatabase, "打开交互式数据库会话："+full)
		}
		return
	}
	for _, q := range queries {
		if !readOnlySQL(q) {
			c.add(CatDatabase, "数据库写入或结构变更："+q)
			return
		}
	}
}

func sqliteHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	p := positionals(args)
	if hasArg(args, "-version", "--version", "-help") {
		return
	}
	if v := optValue(args, "-cmd", ""); v != "" {
		p = append(p, v)
	}
	if len(p) < 2 {
		if sc.Stdin || sc.Piped {
			c.add(CatDatabase, "通过标准输入执行 SQL（内容未经审查）："+full)
		} else {
			c.add(CatDatabase, "打开交互式数据库会话："+full)
		}
		return
	}
	for _, q := range p[1:] {
		t := strings.TrimSpace(q)
		if strings.HasPrefix(t, ".") {
			f := strings.Fields(t)[0]
			if !set(".tables", ".schema", ".indexes", ".indices", ".headers", ".mode", ".dump", ".databases", ".show", ".fullschema", ".dbinfo", ".width", ".help", ".timer", ".stats", ".eqp")[f] {
				c.add(CatDatabase, "sqlite 命令可写文件或执行程序："+t)
				return
			}
			continue
		}
		if !readOnlySQL(t) {
			c.add(CatDatabase, "数据库写入或结构变更："+t)
			return
		}
	}
}

var redisRead = set("PING", "INFO", "GET", "MGET", "STRLEN", "EXISTS", "TYPE", "TTL", "PTTL", "KEYS", "SCAN", "HGET", "HGETALL", "HKEYS", "HVALS", "HLEN", "HSCAN", "HMGET", "HEXISTS", "HSTRLEN", "LRANGE", "LLEN", "LINDEX", "SMEMBERS", "SCARD", "SISMEMBER", "SMISMEMBER", "SSCAN", "SRANDMEMBER", "ZRANGE", "ZRANGEBYSCORE", "ZREVRANGE", "ZREVRANGEBYSCORE", "ZCARD", "ZSCORE", "ZSCAN", "ZRANK", "ZREVRANK", "ZCOUNT", "DBSIZE", "LASTSAVE", "TIME", "ROLE", "XRANGE", "XREVRANGE", "XLEN", "XINFO", "XPENDING", "PUBSUB", "COMMAND", "ECHO", "GETRANGE", "BITCOUNT", "PFCOUNT", "GEOPOS", "GEODIST", "OBJECT", "DUMP", "RANDOMKEY", "LPOS", "SINTER", "SUNION", "SDIFF", "ZRANGEBYLEX", "HRANDFIELD", "ZRANDMEMBER", "SUBSTR", "LOLWUT")
var redisSubRead = map[string]map[string]bool{"CLIENT": set("LIST", "INFO", "GETNAME", "ID"), "CONFIG": set("GET"), "SLOWLOG": set("GET", "LEN"), "MEMORY": set("USAGE", "STATS", "DOCTOR", "MALLOC-STATS"), "LATENCY": set("LATEST", "HISTORY", "DOCTOR", "GRAPH"), "CLUSTER": set("INFO", "NODES", "SLOTS", "SHARDS", "MYID", "KEYSLOT", "COUNTKEYSINSLOT"), "MODULE": set("LIST"), "ACL": set("WHOAMI", "LIST", "USERS", "CAT", "LOG"), "SENTINEL": set("MASTERS", "MASTER", "REPLICAS", "SLAVES", "SENTINELS", "GET-MASTER-ADDR-BY-NAME", "CKQUORUM", "INFO-CACHE"), "FUNCTION": set("LIST", "STATS"), "SCRIPT": set("EXISTS")}

func redisHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	valued := map[string]bool{"-h": true, "-p": true, "-a": true, "-n": true, "-u": true, "-s": true, "--user": true, "--pass": true, "-r": true, "-i": true, "--pattern": true, "-d": true, "--count": true, "--cacert": true, "--cert": true, "--key": true, "--sni": true}
	for _, a := range args {
		switch a {
		case "--rdb", "--functions-rdb":
			c.add(CatModify, "redis-cli 导出数据到文件："+full)
			return
		case "--pipe", "--eval", "-x":
			c.add(CatDatabase, "redis-cli 批量写入或执行脚本："+full)
			return
		case "--scan", "--bigkeys", "--memkeys", "--hotkeys", "--stat", "--latency", "--latency-history", "--latency-dist", "--version", "-v", "--help", "--intrinsic-latency":
			return
		}
	}
	cmd, rest := firstSub(args, valued)
	if cmd == "" {
		if sc.Stdin || sc.Piped {
			c.add(CatDatabase, "通过标准输入执行 Redis 命令："+full)
		} else {
			c.add(CatDatabase, "打开交互式 Redis 会话："+full)
		}
		return
	}
	up := strings.ToUpper(cmd)
	if redisRead[up] {
		return
	}
	if sub, ok := redisSubRead[up]; ok && len(rest) > 0 && sub[strings.ToUpper(rest[0])] {
		return
	}
	cat := CatDatabase
	if up == "FLUSHALL" || up == "FLUSHDB" || up == "DEL" || up == "UNLINK" || up == "SHUTDOWN" {
		cat = CatDestructive
	}
	c.add(cat, "Redis 写入或管理命令："+full)
}

var mongoRead = regexp.MustCompile(`^\s*(printjson\()?\s*(db\.(\w+\.)?(find|findOne|count|countDocuments|estimatedDocumentCount|distinct|stats|getIndexes|aggregate|explain|dataSize|storageSize|totalSize|totalIndexSize|getCollectionNames|getCollectionInfos|getName|serverStatus|version|hello|isMaster|currentOp|getUsers|getRoles|adminCommand\(\s*\{\s*(listDatabases|serverStatus|getLog|getCmdLineOpts|hostInfo|buildInfo|replSetGetStatus|ping))|rs\.(status|conf|printReplicationInfo|printSecondaryReplicationInfo)|sh\.status|show\s)`)
var mongoWrite = regexp.MustCompile(`(?i)(drop|remove|delete|update|insert|replace|rename|create|shutdown|\$out|\$merge|bulkWrite|findAndModify|findOneAnd|eval|setParameter|fsync|kill|compact|repair|grant|revoke|reconfig|stepDown|addShard|enableSharding|load\(|run\(|runProgram|_runMongoProgram|copyDatabase|cloneCollection)`)

func mongoHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "--version", "--help", "-h") {
		return
	}
	js := optValue(args, "--eval", "")
	if js == "" {
		if sc.Stdin || sc.Piped || len(positionals(args)) > 1 {
			c.add(CatDatabase, "通过脚本或标准输入执行 MongoDB 命令："+full)
		} else {
			c.add(CatDatabase, "打开交互式 MongoDB 会话："+full)
		}
		return
	}
	if mongoWrite.MatchString(js) || !mongoRead.MatchString(js) {
		c.add(CatDatabase, "MongoDB 写入或无法确认只读的脚本："+js)
	}
}

func crontabHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-r") || hasShort(args, 'r') {
		c.add(CatDestructive, "删除全部计划任务：crontab -r")
		c.add(CatSchedule, full)
		return
	}
	if hasArg(args, "-l") {
		return
	}
	c.add(CatSchedule, "修改计划任务："+full)
}

func iptablesHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	read := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-t" || a == "--table" {
			i++
			continue
		}
		switch a {
		case "--list", "--list-rules", "--check", "-L", "-S", "-C", "--version", "-V", "--help", "-h":
			read = true
			continue
		case "--append", "--insert", "--delete", "--flush", "--delete-chain", "--policy", "--new-chain", "--rename-chain", "--replace", "--zero":
			c.add(CatNetwork, "修改防火墙规则："+full)
			return
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			if strings.ContainsAny(a[1:], "AIDFXPNERZ") {
				c.add(CatNetwork, "修改防火墙规则："+full)
				return
			}
			if strings.ContainsAny(a[1:], "LSC") {
				read = true
			}
		}
	}
	if !read {
		c.add(CatNetwork, "修改防火墙规则："+full)
	}
}

func nftHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-c", "--check", "-v", "--version", "-h", "--help") {
		return
	}
	sub, _ := firstSub(args, map[string]bool{"-f": true, "--file": true, "-I": true, "--includepath": true})
	if sub == "list" || sub == "describe" || sub == "monitor" {
		return
	}
	c.add(CatNetwork, "修改 nftables 规则："+full)
}

func firewallCmdHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	read := false
	for _, a := range args {
		for _, w := range []string{"--add-", "--remove-", "--set-", "--reload", "--complete-reload", "--runtime-to-permanent", "--new-", "--delete-", "--change-", "--panic-on", "--lockdown-on", "--direct", "--reset-", "--load-"} {
			if strings.HasPrefix(a, w) {
				c.add(CatNetwork, "修改防火墙规则："+full)
				return
			}
		}
		if strings.HasPrefix(a, "--list-") || strings.HasPrefix(a, "--get-") || strings.HasPrefix(a, "--query-") || strings.HasPrefix(a, "--info-") || a == "--state" || a == "--version" || a == "--check-config" {
			read = true
		}
	}
	if !read {
		c.add(CatNetwork, "防火墙操作："+full)
	}
}

var ipObjects = set("a", "ad", "add", "addr", "addre", "addres", "address", "l", "li", "lin", "link", "r", "ro", "rou", "rout", "route", "ru", "rul", "rule", "n", "ne", "nei", "neigh", "neighbor", "neighbour", "maddr", "maddress", "mroute", "tunnel", "tun", "xfrm", "netns", "ntable", "tcp_metrics", "token", "netconf", "mptcp", "nexthop", "vrf", "sr", "ioam", "macsec", "tuntap", "l2tp", "fou", "ila", "stats")
var ipReadVerbs = set("show", "sh", "s", "list", "lst", "ls", "l", "get", "g", "help", "identify", "pids")

func ipHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	valued := map[string]bool{"-n": true, "-netns": true, "--netns": true, "-f": true, "-family": true, "-b": true, "-batch": true, "-rc": true, "-rcvbuf": true}
	if hasArg(args, "-b", "-batch", "--batch", "-force") {
		c.add(CatNetwork, "批量修改网络配置："+full)
		return
	}
	obj, rest := firstSub(args, valued)
	if obj == "" || obj == "monitor" || obj == "help" {
		return
	}
	if !ipObjects[obj] {
		c.add(CatNetwork, "ip 子命令无法确认只读："+full)
		return
	}
	if obj == "netns" {
		if p := positionals(rest); len(p) > 0 && p[0] == "exec" {
			c.add(CatNetwork, "在网络命名空间中执行命令："+full)
			return
		}
	}
	verb, _ := firstSub(rest, nil)
	if verb == "" || ipReadVerbs[verb] {
		return
	}
	// Words like "dev eth0" follow show; anything else is a change.
	c.add(CatNetwork, "修改网络配置："+full)
	if verb == "del" || verb == "delete" || verb == "flush" || verb == "down" || strings.Contains(full, " down") {
		c.add(CatCritical, "可能断开当前远程连接："+full)
	}
}

func ifconfigHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	p := positionals(args)
	if len(p) <= 1 && !hasArg(args, "down", "up") {
		return
	}
	c.add(CatNetwork, "修改网卡配置："+full)
}

func nmcliHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range positionals(args) {
		switch a {
		case "up", "down", "modify", "mod", "add", "delete", "del", "connect", "disconnect", "reload", "load", "import", "set", "on", "off", "clone", "edit", "reapply", "wifi-connect":
			c.add(CatNetwork, "修改网络连接："+full)
			return
		}
	}
}

func ethtoolHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		switch a {
		case "-s", "-K", "-G", "-A", "-C", "-L", "-r", "-E", "-f", "-W", "-X", "-N", "-U", "--change", "--reset", "--flash", "--offload", "--set-ring", "--pause", "--coalesce", "--set-channels", "--negotiate", "--set-eee", "--set-fec", "--set-priv-flags", "--set-rxfh-indir", "--config-nfc", "--config-ntuple":
			c.add(CatNetwork, "修改网卡参数："+full)
			return
		}
	}
}

func tcHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range positionals(args) {
		switch a {
		case "add", "del", "delete", "change", "replace", "link", "exec":
			c.add(CatNetwork, "修改流量控制规则："+full)
			return
		}
	}
}

func sysctlHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-w", "--write", "-p", "--load", "--system") {
		c.add(CatSystem, "修改内核参数："+full)
		return
	}
	for _, a := range args {
		if strings.Contains(a, "=") {
			c.add(CatSystem, "修改内核参数："+full)
			return
		}
	}
}

func curlHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	valued := "ToFdKXcDHuAeEmrwxbCQtUYyzhP"
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		if strings.HasPrefix(a, "--") {
			key, val, hasVal := strings.Cut(a, "=")
			if !hasVal {
				val = next
			}
			switch key {
			case "--output", "--cookie-jar", "--dump-header", "--trace", "--trace-ascii", "--stderr", "--libcurl", "--etag-save", "--hsts", "--alt-svc":
				if !safeSinks[val] && !scratchPath(val) {
					c.add(CatModify, "curl 写入文件："+val)
				}
			case "--remote-name", "--remote-name-all", "--create-dirs", "--output-dir":
				c.add(CatModify, "curl 下载保存文件："+full)
			case "--upload-file", "--data", "--data-ascii", "--data-binary", "--data-raw", "--data-urlencode", "--form", "--form-string", "--json":
				c.add(CatNetwork, "curl 上传或提交数据："+full)
			case "--request":
				m := strings.ToUpper(val)
				if m != "GET" && m != "HEAD" && m != "OPTIONS" {
					c.add(CatNetwork, "curl 发送 "+m+" 请求："+full)
				}
			case "--config":
				c.add(CatObfuscation, "curl 从配置文件读取参数："+full)
			case "--unix-socket", "--abstract-unix-socket":
				if strings.Contains(val, "docker") || strings.Contains(val, "containerd") {
					c.add(CatContainer, "通过 Docker 套接字直接调用 API："+full)
				}
			}
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			if l := strings.ToLower(a); strings.HasPrefix(l, "file:") || strings.HasPrefix(l, "gopher:") || strings.HasPrefix(l, "dict:") || strings.HasPrefix(l, "ldap") || strings.HasPrefix(l, "telnet:") || strings.HasPrefix(l, "tftp:") || strings.HasPrefix(l, "scp:") || strings.HasPrefix(l, "sftp:") || strings.HasPrefix(l, "smtp") {
				c.add(CatNetwork, "curl 使用非 HTTP 协议："+a)
			}
			continue
		}
		for j := 1; j < len(a); j++ {
			ch := a[j]
			if strings.IndexByte(valued, ch) < 0 {
				if ch == 'O' {
					c.add(CatModify, "curl 下载保存文件："+full)
				}
				continue
			}
			val := a[j+1:]
			if val == "" {
				val = next
				i++
			}
			switch ch {
			case 'o', 'c', 'D':
				if !safeSinks[val] && !scratchPath(val) {
					c.add(CatModify, "curl 写入文件："+val)
				}
			case 'T', 'd', 'F':
				c.add(CatNetwork, "curl 上传或提交数据："+full)
			case 'X':
				m := strings.ToUpper(val)
				if m != "GET" && m != "HEAD" && m != "OPTIONS" {
					c.add(CatNetwork, "curl 发送 "+m+" 请求："+full)
				}
			case 'K':
				c.add(CatObfuscation, "curl 从配置文件读取参数："+full)
			}
			break
		}
	}
}

func wgetHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	stdout := hasArg(args, "--spider", "--version", "-V", "--help", "-h")
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		key, val, hasVal := strings.Cut(a, "=")
		if !hasVal {
			val = next
		}
		switch key {
		case "--post-data", "--post-file", "--body-data", "--body-file":
			c.add(CatNetwork, "wget 提交数据："+full)
		case "--method":
			if m := strings.ToUpper(val); m != "GET" && m != "HEAD" {
				c.add(CatNetwork, "wget 发送 "+m+" 请求："+full)
			}
		case "--output-document":
			if safeSinks[val] || scratchPath(val) {
				stdout = true
			}
		case "--output-file", "--append-output":
			if !safeSinks[val] && !scratchPath(val) {
				c.add(CatModify, "wget 写入日志文件："+val)
			}
		case "--recursive", "--mirror":
			c.add(CatModify, "wget 递归下载："+full)
		}
		if len(a) > 1 && a[0] == '-' && a[1] != '-' {
			if k := strings.IndexByte(a, 'O'); k > 0 {
				v := a[k+1:]
				if v == "" {
					v = next
				}
				if safeSinks[v] || scratchPath(v) {
					stdout = true
				}
			}
			if strings.ContainsAny(a[1:], "rm") {
				c.add(CatModify, "wget 递归下载："+full)
			}
		}
	}
	if !stdout {
		c.add(CatModify, "wget 下载文件到磁盘："+full)
	}
}

func opensslHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	sub, rest := firstSub(args, nil)
	out := optValue(rest, "-out", "")
	writes := out != "" && !safeSinks[out] && !scratchPath(out)
	switch sub {
	case "version", "verify", "ciphers", "list", "errstr", "dgst", "sha1", "sha256", "sha512", "md5", "rand", "s_client", "crl", "x509", "req", "speed", "asn1parse", "base64", "enc", "ocsp", "pkcs7", "storeutl", "help", "":
		if sub == "req" && (hasArg(rest, "-new", "-newkey", "-x509") || !hasArg(rest, "-noout", "-text", "-verify")) {
			c.add(CatModify, "生成证书请求或证书："+full)
			return
		}
		if (sub == "enc") && hasArg(rest, "-e") {
			c.add(CatModify, "openssl 加密数据："+full)
			return
		}
		if writes {
			c.add(CatModify, "openssl 写入文件："+out)
		}
	case "rsa", "pkey", "ec", "dsa", "pkcs12":
		if !hasArg(rest, "-noout") || hasArg(rest, "-text") && !hasArg(rest, "-pubout") {
			c.add(CatSecretRead, "openssl 输出或转换私钥："+full)
		}
		if writes {
			c.add(CatModify, "openssl 写入文件："+out)
		}
	case "s_server":
		c.add(CatNetwork, "openssl 启动监听服务："+full)
	default:
		c.add(CatModify, "openssl 生成密钥或写入文件："+full)
	}
}

func ncHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-e", "-c", "--exec", "--sh-exec", "--lua-exec", "-l", "--listen") || hasShort(args, 'l') || hasShort(args, 'e') {
		c.add(CatNetwork, "打开网络监听或反弹 shell："+full)
		return
	}
	if hasShort(args, 'z') && !sc.Piped && !sc.Stdin {
		return
	}
	c.add(CatNetwork, "通过网络发送数据："+full)
}

func tarHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	mode := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		mode = args[0]
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			mode += a[1:]
		}
	}
	long := func(names ...string) bool { return hasArg(args, names...) }
	if long("--delete") {
		c.add(CatDestructive, "tar 删除归档内容："+full)
		return
	}
	if strings.ContainsAny(mode, "x") || long("--extract", "--get") {
		c.add(CatModify, "解压并可能覆盖文件："+full)
		if dir := optValue(args, "-C", "--directory"); criticalPath(dir) {
			c.add(CatCritical, "解压到关键路径："+dir)
		}
		return
	}
	if strings.ContainsAny(mode, "t") || long("--list", "--diff", "--compare") || strings.ContainsAny(mode, "d") {
		return
	}
	if strings.ContainsAny(mode, "cruA") || long("--create", "--append", "--update", "--concatenate") {
		f := optValue(args, "-f", "--file")
		if f == "" {
			// "tar czf FILE" form: file follows the mode word.
			if p := positionals(args); len(p) > 1 && strings.ContainsRune(mode, 'f') {
				f = p[1]
			}
		}
		if f == "" || safeSinks[f] || scratchPath(f) {
			return
		}
		c.add(CatModify, "创建归档文件："+f)
		return
	}
	if long("--version", "--help") {
		return
	}
	c.add(CatModify, full)
}

func compressHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-c", "--stdout", "--to-stdout", "-l", "--list", "-t", "--test", "-V", "--version", "-h", "--help") || hasShort(args, 'c') || hasShort(args, 't') || hasShort(args, 'l') {
		return
	}
	if len(positionals(args)) == 0 {
		return // stdin to stdout
	}
	c.add(CatModify, "压缩或解压会替换原文件："+full)
}

func smartctlHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-t", "--test", "-X", "--abort", "-s", "--smart", "-o", "--offlineauto", "-S", "--saveauto", "--set") {
		c.add(CatDisk, "修改磁盘 SMART 设置或启动自检："+full)
	}
}

func partedHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	if hasArg(args, "-l", "--list", "-v", "--version") {
		return
	}
	p := positionals(args)
	if len(p) == 2 && p[1] == "print" {
		return
	}
	c.add(CatDisk, "修改分区表："+full)
}

func btrfsHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	p := positionals(args)
	if hasArg(args, "--version", "version") || len(p) >= 2 && (p[0] == "filesystem" || p[0] == "fi") && (p[1] == "show" || p[1] == "df" || p[1] == "usage" || p[1] == "du") || len(p) >= 2 && (p[0] == "subvolume" || p[0] == "sub") && (p[1] == "list" || p[1] == "show") || len(p) >= 2 && p[0] == "device" && p[1] == "stats" && !hasArg(args, "-z", "--reset") || len(p) >= 2 && (p[0] == "scrub" || p[0] == "balance") && p[1] == "status" {
		return
	}
	c.add(CatDisk, "btrfs 变更："+full)
}

func ddHandler(c *classifier, name string, args []string, full string, sc simple, depth int) {
	for _, a := range args {
		if strings.HasPrefix(a, "of=") {
			t := a[3:]
			if safeSinks[t] || scratchPath(t) {
				return
			}
			if strings.HasPrefix(t, "/dev/") {
				c.add(CatDisk, "dd 写入块设备："+t)
				c.add(CatCritical, "覆盖磁盘设备："+t)
				return
			}
			c.add(CatDestructive, "dd 覆盖文件："+t)
			if criticalPath(t) {
				c.add(CatCritical, "覆盖关键文件："+t)
			}
			return
		}
	}
}
