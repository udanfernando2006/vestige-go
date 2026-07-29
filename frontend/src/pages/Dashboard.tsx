import { useEffect, useState, useCallback } from "react";
import { getAvailability, getRuns, getRunStatus, triggerRun } from "../api/client";
import type { AvailabilityDto, RunSummaryDto } from "../api/types";
import BookCard from "../components/BookCard";
import RunLog from "../components/RunLog";

const STATUS_POLL_MS = 3000;

export default function Dashboard() {
    const [availability, setAvailability] = useState<AvailabilityDto[]>([]);
    const [runs, setRuns] = useState<RunSummaryDto[]>([]);
    const [loading, setLoading] = useState(true);
    const [running, setRunning] = useState(false); // now reflects the BACKEND's srv.running, not just "did I click"
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

    // Checks real backend run state on mount AND on an interval — this is
    // what fixes navigate-away-and-back: on remount, this immediately asks
    // the server "is a run actually in progress" instead of assuming false.
    // Also covers scheduled runs the user never clicked to start.
    useEffect(() => {
        let cancelled = false;

        async function poll() {
            try {
                const status = await getRunStatus();
                if (!cancelled) setRunning(status.running);
            } catch {
                // status-poll failures shouldn't surface as a page error —
                // the button just stays in its last-known state until the
                // next successful poll.
            }
        }

        poll();
        const interval = setInterval(poll, STATUS_POLL_MS);
        return () => {
            cancelled = true;
            clearInterval(interval);
        };
    }, []);

    useEffect(() => {
        load();
    }, [load]);

    async function handleRunNow() {
        setRunning(true); // optimistic — the next poll tick confirms/corrects it either way
        setError(null);
        try {
            await triggerRun();
            await load();
        } catch (err) {
            // A 409 here means the poll simply hasn't caught up yet (race
            // between click and the next tick) — not a real failure, so
            // don't show it as one.
            if (!(err instanceof Error && err.message.includes("already in progress"))) {
                setError(err instanceof Error ? err.message : "Run failed");
            }
        } finally {
            setRunning(false);
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
                <button onClick={handleRunNow} disabled={running}>
                    {running ? "Running…" : "Run Now"}
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
