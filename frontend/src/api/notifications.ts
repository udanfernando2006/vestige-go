// Notification FIRING now goes through Wails' own first-party service
// (github.com/wailsapp/wails/v3/pkg/services/notifications — confirmed real,
// registered in cmd/vestige/main.go's Services list alongside APIService),
// replacing @tauri-apps/plugin-notification entirely. Import path below
// mirrors the module-path-as-folder convention confirmed for apiservice.ts;
// treat it as needing confirmation against the real generated
// frontend/bindings/ tree the first time `task generate:bindings` actually
// runs with the notification service registered — same caveat as
// api/settings.ts's GetAPIPort import.
import {
    NotificationService,
    type NotificationOptions,
} from "../../bindings/github.com/wailsapp/wails/v3/pkg/services/notifications";
import { getRuns, getRunDetail, ApiError } from "./client";
import { getNotificationsEnabled } from "./settings";
import type { RunChangeDto, RunSummaryDto } from "./types";

const POLL_INTERVAL_MS = 90_000;

let pollHandle: ReturnType<typeof setInterval> | null = null;
let permissionGranted = false;

// TODO: this was @tauri-apps/plugin-store's LazyStore('settings.json') —
// persisted across restarts. There's no Wails-native key-value store
// confirmed yet, and this shouldn't get a second, separate SQLite file just
// for one string when the app already has a real settings store. Until
// lastNotifiedRunId is folded into domain.Settings on the Go side (same
// TODO as notificationsEnabled in settings.ts), this only survives for the
// lifetime of the current app session — a restart will re-notify for
// whatever the latest run is, once, which is a real but minor regression
// worth fixing, not a silent one.
let lastNotifiedRunId: string | null = null;

async function ensurePermission(): Promise<boolean> {
    if (permissionGranted) return true;
    try {
        // CheckNotificationAuthorization()'s exact return shape hasn't been
        // confirmed against the real generated binding yet — treating the
        // resolved value as truthy/falsy here. Revisit once
        // frontend/bindings/.../notifications.ts actually exists.
        const authorized = await NotificationService.CheckNotificationAuthorization();
        permissionGranted = !!authorized;
    } catch (err) {
        console.error("[notifications] permission check failed:", err);
        return false;
    }
    return permissionGranted;
}

// Pure — no I/O. `runs` must be newest-first (what getRuns() already returns).
// Returns the runs to notify for, oldest-first, so toasts land in order.
export function selectNewRuns(
    runs: RunSummaryDto[],
    lastNotifiedRunId: string | null,
): RunSummaryDto[] {
    if (runs.length === 0) return [];
    if (lastNotifiedRunId === null) return [runs[0]]; // first poll ever — only the latest, not all history
    return runs.filter((r) => r.runId > lastNotifiedRunId).reverse(); // ISO-8601 sorts lexically
}

// Pure — no I/O.
export function formatChange(c: RunChangeDto): string {
    const statusChanged = !!c.fromStatus && c.fromStatus !== c.toStatus;
    const priceChanged =
        c.fromPrice != null && c.toPrice != null && c.fromPrice !== c.toPrice;

    if (statusChanged && priceChanged) {
        return `${c.bookName} @ ${c.storeName}: ${c.fromStatus} → ${c.toStatus}, ${c.fromPrice!.toFixed(2)} → ${c.toPrice!.toFixed(2)}`;
    }
    if (statusChanged) {
        return `${c.bookName} @ ${c.storeName}: ${c.fromStatus} → ${c.toStatus}`;
    }
    return `${c.bookName} @ ${c.storeName}: price ${c.fromPrice!.toFixed(2)} → ${c.toPrice!.toFixed(2)}`;
}

function notifyForChanges(changes: RunChangeDto[]) {
    if (changes.length === 0) return;
    const lines = changes.slice(0, 3).map(formatChange);
    const more = changes.length > 3 ? `\n+${changes.length - 3} more` : "";

    const options: NotificationOptions = {
        id: crypto.randomUUID(),
        title:
            changes.length === 1
                ? "Availability changed"
                : `${changes.length} books changed`,
        body: lines.join("\n") + more,
    };
    NotificationService.SendNotification(options).catch((err: unknown) =>
        console.error("[notifications] send failed:", err),
    );
}

export async function pollOnce() {
    const notificationsEnabled = await getNotificationsEnabled().catch(
        () => true, // settings.ts's stub currently throws — fail open rather than silently never notifying
    );

    // Skip the OS permission dance entirely when notifications are off — no
    // reason to prompt for something that won't fire.
    if (notificationsEnabled) {
        if (!(await ensurePermission())) return;
    }

    let runs: RunSummaryDto[];
    try {
        runs = await getRuns();
    } catch {
        return;
    }
    if (runs.length === 0) return;

    const newRuns = selectNewRuns(runs, lastNotifiedRunId);

    let firstFailedRunId: string | null = null;
    if (notificationsEnabled) {
        for (const run of newRuns) {
            try {
                const detail = await getRunDetail(run.runId);
                notifyForChanges(detail.changes);
            } catch (err) {
                // CodeRabbit-flagged, refined further: the backend
                // (runs.go's getRunDetail) already distinguishes "genuinely
                // gone" from "transient" cleanly — runlog.ErrNotFound maps
                // to HTTP 404 (the log file was rotated away or never
                // existed; retrying can never succeed), and every other
                // failure maps to 500 (a read error, a request that raced
                // an in-progress log rotation, a momentary hiccup —
                // genuinely worth retrying). A 404 is treated as
                // "processed" here (nothing to notify, but not held back
                // either — retrying forever on a file that will never
                // exist just wastes a request every 90s for no benefit).
                // Anything else holds the watermark back so it's retried
                // next poll, instead of the previous behavior of silently
                // and permanently dropping ANY failure the same way,
                // including ones that would have succeeded on retry.
                if (!(err instanceof ApiError && err.status === 404)) {
                    firstFailedRunId ??= run.runId;
                }
            }
        }
    }
    // When disabled, detection/dedup bookkeeping still runs below — only the
    // toast is skipped. lastNotifiedRunId still has to advance even on this
    // branch, or re-enabling later replays everything missed as one burst.

    if (newRuns.length > 0) {
        if (firstFailedRunId !== null) {
            // Advance to just short of the first NON-404 failure (404s are
            // treated as processed above, so they don't hold this back).
            // selectNewRuns' strict `>` filter means this correctly
            // re-includes firstFailedRunId (and anything after it) on the
            // next poll — at worst a run that succeeded earlier in this
            // same batch gets notified a second time, a far safer failure
            // mode than the previous silent, permanent loss.
            const idx = newRuns.findIndex((r) => r.runId === firstFailedRunId);
            lastNotifiedRunId = idx > 0 ? newRuns[idx - 1].runId : lastNotifiedRunId;
        } else {
            lastNotifiedRunId = runs[0].runId;
        }
    }
}

export function startNotificationPolling() {
    if (pollHandle) return;
    pollOnce().catch((err) => console.error("[notifications] pollOnce failed:", err));
    pollHandle = setInterval(() => {
        pollOnce().catch((err) => console.error("[notifications] pollOnce failed:", err));
    }, POLL_INTERVAL_MS);
}

export function stopNotificationPolling() {
    if (pollHandle) clearInterval(pollHandle);
    pollHandle = null;
}