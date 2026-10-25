package api

import (
	"github.com/gin-gonic/gin"
	"io"
	"mime"
	"net/http"
	"onefrog/internal/attachment"
	"onefrog/internal/model"
	"onefrog/internal/preferences"
	"onefrog/internal/usage"
	"strings"
)

type botView struct {
	model.Bot
	Enabled        bool              `json:"enabled"`
	ModelRoute     preferences.Route `json:"model_route"`
	EffectiveRoute preferences.Route `json:"effective_route"`
	Usage          usage.Totals      `json:"usage"`
}

func (s *Server) botEnabled(c *gin.Context) {
	if _, err := s.Store.GetBot(c.Request.Context(), c.Param("id")); mapStore(c, err) {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&req) != nil || req.Enabled == nil {
		fail(c, 400, "请选择启用状态")
		return
	}
	if err := s.Settings.SetBotEnabled(c.Param("id"), *req.Enabled); err != nil {
		fail(c, 500, "状态保存失败")
		return
	}
	c.JSON(200, gin.H{"enabled": *req.Enabled})
}
func (s *Server) usageView(c *gin.Context) { c.JSON(200, s.Usage.Rows()) }
func (s *Server) uploadAttachment(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, attachment.MaxSize+1024*1024)
	if err := c.Request.ParseMultipartForm(1024 * 1024); err != nil {
		fail(c, 400, "文件过大或上传格式无效")
		return
	}
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	f, header, err := c.Request.FormFile("file")
	if err != nil {
		fail(c, 400, "请选择文件")
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, attachment.MaxSize+1))
	if err != nil {
		fail(c, 400, "文件读取失败")
		return
	}
	user := c.MustGet("user").(model.User)
	ref, err := s.Files.Put(user.ID, header.Filename, b)
	if err != nil {
		fail(c, 400, err.Error())
		return
	}
	c.JSON(201, ref)
}
func (s *Server) attachmentContent(c *gin.Context) {
	user := c.MustGet("user").(model.User)
	f, err := s.Files.Get(user.ID, c.Param("id"))
	if err != nil {
		fail(c, 404, "附件不存在")
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	kind := "attachment"
	if strings.HasPrefix(f.Mime, "image/") {
		kind = "inline"
	}
	c.Header("Content-Disposition", mime.FormatMediaType(kind, map[string]string{"filename": f.Name}))
	c.Data(200, f.Mime, f.Data)
}
