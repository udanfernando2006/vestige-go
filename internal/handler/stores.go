package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/domain"
)

func storeToDto(s domain.Store) StoreDto {
	return StoreDto{ID: s.ID, Name: s.Name, BaseURL: s.BaseURL, SearchURLTemplate: s.SearchURLTemplate}
}

func (srv *Server) listStores(c *gin.Context) {
	rows, err := srv.store.GetAllStores(c.Request.Context())
	if handleStoreError(c, err, "") {
		return
	}
	out := make([]StoreDto, 0, len(rows))
	for _, r := range rows {
		out = append(out, storeToDto(r))
	}
	c.JSON(http.StatusOK, out)
}

func (srv *Server) createStore(c *gin.Context) {
	var dto StoreCreateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	errs := map[string]string{}
	if strings.TrimSpace(dto.Name) == "" {
		errs["name"] = "Store name is required"
	}
	if strings.TrimSpace(dto.BaseURL) == "" {
		errs["baseUrl"] = "Base URL is required"
	}
	if len(errs) > 0 {
		respondValidation(c, errs)
		return
	}
	created, err := srv.store.CreateStore(c.Request.Context(), domain.Store{Name: dto.Name, BaseURL: dto.BaseURL})
	if handleStoreError(c, err, "") {
		return
	}
	c.JSON(http.StatusCreated, storeToDto(*created))
}

func (srv *Server) updateStore(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var dto StoreUpdateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	updated, err := srv.store.UpdateStore(c.Request.Context(), id, dto.Name, dto.BaseURL, dto.SearchURLTemplate)
	if handleStoreError(c, err, fmt.Sprintf("Store not found: %d", id)) {
		return
	}
	c.JSON(http.StatusOK, storeToDto(*updated))
}

func (srv *Server) deleteStore(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	err := srv.store.DeleteStore(c.Request.Context(), id)
	if handleStoreError(c, err, fmt.Sprintf("Store not found: %d", id)) {
		return
	}
	c.Status(http.StatusNoContent)
}
