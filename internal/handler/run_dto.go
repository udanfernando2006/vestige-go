package handler

type RunSummaryResponse struct {
	RunID           string  `json:"runId"`
	TotalPairs      int     `json:"totalPairs"`
	Changes         int     `json:"changes"`
	Errors          int     `json:"errors"`
	DurationSeconds float64 `json:"durationSeconds"`
	LogPath         string  `json:"logPath"`
}

type RunChangeResponse struct {
	PairID     int64    `json:"pairId"`
	BookName   string   `json:"bookName"`
	StoreName  string   `json:"storeName"`
	FromStatus string   `json:"fromStatus"`
	ToStatus   string   `json:"toStatus"`
	FromPrice  *float64 `json:"fromPrice"`
	ToPrice    *float64 `json:"toPrice"`
	ProductURL string   `json:"productUrl"`
}

type RunDetailResponse struct {
	RunID           string              `json:"runId"`
	TotalPairs      int                 `json:"totalPairs"`
	Errors          int                 `json:"errors"`
	DurationSeconds float64             `json:"durationSeconds"`
	Changes         []RunChangeResponse `json:"changes"`
}

// DiscoverResultResponse mirrors DiscoverResultDto.java — that file's own
// header comment ("revert to plain camelCase, no @JsonProperty") is
// preserved here too: no field renaming beyond the struct's own Go names
// lowercased to camelCase.
type DiscoverResultResponse struct {
	PairID        int64   `json:"pairId"`
	PriceSelector *string `json:"priceSelector"`
	StockSelector *string `json:"stockSelector"`
	PriceSample   *string `json:"priceSample"`
	StockSample   *string `json:"stockSample"`
	ModelUsed     string  `json:"modelUsed"`
	Reason        *string `json:"reason"`
	Committed     bool    `json:"committed"`
}
