package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// optionalQuery returns nil for an absent or empty query param, a pointer
// otherwise — matches Java's @RequestParam(required = false) String
// semantics (an omitted param and an empty one both mean "don't filter").
func optionalQuery(c *gin.Context, key string) *string {
	v := c.Query(key)
	if v == "" {
		return nil
	}
	return &v
}

func (srv *Server) currentAvailability(c *gin.Context) {
	rows, err := srv.store.GetCurrentAvailability(c.Request.Context())
	if handleStoreError(c, err, "") {
		return
	}
	out := make([]AvailabilityDto, 0, len(rows))
	for _, r := range rows {
		out = append(out, AvailabilityDto{
			PairID: r.PairID, BookName: r.BookName, StoreName: r.StoreName,
			Status: r.Status, Price: r.Price, ProductURL: r.ProductURL, ScrapedAt: r.ScrapedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// availabilityHistory mirrors AvailabilityController.getHistory(): every
// filter independently optional, default limit 100 (matches Java's
// @RequestParam(defaultValue = "100")).
func (srv *Server) availabilityHistory(c *gin.Context) {
	isbn := optionalQuery(c, "isbn")
	storeName := optionalQuery(c, "storeName")
	status := optionalQuery(c, "status")

	limit := 100
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			respondBadRequest(c, "Invalid limit: "+raw)
			return
		}
		limit = n
	}

	rows, err := srv.store.GetAvailabilityHistory(c.Request.Context(), isbn, storeName, status, limit)
	if handleStoreError(c, err, "") {
		return
	}
	out := make([]SnapshotHistoryDto, 0, len(rows))
	for _, r := range rows {
		out = append(out, SnapshotHistoryDto{
			ID: r.ID, PairID: r.PairID, BookName: r.BookName, StoreName: r.StoreName,
			Status: r.Status, Price: r.Price, ScrapedAt: r.ScrapedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

func (srv *Server) deleteSnapshot(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	err := srv.store.DeleteSnapshot(c.Request.Context(), id)
	if handleStoreError(c, err, fmt.Sprintf("Snapshot not found: %d", id)) {
		return
	}
	c.Status(http.StatusNoContent)
}

func (srv *Server) deleteHistoryForPair(c *gin.Context) {
	pairID, ok := parseID(c, "pairId")
	if !ok {
		return
	}
	err := srv.store.DeleteHistoryForPair(c.Request.Context(), pairID)
	if handleStoreError(c, err, fmt.Sprintf("Tracking pair not found: %d", pairID)) {
		return
	}
	c.Status(http.StatusNoContent)
}
