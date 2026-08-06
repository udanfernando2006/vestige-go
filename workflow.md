# Vestige-Go — Migration Workflow (Antigravity)

Procedural companion to the `vestige-go` lean blueprint set (`vestige_go_guide.md`, `vestige_go_pipeline_implementation.md`, `vestige_go_migration_blueprint.md`) — those documents own architecture; this one owns *sequencing and discipline* for the actual migration work happening in this `/go` staging directory. Read the relevant lean guide before porting any file; this document doesn't restate architecture it doesn't need to.

**Working relationship:** Antigravity has direct access to the real `scraper/` Python source and writes real Go files here, one at a time. Claude (in a separate chat) reviews each finished file against the real Python source and this workflow's conventions, then hands back revisions. Nothing here is final until that review happens — Antigravity should not treat "compiles and passes tests" as equivalent to "approved."

---

## 1. Ground Rules

- **Source-first, always.** Before writing any Go file, open and read the actual Python source file(s) it ports from. Never write implementation logic from a blueprint doc's prose summary alone — blueprint prose is architectural context, not ground truth for code-level details. Where a blueprint's description and the real source disagree, the real source wins; note the discrepancy in the file's own doc comment rather than silently reconciling it.
- **One file per session, full stop.** See Section 5 — this is a rate-limit necessity, not a style preference.
- **Explicit over silent.** A genuinely ambiguous design point (not already covered by a settled decision) gets flagged in the file's doc comment with a clearly-labeled best-guess choice — never silently resolved, never left blocking either.
- **Don't re-litigate settled decisions.** The three Phase 1 interfaces (`store.PairStore`, `browser.Session`, `llm.Client`), the dropped standalone discovery CLI, and `Discoverer`'s `FreshContext`-based session handling are already decided in `vestige_go_pipeline_implementation.md` Sections 3 and 5. Implement against them; don't redesign them mid-port.
- **Extend, don't regenerate, already-completed files.** See Section 2 — several files are partially ported already. Add to them; don't rewrite them from scratch.

---

## 2. Already Completed — Do Not Redo

| File | Contains | Status |
| --- | --- | --- |
| `internal/pipeline/heuristics/stock_status.go` | `ClassifyStockText` (from `scraper.py::parse_stock_status`) | Done, build-verified |
| `internal/pipeline/heuristics/candidate_scoring.go` | `ScoreCandidates` (from `crawler.py::_score_candidates`) | Done, build-verified |
| `internal/pipeline/heuristics/validation_scoring.go` | `ScoreValidation` (from `crawler.py::_validate_from_html`) | Done, syntax-verified (goquery calls unrun in review sandbox — verify for real here) |
| `internal/pipeline/scraper.go` | `ParsePrice`, `CheckResponseStatus` (from `scraper.py`) | **Partial** — pure functions only; the `Scraper` struct itself is not yet ported |
| `internal/pipeline/crawler.go` | `BuildSearchURL` (from `crawler.py::_build_search_url`) | **Partial** — pure function only; the `Crawler` struct itself is not yet ported |

When a session's target is one of the two partial files, open the existing file, keep everything in it, and add the new logic alongside — don't regenerate the file.

---

## 3. Migration Order & File Map

Work top to bottom. Each row is (at minimum) one session.

| Target | Ports from | Depends on | Status |
| --- | --- | --- | --- |
| `internal/domain/result.go` | `models/result.py` | none | Pending — do first, unblocks everything below |
| `internal/domain/models.go` | `models.py` (entity shapes referenced by `writer.py`) | none | Pending |
| `internal/store/store.go` | `writer.py` (`DBWriter`'s public method surface → interface only, no implementation) | domain | Pending |
| `internal/browser/session.go` | `browser/session.py` | none | Pending |
| `internal/llm/client.go` | `llm_extractor.py`'s `_call_llm` dispatch/retry shape | none | Pending |
| `internal/pipeline/scraper.go` (remainder) | `scraper.py`: `Scraper`, `scrape`/`_do_scrape`/`_extract_data` | browser, domain, heuristics | Pending — extend existing file |
| `internal/pipeline/crawler.go` (remainder) | `crawler.py`: `Crawler`, `find_product_url`/`_run_with_session`/`_run_discovery`/`_validate_candidates`/`_extract_candidate_links` | browser, heuristics | Pending — extend existing file |
| `internal/pipeline/extractor.go` | `llm_extractor.py` (full) | llm, domain | Pending |
| `internal/pipeline/discovery.go` | `discover_selectors.py`, **minus** the `--url`/`--store` CLI mode (dropped — see archive catalog Part B) | store, browser, llm, extractor | Pending |
| `internal/pipeline/orchestrator.go` | `orchestrator.py` + `main.py`'s `run_once` | everything above | Pending — split across sessions, see 4.1 |

Don't jump ahead in this order without an explicit instruction to do so — later files assume earlier interfaces exist and are stable.

---

## 4. Per-File Session Procedure

1. Confirm the target is next per Section 3 (or an explicitly-redirected file).
2. Read the real Python source file(s) for it, in full — not a partial skim.
3. If the target file already exists (Section 2), open it and extend in place.
4. Port function-by-function. Match the doc-comment convention already established in the completed heuristics files: cite the Python function each Go function mirrors, note any deliberate behavioral judgment calls, flag unverified assumptions explicitly rather than asserting silent parity.
5. Self-verify before ending the session: `go build` and `go vet` against the affected package. Fix compile errors before stopping — don't hand over broken code expecting the review pass to catch it.
6. If the newly-ported logic is pure (no I/O), add table-driven tests in the same session if the rate-limit budget allows; otherwise note it as a follow-up in the session summary rather than skipping silently.
7. End with a short session summary — what was ported, what was assumed, what's still open. This is what gets pasted into the Claude review chat alongside the file, so keep it specific, not a restatement of the diff.

### 4.1 Large-file exception — `orchestrator.go`

The biggest single port in Phase 1: `RunAll`/`DeterminePath`/`RunPair`, four path handlers, `detectChange`, `handleError`, `collectRunSummary`, plus the v3.9 stock-fallback pair (`classifyStockFallback`/`applyStockFallbackIfNeeded`). Do not attempt in one session. Default split:

1. `DeterminePath` / `RunPair` / path handlers A–D
2. `detectChange` / `handleError` / `collectRunSummary`
3. The stock-fallback pair

Re-confirm this split once the earlier files in Section 3 actually exist — `RunPair`'s real dependency shape may suggest a better split than this default.

---

## 5. Rate-Limit Discipline

- One file (or one sub-chunk, per 4.1) per session. No chaining into the next file even if budget looks available mid-session.
- After each file: build, test, write the session summary, stop.
- The Claude review cycle between sessions is part of the workflow, not an interruption to it — expect revision requests before the next file starts, and treat those as normal, not as a sign something went wrong.

---

## 6. Open Decisions — Do Not Resolve Unilaterally

Tracked as genuinely open in `vestige_go_migration_blueprint.md` Section 7. Implement around them, following the "leaning" direction where one is stated, but don't treat any as settled — flag in the session summary if a file's implementation forces a choice among them:

- **Settings shape** (item 12) — typed struct vs. stringly-typed map. Affects `store.go` and anything reading settings.
- **Error classification** (item 13) — sentinel errors vs. ported string-matching. Leaning sentinel errors. Relevant to `orchestrator.go`'s `handleError`.
- **Gin vs. Fiber, sqlc vs. GORM, migration tooling** (items 1–3) — irrelevant to every file in Section 3; relevant once Phase 2/3 starts.
- **Chromium distribution mechanism** (item 10) — irrelevant to `browser/session.go`'s interface shape; relevant once its real chromedp-backed implementation is written (later, not Phase 1's interface pass).

---

## 7. Testing Expectations

Per `vestige_go_pipeline_implementation.md` Section 8:

- Pure functions (`heuristics/*`, `ParsePrice`, `CheckResponseStatus`, `BuildSearchURL`, and anything in `extractor.go`/`orchestrator.go` that turns out pure) → table-driven tests, no mocking.
- `Orchestrator`/`Crawler`/`Discoverer` → unit tests against the three Phase 1 interfaces, faked — no real browser, no real DB, no real LLM call.
- Integration/E2E → out of scope for Phase 1 entirely, deferred to Phase 2+ once a real `store.PairStore` and `browser.Session` exist.

---

## 8. Output Location & Handoff

- Everything lands under `/go/internal/...`, mirroring `vestige_go_pipeline_implementation.md` Section 1's package layout exactly, just rooted at `/go` instead of the eventual repo root — makes the final copy into `vestige-go` a straight directory merge, not a restructure.
- No `/go/cmd/discover/` — dropped, per Section 1.
- Session summaries (Section 4, step 7) travel with the file upload as plain text, not baked into the `.go` file beyond the existing doc-comment convention.
