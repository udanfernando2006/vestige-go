import { useEffect, useState, useCallback } from "react";
import {
    getBooks,
    getStores,
    getHistory,
    deleteSnapshot,
    deleteHistoryForPair,
} from "../api/client";
import type { BookGroupDto, StoreDto, SnapshotHistoryDto } from "../api/types";
import AvailabilityBadge from "../components/AvailabilityBadge";
import Window from "../components/Window";
import { useConfirm } from "../hooks/useConfirm";

export default function History() {
    const [bookGroups, setBookGroups] = useState<BookGroupDto[]>([]);
    const [stores, setStores] = useState<StoreDto[]>([]);
    const [isbn, setIsbn] = useState("");
    const [storeFilter, setStoreFilter] = useState("");
    const [statusFilter, setStatusFilter] = useState("");
    const [snapshots, setSnapshots] = useState<SnapshotHistoryDto[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    const { confirm, dialog } = useConfirm();

    // Store options come from a dedicated fetch, not derived from the current
    // (possibly already store-filtered) snapshot list — deriving them from
    // snapshots would make the dropdown's own option list shrink to match
    // whatever's currently selected, locking out switching back to a different store.
    useEffect(() => {
        getBooks()
            .then(setBookGroups)
            .catch((err) => setError(err.message));
        getStores()
            .then(setStores)
            .catch((err) => setError(err.message));
    }, []);

    const load = useCallback(async () => {
        setLoading(true);
        setError(null);
        try {
            setSnapshots(
                await getHistory({
                    isbn: isbn || undefined,
                    storeName: storeFilter || undefined,
                    status: statusFilter || undefined,
                    limit: 100,
                }),
            );
        } catch (err) {
            setError(
                err instanceof Error ? err.message : "Failed to load history",
            );
        } finally {
            setLoading(false);
        }
    }, [isbn, storeFilter, statusFilter]);

    // Loads on mount with no filters — every recent snapshot across every book —
    // and again whenever a filter changes. Filtering happens server-side via the
    // generalized /api/availability/history endpoint, not by slicing an
    // already-fetched array client-side.
    useEffect(() => {
        load();
    }, [load]);

    const books = bookGroups.flatMap((g) => g.books);

    async function handleDeleteSnapshot(id: number) {
        const confirmed = await confirm(
            "Delete this snapshot? This cannot be undone.",
            {
                title: "Delete snapshot",
                confirmLabel: "Delete",
                destructive: true,
            },
        );
        if (!confirmed) return;
        try {
            await deleteSnapshot(id);
            await load();
        } catch (err) {
            setError(
                err instanceof Error
                    ? err.message
                    : "Failed to delete snapshot",
            );
        }
    }

    async function handleDeletePairHistory(
        pairId: number,
        bookName: string,
        storeName: string,
    ) {
        const confirmed = await confirm(
            `Delete ALL history for ${bookName} at ${storeName}? This cannot be undone.`,
            {
                title: "Delete history",
                confirmLabel: "Delete all",
                destructive: true,
            },
        );
        if (!confirmed) return;
        try {
            await deleteHistoryForPair(pairId);
            await load();
        } catch (err) {
            setError(
                err instanceof Error
                    ? err.message
                    : "Failed to delete history for this pair",
            );
        }
    }

    return (
        <div className="page">
            <h2>History</h2>
            {error && <p className="form-error">{error}</p>}
            {dialog}

            <Window title="Filters">
                <label>
                    Book
                    <select
                        value={isbn}
                        onChange={(e) => setIsbn(e.target.value)}>
                        <option value="">All books</option>
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
                        value={storeFilter}
                        onChange={(e) => setStoreFilter(e.target.value)}>
                        <option value="">All stores</option>
                        {stores.map((s) => (
                            <option key={s.id} value={s.name}>
                                {s.name}
                            </option>
                        ))}
                    </select>
                </label>
                <label>
                    Status
                    <select
                        value={statusFilter}
                        onChange={(e) => setStatusFilter(e.target.value)}>
                        <option value="">All statuses</option>
                        <option value="IN_STOCK">In stock</option>
                        <option value="OUT_OF_STOCK">Out of stock</option>
                        <option value="NOT_LISTED">Not listed</option>
                        <option value="ERROR">Error</option>
                    </select>
                </label>
            </Window>

            <Window title="Snapshots">
                {loading ? (
                    <p>Loading…</p>
                ) : (
                    <table>
                        <thead>
                            <tr>
                                <th>Book</th>
                                <th>Store</th>
                                <th>Status</th>
                                <th>Price</th>
                                <th>Scraped at</th>
                                <th></th>
                            </tr>
                        </thead>
                        <tbody>
                            {snapshots.map((s) => (
                                <tr key={s.id}>
                                    <td>{s.bookName}</td>
                                    <td>{s.storeName}</td>
                                    <td>
                                        <AvailabilityBadge status={s.status} />
                                    </td>
                                    <td>
                                        {s.price != null
                                            ? s.price.toFixed(2)
                                            : "—"}
                                    </td>
                                    <td>
                                        {new Date(s.scrapedAt).toLocaleString()}
                                    </td>
                                    <td>
                                        {/* <span className="inline-flex gap-2"> */}
                                        <span className="btn-group">
                                            <button
                                                onClick={() =>
                                                    handleDeleteSnapshot(s.id)
                                                }>
                                                Delete
                                            </button>
                                            <button
                                                onClick={() =>
                                                    handleDeletePairHistory(
                                                        s.pairId,
                                                        s.bookName,
                                                        s.storeName,
                                                    )
                                                }>
                                                Delete all for this pair
                                            </button>
                                        </span>
                                    </td>
                                </tr>
                            ))}
                            {snapshots.length === 0 && (
                                <tr>
                                    <td colSpan={6} className="muted">
                                        No history matches these filters.
                                    </td>
                                </tr>
                            )}
                        </tbody>
                    </table>
                )}
            </Window>
        </div>
    );
}
