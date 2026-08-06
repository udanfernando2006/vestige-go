import { useEffect, useState, useCallback } from "react";
import {
    getStores,
    createStore,
    updateStore,
    deleteStore,
} from "../api/client";
import type { StoreDto } from "../api/types";
import Window from "../components/Window";
import { useConfirm } from "../hooks/useConfirm";

export default function Stores() {
    const [stores, setStores] = useState<StoreDto[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const [name, setName] = useState("");
    const [baseUrl, setBaseUrl] = useState("");
    const [submitting, setSubmitting] = useState(false);
    const [editingId, setEditingId] = useState<number | null>(null);
    const [editDraft, setEditDraft] = useState({
        name: "",
        baseUrl: "",
        searchUrlTemplate: "",
    });
    const [savingEdit, setSavingEdit] = useState(false);

    const { confirm, dialog } = useConfirm();

    const load = useCallback(async () => {
        setLoading(true);
        try {
            setStores(await getStores());
        } catch (err) {
            setError(
                err instanceof Error ? err.message : "Failed to load stores",
            );
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        load();
    }, [load]);

    async function handleAdd(e: React.FormEvent) {
        e.preventDefault();
        setSubmitting(true);
        setError(null);
        try {
            await createStore({ name, baseUrl });
            setName("");
            setBaseUrl("");
            await load();
        } catch (err) {
            setError(
                err instanceof Error ? err.message : "Failed to add store",
            );
        } finally {
            setSubmitting(false);
        }
    }

    function startEdit(store: StoreDto) {
        setEditingId(store.id);
        setEditDraft({
            name: store.name,
            baseUrl: store.baseUrl,
            searchUrlTemplate: store.searchUrlTemplate ?? "",
        });
    }

    async function saveEdit(id: number) {
        setSavingEdit(true);
        setError(null);
        try {
            await updateStore(id, {
                name: editDraft.name,
                baseUrl: editDraft.baseUrl,
                // "" clears the template back to "undiscovered" — same convention
                // as the pipeline settings' secret fields and the Tracking page's
                // selectors. The draft is pre-filled from the store's actual current
                // value in startEdit() above, so saving without touching this field
                // sends that same value back, not an accidental clear.
                searchUrlTemplate: editDraft.searchUrlTemplate,
            });
            setEditingId(null);
            await load();
        } catch (err) {
            setError(
                err instanceof Error ? err.message : "Failed to update store",
            );
        } finally {
            setSavingEdit(false);
        }
    }

    async function handleDelete(store: StoreDto) {
        const confirmed = await confirm(
            `Delete "${store.name}"? This also deletes every tracking pair for this store and all of its availability history.`,
            {
                title: "Delete store",
                confirmLabel: "Delete",
                destructive: true,
            },
        );
        if (!confirmed) return;
        try {
            await deleteStore(store.id);
            await load();
        } catch (err) {
            setError(
                err instanceof Error ? err.message : "Failed to delete store",
            );
        }
    }

    return (
        <div className="page">
            <h2>Stores</h2>
            {error && <p className="form-error">{error}</p>}
            {dialog}

            <Window title="Add store">
                <form onSubmit={handleAdd}>
                    <label>
                        Name{" "}
                        <input
                            value={name}
                            onChange={(e) => setName(e.target.value)}
                            required
                        />
                    </label>
                    <label>
                        Base URL
                        <input
                            value={baseUrl}
                            onChange={(e) => setBaseUrl(e.target.value)}
                            placeholder="https://…"
                            required
                        />
                    </label>
                    <button type="submit" disabled={submitting}>
                        {submitting ? "Adding…" : "Add store"}
                    </button>
                </form>
            </Window>

            <Window title="Stores">
                <table>
                    <thead>
                        <tr>
                            <th>Name</th>
                            <th>Base URL</th>
                            <th>Search URL template</th>
                            <th></th>
                        </tr>
                    </thead>
                    <tbody>
                        {loading ? (
                            <tr>
                                <td colSpan={4}>Loading…</td>
                            </tr>
                        ) : (
                            stores.map((s) =>
                                editingId === s.id ? (
                                    <tr key={s.id}>
                                        <td>
                                            <input
                                                value={editDraft.name}
                                                onChange={(e) =>
                                                    setEditDraft({
                                                        ...editDraft,
                                                        name: e.target.value,
                                                    })
                                                }
                                            />
                                        </td>
                                        <td>
                                            <input
                                                value={editDraft.baseUrl}
                                                onChange={(e) =>
                                                    setEditDraft({
                                                        ...editDraft,
                                                        baseUrl:
                                                            e.target.value,
                                                    })
                                                }
                                            />
                                        </td>
                                        <td>
                                            <input
                                                value={
                                                    editDraft.searchUrlTemplate
                                                }
                                                onChange={(e) =>
                                                    setEditDraft({
                                                        ...editDraft,
                                                        searchUrlTemplate:
                                                            e.target.value,
                                                    })
                                                }
                                                placeholder="Undiscovered — leave blank"
                                            />
                                        </td>
                                        <td>
                                            {/* <span className="inline-flex gap-2"> */}
                                            <span className="btn-group">
                                                <button
                                                    onClick={() =>
                                                        saveEdit(s.id)
                                                    }
                                                    disabled={savingEdit}>
                                                    Save
                                                </button>
                                                <button
                                                    type="button"
                                                    onClick={() =>
                                                        setEditingId(null)
                                                    }>
                                                    Cancel
                                                </button>
                                            </span>
                                        </td>
                                    </tr>
                                ) : (
                                    <tr key={s.id}>
                                        <td>{s.name}</td>
                                        <td>{s.baseUrl}</td>
                                        <td className="muted">
                                            {s.searchUrlTemplate ??
                                                "Undiscovered"}
                                        </td>
                                        <td>
                                            {/* <span className="inline-flex gap-2"> */}
                                            <span className="btn-group">
                                                <button
                                                    type="button"
                                                    onClick={() =>
                                                        startEdit(s)
                                                    }>
                                                    Edit
                                                </button>
                                                <button
                                                    type="button"
                                                    onClick={() =>
                                                        handleDelete(s)
                                                    }>
                                                    Delete
                                                </button>
                                            </span>
                                        </td>
                                    </tr>
                                ),
                            )
                        )}
                    </tbody>
                </table>
                <p className="muted">
                    The search URL template must contain the literal token{" "}
                    <code>=test</code> (e.g.{" "}
                    <code>https://store.com/?s=test</code>) — the Crawler
                    replaces it with the encoded search query on each
                    discovery run. Leaving it blank just means the Crawler
                    re-discovers it the next time a pair on this store needs a
                    product URL.
                </p>
            </Window>
        </div>
    );
}
