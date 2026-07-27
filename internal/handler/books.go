package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

func bookRowToDto(b store.BookRow) BookDto {
	return BookDto{
		ID: b.ID, Name: b.Name, ISBN: b.ISBN, IsSeriesEntry: b.IsSeriesEntry,
		SeriesID: b.SeriesID, SeriesName: b.SeriesName, Author: b.Author, Description: b.Description,
	}
}

func domainBookToDto(b domain.Book, seriesName *string) BookDto {
	return BookDto{
		ID: b.ID, Name: b.Name, ISBN: b.ISBN, IsSeriesEntry: b.IsSeriesEntry,
		SeriesID: b.SeriesID, SeriesName: seriesName, Author: b.Author, Description: b.Description,
	}
}

// listBooksGrouped mirrors BookService.getAllGrouped(): groups the flat,
// name-sorted row list by series name, preserving first-seen order
// (Java's `LinkedHashMap::new` supplier to groupingBy) — a standard Go map
// has no ordering guarantee, so this walks the already-sorted rows once,
// tracking key order in a parallel slice, rather than relying on map
// iteration order the way a naive port might.
func (srv *Server) listBooksGrouped(c *gin.Context) {
	rows, err := srv.store.GetAllBooksGrouped(c.Request.Context())
	if handleStoreError(c, err, "") {
		return
	}

	const standaloneKey = "__standalone__"
	order := []string{}
	grouped := map[string][]BookDto{}
	for _, b := range rows {
		key := standaloneKey
		if b.SeriesName != nil {
			key = *b.SeriesName
		}
		if _, seen := grouped[key]; !seen {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], bookRowToDto(b))
	}

	out := make([]BookGroupDto, 0, len(order))
	for _, key := range order {
		var seriesName *string
		if key != standaloneKey {
			k := key
			seriesName = &k
		}
		out = append(out, BookGroupDto{SeriesName: seriesName, Books: grouped[key]})
	}
	c.JSON(http.StatusOK, out)
}

func (srv *Server) createBook(c *gin.Context) {
	var dto BookCreateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	errs := map[string]string{}
	if strings.TrimSpace(dto.Name) == "" {
		errs["name"] = "Book name is required"
	}
	if strings.TrimSpace(dto.ISBN) == "" {
		errs["isbn"] = "ISBN is required"
	}
	if len(errs) > 0 {
		respondValidation(c, errs)
		return
	}

	ctx := c.Request.Context()

	var seriesID *int64
	var seriesName *string
	if dto.SeriesName != nil && strings.TrimSpace(*dto.SeriesName) != "" {
		id, name, err := findOrCreateSeriesByName(ctx, srv.store, *dto.SeriesName)
		if handleStoreError(c, err, "") {
			return
		}
		seriesID, seriesName = &id, &name
	}

	book, err := srv.store.CreateBook(ctx, domain.Book{
		Name: dto.Name, ISBN: dto.ISBN, IsSeriesEntry: dto.IsSeriesEntry,
		Author: dto.Author, Description: dto.Description, SeriesID: seriesID,
	})
	if handleStoreError(c, err, "") {
		return
	}
	c.JSON(http.StatusCreated, domainBookToDto(*book, seriesName))
}

func (srv *Server) updateBook(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var dto BookUpdateDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	book, err := srv.store.UpdateBook(c.Request.Context(), id, dto.Author, dto.Description)
	if handleStoreError(c, err, fmt.Sprintf("Book not found: %d", id)) {
		return
	}
	// UpdateBook doesn't know the series name — one cheap follow-up lookup
	// only when the book actually has a series, mirroring what a real join
	// would give for free; acceptable since PATCH is not a hot path.
	seriesName := lookupSeriesName(c.Request.Context(), srv.store, book.SeriesID)
	c.JSON(http.StatusOK, domainBookToDto(*book, seriesName))
}

func (srv *Server) deleteBook(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	err := srv.store.DeleteBook(c.Request.Context(), id)
	if handleStoreError(c, err, fmt.Sprintf("Book not found: %d", id)) {
		return
	}
	c.Status(http.StatusNoContent)
}

// bulkAssignSeries mirrors BookService.bulkAssignSeries(): exactly one of
// seriesId/newSeriesName, validated the same way IllegalArgumentException
// was in Java (400, exact message text preserved).
func (srv *Server) bulkAssignSeries(c *gin.Context) {
	var dto BulkSeriesAssignDto
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	if len(dto.BookIDs) == 0 {
		respondValidation(c, map[string]string{"bookIds": "At least one book must be selected"})
		return
	}
	hasExisting := dto.SeriesID != nil
	hasNew := dto.NewSeriesName != nil && strings.TrimSpace(*dto.NewSeriesName) != ""
	if hasExisting == hasNew {
		respondBadRequest(c, "Provide exactly one of seriesId or newSeriesName")
		return
	}

	ctx := c.Request.Context()
	var targetID int64
	var targetName string
	if hasExisting {
		sr, err := getSeriesByID(ctx, srv.store, *dto.SeriesID)
		if handleStoreError(c, err, fmt.Sprintf("Series not found: %d", *dto.SeriesID)) {
			return
		}
		targetID, targetName = sr.ID, sr.Name
	} else {
		id, name, err := findOrCreateSeriesByName(ctx, srv.store, *dto.NewSeriesName)
		if handleStoreError(c, err, "") {
			return
		}
		targetID, targetName = id, name
	}

	books, err := srv.store.BulkAssignSeries(ctx, dto.BookIDs, targetID)
	if handleStoreError(c, err, "One or more books not found") {
		return
	}
	name := targetName
	out := make([]BookDto, 0, len(books))
	for _, b := range books {
		out = append(out, domainBookToDto(b, &name))
	}
	c.JSON(http.StatusOK, out)
}

// =============================================================================
// small shared helpers used across handlers
// =============================================================================

func parseID(c *gin.Context, param string) (int64, bool) {
	raw := c.Param(param)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		respondBadRequest(c, "Invalid id: "+raw)
		return 0, false
	}
	return id, true
}

// findOrCreateSeriesByName mirrors the `seriesRepo.findByName(...).orElseGet(
// () -> seriesRepo.save(...))` pattern used identically in BookService.create()
// and BookService.bulkAssignSeries().
func findOrCreateSeriesByName(ctx context.Context, s store.PairStore, name string) (id int64, resolvedName string, err error) {
	existing, err := s.GetSeriesByName(ctx, name)
	if err != nil {
		return 0, "", err
	}
	if existing != nil {
		return existing.ID, existing.Name, nil
	}
	created, err := s.CreateSeries(ctx, domain.Series{Name: name})
	if err != nil {
		return 0, "", err
	}
	return created.ID, created.Name, nil
}

func getSeriesByID(ctx context.Context, s store.PairStore, id int64) (*domain.Series, error) {
	all, err := s.GetAllSeries(ctx)
	if err != nil {
		return nil, err
	}
	for _, sr := range all {
		if sr.ID == id {
			return &domain.Series{ID: sr.ID, Name: sr.Name, Author: sr.Author, Description: sr.Description}, nil
		}
	}
	return nil, store.ErrNotFound
}

func lookupSeriesName(ctx context.Context, s store.PairStore, seriesID *int64) *string {
	if seriesID == nil {
		return nil
	}
	sr, err := getSeriesByID(ctx, s, *seriesID)
	if err != nil || sr == nil {
		return nil
	}
	return &sr.Name
}
