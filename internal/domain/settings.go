// Package domain defines the core domain models used throughout the vestige-go pipeline.
// See vestige_go_guide.md Section 5.
package domain

// Settings is the typed effective-configuration shape for all 11 setting_overrides
// keys (see writer.py's SETTINGS_KEYS / vestige_guide.md Section 6).
//
// Resolved per migration blueprint §7 item 12: typed struct, not a stringly-typed
// map[string]string. Rationale (decided in conversation, not yet written back into
// the blueprint doc): the settings list is small and closed by design — adding a
// 12th key is "add a field + a line in the store-layer mapping," the same cost as
// adding a map key — while a typed struct removes the .get(key, "")/manual
// strip().lower() parsing scattered across writer.py/Orchestrator in Python, and
// gives Wails' Go->TypeScript binding generation a real typed interface on the
// frontend instead of Record<string, string>.
//
// A field is a pointer ONLY where the Python source's absence-vs-empty-string
// distinction is actually meaningful (ScrapeIntervalHours: nil = disabled, matches
// "" in v1's DB-override table meaning "fall back to unset"). Plain string fields
// default to "" for "not configured," matching get_settings()'s own env-fallback
// default of "" — there is no separate absent/empty distinction for those in the
// Python source, so a pointer there would add a state Python's own code never had.
type Settings struct {
	LLMDiscoveryEnabled bool   `json:"llm_discovery_enabled"`
	LLMMode             string `json:"llm_mode"` // "selector" | "direct"
	NotificationsEnabled bool  `json:"notifications_enabled"`

	SelectorAPIBase string `json:"selector_api_base"`
	SelectorAPIKey  string `json:"selector_api_key"` // secret — never echoed by handlers, see writer.py get_settings_status()
	SelectorModel   string `json:"selector_model"`

	DirectAPIBase string `json:"direct_api_base"`
	DirectAPIKey  string `json:"direct_api_key"` // secret, same handling as SelectorAPIKey
	DirectModel   string `json:"direct_model"`

	// ScrapeIntervalHours: nil = scheduling disabled (matches v1's "unset in
	// setting_overrides, or explicit \"\" override" meaning "no schedule").
	// A non-nil 0 is deliberately not used as the disable sentinel here, unlike
	// the Java SettingsUpdateDto's incoming-request convention (0 = explicit
	// disable over the wire) — that wire-format sentinel is a request-shape
	// concern for the future Settings handler (Phase 3) to translate into
	// nil when calling the store layer, not something this struct itself
	// should encode. Keeping those two concerns separate on purpose.
	ScrapeIntervalHours *int `json:"scrape_interval_hours"`

	// CustomStockInPatterns / CustomStockOutPatterns: comma-separated regex in
	// v1's stored representation (writer.py: ",".join(in_stock)), represented
	// here as []string — the store layer's GetSettings()/ApplySettingUpdate()
	// implementation owns the split/join at the DB boundary, so every other
	// caller (heuristics.ClassifyStockText, the future Settings UI/handler)
	// works with a real slice, not a delimited string it has to parse itself.
	CustomStockInPatterns  []string `json:"custom_stock_in_patterns"`
	CustomStockOutPatterns []string `json:"custom_stock_out_patterns"`
}

// SettingsStatus mirrors get_settings_status()'s outgoing shape: secret fields
// collapse to "configured + masked hint," never plaintext. Distinct type from
// Settings itself for the same wire-isolation reason vestige_api_implementation.md
// documents for DiscoverToolOutput/ScraperSettingsResponse — a struct that can
// carry a real secret must never be the same type serialized back out over a
// handler boundary.
type SettingsStatus struct {
	LLMDiscoveryEnabled bool   `json:"llm_discovery_enabled"`
	LLMMode             string `json:"llm_mode"`
	NotificationsEnabled bool  `json:"notifications_enabled"`

	SelectorAPIBase        string  `json:"selector_api_base"`
	SelectorAPIKeyConfig   bool    `json:"selector_api_key_configured"`
	SelectorAPIKeyHint     *string `json:"selector_api_key_hint"`
	SelectorModel          string  `json:"selector_model"`

	DirectAPIBase      string  `json:"direct_api_base"`
	DirectAPIKeyConfig bool    `json:"direct_api_key_configured"`
	DirectAPIKeyHint   *string `json:"direct_api_key_hint"`
	DirectModel        string  `json:"direct_model"`

	ScrapeIntervalHours    *int     `json:"scrape_interval_hours"`
	CustomStockInPatterns  []string `json:"custom_stock_in_patterns"`
	CustomStockOutPatterns []string `json:"custom_stock_out_patterns"`
}
