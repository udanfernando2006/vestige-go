package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

// handleStoreError mirrors GlobalExceptionHandler.java's mapping table:
// ErrNotFound -> 404 (msg is the handler's own constructed message, NOT
// err.Error() — Java's ResourceNotFoundException carries a clean per-call
// message like "Book not found: 5", and reusing store.go's own wrapped
// error text here would leak "store: update book 5: ..." plumbing details
// into the API response instead). ErrConflict -> 409 with Java's exact
// fixed generic message (handleConflict() ignores the real exception
// message too — this is faithful to that, not a simplification). Anything
// else -> 500, matching handleGeneral()'s catch-all.
//
// Returns true if it wrote a response (caller should return immediately
// after), false if err was nil.
func handleStoreError(c *gin.Context, err error, notFoundMsg string) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": notFoundMsg})
	case errors.Is(err, store.ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"error": "Resource already exists or constraint violated"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: " + err.Error()})
	}
	return true
}

// respondBadRequest mirrors handleBadRequest() (IllegalArgumentException -> 400)
// for single-message cases like the bulk-assign "exactly one of" check.
func respondBadRequest(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": message})
}

// respondValidation mirrors handleValidation() (MethodArgumentNotValidException
// -> 400, field -> message map) — used for hand-rolled required-field checks
// on Create DTOs, since Gin's binding validator error format doesn't match
// Java's per-field message strings closely enough to reuse directly.
func respondValidation(c *gin.Context, errs map[string]string) {
	c.JSON(http.StatusBadRequest, errs)
}
