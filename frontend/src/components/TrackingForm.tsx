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
            const finalPair = doesNotCarry
                ? await updateTracking(pair.id, { status: "SKIP" })
                : pair;
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
