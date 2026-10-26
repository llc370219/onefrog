package onboarding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"onefrog/internal/id"
	"onefrog/internal/model"
	"onefrog/internal/monitor"
	"onefrog/internal/remote"
	"onefrog/internal/secret"
	"onefrog/internal/store"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Executor interface {
	Run(context.Context, string, int, string, string, string, string, string) (string, string, error)
}
type Request struct {
	RequestID  string   `json:"request_id"`
	ServerID   string   `json:"server_id"`
	Name       string   `json:"name"`
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	Username   string   `json:"username"`
	AuthType   string   `json:"auth_type"`
	Secret     string   `json:"secret"`
	Passphrase string   `json:"passphrase"`
	Role       string   `json:"role"`
	Configure  bool     `json:"configure"`
	ReportBase string   `json:"report_base"`
	Interval   int      `json:"interval"`
	Targets    []string `json:"targets"`
}
type Step struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}
type View struct {
	ID          string    `json:"id"`
	ServerID    string    `json:"server_id"`
	Status      string    `json:"status"`
	Steps       []Step    `json:"steps"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Message     string    `json:"message,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
type job struct {
	View
	Owner string `json:"owner"`
	// confirm receives the operator-confirmed host fingerprint while the job
	// waits in status fingerprint_required. Not persisted.
	confirm chan string
}

// FingerprintTimeout bounds how long a job waits for the operator to confirm
// the SSH host fingerprint.
var FingerprintTimeout = 10 * time.Minute

type Hook struct {
	ID         string
	Name       string
	Initialize func(context.Context, model.Server, func(string) (string, error)) error
}
type Manager struct {
	startMu       sync.Mutex
	mu            sync.Mutex
	db            store.Store
	box           *secret.Box
	monitor       *monitor.Manager
	exec          Executor
	path          string
	jobs          map[string]*job
	hooks         map[string]Hook
	VerifyTimeout time.Duration
}

func New(path string, db store.Store, box *secret.Box, m *monitor.Manager, e Executor) (*Manager, error) {
	v := &Manager{path: path, db: db, box: box, monitor: m, exec: e, jobs: map[string]*job{}, hooks: map[string]Hook{}, VerifyTimeout: 30 * time.Second}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err == nil {
			if err = json.Unmarshal(raw, &v.jobs); err != nil {
				return nil, err
			}
			for _, j := range v.jobs {
				if j.Status == "running" || j.Status == "fingerprint_required" {
					j.Status = "interrupted"
					j.Message = "服务已重启，请重新配置以继续"
					for i := range j.Steps {
						if j.Steps[i].Status == "running" {
							j.Steps[i].Status = "failed"
						}
					}
				}
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	return v, nil
}
func (m *Manager) Register(h Hook) { m.mu.Lock(); defer m.mu.Unlock(); m.hooks[h.ID] = h }
func (m *Manager) persistLocked() {
	if m.path == "" {
		return
	}
	raw, err := json.Marshal(m.jobs)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(m.path), 0700) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(m.path), ".onboarding-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return
	}
	if f.Close() == nil {
		_ = os.Rename(f.Name(), m.path)
	}
}
func (m *Manager) Get(owner, key string) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[key]
	if j == nil || j.Owner != owner {
		return View{}, store.ErrNotFound
	}
	v := j.View
	v.Steps = append([]Step{}, j.Steps...)
	return v, nil
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)

func Validate(r *Request) error {
	r.Name = strings.TrimSpace(r.Name)
	r.Host = strings.TrimSpace(r.Host)
	r.Username = strings.TrimSpace(r.Username)
	if r.Port == 0 {
		r.Port = 22
	}
	if r.Username == "" {
		r.Username = "root"
	}
	if r.AuthType == "" {
		r.AuthType = "password"
	}
	if r.Interval == 0 {
		r.Interval = 15
	}
	if r.Name == "" || len([]rune(r.Name)) > 80 {
		return errors.New("请填写 1–80 字的服务器名称")
	}
	if net.ParseIP(r.Host) == nil && !hostPattern.MatchString(r.Host) {
		return errors.New("请输入有效 IP 或主机名，不要包含端口和协议")
	}
	if r.Port < 1 || r.Port > 65535 {
		return errors.New("SSH 端口必须为 1–65535")
	}
	if len(r.Username) > 64 || strings.ContainsAny(r.Username, "\n\r\x00 ") {
		return errors.New("SSH 用户名无效")
	}
	if r.AuthType != "password" && r.AuthType != "private_key" {
		return errors.New("认证方式无效")
	}
	if len(r.Secret) > 256*1024 || len(r.Passphrase) > 4096 {
		return errors.New("凭据过大")
	}
	if r.Configure {
		u, err := url.Parse(strings.TrimRight(r.ReportBase, "/"))
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("请填写节点可访问的面板 HTTP(S) 地址")
		}
		hostIP := net.ParseIP(r.Host)
		panelIP := net.ParseIP(u.Hostname())
		if (u.Hostname() == "localhost" || panelIP != nil && panelIP.IsLoopback()) && (hostIP == nil || !hostIP.IsLoopback()) {
			return errors.New("远程节点无法访问本机 localhost，请填写面板的公网或内网地址")
		}
		if r.Interval < 10 || r.Interval > 300 {
			return errors.New("采集间隔为 10–300 秒")
		}
		if len(r.Targets) > 8 {
			return errors.New("最多 8 个线路检测目标")
		}
		for _, t := range r.Targets {
			if net.ParseIP(t) == nil && !hostPattern.MatchString(t) {
				return errors.New("线路目标仅支持 IP 或域名")
			}
		}
	}
	return nil
}
func (m *Manager) Start(owner string, r Request) (View, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if err := Validate(&r); err != nil {
		return View{}, err
	}
	if r.RequestID == "" {
		r.RequestID = id.New()
	}
	m.mu.Lock()
	if j := m.jobs[r.RequestID]; j != nil {
		m.mu.Unlock()
		return m.Get(owner, r.RequestID)
	}
	for _, j := range m.jobs {
		if r.ServerID != "" && j.ServerID == r.ServerID && (j.Status == "running" || j.Status == "fingerprint_required") {
			m.mu.Unlock()
			return View{}, errors.New("该服务器正在配置中")
		}
	}
	m.mu.Unlock()
	ctx := context.Background()
	var rec store.ServerRec
	var err error
	if r.ServerID != "" {
		rec, err = m.db.GetServer(ctx, r.ServerID)
		if err != nil {
			return View{}, err
		}
	}
	if r.Secret == "" && rec.EncryptedKey == "" {
		return View{}, errors.New("请输入密码或选择 SSH 私钥文件")
	}
	if r.Secret != "" {
		plain := r.Secret
		if r.AuthType == "private_key" && r.Passphrase != "" {
			b, _ := json.Marshal(map[string]string{"key": r.Secret, "passphrase": r.Passphrase})
			plain = "onefrog-ssh-v1:" + string(b)
		}
		rec.EncryptedKey, err = m.box.Encrypt(plain)
		if err != nil {
			return View{}, err
		}
	}
	if rec.Host != r.Host || rec.Port != r.Port {
		rec.HostKey, rec.ClearHostKey = "", true
	}
	rec.Name = r.Name
	rec.Host = r.Host
	rec.Port = r.Port
	rec.Username = r.Username
	rec.AuthType = r.AuthType
	rec.Role = r.Role
	rec.Status = "unknown"
	var server model.Server
	if rec.ID == "" {
		server, err = m.db.CreateServer(ctx, rec)
	} else {
		server, err = m.db.UpdateServer(ctx, rec)
	}
	if err != nil {
		return View{}, err
	}
	rec.Server = server
	v := View{ID: r.RequestID, ServerID: server.ID, Status: "running", CreatedAt: time.Now().UTC(), Steps: []Step{{ID: "credentials", Label: "加密保存登录凭据", Status: "done"}, {ID: "panel", Label: "加入服务器面板", Status: "done"}, {ID: "ssh", Label: "验证 SSH 连接", Status: "pending"}, {ID: "probe", Label: "安装并启动只读探针", Status: "pending"}, {ID: "modules", Label: "初始化已启用模块", Status: "pending"}, {ID: "verify", Label: "等待首次指标上报", Status: "pending"}}}
	m.mu.Lock()
	m.jobs[v.ID] = &job{View: v, Owner: owner, confirm: make(chan string, 1)}
	m.persistLocked()
	m.mu.Unlock()
	r.Secret = ""
	r.Passphrase = ""
	go m.run(v.ID, rec, r)
	return m.Get(owner, v.ID)
}

// Confirm resumes a job waiting in fingerprint_required. The fingerprint must
// match the one shown to the operator.
func (m *Manager) Confirm(owner, key, fingerprint string) (View, error) {
	m.mu.Lock()
	j := m.jobs[key]
	if j == nil || j.Owner != owner {
		m.mu.Unlock()
		return View{}, store.ErrNotFound
	}
	if j.Status != "fingerprint_required" || j.confirm == nil {
		m.mu.Unlock()
		return View{}, errors.New("当前任务不需要确认主机指纹")
	}
	if strings.TrimSpace(fingerprint) != j.Fingerprint {
		m.mu.Unlock()
		return View{}, errors.New("指纹与服务器提供的不一致")
	}
	select {
	case j.confirm <- j.Fingerprint:
	default:
	}
	m.mu.Unlock()
	return m.Get(owner, key)
}
func (m *Manager) step(key, stage, status, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[key]
	for i := range j.Steps {
		if j.Steps[i].ID == stage {
			j.Steps[i].Status = status
			j.Steps[i].Message = message
		}
	}
	m.persistLocked()
}
func (m *Manager) finish(key, status, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[key]
	j.Status = status
	j.Message = message
	for i := range j.Steps {
		if j.Steps[i].Status == "pending" {
			j.Steps[i].Status = "skipped"
		}
	}
	m.persistLocked()
}
func (m *Manager) run(key string, rec store.ServerRec, r Request) {
	if !r.Configure {
		for _, s := range []string{"ssh", "probe", "modules", "verify"} {
			m.step(key, s, "skipped", "已选择仅录入")
		}
		m.finish(key, "completed", "已加入面板；未连接或安装探针")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	m.step(key, "ssh", "running", "")
	if m.exec == nil {
		m.step(key, "ssh", "failed", "SSH 执行器不可用")
		m.finish(key, "failed", "服务器已保存，请配置执行器后重试")
		return
	}
	plain, err := m.box.Decrypt(rec.EncryptedKey)
	if err != nil {
		m.step(key, "ssh", "failed", "凭据无法解密")
		m.finish(key, "failed", "请重新录入凭据")
		return
	}
	if rec.HostKey == "" {
		// First contact: learn the presented fingerprint without sending
		// credentials, then wait for the operator to confirm it.
		_, fp, e := m.exec.Run(ctx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, "", "true")
		if !errors.Is(e, remote.ErrHostKeyUnknown) || fp == "" {
			m.step(key, "ssh", "failed", "连接失败：请检查地址和端口")
			rec.Status = "offline"
			_, _ = m.db.UpdateServer(ctx, rec)
			m.finish(key, "failed", "凭据已加密保存，可修改信息后重试")
			return
		}
		m.mu.Lock()
		j := m.jobs[key]
		j.Fingerprint = fp
		j.Status = "fingerprint_required"
		j.Message = "请核对服务器的 SSH 主机指纹，确认无误后继续"
		confirm := j.confirm
		m.persistLocked()
		m.mu.Unlock()
		m.step(key, "ssh", "running", "等待确认主机指纹 "+fp)
		timer := time.NewTimer(FingerprintTimeout)
		var confirmed string
		select {
		case confirmed = <-confirm:
		case <-timer.C:
		}
		timer.Stop()
		if confirmed == "" {
			m.step(key, "ssh", "failed", "未在时限内确认主机指纹")
			m.finish(key, "failed", "主机指纹未确认，未发送登录凭据；可重新发起")
			return
		}
		m.mu.Lock()
		m.jobs[key].Status = "running"
		m.jobs[key].Message = ""
		m.persistLocked()
		m.mu.Unlock()
		// Pin to the confirmed key; it is stored only after the login succeeds.
		rec.HostKey = confirmed
		ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
	}
	run := func(command string) (string, error) {
		out, _, e := m.exec.Run(ctx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, command)
		return out, e
	}
	out, err := run("printf 'ONEFROG_CONNECTION_OK\\n'; uname -s")
	if err != nil {
		msg := "连接失败：请检查地址、端口和登录凭据"
		if errors.Is(err, remote.ErrHostKeyMismatch) {
			msg = "主机指纹与已保存的不一致，已停止（未覆盖已保存指纹）；如服务器确实重装过，请在服务器页面重置指纹"
		}
		m.step(key, "ssh", "failed", msg)
		rec.Status = "offline"
		if errors.Is(err, remote.ErrHostKeyMismatch) {
			rec.Status = "host_key_mismatch"
		} else {
			rec.HostKey = "" // never persist an unverified key
		}
		_, _ = m.db.UpdateServer(ctx, rec)
		m.finish(key, "failed", "凭据已加密保存，可修改信息后重试")
		return
	}
	m.mu.Lock()
	m.jobs[key].Fingerprint = rec.HostKey
	m.mu.Unlock()
	rec.Status = "online"
	_, _ = m.db.UpdateServer(ctx, rec)
	m.step(key, "ssh", "done", "连接成功，已保存确认过的主机指纹")
	if !strings.Contains(out, "Linux") {
		m.step(key, "probe", "failed", "自动探针配置目前支持 Linux + systemd + Python 3.8+")
		m.finish(key, "partial", "服务器已加入；当前系统需手动配置采集端")
		return
	}
	m.step(key, "probe", "running", "")
	token, err := m.monitor.Issue(rec.ID)
	if err != nil {
		m.step(key, "probe", "failed", "探针凭据生成失败")
		m.finish(key, "partial", "服务器已加入，探针未完成")
		return
	}
	config := map[string]any{"server_id": rec.ID, "token": token, "endpoint": strings.TrimRight(r.ReportBase, "/") + "/api/probe/report", "interval_seconds": r.Interval}
	targets := []map[string]string{}
	for _, t := range r.Targets {
		targets = append(targets, map[string]string{"name": t, "host": t})
	}
	config["ping_targets"] = targets
	raw, _ := json.Marshal(config)
	out, err = run(installScript(raw))
	if err != nil || !strings.Contains(out, "ONEFROG_PROBE_INSTALLED") {
		message := "安装失败：需要 root 或免密 sudo、Python 3.8+ 和 systemd"
		if strings.Contains(out, "ONEFROG_UNSUPPORTED") {
			message = "节点缺少 Linux、Python 3.8+ 或 systemd"
		}
		m.step(key, "probe", "failed", message)
		m.finish(key, "partial", "服务器已保存，探针安装未完成")
		return
	}
	m.step(key, "probe", "done", "systemd 服务已启动，配置文件权限 0600")
	m.step(key, "modules", "running", "")
	mods, err := m.db.ListModules(ctx)
	if err != nil {
		m.step(key, "modules", "failed", "无法读取已启用模块")
		m.finish(key, "partial", "探针已安装，模块初始化未完成")
		return
	}
	missing, skipped, ready := []string{}, []string{}, []string{}
	for _, mod := range mods {
		if !mod.Enabled {
			continue
		}
		if mod.ID == "probe" || mod.ID == "services" || mod.ID == "alerts" || mod.ID == "activity" || mod.ID == "compare" {
			continue
		}
		m.mu.Lock()
		h, ok := m.hooks[mod.ID]
		m.mu.Unlock()
		if !ok {
			// Nothing to set up on the node for this module.
			skipped = append(skipped, mod.Name)
			continue
		}
		if e := h.Initialize(ctx, rec.Server, run); e != nil {
			missing = append(missing, h.Name+"初始化失败")
		} else {
			ready = append(ready, h.Name)
		}
	}
	switch {
	case len(missing) > 0:
		m.step(key, "modules", "failed", strings.Join(missing, "；"))
	case len(ready) == 0:
		msg := "启用的模块无需在节点上初始化"
		if len(skipped) > 0 {
			msg += "（已跳过：" + strings.Join(skipped, "、") + "）"
		}
		m.step(key, "modules", "skipped", msg)
	default:
		msg := "已初始化：" + strings.Join(ready, "、")
		if len(skipped) > 0 {
			msg += "；无需初始化：" + strings.Join(skipped, "、")
		}
		m.step(key, "modules", "done", msg)
	}
	m.step(key, "verify", "running", "")
	deadline := time.NewTimer(m.VerifyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		views := m.monitor.Views([]string{rec.ID}, time.Now())
		if len(views) > 0 && views[0].Status == "online" {
			m.step(key, "verify", "done", "已收到真实指标上报")
			if len(missing) > 0 {
				m.finish(key, "partial", "探针在线，部分模块初始化失败")
			} else {
				m.finish(key, "completed", "服务器、探针与模块已就绪")
			}
			return
		}
		select {
		case <-ctx.Done():
			m.step(key, "verify", "failed", "等待上报中止")
			m.finish(key, "partial", "探针服务已启动，请检查上报地址")
			return
		case <-deadline.C:
			m.step(key, "verify", "failed", "尚未收到上报，请检查面板地址、防火墙和节点服务日志")
			m.finish(key, "partial", "服务器已加入，探针进程已启动；监控尚未确认在线")
			return
		case <-ticker.C:
		}
	}
}

// Probe locations on the node. They are deliberately outside /opt/onefrog so
// uninstalling a panel that runs on the same host does not break the probe.
const (
	ProbeDir    = "/opt/onefrog-probe"
	ProbeConfig = "/etc/onefrog-probe/probe.json"
	ProbeUnit   = "/etc/systemd/system/onefrog-probe.service"
	// Paths used by v0.1.1 and earlier; removed when the probe is re-deployed.
	legacyProbeDir    = "/opt/onefrog/probe"
	legacyProbeConfig = "/etc/onefrog/probe.json"
)

func probeUnit() string {
	return "[Unit]\nDescription=OneFrog read-only probe\nAfter=network-online.target\nWants=network-online.target\nConditionPathExists=" + ProbeDir + "/agent.py\n[Service]\nExecStart=/usr/bin/python3 " + ProbeDir + "/agent.py " + ProbeConfig + "\nRestart=on-failure\nRestartSec=5\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=true\nPrivateTmp=true\n[Install]\nWantedBy=multi-user.target\n"
}

func installScript(config []byte) string {
	py := `import os,sys,base64,subprocess,shutil
if sys.platform!='linux' or sys.version_info<(3,8) or not os.path.isdir('/run/systemd/system'):
 print('ONEFROG_UNSUPPORTED');sys.exit(2)
os.makedirs('` + ProbeDir + `',exist_ok=True)
os.makedirs(os.path.dirname('` + ProbeConfig + `'),mode=0o700,exist_ok=True)
files=[('` + ProbeDir + `/agent.py','%s',0o755),('` + ProbeConfig + `','%s',0o600),('` + ProbeUnit + `','%s',0o644)]
for path,data,mode in files:
 temp=path+'.onefrog-tmp'
 fd=os.open(temp,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,mode)
 with os.fdopen(fd,'wb') as f:f.write(base64.b64decode(data))
 os.chmod(temp,mode);os.replace(temp,path)
shutil.rmtree('` + legacyProbeDir + `',ignore_errors=True)
for p in ['` + legacyProbeConfig + `']:
 try:os.remove(p)
 except FileNotFoundError:pass
for d in ['/etc/onefrog','/opt/onefrog']:
 try:os.rmdir(d)
 except OSError:pass
subprocess.run(['systemctl','daemon-reload'],check=True)
subprocess.run(['systemctl','enable','--now','onefrog-probe.service'],check=True)
subprocess.run(['systemctl','restart','onefrog-probe.service'],check=True)
subprocess.run(['systemctl','is-active','--quiet','onefrog-probe.service'],check=True)
print('ONEFROG_PROBE_INSTALLED')`
	return fmt.Sprintf("if [ \"$(id -u)\" -eq 0 ]; then OF_RUN=''; else OF_RUN='sudo -n'; fi\n$OF_RUN python3 - <<'ONEFROG_SETUP'\n"+py+"\nONEFROG_SETUP", base64.StdEncoding.EncodeToString([]byte(monitor.AgentScript)), base64.StdEncoding.EncodeToString(config), base64.StdEncoding.EncodeToString([]byte(probeUnit())))
}

// UninstallScript stops and removes the probe service and files, including
// the v0.1.1 locations. It prints ONEFROG_PROBE_REMOVED, or
// ONEFROG_PROBE_ABSENT when nothing was installed.
func UninstallScript() string {
	sh := `found=0
for f in ` + ProbeUnit + ` ` + ProbeDir + ` ` + filepath.Dir(ProbeConfig) + ` ` + legacyProbeDir + ` ` + legacyProbeConfig + `; do [ -e "$f" ] && found=1; done
if [ "$found" = 0 ]; then echo ONEFROG_PROBE_ABSENT; exit 0; fi
systemctl disable --now onefrog-probe.service >/dev/null 2>&1 || true
rm -f ` + ProbeUnit + `
systemctl daemon-reload >/dev/null 2>&1 || true
systemctl reset-failed onefrog-probe.service >/dev/null 2>&1 || true
rm -rf ` + ProbeDir + ` ` + filepath.Dir(ProbeConfig) + ` ` + legacyProbeDir + `
rm -f ` + legacyProbeConfig + `
rmdir /etc/onefrog /opt/onefrog 2>/dev/null || true
echo ONEFROG_PROBE_REMOVED`
	return "if [ \"$(id -u)\" -eq 0 ]; then OF_RUN=''; else OF_RUN='sudo -n'; fi\n$OF_RUN sh -s <<'ONEFROG_REMOVE'\n" + sh + "\nONEFROG_REMOVE"
}

// ProbeRemoval reports what happened to the probe when a server is deleted.
type ProbeRemoval struct {
	Status string `json:"status"` // removed | absent | skipped | failed
	Detail string `json:"detail"`
}

// RemoveProbe uninstalls the probe from a server over SSH, pinned to the
// confirmed host key. It never trusts a new key.
func (m *Manager) RemoveProbe(ctx context.Context, rec store.ServerRec) ProbeRemoval {
	if m.exec == nil {
		return ProbeRemoval{"skipped", "SSH 执行器不可用"}
	}
	if rec.HostKey == "" || rec.EncryptedKey == "" {
		return ProbeRemoval{"skipped", "没有已确认的主机指纹或登录凭据，未连接服务器；如装过探针请按文档手动卸载"}
	}
	plain, err := m.box.Decrypt(rec.EncryptedKey)
	if err != nil {
		return ProbeRemoval{"failed", "凭据无法解密"}
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, _, err := m.exec.Run(ctx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, UninstallScript())
	switch {
	case strings.Contains(out, "ONEFROG_PROBE_REMOVED"):
		return ProbeRemoval{"removed", "已停止并删除节点上的探针服务和文件"}
	case strings.Contains(out, "ONEFROG_PROBE_ABSENT"):
		return ProbeRemoval{"absent", "节点上没有安装探针"}
	case errors.Is(err, remote.ErrHostKeyMismatch):
		return ProbeRemoval{"failed", "主机指纹不一致，未连接；请按文档在节点上手动卸载探针"}
	case err != nil && strings.Contains(out, "sudo"):
		return ProbeRemoval{"failed", "需要 root 或免密 sudo；请按文档在节点上手动卸载探针"}
	default:
		return ProbeRemoval{"failed", "无法连接或卸载失败；请按文档在节点上手动卸载探针"}
	}
}
