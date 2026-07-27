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
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/udanfernando2006/vestige-go/internal/domain"
)

func settingsStatusToResponse(s *domain.SettingsStatus) SettingsResponse {
	return SettingsResponse{
		LLMDiscoveryEnabled:      s.LLMDiscoveryEnabled,
		LLMMode:                  s.LLMMode,
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
	updates := map[string]*string{
		"LLM_DISCOVERY_ENABLED": boolToSettingValue(dto.LLMDiscoveryEnabled),
		"LLM_MODE":               dto.LLMMode,
		"SELECTOR_API_BASE":      dto.SelectorAPIBase,
		"SELECTOR_API_KEY":       dto.SelectorAPIKey,
		"SELECTOR_MODEL":         dto.SelectorModel,
		"DIRECT_API_BASE":        dto.DirectAPIBase,
		"DIRECT_API_KEY":         dto.DirectAPIKey,
		"DIRECT_MODEL":           dto.DirectModel,
		"SCRAPE_INTERVAL_HOURS":  intToSettingValue(dto.ScrapeIntervalHours),
	}
	for key, value := range updates {
		if err := srv.store.ApplySettingUpdate(ctx, key, value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: " + err.Error()})
			return
		}
	}

	// Custom stock patterns: nil slice (JSON field omitted) = no change;
	// non-nil empty slice ("[]" sent explicitly) = clear; non-nil
	// non-empty = set. See dto.go's SettingsUpdateRequest doc comment for
	// why this distinction works without a pointer-to-slice wrapper.
	if err := applyPatternListUpdate(ctx, srv, "CUSTOM_STOCK_IN_PATTERNS", dto.CustomStockInPatterns); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal server error: " + err.Error()})
		return
	}
	if err := applyPatternListUpdate(ctx, srv, "CUSTOM_STOCK_OUT_PATTERNS", dto.CustomStockOutPatterns); err != nil {
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

// applyPatternListUpdate: nil slice (JSON field omitted entirely) = no
// change, skip the call. Non-nil empty slice ("[]" sent explicitly) =
// clear (pointer to ""). Non-nil non-empty = set, comma-joined — matches
// domain.Settings' own documented storage convention (writer.py:
// ",".join(in_stock)) and resources.go's ApplySettingUpdate value semantics.
func applyPatternListUpdate(ctx context.Context, srv *Server, key string, patterns []string) error {
	if patterns == nil {
		return nil
	}
	if len(patterns) == 0 {
		empty := ""
		return srv.store.ApplySettingUpdate(ctx, key, &empty)
	}
	joined := strings.Join(patterns, ",")
	return srv.store.ApplySettingUpdate(ctx, key, &joined)
}
