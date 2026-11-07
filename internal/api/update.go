package api

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"onefrog/internal/update"
)

// rootFiles are web build files served from "/" (icons and the manifest);
// everything else under "/" falls back to index.html.
var rootFiles = []string{"favicon.ico", "favicon-32.png", "apple-touch-icon.png", "icon-192.png", "icon-512.png", "manifest.webmanifest"}

func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

func (s *Server) updateStatus(c *gin.Context) {
	c.JSON(http.StatusOK, s.Updates.Status())
}

func (s *Server) updateCheck(c *gin.Context) {
	status, err := s.Updates.CheckNow(c.Request.Context())
	if errors.Is(err, update.ErrTooSoon) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": err.Error(), "status": status})
		return
	}
	// Network failures are reported inside the status so the UI can show them.
	c.JSON(http.StatusOK, status)
}

func (s *Server) updateApply(c *gin.Context) {
	// The version is the cached release only. A body, if any, is ignored.
	tag, err := s.Updates.Apply()
	switch {
	case err == nil:
		c.JSON(http.StatusAccepted, gin.H{
			"ok":      true,
			"version": tag,
			"message": "正在升级，容器会重启，请稍等后刷新。账号和数据保留。",
		})
	case errors.Is(err, update.ErrNoUpdate), errors.Is(err, update.ErrUpgradeUnavailable), errors.Is(err, update.ErrHelperStale):
		fail(c, http.StatusConflict, err.Error())
	default:
		fail(c, http.StatusInternalServerError, "升级请求没有写出去，请改用安装命令")
	}
}

func (s *Server) putUpdateSetting(c *gin.Context) {
	var req struct {
		Enabled *bool   `json:"enabled"`
		Channel *string `json:"channel"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || (req.Enabled == nil && req.Channel == nil) {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	if req.Channel != nil {
		ch := strings.TrimSpace(*req.Channel)
		if ch != update.ChannelStable && ch != update.ChannelBeta {
			fail(c, http.StatusBadRequest, "更新渠道只能是 stable 或 beta")
			return
		}
		if err := s.Settings.SetUpdateChannel(ch); err != nil {
			fail(c, http.StatusInternalServerError, "保存失败")
			return
		}
		s.Updates.Forget()
	}
	if req.Enabled != nil {
		if err := s.Settings.SetUpdateCheck(*req.Enabled); err != nil {
			fail(c, http.StatusInternalServerError, "保存失败")
			return
		}
	}
	status := s.Updates.Status()
	if req.Channel != nil && status.Enabled {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
		defer cancel()
		status, _ = s.Updates.CheckNow(ctx)
	} else if req.Enabled != nil && *req.Enabled && status.Enabled && status.CheckedAt == nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = s.Updates.CheckNow(ctx)
		}()
	}
	c.JSON(http.StatusOK, status)
}
