// Port discovery replaces the old stored-URL design entirely: the backend
// is always co-located now (no cloud-VM target to point at), and the Gin
// server binds an OS-assigned ephemeral port (net.Listen("tcp", "127.0.0.1:0"))
// specifically so there's nothing for a user's other running app to collide
// with. GetAPIPort() is a real Wails-bound Go method (internal.APIService,
// see cmd/vestige/main.go) — calling it is in-process IPC, not an HTTP
// request, so it works before the frontend has any idea what port the REST
// API is even on. Import path below assumes go.mod's module path is
// github.com/udanfernando2006/vestige-go — confirm against the real
// generated frontend/bindings/ tree once it exists; Wails names the folder
// after the actual module path, not a fixed convention.
import { GetAPIPort } from "../../bindings/github.com/udanfernando2006/vestige-go/cmd/vestige/apiservice";

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

// TODO: notificationsEnabled has nowhere to live now. It was device-local
// (tauri-plugin-store) in v1 specifically because Java/Python couldn't reach
// it — that reason no longer applies in a single Go process. Recommended
// fix: fold this into the existing domain.Settings/setting_overrides system
// (internal/store, already has full REST plumbing via Phase 3's Settings
// handler) as a 12th key, rather than sourcing a separate Wails-native
// key-value store for one boolean. Left unimplemented here on purpose —
// needs the Settings handler/DTO touched on the Go side first.
export async function getNotificationsEnabled(): Promise<boolean> {
    throw new Error(
        "getNotificationsEnabled: not yet wired to the new settings storage — see TODO above",
    );
}

export async function setNotificationsEnabled(_enabled: boolean): Promise<void> {
    throw new Error(
        "setNotificationsEnabled: not yet wired to the new settings storage — see TODO above",
    );
}

// getAutoDockerEnabled / setAutoDockerEnabled / getAutoDockerPromptContext /
// setAutoDockerPromptContext / isLocalDeployment — all deleted outright.
// There's no Docker stack to auto-manage and no other deployment target to
// detect; see vestige_go_ui_implementation.md §4 for the full removal list.
