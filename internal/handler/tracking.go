package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

func trackingRowToDto(t store.TrackingRow) TrackingPairDto {
	return TrackingPairDto{
		ID:              t.ID,
		Book:            BookRef{ID: t.BookID, Name: t.BookName, ISBN: t.BookISBN},
		Store:           StoreRef{ID: t.StoreID, Name: t.StoreName},
		ProductURL:      t.ProductURL,
		PriceSelector:   t.PriceSelector,
		StockSelector:   t.StockSelector,
		SelectorsCached: t.PriceSelector != nil && t.StockSelector != nil,
		Status:          t.Status,
		LastScrapedAt:   t.LastScrapedAt,
	}
}

// trackingRowByID re-derives a single joined row (including LastScrapedAt)
// by filtering GetAllTrackingPairs. NOT the most efficient option — a
// dedicated single-row query would be — but avoids guessing at
// GetLastSnapshot's exact signature/return type, which was never uploaded
// and shouldn't be fabricated per this project's own file-first-
// verification rule. Fine for a single-user desktop pair count; revisit if
// this ever shows up as a real bottleneck.
func trackingRowByID(ctx context.Context, s store.PairStore, id int64) (*store.TrackingRow, error) {
	all, err := s.GetAllTrackingPairs(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range all {
		if t.ID == id {
			return &t, nil
		}
	}
	return nil, store.ErrNotFound
}

func (srv *Server) listTrackingPairs(c *gin.Context) {
	rows, err := srv.store.GetAllTrackingPairs(c.Request.Context())
	if handleStoreError(c, err, "") {
		return
	}
	out := make([]TrackingPairDto, 0, len(rows))
	for _, r := range rows {
		out = append(out, trackingRowToDto(r))
	}
	c.JSON(http.StatusOK, out)
}

// listNeedsSetup uses store.go's existing GetPairsNeedingSetup (pre-dates
// this layer) rather than filtering GetAllTrackingPairs — NeedsSetupPair's
// exact shape wasn't re-verified line-by-line in this session, so this
// maps only the fields TrackingPairDto actually needs and leaves
// LastScrapedAt nil for this endpoint specifically (a NEEDS_SETUP pair has
// never successfully scraped by definition in every real case this
// project has hit so far, so this is a reasonable simplification here —
// flagged rather than silently assumed equivalent to the full listing).
func (srv *Server) listNeedsSetup(c *gin.Context) {
	rows, err := srv.store.GetPairsNeedingSetup(c.Request.Context())
	if handleStoreError(c, err, "") {
		return
	}
	out := make([]TrackingPairDto, 0, len(rows))
	for _, r := range rows {
		out = append(out, TrackingPairDto{
			ID:              r.ID,
			Book:            BookRef{ID: r.BookID, Name: r.BookName, ISBN: r.BookISBN},
			Store:           StoreRef{ID: r.StoreID, Name: r.StoreName},
			ProductURL:      r.ProductURL,
			PriceSelector:   r.PriceSelector,
			StockSelector:   r.StockSelector,
			SelectorsCached: r.PriceSelector != nil && r.StockSelector != nil,
			Status:          r.Status,
			LastScrapedAt:   nil,
		})
	}
	c.JSON(http.StatusOK, out)
}

// createTrackingPair mirrors TrackingService.create(): resolves book by
// ISBN and store by name first (404 if either is missing), relies on the
// store layer's UNIQUE(book_id, store_id) constraint for the 409 case.
func (srv *Server) createTrackingPair(c *gin.Context) {
	var dto TrackingPairCreateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	errs := map[string]string{}
	if strings.TrimSpace(dto.ISBN) == "" {
		errs["isbn"] = "ISBN is required"
	}
	if strings.TrimSpace(dto.StoreName) == "" {
		errs["storeName"] = "Store name is required"
	}
	if len(errs) > 0 {
		respondValidation(c, errs)
		return
	}

	ctx := c.Request.Context()
	book, err := srv.store.GetBookByISBN(ctx, dto.ISBN)
	if handleStoreError(c, err, "") {
		return
	}
	if book == nil {
		respondError404(c, fmt.Sprintf("Book not found with ISBN: %s", dto.ISBN))
		return
	}
	st, err := srv.store.GetStoreByName(ctx, dto.StoreName)
	if handleStoreError(c, err, "") {
		return
	}
	if st == nil {
		respondError404(c, fmt.Sprintf("Store not found: %s", dto.StoreName))
		return
	}

	pair, err := srv.store.CreateTrackingPair(ctx, book.ID, st.ID, dto.ProductURL)
	if handleStoreError(c, err, "") {
		return
	}
	// CreateTrackingPair's own implementation returns via GetPair(ctx, id)
	// after insert, and GetPair's documented contract is (nil, nil) when the
	// row doesn't exist — mirroring Python's Optional[Dict] -> None. err==nil
	// therefore does NOT guarantee pair!=nil (a TOCTOU on the freshly-inserted
	// row, or a future refactor, could hit that path) — CodeRabbit flagged
	// the unguarded pair.ID dereference below as a nil-pointer panic risk.
	// Guarded explicitly rather than trusting the error alone.
	if pair == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Tracking pair was created but could not be reloaded"})
		return
	}
	row, err := trackingRowByID(ctx, srv.store, pair.ID)
	if handleStoreError(c, err, "") {
		return
	}
	c.JSON(http.StatusCreated, trackingRowToDto(*row))
}

func (srv *Server) updateTrackingPair(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var dto TrackingPairUpdateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	ctx := c.Request.Context()
	_, err := srv.store.UpdateTrackingPairFields(ctx, id, dto.ProductURL, dto.PriceSelector, dto.StockSelector, dto.Status)
	if handleStoreError(c, err, fmt.Sprintf("Tracking pair not found: %d", id)) {
		return
	}
	row, err := trackingRowByID(ctx, srv.store, id)
	if handleStoreError(c, err, fmt.Sprintf("Tracking pair not found: %d", id)) {
		return
	}
	c.JSON(http.StatusOK, trackingRowToDto(*row))
}

// respondError404 is a tiny helper for the two "resolved by lookup, not by
// a store.ErrNotFound-wrapping call" 404 cases above (book-by-ISBN,
// store-by-name both return (nil, nil) on a clean miss rather than an
// error, mirroring GetBookByISBN/GetStoreByName's own documented
// sql.ErrNoRows -> (nil, nil) contract).
func respondError404(c *gin.Context, message string) {
	c.JSON(http.StatusNotFound, map[string]string{"error": message})
}