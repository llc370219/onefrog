package api

import (
	"encoding/base64"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"onefrog/internal/model"
	"regexp"
	"strings"
	"unicode/utf8"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,64}$`)

func (s *Server) updateProfile(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 400*1024)
	var req struct {
		Username        string `json:"username"`
		DisplayName     string `json:"display_name"`
		Avatar          string `json:"avatar"`
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, 400, "资料格式无效或头像过大")
		return
	}
	current := c.MustGet("user").(model.User)
	req.Username = strings.TrimSpace(req.Username)
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if !usernamePattern.MatchString(req.Username) {
		fail(c, 400, "用户名需为 3–64 位字母、数字、下划线、点或短横线")
		return
	}
	if utf8.RuneCountInString(req.DisplayName) > 40 {
		fail(c, 400, "显示名称最多 40 字")
		return
	}
	if current.MustChangeCredentials {
		if req.Username == current.Username {
			fail(c, 400, "第一次登录必须更换用户名")
			return
		}
		if req.NewPassword == "" {
			fail(c, 400, "第一次登录必须更换密码")
			return
		}
	}
	if req.Avatar != "" {
		prefix, data, ok := strings.Cut(req.Avatar, ",")
		raw, err := base64.StdEncoding.DecodeString(data)
		if !ok || err != nil || len(raw) > 256*1024 || len(raw) == 0 {
			fail(c, 400, "头像需小于 256 KB")
			return
		}
		mime := http.DetectContentType(raw)
		if (mime != "image/png" && mime != "image/jpeg" && mime != "image/webp") || prefix != "data:"+mime+";base64" {
			fail(c, 400, "头像只支持 PNG、JPEG 或 WebP 图片")
			return
		}
	}
	hash := ""
	if req.Username != current.Username || req.NewPassword != "" {
		_, old, err := s.Store.UserByName(c.Request.Context(), current.Username)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(old), []byte(req.CurrentPassword)) != nil {
			fail(c, 400, "当前密码不正确")
			return
		}
		if req.NewPassword != "" {
			if len(req.NewPassword) < 8 || len(req.NewPassword) > 72 {
				fail(c, 400, "新密码需为 8–72 字节")
				return
			}
			if bcrypt.CompareHashAndPassword([]byte(old), []byte(req.NewPassword)) == nil {
				fail(c, 400, "新密码不能与当前密码相同")
				return
			}
			b, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
			if err != nil {
				fail(c, 500, "密码更新失败")
				return
			}
			hash = string(b)
		}
	}
	current.Username = req.Username
	current.DisplayName = req.DisplayName
	current.Avatar = req.Avatar
	if current.MustChangeCredentials {
		current.MustChangeCredentials = false
	}
	cookie, _ := c.Cookie("of_session")
	user, err := s.Store.UpdateUser(c.Request.Context(), current, hash, hashToken(cookie))
	if mapStore(c, err) {
		return
	}
	c.JSON(200, user)
}
func (s *Server) listSessions(c *gin.Context) {
	items, err := s.Store.ListChatSessions(c.Request.Context())
	if mapStore(c, err) {
		return
	}
	c.JSON(200, items)
}
