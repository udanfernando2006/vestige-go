import { useEffect, useState } from "react";
import { getNotificationsEnabled, setNotificationsEnabled } from "../api/settings";
import { getSettings, updateSettings } from "../api/client";
import type { SettingsDto, SettingsUpdateDto } from "../api/types";
import Window from "../components/Window";

export default function Settings() {
    const [notificationsEnabled, setNotificationsEnabledState] = useState(true);
    const [notificationsLoaded, setNotificationsLoaded] = useState(false);
    // getNotificationsEnabled/setNotificationsEnabled currently throw — the
    // toggle's persistence needs domain.Settings extended on the Go side
    // (see settings.ts's own TODO). Surfaced here instead of crashing the
    // page, so the rest of Settings stays usable in the meantime.
    const [notificationsError, setNotificationsError] = useState<
        string | null
    >(null);

    const [pipeline, setPipeline] = useState<SettingsDto | null>(null);
    const [pipelineError, setPipelineError] = useState<string | null>(null);
    const [syncing, setSyncing] = useState(false);
    const [pipelineSaved, setPipelineSaved] = useState(false);

    const [draft, setDraft] = useState({
        llmDiscoveryEnabled: false,
        llmMode: "direct",
        scrapeIntervalHours: "" as number | "", // '' renders as a blank box, means disabled
        selectorApiBase: "",
        selectorApiKey: "", // secret fields start blank — never pre-filled with the real value
        selectorModel: "",
        directApiBase: "",
        directApiKey: "",
        directModel: "",
    });

    function loadPipelineSettings() {
        setPipelineError(null);
        getSettings()
            .then((s) => {
                setPipeline(s);
                setDraft((d) => ({
                    ...d,
                    llmDiscoveryEnabled: s.llmDiscoveryEnabled,
                    llmMode: s.llmMode,
                    scrapeIntervalHours: s.scrapeIntervalHours ?? "",
                    selectorApiBase: s.selectorApiBase,
                    selectorApiKey: "",
                    selectorModel: s.selectorModel,
                    directApiBase: s.directApiBase,
                    directApiKey: "",
                    directModel: s.directModel,
                }));
            })
            .catch((err) =>
                setPipelineError(
                    err instanceof Error
                        ? err.message
                        : "Failed to load pipeline settings",
                ),
            );
    }

    useEffect(() => {
        getNotificationsEnabled()
            .then((enabled) => {
                setNotificationsEnabledState(enabled);
                setNotificationsLoaded(true);
            })
            .catch((err) => {
                setNotificationsError(
                    err instanceof Error
                        ? err.message
                        : "Notification preferences aren't wired up yet",
                );
                setNotificationsLoaded(true); // stop showing "loading", show the error state instead
            });
        loadPipelineSettings();
    }, []);

    async function handleToggleNotifications(checked: boolean) {
        const previous = notificationsEnabled;
        setNotificationsEnabledState(checked); // optimistic
        try {
            await setNotificationsEnabled(checked);
            setNotificationsError(null);
        } catch (err) {
            setNotificationsEnabledState(previous); // roll back — the write didn't actually happen
            setNotificationsError(
                err instanceof Error
                    ? err.message
                    : "Failed to save notification preference",
            );
        }
    }

    async function handleSavePipeline(e: React.FormEvent) {
        e.preventDefault();
        setSyncing(true);
        setPipelineError(null);
        try {
            const update: SettingsUpdateDto = {
                llmDiscoveryEnabled: draft.llmDiscoveryEnabled,
                llmMode: draft.llmMode,
                // '' means disabled — send 0, which the scraper service converts
                // into an explicit "" clear on the SCRAPE_INTERVAL_HOURS override.
                scrapeIntervalHours:
                    draft.scrapeIntervalHours === ""
                        ? 0
                        : draft.scrapeIntervalHours,
                selectorApiBase: draft.selectorApiBase,
                selectorModel: draft.selectorModel,
                directApiBase: draft.directApiBase,
                directModel: draft.directModel,
                ...(draft.selectorApiKey
                    ? { selectorApiKey: draft.selectorApiKey }
                    : {}),
                ...(draft.directApiKey
                    ? { directApiKey: draft.directApiKey }
                    : {}),
            };
            await updateSettings(update);
            setPipelineSaved(true);
            setTimeout(() => setPipelineSaved(false), 2000);
            loadPipelineSettings();
        } catch (err) {
            setPipelineError(
                err instanceof Error
                    ? err.message
                    : "Failed to save pipeline settings",
            );
        } finally {
            setSyncing(false);
        }
    }

    function clearKey(field: "selectorApiKey" | "directApiKey") {
        updateSettings({ [field]: "" } as SettingsUpdateDto)
            .then(loadPipelineSettings)
            .catch((err) =>
                setPipelineError(
                    err instanceof Error ? err.message : "Failed to clear key",
                ),
            );
    }

    return (
        <div className="page">
            <h2>Settings</h2>

            {/* "Backend connection" window removed entirely — the backend is
                always co-located now (same binary as the shell), discovered
                automatically via GetAPIPort(), so there's no other
                deployment target to point at and nothing left to configure
                here. See api/settings.ts. */}

            <Window title="Notifications">
                <label className="checkbox-label">
                    <input
                        type="checkbox"
                        checked={notificationsEnabled}
                        disabled={!notificationsLoaded}
                        onChange={(e) =>
                            handleToggleNotifications(e.target.checked)
                        }
                    />
                    Show a desktop notification when a tracked book's price or
                    stock changes
                </label>
                <p className="muted">
                    Takes effect on the next check, within about 90 seconds — no
                    restart needed. Turning this off doesn't pause checking for
                    changes, so turning it back on later won't replay everything
                    that happened while it was off.
                </p>
                {notificationsError && (
                    <p className="form-error">{notificationsError}</p>
                )}
            </Window>

            {/* "Local backend automation" window removed entirely — no
                Docker stack exists to auto-start/stop; the backend is the
                same process as the shell. */}

            <Window title="Pipeline configuration">
                <form onSubmit={handleSavePipeline}>
                    {pipelineError && (
                        <p className="form-error">{pipelineError}</p>
                    )}
                    {!pipeline ? (
                        <p className="muted">Loading…</p>
                    ) : (
                        <>
                            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '1.5rem', alignItems: 'start' }}>
                                {/* Left Column: LLM mode */}
                                <div>
                                    <label>
                                        LLM mode
                                        <select
                                            value={draft.llmMode}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    llmMode: e.target.value,
                                                })
                                            }>
                                            <option value="direct">
                                                Direct extraction (Path D)
                                            </option>
                                            <option value="selector">
                                                Selector discovery (Path B/C)
                                            </option>
                                        </select>
                                    </label>
                                    <label className="checkbox-label">
                                        <input
                                            type="checkbox"
                                            checked={draft.llmDiscoveryEnabled}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    llmDiscoveryEnabled:
                                                        e.target.checked,
                                                })
                                            }
                                        />
                                        Run selector discovery automatically in the
                                        pipeline
                                    </label>
                                </div>

                                {/* Right Column: Automation */}
                                <div>
                                    <h4 style={{ marginTop: 0 }}>Automation</h4>
                                    <label>
                                        Run scraper automatically every (hours)
                                        <input
                                            type="number"
                                            min="1"
                                            step="1"
                                            placeholder="Disabled"
                                            value={draft.scrapeIntervalHours}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    scrapeIntervalHours:
                                                        e.target.value === ""
                                                            ? ""
                                                            : Number(e.target.value),
                                                })
                                            }
                                        />
                                    </label>
                                    <p className="muted">
                                        Checked roughly once a minute — actual run time
                                        may drift slightly from the exact hour mark.
                                        Leave blank to disable.
                                    </p>
                                </div>
                            </div>

                            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '1.5rem', marginTop: '1.5rem', alignItems: 'start' }}>
                                {/* Left Column: Selector Discovery */}
                                <div>
                                    <h4 style={{ marginTop: 0 }}>Selector discovery (Path B)</h4>
                                    <label>
                                        API base
                                        <input
                                            value={draft.selectorApiBase}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    selectorApiBase: e.target.value,
                                                })
                                            }
                                        />
                                    </label>
                                    <label>
                                        API key{" "}
                                        {pipeline.selectorApiKeyConfigured && (
                                            <span className="muted">
                                                currently set (
                                                {pipeline.selectorApiKeyHint})
                                            </span>
                                        )}
                                        <input
                                            type="password"
                                            value={draft.selectorApiKey}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    selectorApiKey: e.target.value,
                                                })
                                            }
                                            placeholder={
                                                pipeline.selectorApiKeyConfigured
                                                    ? "Leave blank to keep current key"
                                                    : "Not set"
                                            }
                                        />
                                        {pipeline.selectorApiKeyConfigured && (
                                            <button
                                                type="button"
                                                className="mt-2"
                                                onClick={() =>
                                                    clearKey("selectorApiKey")
                                                }>
                                                Clear key
                                            </button>
                                        )}
                                    </label>
                                    <label>
                                        Model
                                        <input
                                            value={draft.selectorModel}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    selectorModel: e.target.value,
                                                })
                                            }
                                        />
                                    </label>
                                </div>

                                {/* Right Column: Direct Extraction */}
                                <div>
                                    <h4 style={{ marginTop: 0 }}>Direct extraction (Path D)</h4>
                                    <label>
                                        API base
                                        <input
                                            value={draft.directApiBase}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    directApiBase: e.target.value,
                                                })
                                            }
                                        />
                                    </label>
                                    <label>
                                        API key{" "}
                                        {pipeline.directApiKeyConfigured && (
                                            <span className="muted">
                                                currently set (
                                                {pipeline.directApiKeyHint})
                                            </span>
                                        )}
                                        <input
                                            type="password"
                                            value={draft.directApiKey}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    directApiKey: e.target.value,
                                                })
                                            }
                                            placeholder={
                                                pipeline.directApiKeyConfigured
                                                    ? "Leave blank to keep current key"
                                                    : "Not set"
                                            }
                                        />
                                        {pipeline.directApiKeyConfigured && (
                                            <button
                                                type="button"
                                                className="mt-2"
                                                onClick={() =>
                                                    clearKey("directApiKey")
                                                }>
                                                Clear key
                                            </button>
                                        )}
                                    </label>
                                    <label>
                                        Model
                                        <input
                                            value={draft.directModel}
                                            onChange={(e) =>
                                                setDraft({
                                                    ...draft,
                                                    directModel: e.target.value,
                                                })
                                            }
                                        />
                                    </label>
                                </div>
                            </div>


                            <button type="submit" disabled={syncing}>
                                {syncing ? "Saving…" : "Save pipeline settings"}
                            </button>
                            {pipelineSaved && (
                                <p className="form-success">
                                    Saved — takes effect on the next run, no
                                    restart needed.
                                </p>
                            )}
                        </>
                    )}
                </form>
            </Window>
        </div>
    );
}
