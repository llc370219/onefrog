package api

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"onefrog/internal/model"
	"onefrog/internal/store"
	"onefrog/internal/workspace"
)

// getWorkspaceCapabilities reports the native local workspace feature
// truthfully: enabled/disabled, the operator-configured roots, whether
// commands may run and the execution environment. It is additive and safe for
// older frontends to ignore.
func (s *Server) getWorkspaceCapabilities(c *gin.Context) {
	cap := gin.H{
		"local": gin.H{
			"enabled":               false,
			"roots":                 []string{},
			"execution_environment": nativeOrContainer(),
			"command_execution":     false,
		},
	}
	if s.Agent != nil && s.Agent.LocalWorkspace != nil {
		caps := s.Agent.LocalWorkspace.Capabilities()
		roots := caps.Roots
		if roots == nil || !caps.Enabled {
			roots = []string{}
		}
		cap["local"] = gin.H{
			"enabled":               caps.Enabled,
			"roots":                 roots,
			"execution_environment": nativeOrContainer(),
			"command_execution":     caps.Enabled && caps.CommandExecution,
		}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, cap)
}

// nativeOrContainer tells the truth about where "local" commands run. Inside
// a Docker deployment the process is containerized unless the operator
// mounted project paths explicitly: then "local" means the container's
// filesystem, never the host. Detection is injectable for tests.
func nativeOrContainer() string {
	if inContainer() {
		return "container"
	}
	return "native"
}

// inContainer checks the usual container markers: /.dockerenv (Docker),
// /.containerenv (Podman), or a container-identifying cgroup.
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/.containerenv"); err == nil {
		return true
	}
	if raw, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(raw)
		if strings.Contains(s, "/docker") || strings.Contains(s, "/containerd") ||
			strings.Contains(s, "/kubepods") || strings.Contains(s, "/lxc") {
			return true
		}
	}
	return false
}

// resolveLocalWorkspaceTarget validates a chat option's workspace target for
// kind=local: it must be inside the operator allowlist. Task users cannot
// supply arbitrary roots. SSH targets are checked by the workspace Service.
func (s *Server) resolveLocalWorkspaceTarget(t workspace.Target) error {
	if t.Kind != "local" {
		return nil
	}
	if s.Agent == nil || s.Agent.LocalWorkspace == nil {
		return errors.New("本地工作区未启用")
	}
	if _, err := s.Agent.LocalWorkspace.Roots().Pick(t.Root); err != nil {
		return err
	}
	return nil
}

// localRootLabel builds the safe workspace label shown in the task card.
func localRootLabel(t workspace.Target) string {
	if t.Kind == "local" {
		return "local:" + strings.TrimSuffix(t.Root, "/")
	}
	return "ssh:" + t.ServerID + strings.TrimSuffix(t.Root, "/")
}

// localCodeWorkspace serves one workspace action against a native local root
// for the workspace editor. It uses the same immutable operator allowlist as
// the task tools; exec is NOT offered over HTTP (no arbitrary command
// endpoint). Writes and edits go through the engine's revision checks.
func (s *Server) localCodeWorkspace(c *gin.Context) {
	if s.Agent == nil || s.Agent.LocalWorkspace == nil {
		fail(c, http.StatusServiceUnavailable, "本地工作区未启用")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
	var body struct {
		Target workspace.Target       `json:"target"`
		Req    workspace.LocalRequest `json:"request"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "工作区请求无效")
		return
	}
	if err := s.resolveLocalWorkspaceTarget(body.Target); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	switch body.Req.Action {
	case "list", "status", "diff", "read", "search":
		// Browsing and inspection only; run is never exposed over HTTP.
	default:
		fail(c, http.StatusBadRequest, "此入口只支持浏览和手动保存文件")
		return
	}
	res, err := s.Agent.LocalWorkspace.Do(c.Request.Context(), body.Target.Root, body.Req)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}

// localRawRead returns the FULL raw file and its revision for the editor. It
// is separate from the model excerpt read: no header, no truncation, no
// excerpt — the editor gets exactly the bytes on disk (bounded by the file
// size limit).
func (s *Server) localRawRead(c *gin.Context) {
	if s.Agent == nil || s.Agent.LocalWorkspace == nil {
		fail(c, http.StatusServiceUnavailable, "本地工作区未启用")
		return
	}
	var body struct {
		Target workspace.Target `json:"target"`
		Path   string           `json:"path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	if err := s.resolveLocalWorkspaceTarget(body.Target); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.Agent.LocalWorkspace.RawRead(body.Target.Root, body.Path)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}

// localRawSave replaces a file's full content. It REQUIRES the revision from
// localRawRead; a stale or missing revision is refused so a concurrent edit
// is never clobbered. New files may be created without a revision.
func (s *Server) localRawSave(c *gin.Context) {
	if s.Agent == nil || s.Agent.LocalWorkspace == nil {
		fail(c, http.StatusServiceUnavailable, "本地工作区未启用")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(workspace.MaxLocalFile)+8<<10)
	var body struct {
		Target   workspace.Target `json:"target"`
		Path     string           `json:"path"`
		Content  string           `json:"content"`
		Revision string           `json:"revision"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		fail(c, http.StatusBadRequest, "请求无效（可能超过大小上限）")
		return
	}
	if err := s.resolveLocalWorkspaceTarget(body.Target); err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.Agent.LocalWorkspace.RawSave(body.Target.Root, body.Path, body.Content, body.Revision)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	_, _ = s.Store.AddHistory(c.Request.Context(), store.HistoryRec{History: model.History{
		ServerID: body.Target.ServerID, Action: "手动保存本地工作区文件：" + body.Target.Root + "/" + body.Path, Status: "success",
	}})
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}
