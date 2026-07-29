import { useState } from "react";
import type { RunSummaryDto } from "../api/types";

export default function RunLog({ runs }: { runs: RunSummaryDto[] }) {
    const [open, setOpen] = useState(true);
    if (runs.length === 0) return null;

    const [latest, ...previous] = runs;

    return (
        <div className="vestige-window">
            <button
                type="button"
                className="vestige-titlebar run-log-toggle"
                onClick={() => setOpen(!open)}>
                <span>
                    {open ? "▾" : "▸"} Last run — {latest.totalPairs} pairs,{" "}
                    {latest.changes} changes, {latest.errors} errors (
                    {latest.durationSeconds.toFixed(1)}s)
                </span>
            </button>
            {open && previous.length > 0 && (
                <div className="vestige-window-body">
                    <ul className="run-log-history">
                        {previous.slice(0, 9).map((r) => (
                            <li key={r.runId}>
                                {new Date(r.runId).toLocaleString()} —{" "}
                                {r.totalPairs} pairs, {r.changes} changes,{" "}
                                {r.errors} errors
                            </li>
                        ))}
                    </ul>
                </div>
            )}
        </div>
    );
}
