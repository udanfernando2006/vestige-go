# smoke_test.ps1 — Vestige-Go API smoke test
#
# Targets the specific areas flagged during the Phase 3 port as
# placeholder/inferred rather than fully source-verified:
#   1. Settings — maskHint() placeholder format, customStock*Patterns
#      nil/[]/set clear semantics
#   2. Tracking pairs — the full NEEDS_SETUP<->PENDING/SKIP-exempt
#      auto-transition state machine (all four branches)
#   3. Books — grouped-by-series response shape, bulkAssignSeries
#      exactly-one-of validation, ""=clear vs omitted=no-change
#
# Run with the server already up: go run ./cmd/vestige
# Then, in a second terminal:      .\smoke_test.ps1
#
# Each section prints what it did and the raw response — read the
# response against the "EXPECT" comment above each call, not just
# whether it errored. A 200 with the WRONG shape is still a bug.

$base = "http://localhost:8080/api"
$ErrorActionPreference = "Stop"

function Section($title) {
    Write-Host "`n=== $title ===" -ForegroundColor Cyan
}

function Show($label, $obj) {
    Write-Host "-- $label --" -ForegroundColor DarkGray
    $obj | ConvertTo-Json -Depth 6
}

# =============================================================================
# 0. Prerequisites — a store and two books, needed by every later section
# =============================================================================
Section "0. Setup: store + books"

$store = Invoke-RestMethod -Uri "$base/stores" -Method Post -ContentType "application/json" -Body (@{
    name = "Smoke Test Store"; baseUrl = "https://example.test"
} | ConvertTo-Json)
Show "created store" $store

$book1 = Invoke-RestMethod -Uri "$base/books" -Method Post -ContentType "application/json" -Body (@{
    name = "Book One"; isbn = "9780000000001"; isSeriesEntry = $true; seriesName = "Test Series A"
} | ConvertTo-Json)
Show "created book1 (with seriesName -> should auto-create 'Test Series A')" $book1

$book2 = Invoke-RestMethod -Uri "$base/books" -Method Post -ContentType "application/json" -Body (@{
    name = "Book Two"; isbn = "9780000000002"
} | ConvertTo-Json)
Show "created book2 (standalone, no seriesName)" $book2

# =============================================================================
# 1. Books — grouping, bulk-assign validation, ""=clear vs omitted=no-change
# =============================================================================
Section "1a. GET /api/books — EXPECT two groups: 'Test Series A' [book1], null/standalone [book2]"
$grouped = Invoke-RestMethod -Uri "$base/books" -Method Get
Show "grouped books" $grouped

Section "1b. bulkAssignSeries with BOTH seriesId and newSeriesName — EXPECT 400"
try {
    Invoke-RestMethod -Uri "$base/books/series" -Method Patch -ContentType "application/json" -Body (@{
        bookIds = @($book2.id); seriesId = 1; newSeriesName = "Should Fail"
    } | ConvertTo-Json)
    Write-Host "!! Did NOT get an error — this is a bug, should have been 400" -ForegroundColor Red
} catch {
    Show "expected 400 error" $_.ErrorDetails.Message
}

Section "1c. bulkAssignSeries with NEITHER seriesId nor newSeriesName — EXPECT 400"
try {
    Invoke-RestMethod -Uri "$base/books/series" -Method Patch -ContentType "application/json" -Body (@{
        bookIds = @($book2.id)
    } | ConvertTo-Json)
    Write-Host "!! Did NOT get an error — this is a bug, should have been 400" -ForegroundColor Red
} catch {
    Show "expected 400 error" $_.ErrorDetails.Message
}

Section "1d. bulkAssignSeries book2 -> new series 'Test Series B' — EXPECT 200, book2 reassigned"
$bulkResult = Invoke-RestMethod -Uri "$base/books/series" -Method Patch -ContentType "application/json" -Body (@{
    bookIds = @($book2.id); newSeriesName = "Test Series B"
} | ConvertTo-Json)
Show "bulk-assigned books" $bulkResult

Section "1e. GET /api/books again — EXPECT book2 now under 'Test Series B'"
$grouped2 = Invoke-RestMethod -Uri "$base/books" -Method Get
Show "grouped books after reassignment" $grouped2

Section "1f. PATCH book1 author = 'Original Author' — EXPECT author set"
$b1 = Invoke-RestMethod -Uri "$base/books/$($book1.id)" -Method Patch -ContentType "application/json" -Body (@{
    author = "Original Author"
} | ConvertTo-Json)
Show "after setting author" $b1

Section "1g. PATCH book1 with EMPTY body {} — EXPECT author UNCHANGED (still 'Original Author')"
$b1b = Invoke-RestMethod -Uri "$base/books/$($book1.id)" -Method Patch -ContentType "application/json" -Body "{}"
Show "after empty-body patch (should be no-op)" $b1b

Section "1h. PATCH book1 with author = '' (empty string) — EXPECT author CLEARED (null)"
$b1c = Invoke-RestMethod -Uri "$base/books/$($book1.id)" -Method Patch -ContentType "application/json" -Body (@{
    author = ""
} | ConvertTo-Json)
Show "after clearing author" $b1c

# =============================================================================
# 2. Tracking pairs — full auto-transition state machine, all four branches
# =============================================================================
Section "2a. Create tracking pair (book1 x store) — EXPECT status=PENDING, selectorsCached=false"
$pair = Invoke-RestMethod -Uri "$base/tracking" -Method Post -ContentType "application/json" -Body (@{
    isbn = "9780000000001"; storeName = "Smoke Test Store"
} | ConvertTo-Json)
Show "created pair" $pair
$pairId = $pair.id

Section "2b. PATCH both selectors while status=PENDING (not NEEDS_SETUP) — EXPECT status STAYS PENDING (branch only fires from NEEDS_SETUP)"
$p2 = Invoke-RestMethod -Uri "$base/tracking/$pairId" -Method Patch -ContentType "application/json" -Body (@{
    priceSelector = ".price"; stockSelector = ".stock"
} | ConvertTo-Json)
Show "after setting both selectors from PENDING" $p2

Section "2c. Force status=NEEDS_SETUP explicitly — EXPECT status=NEEDS_SETUP (explicit always wins)"
$p3 = Invoke-RestMethod -Uri "$base/tracking/$pairId" -Method Patch -ContentType "application/json" -Body (@{
    status = "NEEDS_SETUP"
} | ConvertTo-Json)
Show "after forcing NEEDS_SETUP" $p3

Section "2d. PATCH both selectors again while status=NEEDS_SETUP — EXPECT auto-transition to PENDING, selectorFoundAt SET"
$p4 = Invoke-RestMethod -Uri "$base/tracking/$pairId" -Method Patch -ContentType "application/json" -Body (@{
    priceSelector = ".price2"; stockSelector = ".stock2"
} | ConvertTo-Json)
Show "after re-setting both selectors from NEEDS_SETUP (watch for auto -> PENDING)" $p4

Section "2e. Clear priceSelector only (status currently PENDING, not SKIP) — EXPECT auto-transition to NEEDS_SETUP"
$p5 = Invoke-RestMethod -Uri "$base/tracking/$pairId" -Method Patch -ContentType "application/json" -Body (@{
    priceSelector = ""
} | ConvertTo-Json)
Show "after clearing priceSelector" $p5

Section "2f. Force status=SKIP explicitly"
$p6 = Invoke-RestMethod -Uri "$base/tracking/$pairId" -Method Patch -ContentType "application/json" -Body (@{
    status = "SKIP"
} | ConvertTo-Json)
Show "after forcing SKIP" $p6

Section "2g. Clear stockSelector while status=SKIP — EXPECT status STAYS SKIP (the SKIP-exemption check)"
$p7 = Invoke-RestMethod -Uri "$base/tracking/$pairId" -Method Patch -ContentType "application/json" -Body (@{
    stockSelector = ""
} | ConvertTo-Json)
Show "after clearing stockSelector under SKIP (should NOT move to NEEDS_SETUP)" $p7

# =============================================================================
# 3. Settings — maskHint placeholder + customStock*Patterns clear semantics
# =============================================================================
Section "3a. PUT a fake selectorApiKey — then GET to see the masked hint format"
Invoke-RestMethod -Uri "$base/settings" -Method Put -ContentType "application/json" -Body (@{
    selectorApiKey = "sk-test-1234567890abcdef"
} | ConvertTo-Json) | Out-Null
$s1 = Invoke-RestMethod -Uri "$base/settings" -Method Get
Show "settings after setting selectorApiKey (check selectorApiKeyHint format — this is a placeholder, judge if it's acceptable)" $s1

Section "3b. PUT customStockInPatterns = [two values] — EXPECT they show up on GET"
Invoke-RestMethod -Uri "$base/settings" -Method Put -ContentType "application/json" -Body (@{
    customStockInPatterns = @("low stock", "few left")
} | ConvertTo-Json) | Out-Null
$s2 = Invoke-RestMethod -Uri "$base/settings" -Method Get
Show "settings after setting customStockInPatterns" $s2

Section "3c. PUT with EMPTY body {} — EXPECT customStockInPatterns UNCHANGED (still the two values above)"
Invoke-RestMethod -Uri "$base/settings" -Method Put -ContentType "application/json" -Body "{}" | Out-Null
$s3 = Invoke-RestMethod -Uri "$base/settings" -Method Get
Show "settings after empty-body PUT (should be a no-op)" $s3

Section "3d. PUT customStockInPatterns = [] (explicit empty array) — EXPECT CLEARED (empty/null)"
Invoke-RestMethod -Uri "$base/settings" -Method Put -ContentType "application/json" -Body (@{
    customStockInPatterns = @()
} | ConvertTo-Json) | Out-Null
$s4 = Invoke-RestMethod -Uri "$base/settings" -Method Get
Show "settings after explicit-empty-array PUT (should now be cleared)" $s4

# =============================================================================
# 4. Availability — just confirm it doesn't error with no snapshot data yet
# =============================================================================
Section "4. GET /api/availability and /api/availability/history — EXPECT empty arrays, no error"
$avail = Invoke-RestMethod -Uri "$base/availability" -Method Get
Show "current availability (expect empty, no snapshots exist yet)" $avail
$hist = Invoke-RestMethod -Uri "$base/availability/history" -Method Get
Show "availability history (expect empty)" $hist

Write-Host "`n=== Done. Review each EXPECT comment above against the actual output. ===" -ForegroundColor Green
