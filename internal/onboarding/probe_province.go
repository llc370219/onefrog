package onboarding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"onefrog/internal/monitor"
)

// ConfigureProvince updates an installed probe without issuing a new token or
// replacing its reporting address, interface selection, or custom targets.
func (m *Manager) ConfigureProvince(ctx context.Context, serverID, province string) error {
	if province != "" {
		p, ok := monitor.LookupProvince(province)
		if !ok {
			return errors.New("不认识的省份")
		}
		province = p.Code
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	for _, j := range m.jobs {
		if j.ServerID == serverID && (j.Status == "running" || j.Status == "fingerprint_required") {
			m.mu.Unlock()
			return errors.New("该服务器正在配置中，请稍后再试")
		}
	}
	m.mu.Unlock()
	rec, err := m.db.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	if m.exec == nil || rec.HostKey == "" || rec.EncryptedKey == "" {
		return errors.New("请先完成 SSH 连接测试并确认主机指纹，再应用测速省份")
	}
	plain, err := m.box.Decrypt(rec.EncryptedKey)
	if err != nil {
		return errors.New("SSH 凭据无法解密，请重新保存服务器凭据")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, _, err := m.exec.Run(ctx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, provinceScript(ProbeConfig, province))
	if err != nil || !strings.Contains(out, "ONEFROG_PROVINCE_APPLIED") {
		return errors.New("测速配置应用失败：请检查 SSH、root 或免密 sudo 权限，以及 onefrog-probe 服务是否已安装；原配置会在重启失败时恢复")
	}
	if err := m.monitor.SetProvince(serverID, province); err != nil {
		return errors.New("节点配置已应用，但面板省份保存失败，请重试")
	}
	return nil
}

func provinceScript(path, province string) string {
	targets := []monitor.PingTarget{}
	if p, ok := monitor.LookupProvince(province); ok {
		targets = monitor.ISPTargets(p)
	}
	raw, _ := json.Marshal(targets)
	quotedPath, _ := json.Marshal(path)
	py := `import os,json,base64,subprocess,tempfile
path=` + string(quotedPath) + `
with open(path,'rb') as f: original=f.read()
config=json.loads(original)
carriers={'电信','联通','移动'}
custom=[t for t in config.get('ping_targets',[]) if not (isinstance(t,dict) and t.get('name') in carriers)]
targets=json.loads(base64.b64decode('` + base64.StdEncoding.EncodeToString(raw) + `'))
if len(custom)+len(targets)>8: raise ValueError('最多 8 个检测目标（三网占 3 个）')
config['ping_targets']=targets+custom
def write(data):
 fd,temp=tempfile.mkstemp(prefix='.onefrog-',dir=os.path.dirname(path))
 try:
  with os.fdopen(fd,'wb') as f: f.write(data)
  os.chmod(temp,0o600)
  os.replace(temp,path)
 finally:
  if os.path.exists(temp): os.unlink(temp)
write(json.dumps(config,ensure_ascii=False).encode())
try:
 subprocess.run(['systemctl','restart','onefrog-probe.service'],check=True)
 subprocess.run(['systemctl','is-active','--quiet','onefrog-probe.service'],check=True)
except Exception:
 write(original)
 subprocess.run(['systemctl','restart','onefrog-probe.service'],check=False)
 raise
print('ONEFROG_PROVINCE_APPLIED')`
	return fmt.Sprintf("if [ \"$(id -u)\" -eq 0 ]; then OF_RUN=''; else OF_RUN='sudo -n'; fi\n$OF_RUN python3 - <<'ONEFROG_PROVINCE'\n%s\nONEFROG_PROVINCE", py)
}
