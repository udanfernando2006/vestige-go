import { useEffect, useState, useCallback } from "react";
import {
    getTracking,
    getBooks,
    getStores,
    updateTracking,
    discoverSelectors,
} from "../api/client";
import type {
    TrackingPairDto,
    BookGroupDto,
    StoreDto,
    DiscoverResultDto,
} from "../api/types";
import AvailabilityBadge from "../components/AvailabilityBadge";
import TrackingForm from "../components/TrackingForm";
import Window from "../components/Window";
import { useConfirm } from "../hooks/useConfirm";

export default function Tracking() {
    const [pairs, setPairs] = useState<TrackingPairDto[]>([]);
    const [bookGroups, setBookGroups] = useState<BookGroupDto[]>([]);
    const [stores, setStores] = useState<StoreDto[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    const { confirm, dialog } = useConfirm();

    const load = useCallback(async () => {
        setLoading(true);
        setError(null);
        try {
            const [p, b, s] = await Promise.all([
                getTracking(),
                getBooks(),
                getStores(),
            ]);
            setPairs(p);
            setBookGroups(b);
            setStores(s);
        } catch (err) {
            setError(
                err instanceof Error
                    ? err.message
                    : "Failed to load tracking data",
            );
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        load();
    }, [load]);

    function replacePair(updated: TrackingPairDto) {
        setPairs((prev) => {
            const exists = prev.some((p) => p.id === updated.id);
            return exists
                ? prev.map((p) => (p.id === updated.id ? updated : p))
                : [...prev, updated];
        });
    }

    const needsSetup = pairs.filter((p) => p.status === "NEEDS_SETUP");
    const others = pairs.filter((p) => p.status !== "NEEDS_SETUP");

    return (
        <div className="page">
            <h2>Tracking</h2>
            {error && <p className="form-error">{error}</p>}
            {dialog}

            {needsSetup.length > 0 && (
                <Window
                    title={`${needsSetup.length} pair(s) need selectors`}
                    variant="warning">
                    {needsSetup.map((p) => (
                        <TrackingRow
                            key={p.id}
                            pair={p}
                            highlighted
                            onUpdated={replacePair}
                            confirm={confirm}
                        />
                    ))}
                </Window>
            )}

            <TrackingForm
                bookGroups={bookGroups}
                stores={stores}
                onCreated={replacePair}
            />

            <Window title="Tracking pairs">
                {loading ? (
                    <p>Loading…</p>
                ) : (
                    <table>
                        <thead>
                            <tr>
                                <th>Book</th>
                                <th>Store</th>
                                <th>Status</th>
                                <th>Product URL</th>
                                <th>Selectors</th>
                                <th>Actions</th>
                            </tr>
                        </thead>
                        <tbody>
                            {others.map((p) => (
                                <TrackingRow
                                    key={p.id}
                                    pair={p}
                                    onUpdated={replacePair}
                                    confirm={confirm}
                                />
                            ))}
                        </tbody>
                    </table>
                )}
                <p className="muted">
                    There's no hard delete for a tracking pair yet — the API
                    layer doesn't expose{" "}
                    <code>DELETE /api/tracking/{"{id}"}</code>. Use Skip to
                    stop tracking a pair without losing its history.
                </p>
            </Window>
        </div>
    );
}

interface TrackingRowProps {
    pair: TrackingPairDto;
    highlighted?: boolean;
    onUpdated: (pair: TrackingPairDto) => void;
    confirm: (
        message: string,
        options?: {
            title?: string;
            confirmLabel?: string;
            destructive?: boolean;
        },
    ) => Promise<boolean>;
}

function TrackingRow({ pair, highlighted, onUpdated, confirm }: TrackingRowProps) {
    const [productUrl, setProductUrl] = useState(pair.productUrl ?? "");
    const [priceSelector, setPriceSelector] = useState(
        pair.priceSelector ?? "",
    );
    const [stockSelector, setStockSelector] = useState(
        pair.stockSelector ?? "",
    );
    const [editingSelectors, setEditingSelectors] = useState(false);
    const [busy, setBusy] = useState(false);
    const [discoverError, setDiscoverError] = useState<string | null>(null);

    async function saveUrl() {
        if (productUrl === (pair.productUrl ?? "")) return; // no-op, avoid a needless PATCH
        setBusy(true);
        try {
            onUpdated(await updateTracking(pair.id, { productUrl }));
        } finally {
            setBusy(false);
        }
    }

    async function saveSelectors() {
        setBusy(true);
        try {
            onUpdated(
                await updateTracking(pair.id, {
                    priceSelector: priceSelector || undefined,
                    stockSelector: stockSelector || undefined,
                }),
            );
            setEditingSelectors(false);
        } finally {
            setBusy(false);
        }
    }

    async function clearSelectors() {
        const confirmed = await confirm(
            `Clear both selectors for ${pair.book.name} at ${pair.store.name}? This pair will move to "Needs setup" until new selectors are provided.`,
            {
                title: "Clear selectors",
                confirmLabel: "Clear",
                destructive: true,
            },
        );
        if (!confirmed) return;
        setBusy(true);
        try {
            onUpdated(
                await updateTracking(pair.id, {
                    priceSelector: "",
                    stockSelector: "",
                }),
            );
            setPriceSelector("");
            setStockSelector("");
            setEditingSelectors(false);
        } finally {
            setBusy(false);
        }
    }

    async function handleDiscover() {
        setBusy(true);
        setDiscoverError(null);
        try {
            const result: DiscoverResultDto = await discoverSelectors(pair.id);
            setPriceSelector(result.priceSelector ?? "");
            setStockSelector(result.stockSelector ?? "");
            if (result.reason) setDiscoverError(result.reason);
        } catch (err) {
            setDiscoverError(
                err instanceof Error ? err.message : "Discovery failed",
            );
        } finally {
            setBusy(false);
        }
    }

    async function toggleSkip() {
        setBusy(true);
        try {
            const nextStatus = pair.status === "SKIP" ? "PENDING" : "SKIP";
            onUpdated(await updateTracking(pair.id, { status: nextStatus }));
        } finally {
            setBusy(false);
        }
    }

    const selectorEditor = (
        <div className="selector-editor">
            <input
                placeholder="Price selector"
                value={priceSelector}
                onChange={(e) => setPriceSelector(e.target.value)}
            />
            <input
                placeholder="Stock selector"
                value={stockSelector}
                onChange={(e) => setStockSelector(e.target.value)}
            />
            <button onClick={handleDiscover} disabled={busy}>
                Discover
            </button>
            <button
                onClick={saveSelectors}
                disabled={busy || !priceSelector || !stockSelector}>
                Save
            </button>
            {(pair.priceSelector || pair.stockSelector) && (
                <button onClick={clearSelectors} disabled={busy}>
                    Clear selectors
                </button>
            )}
            {discoverError && (
                <span className="form-error">{discoverError}</span>
            )}
        </div>
    );

    if (highlighted) {
        return (
            <div className="needs-setup-row">
                <div>
                    <strong>{pair.book.name}</strong> at {pair.store.name}
                    <div className="muted">{pair.productUrl}</div>
                </div>
                {selectorEditor}
            </div>
        );
    }

    return (
        <>
            <tr>
                <td>{pair.book.name}</td>
                <td>{pair.store.name}</td>
                <td>
                    <AvailabilityBadge status={pair.status} />
                </td>
                <td>
                    <input
                        value={productUrl}
                        onChange={(e) => setProductUrl(e.target.value)}
                        onBlur={saveUrl}
                    />
                </td>
                <td>{pair.selectorsCached ? "Cached" : "—"}</td>
                <td>
                    {/* <span className="inline-flex gap-2"> */}
                    <span className="btn-group">
                        <button
                            onClick={() => setEditingSelectors((v) => !v)}>
                            {editingSelectors
                                ? "Close Selectors"
                                : "Edit Selectors"}
                        </button>
                        <button onClick={toggleSkip} disabled={busy}>
                            {pair.status === "SKIP"
                                ? "Re-enable"
                                : "Mark as Skip"}
                        </button>
                    </span>
                </td>
            </tr>
            {editingSelectors && (
                <tr className="selector-editor-row">
                    <td colSpan={6}>{selectorEditor}</td>
                </tr>
            )}
        </>
    );
}
