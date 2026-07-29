import { useEffect, useState, useCallback } from "react";
import {
    getBooks,
    createBook,
    deleteBook,
    updateBook,
    getSeries,
    updateSeries,
    deleteSeries,
    bulkAssignSeries,
} from "../api/client";
import type { BookGroupDto, SeriesDto } from "../api/types";
import Window from "../components/Window";
import KebabMenu from "../components/KebabMenu";
import { useConfirm } from "../hooks/useConfirm";

export default function Books() {
    const [groups, setGroups] = useState<BookGroupDto[]>([]);
    const [seriesList, setSeriesList] = useState<SeriesDto[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    // Add-book form
    const [name, setName] = useState("");
    const [isbn, setIsbn] = useState("");
    const [seriesName, setSeriesName] = useState("");
    const [isSeriesEntry, setIsSeriesEntry] = useState(false);
    const [submitting, setSubmitting] = useState(false);

    // Selection + bulk assign
    const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
    const [bulkTarget, setBulkTarget] = useState<string>(""); // "" = none, "__new__" = new series, else seriesId
    const [bulkNewName, setBulkNewName] = useState("");
    const [bulkBusy, setBulkBusy] = useState(false);

    // Series rename (one at a time)
    const [renamingSeriesId, setRenamingSeriesId] = useState<number | null>(null);
    const [renameDraft, setRenameDraft] = useState("");

    // Series detail editing (author/description), one at a time
    const [editingSeriesId, setEditingSeriesId] = useState<number | null>(null);
    const [seriesAuthorDraft, setSeriesAuthorDraft] = useState("");
    const [seriesDescDraft, setSeriesDescDraft] = useState("");
    const [seriesEditBusy, setSeriesEditBusy] = useState(false);

    // Book detail editing (author/description), one at a time
    const [editingBookId, setEditingBookId] = useState<number | null>(null);
    const [editAuthor, setEditAuthor] = useState("");
    const [editDescription, setEditDescription] = useState("");
    const [editBusy, setEditBusy] = useState(false);

    const { confirm, dialog } = useConfirm();

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const [bookGroups, series] = await Promise.all([getBooks(), getSeries()]);
            setGroups(bookGroups);
            setSeriesList(series);
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to load books");
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
            await createBook({
                name,
                isbn,
                isSeriesEntry,
                seriesName: seriesName || null,
            });
            setName("");
            setIsbn("");
            setSeriesName("");
            setIsSeriesEntry(false);
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to add book");
        } finally {
            setSubmitting(false);
        }
    }

    async function handleDelete(id: number, title: string) {
        const confirmed = await confirm(
            `Delete "${title}"? This also deletes all tracking pairs and history for this book.`,
            { title: "Delete book", confirmLabel: "Delete", destructive: true },
        );
        if (!confirmed) return;
        try {
            await deleteBook(id);
            setSelectedIds((prev) => {
                const next = new Set(prev);
                next.delete(id);
                return next;
            });
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to delete book");
        }
    }

    function toggleSelected(id: number) {
        setSelectedIds((prev) => {
            const next = new Set(prev);
            if (next.has(id)) next.delete(id);
            else next.add(id);
            return next;
        });
    }

    function clearSelection() {
        setSelectedIds(new Set());
        setBulkTarget("");
        setBulkNewName("");
    }

    async function handleBulkAssign() {
        if (selectedIds.size === 0 || !bulkTarget) return;
        setBulkBusy(true);
        setError(null);
        try {
            if (bulkTarget === "__new__") {
                if (!bulkNewName.trim()) {
                    setError("Enter a name for the new series");
                    setBulkBusy(false);
                    return;
                }
                await bulkAssignSeries({
                    bookIds: Array.from(selectedIds),
                    newSeriesName: bulkNewName.trim(),
                });
            } else {
                await bulkAssignSeries({
                    bookIds: Array.from(selectedIds),
                    seriesId: Number(bulkTarget),
                });
            }
            clearSelection();
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to assign series");
        } finally {
            setBulkBusy(false);
        }
    }

    function startRenameSeries(s: SeriesDto) {
        setRenamingSeriesId(s.id);
        setRenameDraft(s.name);
        setEditingSeriesId(null);
    }

    async function saveRenameSeries(id: number) {
        if (!renameDraft.trim()) return;
        try {
            await updateSeries(id, { name: renameDraft.trim() });
            setRenamingSeriesId(null);
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to rename series");
        }
    }

    function startEditSeries(s: SeriesDto) {
        setEditingSeriesId(s.id);
        setSeriesAuthorDraft(s.author ?? "");
        setSeriesDescDraft(s.description ?? "");
        setRenamingSeriesId(null);
    }

    async function saveEditSeries(id: number) {
        setSeriesEditBusy(true);
        setError(null);
        try {
            await updateSeries(id, {
                author: seriesAuthorDraft,
                description: seriesDescDraft,
            });
            setEditingSeriesId(null);
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to update series");
        } finally {
            setSeriesEditBusy(false);
        }
    }

    async function handleDeleteSeries(s: SeriesDto) {
        const confirmed = await confirm(
            `Delete series "${s.name}"? Its ${s.bookCount} book(s) become standalone — they are not deleted.`,
            { title: "Delete series", confirmLabel: "Delete", destructive: true },
        );
        if (!confirmed) return;
        try {
            await deleteSeries(s.id);
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to delete series");
        }
    }

    function startEditBook(id: number, author?: string, description?: string) {
        setEditingBookId(id);
        setEditAuthor(author ?? "");
        setEditDescription(description ?? "");
    }

    async function saveEditBook(id: number) {
        setEditBusy(true);
        setError(null);
        try {
            await updateBook(id, {
                author: editAuthor,
                description: editDescription,
            });
            setEditingBookId(null);
            await load();
        } catch (err) {
            setError(err instanceof Error ? err.message : "Failed to update book");
        } finally {
            setEditBusy(false);
        }
    }

    const selectedCount = selectedIds.size;

    return (
        <div className="page">
            <h2>Books</h2>
            {error && <p className="form-error">{error}</p>}
            {dialog}

            <Window title="Add book">
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
                        ISBN{" "}
                        <input
                            value={isbn}
                            onChange={(e) => setIsbn(e.target.value)}
                            required
                        />
                    </label>
                    <label>
                        Series (optional){" "}
                        <input
                            value={seriesName}
                            onChange={(e) => setSeriesName(e.target.value)}
                        />
                    </label>
                    <label className="checkbox-label">
                        <input
                            type="checkbox"
                            checked={isSeriesEntry}
                            onChange={(e) => setIsSeriesEntry(e.target.checked)}
                        />
                        Part of a series
                    </label>
                    <button type="submit" disabled={submitting}>
                        {submitting ? "Adding…" : "Add book"}
                    </button>
                </form>
            </Window>

            {selectedCount > 0 && (
                <Window title={`${selectedCount} book(s) selected`}>
                    <div className="btn-group">
                        <select
                            value={bulkTarget}
                            onChange={(e) => setBulkTarget(e.target.value)}
                        >
                            <option value="">Add to series…</option>
                            {seriesList.map((s) => (
                                <option key={s.id} value={s.id}>
                                    {s.name}
                                </option>
                            ))}
                            <option value="__new__">+ New series…</option>
                        </select>
                        {bulkTarget === "__new__" && (
                            <input
                                placeholder="New series name"
                                value={bulkNewName}
                                onChange={(e) => setBulkNewName(e.target.value)}
                            />
                        )}
                        <button
                            onClick={handleBulkAssign}
                            disabled={bulkBusy || !bulkTarget}
                        >
                            {bulkBusy ? "Assigning…" : "Assign"}
                        </button>
                        <button onClick={clearSelection} disabled={bulkBusy}>
                            Clear selection
                        </button>
                    </div>
                    <p className="muted">
                        This replaces each selected book's current series, if any.
                    </p>
                </Window>
            )}

            {loading ? (
                <p>Loading…</p>
            ) : (
                groups.map((g) => {
                    const seriesEntity = seriesList.find((s) => s.name === g.seriesName);
                    const isRenaming = seriesEntity && renamingSeriesId === seriesEntity.id;
                    const isEditingSeries = seriesEntity && editingSeriesId === seriesEntity.id;

                    const headerActions = seriesEntity ? (
                        isRenaming ? (
                            <div className="btn-group">
                                <input
                                    value={renameDraft}
                                    onChange={(e) => setRenameDraft(e.target.value)}
                                />
                                <button onClick={() => saveRenameSeries(seriesEntity.id)}>
                                    Save
                                </button>
                                <button onClick={() => setRenamingSeriesId(null)}>
                                    Cancel
                                </button>
                            </div>
                        ) : (
                            <KebabMenu
                                label={`Actions for ${seriesEntity.name}`}
                                items={[
                                    {
                                        label: "Rename series",
                                        onClick: () => startRenameSeries(seriesEntity),
                                    },
                                    {
                                        label: "Edit series details",
                                        onClick: () => startEditSeries(seriesEntity),
                                    },
                                    {
                                        label: "Delete series",
                                        destructive: true,
                                        onClick: () => handleDeleteSeries(seriesEntity),
                                    },
                                ]}
                            />
                        )
                    ) : undefined;

                    return (
                        <Window
                            key={g.seriesName ?? "__standalone__"}
                            title={g.seriesName ?? "Standalone"}
                            actions={headerActions}
                        >
                            {isEditingSeries && seriesEntity && (
                                <div className="series-detail-editor">
                                    <label>
                                        Author(s){" "}
                                        <input
                                            value={seriesAuthorDraft}
                                            onChange={(e) =>
                                                setSeriesAuthorDraft(e.target.value)
                                            }
                                        />
                                    </label>
                                    <label>
                                        Description
                                        <textarea
                                            value={seriesDescDraft}
                                            onChange={(e) =>
                                                setSeriesDescDraft(e.target.value)
                                            }
                                            rows={3}
                                        />
                                    </label>
                                    <div className="btn-group">
                                        <button
                                            onClick={() => saveEditSeries(seriesEntity.id)}
                                            disabled={seriesEditBusy}
                                        >
                                            {seriesEditBusy ? "Saving…" : "Save"}
                                        </button>
                                        <button
                                            onClick={() => setEditingSeriesId(null)}
                                            disabled={seriesEditBusy}
                                        >
                                            Cancel
                                        </button>
                                    </div>
                                </div>
                            )}
                            {seriesEntity &&
                                !isEditingSeries &&
                                (seriesEntity.author || seriesEntity.description) && (
                                    <div className="series-detail-summary muted">
                                        {seriesEntity.author && (
                                            <span>{seriesEntity.author}</span>
                                        )}
                                        {seriesEntity.description && (
                                            <p>{seriesEntity.description}</p>
                                        )}
                                    </div>
                                )}
                            <table>
                                <thead>
                                    <tr>
                                        <th></th>
                                        <th>Name</th>
                                        <th>ISBN</th>
                                        <th>Author</th>
                                        <th>Description</th>
                                        <th></th>
                                    </tr>
                                </thead>
                                <tbody>
                                    {g.books.map((b) => (
                                        <>
                                            <tr key={b.id}>
                                                <td>
                                                    <input
                                                        type="checkbox"
                                                        checked={selectedIds.has(b.id)}
                                                        onChange={() => toggleSelected(b.id)}
                                                    />
                                                </td>
                                                <td>{b.name}</td>
                                                <td>{b.isbn}</td>
                                                <td>{b.author || "—"}</td>
                                                <td className="truncate-cell">
                                                    {b.description || "—"}
                                                </td>
                                                <td>
                                                    <KebabMenu
                                                        label={`Actions for ${b.name}`}
                                                        items={[
                                                            {
                                                                label:
                                                                    editingBookId === b.id
                                                                        ? "Close details"
                                                                        : "Edit details",
                                                                onClick: () =>
                                                                    editingBookId === b.id
                                                                        ? setEditingBookId(null)
                                                                        : startEditBook(
                                                                              b.id,
                                                                              b.author,
                                                                              b.description,
                                                                          ),
                                                            },
                                                            {
                                                                label: "Delete",
                                                                destructive: true,
                                                                onClick: () =>
                                                                    handleDelete(b.id, b.name),
                                                            },
                                                        ]}
                                                    />
                                                </td>
                                            </tr>
                                            {editingBookId === b.id && (
                                                <tr key={`${b.id}-edit`}>
                                                    <td colSpan={6}>
                                                        <label>
                                                            Author{" "}
                                                            <input
                                                                value={editAuthor}
                                                                onChange={(e) =>
                                                                    setEditAuthor(e.target.value)
                                                                }
                                                            />
                                                        </label>
                                                        <label>
                                                            Description
                                                            <textarea
                                                                value={editDescription}
                                                                onChange={(e) =>
                                                                    setEditDescription(
                                                                        e.target.value,
                                                                    )
                                                                }
                                                                rows={3}
                                                            />
                                                        </label>
                                                        <div className="btn-group">
                                                            <button
                                                                onClick={() => saveEditBook(b.id)}
                                                                disabled={editBusy}
                                                            >
                                                                {editBusy ? "Saving…" : "Save"}
                                                            </button>
                                                            <button
                                                                onClick={() =>
                                                                    setEditingBookId(null)
                                                                }
                                                                disabled={editBusy}
                                                            >
                                                                Cancel
                                                            </button>
                                                        </div>
                                                    </td>
                                                </tr>
                                            )}
                                        </>
                                    ))}
                                </tbody>
                            </table>
                        </Window>
                    );
                })
            )}
        </div>
    );
}
