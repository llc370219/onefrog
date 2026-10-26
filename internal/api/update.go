package api

import (
	"context"
	"errors"
	"mime"
	"net/http"
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

func (s *Server) putUpdateSetting(c *gin.Context) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	if err := s.Settings.SetUpdateCheck(*req.Enabled); err != nil {
		fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	status := s.Updates.Status()
	if *req.Enabled && status.Enabled && status.CheckedAt == nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = s.Updates.CheckNow(ctx)
		}()
	}
	c.JSON(http.StatusOK, status)
}
