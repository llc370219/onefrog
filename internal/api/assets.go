package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"onefrog/internal/asset"
)

func (s *Server) listAssets(c *gin.Context) {
	if s.Assets == nil {
		fail(c, http.StatusServiceUnavailable, "资产模块未就绪")
		return
	}
	if servers, err := s.Store.ListServers(c.Request.Context()); err == nil {
		s.Assets.AutoLink(servers)
	}
	records := s.Assets.List()
	if records == nil {
		records = []asset.View{}
	}
	c.JSON(http.StatusOK, gin.H{"summary": s.Assets.Summary(), "records": records})
}

func (s *Server) createAsset(c *gin.Context) {
	if s.Assets == nil {
		fail(c, http.StatusServiceUnavailable, "资产模块未就绪")
		return
	}
	var in asset.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	row, err := s.Assets.Create(in)
	if err != nil {
		fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if servers, e := s.Store.ListServers(c.Request.Context()); e == nil {
		if s.Assets.AutoLink(servers) > 0 {
			if refreshed, ge := s.Assets.Get(row.ID); ge == nil {
				row = refreshed
			}
		}
	}
	c.JSON(http.StatusCreated, row)
}

func (s *Server) updateAsset(c *gin.Context) {
	if s.Assets == nil {
		fail(c, http.StatusServiceUnavailable, "资产模块未就绪")
		return
	}
	var in asset.Input
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "请求无效")
		return
	}
	row, err := s.Assets.Update(c.Param("id"), in)
	if err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "没有这条") {
			code = http.StatusNotFound
		}
		fail(c, code, err.Error())
		return
	}
	c.JSON(http.StatusOK, row)
}

func (s *Server) deleteAsset(c *gin.Context) {
	if s.Assets == nil {
		fail(c, http.StatusServiceUnavailable, "资产模块未就绪")
		return
	}
	if err := s.Assets.Delete(c.Param("id")); err != nil {
		code := http.StatusBadRequest
		if strings.Contains(err.Error(), "没有这条") {
			code = http.StatusNotFound
		}
		fail(c, code, err.Error())
		return
	}
	c.Status(http.StatusNoContent)
}
