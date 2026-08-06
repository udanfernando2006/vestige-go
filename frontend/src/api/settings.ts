import { GetAPIPort } from "../../bindings/github.com/udanfernando2006/vestige-go/cmd/vestige/apiservice";
import { getSettings as fetchSettings, updateSettings as pushSettings } from "./client";

let cachedBaseUrl: string | null = null;

export async function getApiBaseUrl(): Promise<string> {
    if (cachedBaseUrl) return cachedBaseUrl;
    const port = await GetAPIPort();
    cachedBaseUrl = `http://127.0.0.1:${port}`;
    return cachedBaseUrl;
}

// No setApiBaseUrl anymore — there's nothing left for a user to configure.
// If a fixed/overridable port ever turns out to be wanted (e.g. for poking
// the API manually with curl during development), that would live as a
// stored override checked *before* falling back to the ephemeral one below,
// not as a replacement for it — not built here since it wasn't confirmed
// as actually wanted yet.

// Wired to the real settings storage: notificationsEnabled is now the
// 12th domain.Settings/setting_overrides key (NOTIFICATIONS_ENABLED),
// same system every other setting already goes through — GET/PUT
// /api/settings via client.ts, not a separate device-local store.
export async function getNotificationsEnabled(): Promise<boolean> {
    const settings = await fetchSettings();
    return settings.notificationsEnabled;
}

export async function setNotificationsEnabled(enabled: boolean): Promise<void> {
    await pushSettings({ notificationsEnabled: enabled });
}

// getAutoDockerEnabled / setAutoDockerEnabled / getAutoDockerPromptContext /
// setAutoDockerPromptContext / isLocalDeployment — all deleted outright.
// There's no Docker stack to auto-manage and no other deployment target to
// detect; see vestige_go_ui_implementation.md §4 for the full removal list.
