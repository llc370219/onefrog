package audit

import "strings"

type Decision struct {
	Allow  bool
	Reason string
}

func Decide(command string) Decision {
	command = strings.TrimSpace(command)
	if command == "" {
		return Decision{Reason: "空命令"}
	}
	low := strings.ToLower(command)
	if strings.Contains(low, "mkfs") {
		return Decision{Reason: "格式化磁盘"}
	}
	if fieldIs(low, "dd") && strings.Contains(low, "of=/dev/") {
		return Decision{Reason: "写入磁盘设备"}
	}
	for _, word := range []string{"shutdown", "reboot", "halt", "poweroff"} {
		if fieldIs(low, word) {
			return Decision{Reason: "关闭或重启机器"}
		}
	}
	if strings.Contains(command, ":(){") || strings.Contains(command, ":() {") {
		return Decision{Reason: "fork 炸弹"}
	}
	if strings.Contains(low, "chmod") && strings.Contains(low, "777") && strings.Contains(low, " /") && (strings.Contains(low, "-r") || strings.Contains(low, "-R")) {
		return Decision{Reason: "递归放宽根目录权限"}
	}
	if strings.Contains(low, ">/dev/sd") || strings.Contains(low, "> /dev/sd") {
		return Decision{Reason: "重定向到磁盘设备"}
	}
	if rmForceRecursive(low) {
		return Decision{Reason: "递归强制删除"}
	}
	return Decision{Allow: true}
}

func fieldIs(s, word string) bool {
	s = strings.NewReplacer(";", " ", "&", " ", "|", " ").Replace(s)
	for _, f := range strings.Fields(s) {
		if f == word {
			return true
		}
	}
	return false
}

func rmForceRecursive(low string) bool {
	if !fieldIs(low, "rm") {
		return false
	}
	recursive, force := false, false
	for _, f := range strings.Fields(low) {
		if strings.HasPrefix(f, "-") && !strings.HasPrefix(f, "--") {
			if strings.ContainsRune(f, 'r') {
				recursive = true
			}
			if strings.ContainsRune(f, 'f') {
				force = true
			}
		}
		if f == "--recursive" {
			recursive = true
		}
		if f == "--force" {
			force = true
		}
	}
	return recursive && force
}
