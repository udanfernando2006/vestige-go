import { getApiBaseUrl } from "./settings";
import type {
    BookGroupDto,
    BookCreateDto,
    BookUpdateDto,
    BookDto,
    SeriesDto,
    SeriesCreateDto,
    SeriesUpdateDto,
    BulkSeriesAssignDto,
    StoreDto,
    StoreCreateDto,
    StoreUpdateDto,
    TrackingPairDto,
    TrackingPairCreateDto,
    TrackingPairUpdateDto,
    AvailabilityDto,
    SnapshotHistoryDto,
    HistoryQuery,
    RunSummaryDto,
    RunDetailDto,
    RunStatusDto,
    DiscoverResultDto,
    SettingsDto,
    SettingsUpdateDto,
} from "./types";

export class ApiError extends Error {
    status: number;
    body: unknown;
    constructor(message: string, status: number, body: unknown) {
        super(message);
        this.status = status;
        this.body = body;
    }
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
    const baseUrl = await getApiBaseUrl();
    const res = await fetch(`${baseUrl}${path}`, {
        ...options,
        headers: {
            "Content-Type": "application/json",
            ...(options.headers ?? {}),
        },
    });

    if (res.status === 204) return undefined as T;

    const text = await res.text();
    const data = text ? JSON.parse(text) : undefined;

    if (!res.ok) {
        // GlobalExceptionHandler returns { error: "..." } for 404/409/500;
        // RunController's local handlers return { error, output } for 422/500.
        const message =
            data && typeof data === "object" && "error" in data
                ? String((data as { error: unknown }).error)
                : `Request failed with status ${res.status}`;
        throw new ApiError(message, res.status, data);
    }

    return data as T;
}

// ---- Health ----

// Was /actuator/health (Spring Boot Actuator) — cmd/vestige registers a
// plain /health route directly inside handler.NewRouter itself (per
// main.go's own doc comment), not an Actuator-style path.
export function checkHealth(): Promise<{ status: string }> {
    return request("/health");
}

// ---- Books ----

export function getBooks(): Promise<BookGroupDto[]> {
    return request("/api/books");
}

export function createBook(dto: BookCreateDto): Promise<BookDto> {
    return request("/api/books", { method: "POST", body: JSON.stringify(dto) });
}

export function deleteBook(id: number): Promise<void> {
    return request(`/api/books/${id}`, { method: "DELETE" });
}

export function updateBook(id: number, dto: BookUpdateDto): Promise<BookDto> {
    return request(`/api/books/${id}`, {
        method: "PATCH",
        body: JSON.stringify(dto),
    });
}

export function bulkAssignSeries(dto: BulkSeriesAssignDto): Promise<BookDto[]> {
    return request("/api/books/series", {
        method: "PATCH",
        body: JSON.stringify(dto),
    });
}

// ---- Series ----

export function getSeries(): Promise<SeriesDto[]> {
    return request("/api/series");
}

export function createSeries(dto: SeriesCreateDto): Promise<SeriesDto> {
    return request("/api/series", {
        method: "POST",
        body: JSON.stringify(dto),
    });
}

export function updateSeries(
    id: number,
    dto: SeriesUpdateDto,
): Promise<SeriesDto> {
    return request(`/api/series/${id}`, {
        method: "PATCH",
        body: JSON.stringify(dto),
    });
}

export function deleteSeries(id: number): Promise<void> {
    return request(`/api/series/${id}`, { method: "DELETE" });
}

// ---- Stores ----

export function getStores(): Promise<StoreDto[]> {
    return request("/api/stores");
}

export function createStore(dto: StoreCreateDto): Promise<StoreDto> {
    return request("/api/stores", {
        method: "POST",
        body: JSON.stringify(dto),
    });
}

export function updateStore(
    id: number,
    dto: StoreUpdateDto,
): Promise<StoreDto> {
    return request(`/api/stores/${id}`, {
        method: "PATCH",
        body: JSON.stringify(dto),
    });
}

export function deleteStore(id: number): Promise<void> {
    return request(`/api/stores/${id}`, { method: "DELETE" });
}

// ---- Tracking ----

export function getTracking(): Promise<TrackingPairDto[]> {
    return request("/api/tracking");
}

export function getNeedsSetup(): Promise<TrackingPairDto[]> {
    return request("/api/tracking/needs-setup");
}

export function createTracking(
    dto: TrackingPairCreateDto,
): Promise<TrackingPairDto> {
    return request("/api/tracking", {
        method: "POST",
        body: JSON.stringify(dto),
    });
}

export function updateTracking(
    id: number,
    dto: Partial<TrackingPairUpdateDto>,
): Promise<TrackingPairDto> {
    return request(`/api/tracking/${id}`, {
        method: "PATCH",
        body: JSON.stringify(dto),
    });
}

// ---- Availability ----

export function getAvailability(): Promise<AvailabilityDto[]> {
    return request("/api/availability");
}

export function getHistory(
    query: HistoryQuery = {},
): Promise<SnapshotHistoryDto[]> {
    const params = new URLSearchParams();
    if (query.isbn) params.set("isbn", query.isbn);
    if (query.storeName) params.set("storeName", query.storeName);
    if (query.status) params.set("status", query.status);
    params.set("limit", String(query.limit ?? 100));
    return request(`/api/availability/history?${params.toString()}`);
}

export function deleteSnapshot(id: number): Promise<void> {
    return request(`/api/availability/${id}`, { method: "DELETE" });
}

export function deleteHistoryForPair(pairId: number): Promise<void> {
    return request(`/api/availability/pair/${pairId}`, { method: "DELETE" });
}

// ---- Runs ----

export function getRuns(): Promise<RunSummaryDto[]> {
    return request("/api/runs");
}

export function getRunDetail(runId: string): Promise<RunDetailDto> {
    return request(`/api/runs/${encodeURIComponent(runId)}`);
}

export function triggerRun(): Promise<RunSummaryDto> {
    return request("/api/runs/trigger", { method: "POST" });
}

export function getRunStatus(): Promise<RunStatusDto> {
    return request("/api/runs/status");
}

export function discoverSelectors(pairId: number): Promise<DiscoverResultDto> {
    return request(`/api/runs/discover/${pairId}`, { method: "POST" });
}

// ---- Settings ----
export function getSettings(): Promise<SettingsDto> {
    return request("/api/settings");
}

export function updateSettings(dto: SettingsUpdateDto): Promise<void> {
    return request("/api/settings", {
        method: "PUT",
        body: JSON.stringify(dto),
    });
}
