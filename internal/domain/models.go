// Package domain defines the core domain models used throughout the vestige-go pipeline.
// See vestige_go_guide.md Section 5.
package domain

import (
	"time"
)

// Series represents a book series.
// Ports from Series in scraper/db/models.py.
type Series struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Author      *string `json:"author"`
	Description *string `json:"description"`
}

// Book represents a book entity.
// Ports from Book in scraper/db/models.py.
type Book struct {
	ID            int64   `json:"id"`
	Name          string  `json:"name"`
	ISBN          string  `json:"isbn"`
	IsSeriesEntry bool    `json:"is_series_entry"`
	Author        *string `json:"author"`
	Description   *string `json:"description"`
	SeriesID      *int64  `json:"series_id"`
}

// Store represents a book store vendor.
// Ports from Store in scraper/db/models.py.
type Store struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	BaseURL           string  `json:"base_url"`
	SearchURLTemplate *string `json:"search_url_template"`
}

// TrackingPair represents a specific book tracked on a specific store.
// Ports from TrackingPair in scraper/db/models.py.
type TrackingPair struct {
	ID              int64      `json:"id"`
	BookID          int64      `json:"book_id"`
	StoreID         int64      `json:"store_id"`
	ProductURL      *string    `json:"product_url"`
	PriceSelector   *string    `json:"price_selector"`
	StockSelector   *string    `json:"stock_selector"`
	Status          string     `json:"status"` // Default "PENDING"
	SelectorFoundAt *time.Time `json:"selector_found_at"`
}

// AvailabilitySnapshot represents a recorded availability result at a point in time.
// Ports from AvailabilitySnapshot in scraper/db/models.py.
//
// NOTE: The SQLite schema must include the composite index:
// idx_pair_scraped_desc ON availability_snapshots (pair_id, scraped_at DESC)
// as it is required for efficient retrieval of the last snapshot.
type AvailabilitySnapshot struct {
	ID      int64    `json:"id"`
	PairID  int64    `json:"pair_id"`
	InStock *bool    `json:"in_stock"`
	Price   *float64 `json:"price"` // Representing Decimal(10,2) as float64
	Status  string   `json:"status"`
	Source  *string  `json:"source"`
	// ScrapedAt is timezone-aware and must be persisted/retrieved retaining UTC.
	ScrapedAt time.Time `json:"scraped_at"`
}

// SettingOverride represents key-value store configurations.
// Ports from SettingOverride in scraper/db/models.py.
type SettingOverride struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	IsEncrypted bool   `json:"is_encrypted"`
}
