package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/udanfernando2006/vestige-go/internal/domain"
)

// MockPairStore implements the PairStore interface for unit testing.
type MockPairStore struct {
	ActivePairs         []ActivePair
	Pair                *ActivePair
	PairsNeedingSetup   []NeedsSetupPair
	Store               *domain.Store
	LastSnapshot        *domain.AvailabilitySnapshot
	WrittenSnapshot     *domain.AvailabilitySnapshot
	Settings            map[string]string
	Err                 error
	WriteSnapshotFunc   func(ctx context.Context, pairID int64, result *domain.AvailabilityResult) (*domain.AvailabilitySnapshot, error)
	UpdateSelectorsFunc func(ctx context.Context, pairID int64, priceSel, stockSel string) error
}

func (m *MockPairStore) GetActivePairs(ctx context.Context) ([]ActivePair, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.ActivePairs, nil
}

func (m *MockPairStore) GetPair(ctx context.Context, pairID int64) (*ActivePair, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Pair, nil
}

func (m *MockPairStore) GetPairsNeedingSetup(ctx context.Context) ([]NeedsSetupPair, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.PairsNeedingSetup, nil
}

func (m *MockPairStore) GetStore(ctx context.Context, storeID int64) (*domain.Store, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Store, nil
}

func (m *MockPairStore) GetLastSnapshot(ctx context.Context, pairID int64) (*domain.AvailabilitySnapshot, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.LastSnapshot, nil
}

func (m *MockPairStore) WriteSnapshot(ctx context.Context, pairID int64, result *domain.AvailabilityResult) (*domain.AvailabilitySnapshot, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	if m.WriteSnapshotFunc != nil {
		return m.WriteSnapshotFunc(ctx, pairID, result)
	}
	return m.WrittenSnapshot, nil
}

func (m *MockPairStore) UpdatePairStatus(ctx context.Context, pairID int64, status string) error {
	return m.Err
}

func (m *MockPairStore) UpdatePairURL(ctx context.Context, pairID int64, productURL string) error {
	return m.Err
}

func (m *MockPairStore) UpdatePairSelectors(ctx context.Context, pairID int64, priceSel, stockSel string) error {
	if m.Err != nil {
		return m.Err
	}
	if m.UpdateSelectorsFunc != nil {
		return m.UpdateSelectorsFunc(ctx, pairID, priceSel, stockSel)
	}
	return nil
}

func (m *MockPairStore) ClearPairSelectors(ctx context.Context, pairID int64) error {
	return m.Err
}

func (m *MockPairStore) UpdateStoreSearchTemplate(ctx context.Context, storeID int64, templateURL string) error {
	return m.Err
}

func (m *MockPairStore) GetSettings(ctx context.Context) (map[string]string, error) {
	if m.Err != nil {
		return nil, m.Err
	}
	return m.Settings, nil
}

// Compile-time check to ensure MockPairStore implements PairStore interface.
var _ PairStore = (*MockPairStore)(nil)

func TestMockPairStore(t *testing.T) {
	ctx := context.TODO()

	// 1. Test error propagation
	expectedErr := errors.New("db error")
	mock := &MockPairStore{Err: expectedErr}

	_, err := mock.GetActivePairs(ctx)
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}

	// 2. Test active pairs retrieval returning joined struct shapes
	urlStr := "https://example.com/prod"
	selFound := time.Now().UTC()
	activePairs := []ActivePair{
		{
			ID:              1,
			BookID:          2,
			StoreID:         3,
			ProductURL:      &urlStr,
			Status:          "PENDING",
			SelectorFoundAt: &selFound,
			BookName:        "Test Book",
			BookISBN:        "1234567890",
			StoreName:       "Test Store",
		},
	}

	mock = &MockPairStore{ActivePairs: activePairs}
	res, err := mock.GetActivePairs(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res) != 1 || res[0].BookName != "Test Book" || res[0].BookISBN != "1234567890" || res[0].StoreName != "Test Store" {
		t.Errorf("MockPairStore returned incorrect joined struct: %+v", res)
	}

	// 3. Test functional override on WriteSnapshot
	inStock := true
	priceVal := 450.00
	sourceStr := "scraper"
	called := false

	mock.WriteSnapshotFunc = func(ctx context.Context, pairID int64, result *domain.AvailabilityResult) (*domain.AvailabilitySnapshot, error) {
		called = true
		if pairID != 99 {
			t.Errorf("expected pairID 99, got %d", pairID)
		}
		return &domain.AvailabilitySnapshot{
			ID:        123,
			PairID:    pairID,
			InStock:   result.InStock,
			Price:     result.Price,
			Status:    *result.Status,
			Source:    result.Source,
			ScrapedAt: *result.ScrapedAt,
		}, nil
	}

	resSnap, err := mock.WriteSnapshot(ctx, 99, &domain.AvailabilityResult{
		InStock:   &inStock,
		Price:     &priceVal,
		Status:    &sourceStr,
		Source:    &sourceStr,
		ScrapedAt: &selFound,
	})

	if err != nil {
		t.Fatalf("WriteSnapshot failed: %v", err)
	}
	if !called {
		t.Error("expected WriteSnapshotFunc to be called")
	}
	if resSnap.ID != 123 || resSnap.PairID != 99 || *resSnap.Price != 450.00 {
		t.Errorf("unexpected snapshot returned: %+v", resSnap)
	}
}
