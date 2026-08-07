-- Vestige-Go — SQLite schema
-- Source-verified translation of models.py (SQLAlchemy/Postgres) — same five
-- tables, same field-for-field shapes, no fields added or dropped.
--
-- All CREATE TABLE / CREATE INDEX statements use IF NOT EXISTS for defense-in-depth
-- idempotency beyond the app-level "only apply schema if the DB file is missing" guard.
-- This does NOT substitute for the still-open migration-tooling decision above — it
-- guards against re-running this file against an existing DB, not against evolving an
-- existing table's columns (a later ALTER TABLE-worthy change still needs real migration
-- tooling to reach a live DB with data already in it).

PRAGMA foreign_keys = ON;

-- =============================================================================
-- series
-- =============================================================================
CREATE TABLE IF NOT EXISTS series (
    id          INTEGER PRIMARY KEY,   -- SQLite INTEGER PRIMARY KEY == rowid alias,
                                        -- behaves like BigInteger autoincrement
    name        TEXT NOT NULL UNIQUE,
    author      TEXT,                  -- nullable by omission of NOT NULL
    description TEXT
);

-- =============================================================================
-- books
-- =============================================================================
CREATE TABLE IF NOT EXISTS books (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL,
    isbn            TEXT NOT NULL UNIQUE,
    is_series_entry INTEGER NOT NULL DEFAULT 0,   -- SQLite has no native BOOLEAN.
                                                    -- 0/1 by convention, same as Postgres
                                                    -- under the hood via the driver
    author          TEXT,
    description     TEXT,
    series_id       INTEGER REFERENCES series(id)  -- nullable FK, matches Optional[int]
);

CREATE INDEX IF NOT EXISTS idx_books_series_id ON books(series_id);

-- =============================================================================
-- stores
-- =============================================================================
CREATE TABLE IF NOT EXISTS stores (
    id                  INTEGER PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    base_url            TEXT NOT NULL,
    search_url_template TEXT   -- nullable; null = undiscovered (Crawler's cached pattern)
);

-- =============================================================================
-- tracking_pairs
-- =============================================================================
CREATE TABLE IF NOT EXISTS tracking_pairs (
    id                 INTEGER PRIMARY KEY,
    book_id            INTEGER NOT NULL REFERENCES books(id),
    store_id           INTEGER NOT NULL REFERENCES stores(id),
    product_url        TEXT,
    price_selector     TEXT,
    stock_selector     TEXT,
    status             TEXT NOT NULL DEFAULT 'PENDING',
    selector_found_at  TEXT,   -- nullable ISO8601 timestamp — see note below on DateTime

    UNIQUE (book_id, store_id)   -- uq_book_store, same composite constraint as v1
);

CREATE INDEX IF NOT EXISTS idx_tracking_pairs_book_id ON tracking_pairs(book_id);
CREATE INDEX IF NOT EXISTS idx_tracking_pairs_store_id ON tracking_pairs(store_id);
CREATE INDEX IF NOT EXISTS idx_tracking_pairs_status ON tracking_pairs(status);

-- =============================================================================
-- availability_snapshots
-- =============================================================================
CREATE TABLE IF NOT EXISTS availability_snapshots (
    id          INTEGER PRIMARY KEY,
    pair_id     INTEGER NOT NULL REFERENCES tracking_pairs(id),
    in_stock    INTEGER,   -- nullable boolean (0/1/NULL) — Optional[bool] in Python

    -- price: Numeric(10,2) in Postgres has no native SQLite equivalent.
    -- SQLite storage classes are NULL/INTEGER/REAL/TEXT/BLOB only — no fixed-point
    -- decimal type. Declared TEXT here as a deliberate placeholder pending the open
    -- decision (blueprint-adjacent, not yet made): store as TEXT and parse via a Go
    -- decimal library (shopspring/decimal) to preserve exact-cents behavior, or
    -- store as REAL and accept float64 rounding risk Python's Decimal never had.
    -- NOT resolved by this schema file — flagging rather than silently picking one.
    price       TEXT,

    status      TEXT NOT NULL,
    source      TEXT,        -- nullable: "scraper" | "llm_direct"
    scraped_at  TEXT NOT NULL   -- ISO8601 UTC string, e.g. 2026-07-19T12:34:56Z
);

-- Direct port of models.py's Index("idx_pair_scraped_desc", pair_id, scraped_at.desc())
CREATE INDEX IF NOT EXISTS idx_pair_scraped_desc ON availability_snapshots(pair_id, scraped_at DESC);

-- =============================================================================
-- setting_overrides
-- =============================================================================
CREATE TABLE IF NOT EXISTS setting_overrides (
    key          TEXT PRIMARY KEY,
    value        TEXT NOT NULL,
    is_encrypted INTEGER NOT NULL DEFAULT 0
);
