package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"onefrog/internal/onboarding"
)

type migrationCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (s *Server) migrationCheckConnections(c *gin.Context) { s.migrationSSH(c, false) }
func (s *Server) migrationUpdateProbes(c *gin.Context)     { s.migrationSSH(c, true) }

func (s *Server) migrationSSH(c *gin.Context, update bool) {
	if !s.migrationReady(c) {
		return
	}
	m := s.Migration
	if !m.Gate.Paused() {
		fail(c, 409, "请先暂停工作台再执行迁移检查")
		return
	}
	command := "printf ONEFROG_CONNECTION_OK"
	if update {
		var req struct {
			Address string `json:"address"`
		}
		if c.ShouldBindJSON(&req) != nil {
			fail(c, 400, "请输入新面板地址")
			return
		}
		u, err := url.Parse(strings.TrimSpace(req.Address))
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			fail(c, 400, "新面板地址无效")
			return
		}
		command = migrationProbeScript(strings.TrimRight(u.String(), "/"))
	}
	if !m.begin("checking") {
		fail(c, 409, "已有迁移操作正在进行")
		return
	}
	defer m.finish("checked", "检查完成，请确认失败项及另一台实例的状态，再恢复运行")
	servers, err := s.Store.ListServers(c.Request.Context())
	if err != nil {
		fail(c, 500, "读取服务器失败")
		return
	}
	results := make([]migrationCheck, len(servers))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i, server := range servers {
		wg.Add(1)
		go func(i int, id, name string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			result := migrationCheck{Name: name, Message: "未连接：请检查凭据、网络和已确认的 SSH 指纹"}
			defer func() { results[i] = result }()
			rec, err := s.Store.GetServer(c.Request.Context(), id)
			if err != nil || rec.HostKey == "" || s.Agent == nil || s.Agent.SSH == nil {
				return
			}
			plain, err := s.Box.Decrypt(rec.EncryptedKey)
			if err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
			defer cancel()
			out, _, err := s.Agent.SSH.Run(ctx, rec.Host, rec.Port, rec.Username, rec.AuthType, plain, rec.HostKey, command)
			if err != nil {
				if update {
					result.Message = "更新失败：请检查权限和探针安装状态，原配置已尝试恢复"
				}
				return
			}
			if update {
				result.OK = strings.Contains(out, "ONEFROG_MIGRATION_PROBE_OK")
				if result.OK {
					result.Message = "探针上报地址已更新"
				}
			} else {
				result.OK = strings.Contains(out, "ONEFROG_CONNECTION_OK")
				if result.OK {
					result.Message = "SSH 连接正常"
				}
			}
		}(i, server.ID, server.Name)
	}
	wg.Wait()
	c.JSON(200, gin.H{"results": results})
}

func migrationProbeScript(address string) string {
	payload, _ := json.Marshal(map[string]string{"path": onboarding.ProbeConfig, "endpoint": address + "/api/probe/report"})
	py := `import os,json,base64,tempfile,subprocess
args=json.loads(base64.b64decode('` + base64.StdEncoding.EncodeToString(payload) + `'))
path=args['path']
with open(path,'rb') as f: original=f.read()
config=json.loads(original)
config['endpoint']=args['endpoint']
def write(data):
 fd,tmp=tempfile.mkstemp(prefix='.onefrog-migrate-',dir=os.path.dirname(path))
 try:
  with os.fdopen(fd,'wb') as f:
   f.write(data)
   f.flush()
   os.fsync(f.fileno())
  os.chmod(tmp,0o600)
  os.replace(tmp,path)
 finally:
  if os.path.exists(tmp): os.unlink(tmp)
try:
 write(json.dumps(config).encode())
 subprocess.run(['systemctl','restart','onefrog-probe.service'],check=True)
 subprocess.run(['systemctl','is-active','--quiet','onefrog-probe.service'],check=True)
except Exception:
 write(original)
 subprocess.run(['systemctl','restart','onefrog-probe.service'])
 raise
print('ONEFROG_MIGRATION_PROBE_OK')
`
	return "if [ \"$(id -u)\" = 0 ]; then python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString([]byte(py)) + "'))\"; else sudo -n python3 -c \"import base64;exec(base64.b64decode('" + base64.StdEncoding.EncodeToString([]byte(py)) + "'))\"; fi"
}
