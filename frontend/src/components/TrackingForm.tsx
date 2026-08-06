import { useState } from "react";
import type { BookGroupDto, StoreDto, TrackingPairDto } from "../api/types";
import { createTracking, updateTracking } from "../api/client";
import Window from "./Window";

interface TrackingFormProps {
    bookGroups: BookGroupDto[];
    stores: StoreDto[];
    onCreated: (pair: TrackingPairDto) => void;
}

export default function TrackingForm({
    bookGroups,
    stores,
    onCreated,
}: TrackingFormProps) {
    const books = bookGroups.flatMap((g) => g.books);
    const [isbn, setIsbn] = useState("");
    const [storeName, setStoreName] = useState("");
    const [productUrl, setProductUrl] = useState("");
    const [doesNotCarry, setDoesNotCarry] = useState(false);
    const [submitting, setSubmitting] = useState(false);
    const [error, setError] = useState<string | null>(null);

    async function handleSubmit(e: React.FormEvent) {
        e.preventDefault();
        if (!isbn || !storeName) return;
        setSubmitting(true);
        setError(null);
        try {
            const pair = await createTracking({
                isbn,
                storeName,
                productUrl: productUrl || null,
            });
            // CodeRabbit-flagged: create-then-update is two independent
            // HTTP round-trips with no atomicity. The pair is already
            // committed as PENDING in the DB the instant createTracking
            // resolves — a failure in the SKIP follow-up below must NOT be
            // reported as "failed to add," since something WAS added, just
            // not yet marked Skip. Reporting it as a full failure previously
            // left the form un-cleared (inviting a duplicate-submit attempt
            // that would 409 on the now-existing pair) and left a real
            // PENDING pair in the DB that a scheduler/manual run could pick
            // up and scrape once before anyone noticed it needed to be
            // Skip. If createTracking itself already supports setting an
            // initial status (worth checking client.ts/types.ts — collapsing
            // this to one request would remove the race entirely rather
            // than just handling it gracefully), that's the better fix;
            // this is the resilient fallback either way.
            let finalPair = pair;
            if (doesNotCarry) {
                try {
                    finalPair = await updateTracking(pair.id, {
                        status: "SKIP",
                    });
                } catch (skipErr) {
                    // The pair exists and is tracked — just not yet marked
                    // Skip. Surface that honestly instead of the generic
                    // create-failure message, clear the form (nothing left
                    // to retry-submit), and still hand the caller the
                    // successfully-created pair so the UI list reflects
                    // reality rather than silently omitting it.
                    onCreated(pair);
                    setIsbn("");
                    setStoreName("");
                    setProductUrl("");
                    setDoesNotCarry(false);
                    setError(
                        "Tracking pair was added, but marking it as Skip failed" +
                            (skipErr instanceof Error
                                ? `: ${skipErr.message}`
                                : "") +
                            " — you can mark it as Skip from the tracking list.",
                    );
                    return;
                }
            }
            onCreated(finalPair);
            setIsbn("");
            setStoreName("");
            setProductUrl("");
            setDoesNotCarry(false);
        } catch (err) {
            setError(
                err instanceof Error
                    ? err.message
                    : "Failed to add tracking pair",
            );
        } finally {
            setSubmitting(false);
        }
    }

    return (
        <Window title="Add tracking pair">
            <form onSubmit={handleSubmit}>
                {error && <p className="form-error">{error}</p>}
                <label>
                    Book
                    <select
                        value={isbn}
                        onChange={(e) => setIsbn(e.target.value)}
                        required>
                        <option value="">Select a book…</option>
                        {books.map((b) => (
                            <option key={b.isbn} value={b.isbn}>
                                {b.name}
                            </option>
                        ))}
                    </select>
                </label>
                <label>
                    Store
                    <select
                        value={storeName}
                        onChange={(e) => setStoreName(e.target.value)}
                        required>
                        <option value="">Select a store…</option>
                        {stores.map((s) => (
                            <option key={s.id} value={s.name}>
                                {s.name}
                            </option>
                        ))}
                    </select>
                </label>
                <label>
                    Product URL (optional — leave blank to let the Crawler
                    find it)
                    <input
                        value={productUrl}
                        onChange={(e) => setProductUrl(e.target.value)}
                        placeholder="https://…"
                    />
                </label>
                <label className="checkbox-label">
                    <input
                        type="checkbox"
                        checked={doesNotCarry}
                        onChange={(e) => setDoesNotCarry(e.target.checked)}
                    />
                    This store doesn't carry this book (mark as Skip
                    immediately)
                </label>
                <button type="submit" disabled={submitting}>
                    {submitting ? "Adding…" : "Add"}
                </button>
            </form>
        </Window>
    );
}