package domain

import (
	"testing"
	"time"
)

func TestNewAvailabilityResult(t *testing.T) {
	before := time.Now().UTC()
	result := NewAvailabilityResult()
	after := time.Now().UTC()

	if result.ScrapedAt == nil {
		t.Fatal("expected ScrapedAt to be initialized, got nil")
	}

	// Verify ScrapedAt is in UTC timezone
	if result.ScrapedAt.Location() != time.UTC {
		t.Errorf("expected ScrapedAt to be UTC, got %v", result.ScrapedAt.Location())
	}

	// Verify ScrapedAt is within the test window
	if result.ScrapedAt.Before(before.Add(-time.Second)) || result.ScrapedAt.After(after.Add(time.Second)) {
		t.Errorf("expected ScrapedAt to be between %v and %v, got %v", before, after, *result.ScrapedAt)
	}

	// Verify all other pointer fields are initialized to nil
	if result.InStock != nil {
		t.Errorf("expected InStock to be nil, got %v", *result.InStock)
	}
	if result.Price != nil {
		t.Errorf("expected Price to be nil, got %v", *result.Price)
	}
	if result.Currency != nil {
		t.Errorf("expected Currency to be nil, got %s", *result.Currency)
	}
	if result.RawPriceText != nil {
		t.Errorf("expected RawPriceText to be nil, got %s", *result.RawPriceText)
	}
	if result.RawStockText != nil {
		t.Errorf("expected RawStockText to be nil, got %s", *result.RawStockText)
	}
	if result.Status != nil {
		t.Errorf("expected Status to be nil, got %s", *result.Status)
	}
	if result.Reason != nil {
		t.Errorf("expected Reason to be nil, got %s", *result.Reason)
	}
	if result.Source != nil {
		t.Errorf("expected Source to be nil, got %s", *result.Source)
	}
}

func TestAvailabilityResultFields(t *testing.T) {
	inStock := true
	price := 1250.00
	currency := "LKR"
	rawPriceText := "Rs. 1,250"
	rawStockText := "In Stock"
	status := "IN_STOCK"
	reason := ""
	source := "scraper"

	result := &AvailabilityResult{
		InStock:      &inStock,
		Price:        &price,
		Currency:     &currency,
		RawPriceText: &rawPriceText,
		RawStockText: &rawStockText,
		Status:       &status,
		Reason:       &reason,
		Source:       &source,
	}

	if result.InStock == nil || *result.InStock != inStock {
		t.Errorf("expected InStock %v, got %v", inStock, result.InStock)
	}
	if result.Price == nil || *result.Price != price {
		t.Errorf("expected Price %v, got %v", price, result.Price)
	}
	if result.Currency == nil || *result.Currency != currency {
		t.Errorf("expected Currency %q, got %v", currency, result.Currency)
	}
	if result.RawPriceText == nil || *result.RawPriceText != rawPriceText {
		t.Errorf("expected RawPriceText %q, got %v", rawPriceText, result.RawPriceText)
	}
	if result.RawStockText == nil || *result.RawStockText != rawStockText {
		t.Errorf("expected RawStockText %q, got %v", rawStockText, result.RawStockText)
	}
	if result.Status == nil || *result.Status != status {
		t.Errorf("expected Status %q, got %v", status, result.Status)
	}
	if result.Reason == nil || *result.Reason != reason {
		t.Errorf("expected Reason %q, got %v", reason, result.Reason)
	}
	if result.Source == nil || *result.Source != source {
		t.Errorf("expected Source %q, got %v", source, result.Source)
	}
}
