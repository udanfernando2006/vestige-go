import type { PairStatus } from "../api/types";

const LABELS: Record<PairStatus, string> = {
    PENDING: "Pending",
    NEEDS_SETUP: "Needs setup",
    IN_STOCK: "In stock",
    OUT_OF_STOCK: "Out of stock",
    NOT_LISTED: "Not listed",
    SKIP: "Skipped",
    ERROR: "Error",
};

// Maps each pipeline status onto one of the four semantic status tokens
// (success/danger/warning/neutral) registered in styles.css's @theme block.
// PENDING and NEEDS_SETUP are deliberately different: PENDING is a normal
// queued state (neutral), NEEDS_SETUP is an actual blocker (warning).
const STYLES: Record<PairStatus, string> = {
    PENDING: "bg-neutral text-titlebar-fg",
    NEEDS_SETUP: "bg-warning text-ink",
    IN_STOCK: "bg-success text-titlebar-fg",
    OUT_OF_STOCK: "bg-danger text-titlebar-fg",
    NOT_LISTED: "bg-neutral text-titlebar-fg",
    SKIP: "bg-neutral text-titlebar-fg",
    ERROR: "bg-danger text-titlebar-fg",
};

export default function AvailabilityBadge({ status }: { status: string }) {
    const key = (status in LABELS ? status : "ERROR") as PairStatus;
    return (
        <span className={`vestige-pill ${STYLES[key]}`}>{LABELS[key]}</span>
    );
}
