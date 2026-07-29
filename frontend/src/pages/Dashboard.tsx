import { useEffect, useState, useCallback } from "react";
import { getAvailability, getRuns, triggerRun } from "../api/client";
import type { AvailabilityDto, RunSummaryDto } from "../api/types";
import BookCard from "../components/BookCard";
import RunLog from "../components/RunLog";

export default function Dashboard() {
    const [availability, setAvailability] = useState<AvailabilityDto[]>([]);
    const [runs, setRuns] = useState<RunSummaryDto[]>([]);
    const [loading, setLoading] = useState(true);
    const [triggering, setTriggering] = useState(false);
    const [error, setError] = useState<string | null>(null);

    const load = useCallback(async () => {
        setLoading(true);
        setError(null);
        try {
            const [avail, recentRuns] = await Promise.all([
                getAvailability(),
                getRuns(),
            ]);
            setAvailability(avail);
            setRuns(recentRuns);
        } catch (err) {
            setError(
                err instanceof Error
                    ? err.message
                    : "Failed to load dashboard data",
            );
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        load();
    }, [load]);

    async function handleRunNow() {
        setTriggering(true);
        setError(null);
        try {
            await triggerRun();
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Run failed");
        } finally {
            setTriggering(false);
        }
    }

    const byBook = new Map<string, { storeName: string; status: string }[]>();
    for (const a of availability) {
        const list = byBook.get(a.bookName) ?? [];
        list.push({ storeName: a.storeName, status: a.status });
        byBook.set(a.bookName, list);
    }

    return (
        <div className="page">
            <div className="page-header">
                <h2>Dashboard</h2>
                <button onClick={handleRunNow} disabled={triggering}>
                    {triggering ? "Running…" : "Run Now"}
                </button>
            </div>
            {error && <p className="form-error">{error}</p>}
            <RunLog runs={runs} />
            {loading ? (
                <p>Loading…</p>
            ) : byBook.size === 0 ? (
                <p className="muted">
                    No tracked books yet — add one on the Books page.
                </p>
            ) : (
                <div className="book-grid">
                    {[...byBook.entries()].map(([bookName, stores]) => (
                        <BookCard
                            key={bookName}
                            bookName={bookName}
                            stores={stores}
                        />
                    ))}
                </div>
            )}
        </div>
    );
}
