package handler

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/domain"
)

func seriesRowToDto(sr domain.Series, bookCount int) SeriesDto {
	return SeriesDto{ID: sr.ID, Name: sr.Name, BookCount: bookCount, Author: sr.Author, Description: sr.Description}
}

func (srv *Server) listSeries(c *gin.Context) {
	rows, err := srv.store.GetAllSeries(c.Request.Context())
	if handleStoreError(c, err, "") {
		return
	}
	out := make([]SeriesDto, 0, len(rows))
	for _, r := range rows {
		out = append(out, SeriesDto{ID: r.ID, Name: r.Name, BookCount: r.BookCount, Author: r.Author, Description: r.Description})
	}
	c.JSON(http.StatusOK, out)
}

func (srv *Server) createSeries(c *gin.Context) {
	var dto SeriesCreateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	if strings.TrimSpace(dto.Name) == "" {
		respondValidation(c, map[string]string{"name": "Series name is required"})
		return
	}
	created, err := srv.store.CreateSeries(c.Request.Context(), domain.Series{
		Name: dto.Name, Author: dto.Author, Description: dto.Description,
	})
	if handleStoreError(c, err, "") {
		return
	}
	c.JSON(http.StatusCreated, seriesRowToDto(*created, 0))
}

// updateSeries mirrors SeriesService.update(): name is a rename only if
// present; a present-but-blank name is a 400 (Java: IllegalArgumentException
// via a manual check, not @NotBlank, since PATCH may omit name entirely).
func (srv *Server) updateSeries(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var dto SeriesUpdateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	if dto.Name != nil && strings.TrimSpace(*dto.Name) == "" {
		respondBadRequest(c, "Series name cannot be blank")
		return
	}
	updated, err := srv.store.UpdateSeries(c.Request.Context(), id, dto.Name, dto.Author, dto.Description)
	if handleStoreError(c, err, fmt.Sprintf("Series not found: %d", id)) {
		return
	}
	// Book count doesn't change from a rename/detail edit — look it up fresh
	// rather than assume 0, matching what a real client re-fetch would show.
	count := 0
	if all, err := srv.store.GetAllSeries(c.Request.Context()); err == nil {
		for _, r := range all {
			if r.ID == updated.ID {
				count = r.BookCount
				break
			}
		}
	}
	c.JSON(http.StatusOK, seriesRowToDto(*updated, count))
}

func (srv *Server) deleteSeries(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	err := srv.store.DeleteSeries(c.Request.Context(), id)
	if handleStoreError(c, err, fmt.Sprintf("Series not found: %d", id)) {
		return
	}
	c.Status(http.StatusNoContent)
}
