package domain

import (
	"encoding/json"
	"testing"
	"time"
)

func TestModelsInitialization(t *testing.T) {
	// Test Series
	author := "Author Name"
	desc := "Series Description"
	series := Series{
		ID:          1,
		Name:        "Test Series",
		Author:      &author,
		Description: &desc,
	}
	if series.ID != 1 || series.Name != "Test Series" || *series.Author != "Author Name" || *series.Description != "Series Description" {
		t.Errorf("Series initialization failed: %+v", series)
	}

	// Test Book
	isbn := "1234567890"
	seriesID := int64(1)
	book := Book{
		ID:            2,
		Name:          "Test Book",
		ISBN:          isbn,
		IsSeriesEntry: true,
		Author:        &author,
		Description:   &desc,
		SeriesID:      &seriesID,
	}
	if book.ID != 2 || book.Name != "Test Book" || book.ISBN != isbn || !book.IsSeriesEntry || *book.SeriesID != 1 {
		t.Errorf("Book initialization failed: %+v", book)
	}

	// Test Store
	searchTemplate := "https://example.com/search?q={isbn}"
	store := Store{
		ID:                3,
		Name:              "Test Store",
		BaseURL:           "https://example.com",
		SearchURLTemplate: &searchTemplate,
	}
	if store.ID != 3 || store.Name != "Test Store" || store.BaseURL != "https://example.com" || *store.SearchURLTemplate != searchTemplate {
		t.Errorf("Store initialization failed: %+v", store)
	}

	// Test TrackingPair
	prodURL := "https://example.com/product/1"
	priceSel := ".price"
	stockSel := ".stock"
	foundAt := time.Now().UTC()
	tp := TrackingPair{
		ID:              4,
		BookID:          2,
		StoreID:         3,
		ProductURL:      &prodURL,
		PriceSelector:   &priceSel,
		StockSelector:   &stockSel,
		Status:          "PENDING",
		SelectorFoundAt: &foundAt,
	}
	if tp.ID != 4 || tp.BookID != 2 || tp.StoreID != 3 || *tp.ProductURL != prodURL || tp.Status != "PENDING" || tp.SelectorFoundAt.IsZero() {
		t.Errorf("TrackingPair initialization failed: %+v", tp)
	}

	// Test AvailabilitySnapshot
	inStock := true
	price := 9.99
	source := "scraper"
	snapshot := AvailabilitySnapshot{
		ID:        5,
		PairID:    4,
		InStock:   &inStock,
		Price:     &price,
		Status:    "IN_STOCK",
		Source:    &source,
		ScrapedAt: foundAt,
	}
	if snapshot.ID != 5 || snapshot.PairID != 4 || !*snapshot.InStock || *snapshot.Price != 9.99 || snapshot.Status != "IN_STOCK" || *snapshot.Source != "scraper" || snapshot.ScrapedAt.IsZero() {
		t.Errorf("AvailabilitySnapshot initialization failed: %+v", snapshot)
	}

	// Test SettingOverride
	setting := SettingOverride{
		Key:         "TEST_KEY",
		Value:       "TEST_VALUE",
		IsEncrypted: false,
	}
	if setting.Key != "TEST_KEY" || setting.Value != "TEST_VALUE" || setting.IsEncrypted {
		t.Errorf("SettingOverride initialization failed: %+v", setting)
	}
}

func TestModelsJSON(t *testing.T) {
	// Test JSON Marshalling/Unmarshalling on one model to ensure tags are working
	author := "Test Author"
	series := Series{
		ID:     10,
		Name:   "JSON Series",
		Author: &author,
	}

	data, err := json.Marshal(series)
	if err != nil {
		t.Fatalf("failed to marshal Series: %v", err)
	}

	var series2 Series
	if err := json.Unmarshal(data, &series2); err != nil {
		t.Fatalf("failed to unmarshal Series: %v", err)
	}

	if series2.ID != series.ID || series2.Name != series.Name || *series2.Author != *series.Author || series2.Description != nil {
		t.Errorf("JSON serialization/deserialization mismatch: expected %+v, got %+v", series, series2)
	}
}
