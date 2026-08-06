// Settings handler.
//
// WORTH NOTING EXPLICITLY: v1's SettingsController.java/SettingsService.java
// exist mostly to handle a REMOTE-call failure mode — SettingsSyncException's
// whole reason to exist is distinguishing "scraper-server rejected our
// input" (400) from "scraper-server unreachable/broke" (502), because
// Java's Settings layer is a proxy over HTTP to a separate Python process.
// Vestige-Go has no such hop: this handler talks to SQLite directly via
// the store layer. That entire exception class and its branching logic
// simply doesn't apply here — not omitted by oversight, genuinely moot.
// ApplySettingUpdate errors below get the plain 500 catch-all instead.
package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/domain"
	"github.com/udanfernando2006/vestige-go/internal/store"
)

func settingsStatusToResponse(s *domain.SettingsStatus) SettingsResponse {
	return SettingsResponse{
		LLMDiscoveryEnabled:      s.LLMDiscoveryEnabled,
		LLMMode:                  s.LLMMode,
		NotificationsEnabled:     s.NotificationsEnabled,
		SelectorAPIBase:          s.SelectorAPIBase,
		SelectorAPIKeyConfigured: s.SelectorAPIKeyConfig,
		SelectorAPIKeyHint:       s.SelectorAPIKeyHint,
		SelectorModel:            s.SelectorModel,
		DirectAPIBase:            s.DirectAPIBase,
		DirectAPIKeyConfigured:   s.DirectAPIKeyConfig,
		DirectAPIKeyHint:         s.DirectAPIKeyHint,
		DirectModel:              s.DirectModel,
		ScrapeIntervalHours:      s.ScrapeIntervalHours,
		CustomStockInPatterns:    s.CustomStockInPatterns,
		CustomStockOutPatterns:   s.CustomStockOutPatterns,
	}
}

func (srv *Server) getSettings(c *gin.Context) {
	status, err := srv.store.GetSettingsStatus(c.Request.Context())
	if handleStoreError(c, err, "") {
		return
	}
	c.JSON(http.StatusOK, settingsStatusToResponse(status))
}

// updateSettings mirrors SettingsController.updateSettings() ->
// SettingsService.updateSettings(), but calls ApplySettingUpdate directly
// per key instead of building a wire-format request body for an HTTP PUT
// (there's no upstream service left to PUT to). Semantics preserved
// exactly: nil = no change, ""/empty = explicit clear, else = set.
func (srv *Server) updateSettings(c *gin.Context) {
	var dto SettingsUpdateRequest
	if err := c.ShouldBindJSON(&dto); err != nil {
		respondValidation(c, map[string]string{"error": "Invalid request body: " + err.Error()})
		return
	}
	if dto.ScrapeIntervalHours != nil && *dto.ScrapeIntervalHours < 0 {
		// Mirrors api_server.py's Field(default=None, ge=0) — a 422 there,
		// kept as a 400 here for consistency with this handler layer's
		// other validation responses.
		respondValidation(c, map[string]string{"scrapeIntervalHours": "must be >= 0"})
		return
	}

	ctx := c.Request.Context()
	// CodeRabbit-flagged, now fully closed rather than just made
	// deterministic: this originally ranged a map[string]*string directly
	// (Go's map iteration order is deliberately randomized by the runtime,
	// so every call processed these 10 keys in a different order), and
	// even after fixing that to a fixed-order slice, each key was still
	// applied via a separate ApplySettingUpdate call against the store
	// directly — no shared transaction, so a failure partway through left
	// whatever subset had already been written committed with no
	// rollback. Both problems are now closed together: every update below
	// (the 10 flat keys AND the two pattern-list keys, folded into the
	// same slice rather than applied via separate calls afterward) goes
	// through ONE store.ApplySettingsBatch call, which runs the whole
	// batch inside a single transaction — either every key commits, or
	// (on any failure, at any point in the sequence) none of them do, and
	// the caller gets a 500 that accurately reflects "nothing changed"
	// rather than an unpredictable partial write.
	updates := []store.SettingUpdateInput{
		{Key: "LLM_DISCOVERY_ENABLED", Value: boolToSettingValue(dto.LLMDiscoveryEnabled)},
		{Key: "LLM_MODE", Value: dto.LLMMode},
		{Key: "NOTIFICATIONS_ENABLED", Value: boolToSettingValue(dto.NotificationsEnabled)},
		{Key: "SELECTOR_API_BASE", Value: dto.SelectorAPIBase},
		{Key: "SELECTOR_API_KEY", Value: dto.SelectorAPIKey},
		{Key: "SELECTOR_MODEL", Value: dto.SelectorModel},
		{Key: "DIRECT_API_BASE", Value: dto.DirectAPIBase},
		{Key: "DIRECT_API_KEY", Value: dto.DirectAPIKey},
		{Key: "DIRECT_MODEL", Value: dto.DirectModel},
		{Key: "SCRAPE_INTERVAL_HOURS", Value: intToSettingValue(dto.ScrapeIntervalHours)},
		// Custom stock patterns: nil slice (JSON field omitted) = no
		// change (patternListValue returns nil, and ApplySettingsBatch's
		// underlying applySettingUpdate already treats a nil Value as a
		// skip — same semantics as every other field here); non-nil empty
		// slice ("[]" sent explicitly) = clear; non-nil non-empty = set,
		// comma-joined. See patternListValue below.
		{Key: "CUSTOM_STOCK_IN_PATTERNS", Value: patternListValue(dto.CustomStockInPatterns)},
		{Key: "CUSTOM_STOCK_OUT_PATTERNS", Value: patternListValue(dto.CustomStockOutPatterns)},
	}
	if err := srv.store.ApplySettingsBatch(ctx, updates); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: " + err.Error()})
		return
	}

	c.Status(http.StatusNoContent)
}

func boolToSettingValue(b *bool) *string {
	if b == nil {
		return nil
	}
	v := strconv.FormatBool(*b)
	return &v
}

func intToSettingValue(n *int) *string {
	if n == nil {
		return nil
	}
	if *n == 0 {
		empty := ""
		return &empty // explicit disable sentinel, matches SettingsUpdateDto.java exactly
	}
	v := strconv.Itoa(*n)
	return &v
}

// patternListValue converts a CustomStockIn/OutPatterns slice into the
// *string value ApplySettingsBatch expects, matching
// resources.go's ApplySettingUpdate value semantics and
// domain.Settings' own documented storage convention (writer.py:
// ",".join(in_stock)): nil slice (JSON field omitted entirely) = nil (no
// change, entry skipped). Non-nil empty slice ("[]" sent explicitly) =
// pointer to "" (explicit clear). Non-nil non-empty = comma-joined.
//
// Replaces the old applyPatternListUpdate, which took *Server and called
// srv.store.ApplySettingUpdate directly as a separate, un-batched call —
// now folded into updateSettings' single ApplySettingsBatch call instead
// (see that function's own comment for why), so this is a pure value
// converter with no store access of its own.
func patternListValue(patterns []string) *string {
	if patterns == nil {
		return nil
	}
	if len(patterns) == 0 {
		empty := ""
		return &empty
	}
	joined := strings.Join(patterns, ",")
	return &joined
}
