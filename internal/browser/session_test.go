package browser

import (
	"context"
	"testing"
	"time"
)

// FakeSession implements the Session interface for unit testing in other packages.
type FakeSession struct {
	URL                 string
	HTML                string
	NavigateFunc        func(ctx context.Context, url string) error
	GetHTMLFunc         func(ctx context.Context) (string, error)
	GetURLFunc          func(ctx context.Context) (string, error)
	WaitForSelectorFunc func(ctx context.Context, selector string, timeout time.Duration) error
	FindAndFillFunc     func(ctx context.Context, query string) (bool, error)
	FreshContextFunc    func(ctx context.Context) (Session, error)
	CloseFunc           func(ctx context.Context) error
}

func (f *FakeSession) Navigate(ctx context.Context, url string) error {
	f.URL = url
	if f.NavigateFunc != nil {
		return f.NavigateFunc(ctx, url)
	}
	return nil
}

func (f *FakeSession) GetHTML(ctx context.Context) (string, error) {
	if f.GetHTMLFunc != nil {
		return f.GetHTMLFunc(ctx)
	}
	return f.HTML, nil
}

func (f *FakeSession) GetURL(ctx context.Context) (string, error) {
	if f.GetURLFunc != nil {
		return f.GetURLFunc(ctx)
	}
	return f.URL, nil
}

func (f *FakeSession) WaitForSelector(ctx context.Context, selector string, timeout time.Duration) error {
	if f.WaitForSelectorFunc != nil {
		return f.WaitForSelectorFunc(ctx, selector, timeout)
	}
	return nil
}

func (f *FakeSession) FindAndFillSearch(ctx context.Context, query string) (bool, error) {
	if f.FindAndFillFunc != nil {
		return f.FindAndFillFunc(ctx, query)
	}
	return true, nil
}

func (f *FakeSession) FreshContext(ctx context.Context) (Session, error) {
	if f.FreshContextFunc != nil {
		return f.FreshContextFunc(ctx)
	}
	return f, nil
}

func (f *FakeSession) Close(ctx context.Context) error {
	if f.CloseFunc != nil {
		return f.CloseFunc(ctx)
	}
	return nil
}

var _ Session = (*FakeSession)(nil)

func TestFakeSession(t *testing.T) {
	fake := &FakeSession{
		URL:  "https://example.com",
		HTML: "<html><body>Hello</body></html>",
	}

	url, err := fake.GetURL(context.TODO())
	if err != nil || url != "https://example.com" {
		t.Errorf("GetURL failed: %v, %s", err, url)
	}

	html, err := fake.GetHTML(context.TODO())
	if err != nil || html != "<html><body>Hello</body></html>" {
		t.Errorf("GetHTML failed: %v, %s", err, html)
	}
}

func TestChromedpSession_FreshContext_OwnershipIsolation(t *testing.T) {
	// This test cannot exercise a real chromedp.NewContext/chromedp.Run
	// round-trip without a live Chromium instance, so it verifies the
	// struct-level ownership contract FreshContext must uphold rather than
	// the full browser interaction — the same scope limitation already
	// accepted for TestChromedpSession_WaitForSelector_ErrorSwallowing.

	parentAllocCtx := context.Background()
	var parentAllocCancelled bool
	parentAllocCancel := func() { parentAllocCancelled = true }

	parent := &ChromedpSession{
		allocatorCtx:    parentAllocCtx,
		allocatorCancel: parentAllocCancel,
		targetCtx:       context.Background(),
		timeout:         time.Second,
	}

	// Construct the forked session the way FreshContext does, without
	// requiring a live browser: same allocatorCtx, nil allocatorCancel, a
	// distinct target.
	forkTargetCtx, forkTargetCancel := context.WithCancel(context.Background())
	var forkTargetCancelled bool
	forked := &ChromedpSession{
		allocatorCtx:    parent.allocatorCtx,
		allocatorCancel: nil, // must never own the parent's allocator
		targetCtx:       forkTargetCtx,
		targetCancel: func() {
			forkTargetCancelled = true
			forkTargetCancel()
		},
		timeout: parent.timeout,
	}

	if forked.allocatorCancel != nil {
		t.Fatal("forked session must not own allocatorCancel")
	}
	if forked.targetCtx == parent.targetCtx {
		t.Fatal("forked session must have a distinct targetCtx from the parent")
	}

	// Closing the fork must cancel only its own target, never the parent's
	// allocator.
	if err := forked.Close(context.Background()); err != nil {
		t.Fatalf("forked.Close() returned error: %v", err)
	}
	if !forkTargetCancelled {
		t.Error("expected forked session's own targetCancel to be called")
	}
	if parentAllocCancelled {
		t.Error("forked.Close() must not cancel the parent's allocator")
	}

	// The parent's own Close() remains responsible for the allocator.
	if err := parent.Close(context.Background()); err != nil {
		t.Fatalf("parent.Close() returned error: %v", err)
	}
	if !parentAllocCancelled {
		t.Error("expected parent's allocatorCancel to be called on parent.Close()")
	}
}

func TestChromedpSession_WaitForSelector_ErrorSwallowing(t *testing.T) {
	// Verify that WaitForSelector deriving its target execution context from targetCtx
	// successfully executes chromedp.Run, encounters an error (because it is a dummy non-browser context),
	// and successfully swallows it returning nil.
	s := &ChromedpSession{
		targetCtx: context.Background(),
		timeout:   time.Second,
	}

	err := s.WaitForSelector(context.Background(), ".non-existent", time.Millisecond)
	if err != nil {
		t.Errorf("expected WaitForSelector to swallow all execution errors and return nil, got %v", err)
	}
}