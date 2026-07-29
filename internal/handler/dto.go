// Package handler is the Gin HTTP layer — the Go equivalent of the Java
// controller/ + dto/ packages combined. Source-verified against the
// uploaded Java DTO files field-for-field, including exact JSON key
// casing (@JsonProperty overrides like "isSeriesEntry" are preserved
// here via explicit `json:"isSeriesEntry"` tags, not left to Go's default
// field-name-based marshaling).
//
// WIRE-FORMAT ISOLATION, same principle vestige_api_implementation.md
// documents for Java's own DTOs: these structs are the ONLY things ever
// JSON-marshaled to the frontend. domain.*/store.* types never cross this
// boundary directly — every handler maps explicitly, same as Java's
// service layer mapping Entity -> DTO via toDto().
//
// ONE DELIBERATE WIDENING beyond the Java contract, flagged here rather
// than silently added: SettingsResponse/SettingsUpdateRequest include
// CustomStockInPatterns/CustomStockOutPatterns, which Java's
// SettingsDto.java/SettingsUpdateDto.java do NOT have. This is intentional
// — per the confirmed Vestige-Go decision (migration blueprint §7 item 12),
// sync_config()/books_config.json is dropped entirely and these two
// patterns become ordinary UI-managed Settings fields, unlike v1 where
// they were edited via a JSON file's "availability-regex" key. The Java
// files describe v1's contract, not Vestige-Go's — this is a genuine,
// intentional divergence point, not a porting error.
//
// ONE MINOR, ACCEPTED FIDELITY GAP: TrackingPairDto.java's toDto() nests
// PARTIALLY-populated BookDto/StoreDto (book: id/name/isbn only; store:
// id/name only — every other field left at Lombok's builder default).
// This port mirrors that exactly in the tracking handler (only those
// fields set, others left as Go zero values) — but Go's zero value for a
// non-pointer string is "" where Java's uninitialized String field is
// null. Fields here that appear ONLY in this partial context use pointer
// types specifically to preserve the null-vs-empty distinction; see
// BookRef/StoreRef below.
package handler

import "time"

// =============================================================================
// Books
// =============================================================================

// BookDto mirrors BookDto.java exactly, including its @JsonProperty
// override on seriesEntry.
type BookDto struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	ISBN          string  `json:"isbn"`
	IsSeriesEntry bool    `json:"isSeriesEntry"`
	SeriesID      *int64  `json:"seriesId"`
	SeriesName    *string `json:"seriesName"`
	Author        *string `json:"author"`
	Description   *string `json:"description"`
}

// BookRef is the PARTIAL book shape nested inside TrackingPairDto — see
// package doc comment's "minor fidelity gap" note. All fields pointers
// so an unset field marshals as JSON null, matching Java's uninitialized-
// String/-Long-is-null semantics exactly (unlike BookDto's IsSeriesEntry
// bool above, which Java's own partial builder also leaves at `false`,
// not null — bool has no null state in either language, so BookRef
// mirrors that by simply omitting the field, always absent, never sent).
type BookRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	ISBN string `json:"isbn"`
}

type BookCreateDto struct {
	Name          string  `json:"name"`
	ISBN          string  `json:"isbn"`
	IsSeriesEntry bool    `json:"isSeriesEntry"`
	SeriesName    *string `json:"seriesName"`
	Author        *string `json:"author"`
	Description   *string `json:"description"`
}

type BookUpdateDto struct {
	Author      *string `json:"author"`      // nil (omitted/null) = no change, ""= clear
	Description *string `json:"description"` // same
}

type BookGroupDto struct {
	SeriesName *string   `json:"seriesName"` // nil = standalone group
	Books      []BookDto `json:"books"`
}

type BulkSeriesAssignDto struct {
	BookIDs       []int64 `json:"bookIds"`
	SeriesID      *int64  `json:"seriesId"`
	NewSeriesName *string `json:"newSeriesName"`
}

// =============================================================================
// Series
// =============================================================================

type SeriesDto struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	BookCount   int     `json:"bookCount"`
	Author      *string `json:"author"`
	Description *string `json:"description"`
}

type SeriesCreateDto struct {
	Name        string  `json:"name"`
	Author      *string `json:"author"`
	Description *string `json:"description"`
}

type SeriesUpdateDto struct {
	Name        *string `json:"name"` // omitted = no rename; "" rejected (400) in handler
	Author      *string `json:"author"`
	Description *string `json:"description"`
}

// =============================================================================
// Stores
// =============================================================================

type StoreDto struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	BaseURL           string  `json:"baseUrl"`
	SearchURLTemplate *string `json:"searchUrlTemplate"`
}

// StoreRef mirrors the PARTIAL store shape nested inside TrackingPairDto
// (id/name only) — same reasoning as BookRef above.
type StoreRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type StoreCreateDto struct {
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
}

type StoreUpdateDto struct {
	Name              *string `json:"name"`
	BaseURL           *string `json:"baseUrl"`
	SearchURLTemplate *string `json:"searchUrlTemplate"` // "" = clear back to undiscovered
}

// =============================================================================
// Tracking pairs
// =============================================================================

type TrackingPairDto struct {
	ID              int64      `json:"id"`
	Book            BookRef    `json:"book"`
	Store           StoreRef   `json:"store"`
	ProductURL      *string    `json:"productUrl"`
	PriceSelector   *string    `json:"priceSelector"`
	StockSelector   *string    `json:"stockSelector"`
	SelectorsCached bool       `json:"selectorsCached"`
	Status          string     `json:"status"`
	LastScrapedAt   *time.Time `json:"lastScrapedAt"`
}

type TrackingPairCreateDto struct {
	ISBN       string  `json:"isbn"`
	StoreName  string  `json:"storeName"`
	ProductURL *string `json:"productUrl"`
}

type TrackingPairUpdateDto struct {
	ProductURL    *string `json:"productUrl"`
	PriceSelector *string `json:"priceSelector"`
	StockSelector *string `json:"stockSelector"`
	Status        *string `json:"status"`
}

// =============================================================================
// Availability
// =============================================================================

type AvailabilityDto struct {
	PairID     int64     `json:"pairId"`
	BookName   string    `json:"bookName"`
	StoreName  string    `json:"storeName"`
	Status     string    `json:"status"`
	Price      *float64  `json:"price"`
	ProductURL *string   `json:"productUrl"`
	ScrapedAt  time.Time `json:"scrapedAt"`
}

type SnapshotHistoryDto struct {
	ID        int64     `json:"id"`
	PairID    int64     `json:"pairId"`
	BookName  string    `json:"bookName"`
	StoreName string    `json:"storeName"`
	Status    string    `json:"status"`
	Price     *float64  `json:"price"`
	ScrapedAt time.Time `json:"scrapedAt"`
}

// =============================================================================
// Settings — see package doc comment for the CustomStock*Patterns widening
// =============================================================================

type SettingsResponse struct {
	LLMDiscoveryEnabled     bool     `json:"llmDiscoveryEnabled"`
	LLMMode                 string   `json:"llmMode"`
	NotificationsEnabled 	bool   	 `json:"notificationsEnabled"`
	SelectorAPIBase         string   `json:"selectorApiBase"`
	SelectorAPIKeyConfigured bool    `json:"selectorApiKeyConfigured"`
	SelectorAPIKeyHint      *string  `json:"selectorApiKeyHint"`
	SelectorModel           string   `json:"selectorModel"`
	DirectAPIBase           string   `json:"directApiBase"`
	DirectAPIKeyConfigured  bool     `json:"directApiKeyConfigured"`
	DirectAPIKeyHint        *string  `json:"directApiKeyHint"`
	DirectModel             string   `json:"directModel"`
	ScrapeIntervalHours     *int     `json:"scrapeIntervalHours"`
	CustomStockInPatterns   []string `json:"customStockInPatterns"`  // NEW beyond Java — see package doc comment
	CustomStockOutPatterns  []string `json:"customStockOutPatterns"` // NEW beyond Java
}

type SettingsUpdateRequest struct {
	LLMDiscoveryEnabled   *bool   `json:"llmDiscoveryEnabled"`
	LLMMode               *string `json:"llmMode"`
	NotificationsEnabled  *bool   `json:"notificationsEnabled"`
	SelectorAPIBase       *string `json:"selectorApiBase"`
	SelectorAPIKey        *string `json:"selectorApiKey"`
	SelectorModel         *string `json:"selectorModel"`
	DirectAPIBase         *string `json:"directApiBase"`
	DirectAPIKey          *string `json:"directApiKey"`
	DirectModel           *string `json:"directModel"`
	// nil = no change; 0 = explicit disable sentinel (matches
	// SettingsUpdateDto.java exactly); >0 = set/enable at that interval.
	ScrapeIntervalHours *int `json:"scrapeIntervalHours"`
	// nil (omitted) = no change. Non-nil-but-empty ([]) = explicit clear.
	// Non-nil-non-empty = set. Distinguishable in Go without a pointer-to-
	// slice wrapper, since json.Unmarshal leaves an omitted []string field
	// as a true nil slice but produces a non-nil empty slice for `[]` —
	// see resources.go's ApplySettingUpdate for the value this collapses to.
	CustomStockInPatterns  []string `json:"customStockInPatterns"`
	CustomStockOutPatterns []string `json:"customStockOutPatterns"`
}
