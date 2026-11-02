package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"onefrog/internal/devmodule"
	"onefrog/internal/model"
)

type devModuleRow struct {
	devmodule.View
	Enabled bool `json:"enabled"`
}

func (s *Server) listDevModules(c *gin.Context) {
	if s.DevModules == nil {
		c.JSON(http.StatusOK, []devModuleRow{})
		return
	}
	stored, _ := s.Store.ListModules(c.Request.Context())
	enabledByID := make(map[string]bool, len(stored))
	for _, m := range stored {
		enabledByID[m.ID] = m.Enabled
	}
	views := s.DevModules.List()
	out := make([]devModuleRow, 0, len(views))
	for _, v := range views {
		en, ok := enabledByID[v.ID]
		if !ok {
			en = true
		}
		out = append(out, devModuleRow{View: v, Enabled: en})
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getDevModule(c *gin.Context) {
	if s.DevModules == nil {
		fail(c, http.StatusServiceUnavailable, "二次开发模块未就绪")
		return
	}
	item, err := s.DevModules.Get(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, err.Error())
		return
	}
	en := true
	if m, e := s.Store.GetModule(c.Request.Context(), item.ID); e == nil {
		en = m.Enabled
	}
	c.JSON(http.StatusOK, gin.H{
		"id":          item.ID,
		"name":        item.Name,
		"description": item.Description,
		"html":        item.HTML,
		"enabled":     en,
		"state":       item.State,
		"records":     item.Records,
		"created_at":  item.CreatedAt,
		"updated_at":  item.UpdatedAt,
	})
}

func (s *Server) createDevModule(c *gin.Context) {
	if s.DevModules == nil {
		fail(c, http.StatusServiceUnavailable, "二次开发模块未就绪")
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		HTML        string `json:"html"`
		UseStarter  bool   `json:"use_starter"`
		Enabled     *bool  `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	html := strings.TrimSpace(req.HTML)
	if html == "" && req.UseStarter {
		html = devmodule.StarterHTML(req.Name)
	}
	created, err := s.DevModules.Create(devmodule.Input{
		Name:        req.Name,
		Description: req.Description,
		HTML:        html,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	en := true
	if req.Enabled != nil {
		en = *req.Enabled
	}
	_, _ = s.Store.SaveModule(c.Request.Context(), model.Module{
		ID:          created.ID,
		Name:        created.Name,
		Version:     "1.0.0",
		Type:        "custom",
		Description: created.Description,
		Enabled:     en,
		CreatedAt:   created.CreatedAt,
	})
	c.JSON(http.StatusCreated, devModuleRow{
		View: devmodule.View{
			ID:          created.ID,
			Name:        created.Name,
			Description: created.Description,
			HTML:        created.HTML,
			RecordCount: len(created.Records),
			CreatedAt:   created.CreatedAt,
			UpdatedAt:   created.UpdatedAt,
		},
		Enabled: en,
	})
}

func (s *Server) updateDevModule(c *gin.Context) {
	if s.DevModules == nil {
		fail(c, http.StatusServiceUnavailable, "二次开发模块未就绪")
		return
	}
	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		HTML        *string `json:"html"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	updated, err := s.DevModules.Update(c.Param("id"), devmodule.Patch{
		Name:        req.Name,
		Description: req.Description,
		HTML:        req.HTML,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	en := true
	if m, e := s.Store.GetModule(c.Request.Context(), updated.ID); e == nil {
		en = m.Enabled
	}
	if req.Enabled != nil {
		en = *req.Enabled
	}
	_, _ = s.Store.SaveModule(c.Request.Context(), model.Module{
		ID:          updated.ID,
		Name:        updated.Name,
		Version:     "1.0.0",
		Type:        "custom",
		Description: updated.Description,
		Enabled:     en,
		CreatedAt:   updated.CreatedAt,
	})
	c.JSON(http.StatusOK, devModuleRow{
		View: devmodule.View{
			ID:          updated.ID,
			Name:        updated.Name,
			Description: updated.Description,
			HTML:        updated.HTML,
			RecordCount: len(updated.Records),
			CreatedAt:   updated.CreatedAt,
			UpdatedAt:   updated.UpdatedAt,
		},
		Enabled: en,
	})
}

func (s *Server) deleteDevModule(c *gin.Context) {
	if s.DevModules == nil {
		fail(c, http.StatusServiceUnavailable, "二次开发模块未就绪")
		return
	}
	removed, err := s.DevModules.Delete(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, err.Error())
		return
	}
	_ = s.Store.DeleteModule(c.Request.Context(), removed.ID)
	c.Status(http.StatusNoContent)
}

func (s *Server) devModuleData(c *gin.Context) {
	if s.DevModules == nil {
		fail(c, http.StatusServiceUnavailable, "二次开发模块未就绪")
		return
	}
	var req struct {
		Action string         `json:"action"`
		ID     string         `json:"id"`
		State  map[string]any `json:"state"`
		Data   map[string]any `json:"data"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	modID := c.Param("id")
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "get"
	}
	var (
		res devmodule.DataPayload
		err error
	)
	switch action {
	case "get":
		res, err = s.DevModules.GetData(modID)
	case "save_state":
		res, err = s.DevModules.SaveState(modID, req.State, true)
	case "set_state":
		res, err = s.DevModules.SaveState(modID, req.State, false)
	case "add_record":
		res, err = s.DevModules.AddRecord(modID, req.Data)
	case "update_record":
		res, err = s.DevModules.UpdateRecord(modID, req.ID, req.Data)
	case "delete_record":
		res, err = s.DevModules.DeleteRecord(modID, req.ID)
	case "clear_records":
		res, err = s.DevModules.ClearRecords(modID)
	default:
		fail(c, http.StatusBadRequest, "不支持的数据动作")
		return
	}
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) devModuleFrame(c *gin.Context) {
	if strings.EqualFold(c.GetHeader("Sec-Fetch-Dest"), "document") {
		fail(c, http.StatusForbidden, "仅限面板沙箱内加载")
		return
	}
	if s.DevModules == nil {
		fail(c, http.StatusServiceUnavailable, "二次开发模块未就绪")
		return
	}
	item, err := s.DevModules.Get(c.Param("id"))
	if err != nil {
		fail(c, http.StatusNotFound, err.Error())
		return
	}
	h := c.Writer.Header()
	h.Set("Content-Security-Policy", devmodule.SandboxCSP)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(devmodule.RenderFrameHTML(item)))
}
