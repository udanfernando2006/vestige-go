// Package domain defines the core domain models used throughout the vestige-go pipeline.
// See vestige_go_guide.md Section 5.
package domain

import (
	"time"
)

// AvailabilityResult represents the outcome of an availability scrape run.
// It is a direct port of scraper/models/result.py's AvailabilityResult dataclass.
//
// Fields use pointers to match the Python Optional/None values, representing
// absent fields explicitly.
type AvailabilityResult struct {
	InStock      *bool      `json:"in_stock"`
	Price        *float64   `json:"price"`
	Currency     *string    `json:"currency"`
	RawPriceText *string    `json:"raw_price_text"`
	RawStockText *string    `json:"raw_stock_text"`
	ScrapedAt    *time.Time `json:"scraped_at"`
	Status       *string    `json:"status"` // IN_STOCK, OUT_OF_STOCK, ERROR
	Reason       *string    `json:"reason"` // For errors: "selector_not_found", "http_error_503", etc.
	Source       *string    `json:"source"` // "scraper" | "llm_direct"
}

// NewAvailabilityResult creates a new AvailabilityResult with ScrapedAt initialized
// to the current UTC time, mirroring the Python default factory value.
func NewAvailabilityResult() *AvailabilityResult {
	now := time.Now().UTC()
	return &AvailabilityResult{
		ScrapedAt: &now,
	}
}
