# <img src="build/appicon.png" alt="Vestige Go logo" width="40" height="40" valign="middle"> Vestige Go

A config-driven book availability tracker. Add books and stores through the desktop app, and Vestige Go scrapes price/stock on a schedule, notifying you the moment a tracked book comes into stock.

Vestige Go is a **single native Go binary** — no Docker, no database server, no separate backend process to keep running. Download the installer, run it, done.

> Vestige Go is a companion rewrite of [Vestige](../../../vestige) (Java/Python/Tauri, Docker + PostgreSQL), built as a second, differently-motivated project rather than a replacement — see that repo if you're looking for the multi-cloud-deployable version of this tool.

---

## How It Works

Vestige Go runs a scraping pipeline on a schedule (or on demand via "Run Now"). For each book-store pair it tracks, it determines the fastest route to current availability data:

| Path | Condition                                    | What happens                                           |
| ---- | --------------------------------------------- | -------------------------------------------------------- |
| A    | No product URL cached                         | Crawler searches the store and finds the product page    |
| B    | URL found, no selectors, `LLM_MODE=selector`  | LLM discovers CSS selectors for price and stock fields   |
| C    | URL + selectors cached                        | Scraper reads directly — no discovery needed             |
| D    | URL found, `LLM_MODE=direct`                  | LLM reads the page HTML directly on every run             |

Path C is the production fast path. Paths A and B are one-time setup paths that resolve to C. Path D trades selector maintenance for LLM cost on every run.

> **Crawler scoring is heuristic, not exhaustive.** Path A ranks candidate product-page links using common URL conventions (e.g. `/products/`, `/product/`, `/item/`) seen across the stores Vestige Go has been tested against. A store with an unusual URL convention can occasionally cause the Crawler to pick the wrong page, or none at all. If a tracked pair's resolved URL doesn't look right (check it on the **Tracking** page), the fix is simple: paste the correct product URL directly into that pair's product URL field — this bypasses the Crawler entirely for that pair going forward.

Every result is written to a local SQLite database as an immutable snapshot row. Status changes surface as OS desktop notifications.

---

## Tech Stack

| Layer             | Technology                                                   |
| ------------------ | ------------------------------------------------------------ |
| Backend            | Go, Gin (REST API served over localhost)                     |
| Pipeline           | Go, chromedp against a bundled, pinned headless Chromium build |
| Database           | SQLite (embedded, single file, single-user)                  |
| Desktop shell      | Wails v3 (Go)                                                |
| Frontend           | React 19, TypeScript, Vite, Tailwind                          |
| LLM                | Any OpenAI-compatible endpoint (e.g. Groq, free tier)          |

**Windows only.** macOS/Linux support was dropped for this project — the author can't test on those platforms and would rather ship a solid Windows app than a shaky cross-platform one.

---

## Install & Run

1. Grab the latest installer from this project's [GitHub Releases](../../releases) page — `VestigeGo_x.y.z_x64-setup.exe` (NSIS).
2. Run it. Standard install — accept the defaults unless you have a reason not to.
3. Launch Vestige Go. On first launch the bundled Chromium build and a fresh local database/encryption key are set up automatically — nothing to install or configure by hand first.

That's the whole install process. There's no separate service to start, no daemon to keep running, and no configuration file to edit before the app opens for the first time.

> **Closing the window does not quit Vestige Go.** Clicking the window's close (X) button just hides it to the system tray — the app keeps running in the background so notification polling keeps working. To actually quit, **right-click the Vestige Go icon in the system tray and choose Quit.**

### Get a free LLM API key (Groq)

Vestige Go uses an LLM only for one-time CSS selector discovery per book/store pair (Path B) or, optionally, for direct-extraction mode (Path D) — not for every scrape. Groq's free tier works well for this:

1. Go to the [Groq Console](https://console.groq.com) and create a free API key.
2. Check your org's **Organization Limits → Chat Completions** page to see which models are available on the free tier and their rate limits (requests/tokens per minute and per day).
3. Pick two models:
    - **Selector discovery (`Selector Model`)** — needs to read a full (if trimmed) HTML product page, so a large context/token budget matters most. `llama-3.3-70b-versatile` (12K tokens/minute on the free tier) worked well enough during testing.
    - **Direct extraction (`Direct Model`)** — runs on every scrape for Path D pairs, so speed/cost matters more than raw context size. `llama-3.3-70b-versatile` (12K TPM) worked reliably in testing. `groq/compound` looked appealing on paper (70K TPM, no daily token cap) but **did not work** for this role in practice — stick with `llama-3.3-70b-versatile` unless you've specifically re-tested `groq/compound` against your own pages.

    Both models speak the standard OpenAI chat-completions format, so either works with Vestige Go's role-based LLM config without any code changes.

    > **Rate limits can surface as pair status, not just an error message.** If you hit a model's requests-per-minute/day or tokens-per-minute/day cap mid-run, a pair can land in `NEEDS_SETUP` (discovery couldn't complete) or stay in `PENDING` (waiting on a run that didn't get to it) instead of the status you expected. If pairs seem stuck, check whether you're bumping into the free-tier limits shown on the Groq Organization Limits page before assuming something's broken.

### Add your API key and models to Vestige Go

1. Open Vestige Go → **Settings**.
2. Under **Pipeline configuration**, set:
    - `Selector API Base` → `https://api.groq.com/openai/v1`
    - `Selector API Key` → your Groq key
    - `Selector Model` → `llama-3.3-70b-versatile`
    - `Direct API Base` → `https://api.groq.com/openai/v1`
    - `Direct API Key` → your Groq key
    - `Direct Model` → `llama-3.3-70b-versatile` (or whichever you settled on)
3. Save. Changes apply on the very next pipeline run — no restart required.

> API keys entered via the Settings page are encrypted at rest (AES-256-GCM, keyed to a per-install key stored in your OS credential store) and never echoed back to the UI — only a "configured" indicator and a masked hint are shown.

### Add your books, stores, and tracking pairs

Everything is managed one-at-a-time through the app — there's no config file to seed or import from:

1. **Stores** — add each bookstore you want to track (name + base URL).
2. **Books** — add the books you want to track (name + ISBN), optionally grouped into a series.
3. **Tracking** — pair each book with a store. Leave the product URL blank to let the Crawler find it automatically, or paste it directly if you already have it.
4. Click **Run Now** on the Dashboard.

On the pipeline's first pass, pairs without cached selectors go through one-time LLM-assisted discovery (Path B) using your `Selector Model`. After that, they run the fast selector-based path (Path C) on every subsequent scrape.

### Verifying LLM-discovered selectors (recommended)

LLMs don't guarantee correct output, and a wrong selector can silently return stale or empty data instead of failing loudly. **Before trusting a newly-discovered selector, it's worth manually confirming it against the live page** — this is admittedly a bit of an anti-pattern for a tool meant to run unattended, but it's the honest tradeoff of using an LLM for this step rather than hand-written selectors.

For any pair sitting in `NEEDS_SETUP` (or after running the on-demand **Discover** button from the Tracking page), check the suggested selector using your browser's DevTools:

1. Open the product page in your regular browser.
2. Right-click the **price** on the page → **Inspect** (Chrome/Edge) or **Inspect Element** (Firefox). This opens DevTools with that exact HTML element highlighted.
3. In DevTools, right-click the highlighted element → **Copy → Copy selector**, or just read off its `class`/`id` attributes directly from the highlighted line.
4. Compare that against the selector Vestige Go suggested. They don't need to be identical strings — Vestige Go's are often written as wildcard attribute selectors (e.g. `div[class*='price']`) to survive framework-hashed class names — but they should be pointing at the same element.
5. Still in DevTools, use **Ctrl+F** inside the **Elements** panel (or the DevTools-wide search) to search for the suggested selector directly and confirm it matches exactly one element, not zero or several.
6. Repeat for the stock/availability selector.
7. Only then save the selector (or accept the pipeline's own `--commit`ed one) — if something looks off, edit it manually in the Tracking page before saving.

### Notifications only cover availability/price changes

Desktop notifications fire **only when a tracked pair's stock status or price actually changes** between runs — not on every run, and not on errors. A `0 changes` run is often completely normal (nothing changed, or the database has nothing yet to diff against) and produces no notification, which is expected behavior, not a sign anything's broken.

This also means **errors and stuck pairs won't notify you** — a pair sitting in `ERROR` or `NEEDS_SETUP` produces no popup. Check the **Dashboard** or **Tracking** page in the UI periodically to catch these rather than relying on notifications alone.

---

## Availability States

| Status         | Meaning                                                                               |
| -------------- | ---------------------------------------------------------------------------------------- |
| `PENDING`      | Ready to scrape on the next run                                                          |
| `NEEDS_SETUP`  | Product URL found but selectors missing; pipeline paused until selectors are provided    |
| `IN_STOCK`     | Currently available                                                                      |
| `OUT_OF_STOCK` | Found on the store but currently unavailable                                             |
| `NOT_LISTED`   | Store confirmed not to carry this book                                                   |
| `SKIP`         | Manually excluded — never scraped                                                        |
| `ERROR`        | Scrape failed (network error, broken selector, etc.)                                     |

> **Stock-status text that doesn't match a known pattern isn't necessarily an `ERROR`.** Vestige Go first checks a store's raw availability text (e.g. "In Stock", "Sold Out", "Low stock: 4 left") against a built-in pattern list. If your `Selector`/`Direct` LLM credentials are configured, an unrecognized string is automatically classified by the LLM as a fallback before giving up — a pair only lands in `ERROR` with reason `unparseable_stock_status` if that fallback also can't tell, or no LLM credentials are set at all.
>
> If a particular store's phrasing keeps needing the LLM fallback (which costs a request per occurrence), you can teach Vestige Go the pattern directly instead: open **Settings** and add your own regex patterns under **Custom stock-status patterns** (in-stock and out-of-stock, entered separately). These are checked before the LLM fallback is ever tried, and apply from the very next run — no file to edit, no restart needed.

---

## Development Setup

The instructions above are for running the packaged app. If you want to build Vestige Go from source or contribute:

- `cmd/vestige/` — application entrypoint.
- `internal/pipeline/` — scraping pipeline (Orchestrator, Crawler, Scraper, Extractor, Discoverer).
- `internal/store/`, `internal/security/` — SQLite data access, settings encryption.
- `internal/handler/` — Gin HTTP layer, scheduler.
- `internal/browser/` — chromedp browser session handling.
- `frontend/` — React + TypeScript + Vite + Tailwind desktop UI, run inside a Wails v3 shell.

Requires the Go toolchain, the Wails v3 CLI (pinned via `go.mod`'s `tool` directive), and Node/npm for the frontend. `wails3 task dev` runs the full stack locally. A GitHub Actions workflow builds and publishes the signed NSIS installer referenced in the Install & Run section above.

---
