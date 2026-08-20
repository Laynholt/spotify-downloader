import type { DownloadResponse, TrackMetadata } from "@/types/api";
import type { BatchTrackPathInfo } from "@/hooks/useDownload";

export interface BatchDownloadWorkItem {
    track: TrackMetadata;
    pathInfo: BatchTrackPathInfo;
    sourceIndex: number;
}

export interface BatchDownloadSourceEntry {
    track: TrackMetadata;
    pathInfo: BatchTrackPathInfo;
}

export interface BatchDownloadWorkerResult {
    item: BatchDownloadWorkItem;
    response: DownloadResponse;
}

export interface UnauthorizedRetryOptions<T> {
    initialToken: string;
    execute: (token: string) => Promise<T>;
    refreshToken: () => Promise<string>;
    isUnauthorizedResult: (result: T) => boolean;
    shouldStop?: () => boolean;
}

function downloadErrorMessage(error: unknown): string {
    if (typeof error === "string") return error;
    if (error instanceof Error) return error.message;
    if (error && typeof error === "object") {
        const candidate = error as { message?: unknown; error?: unknown };
        if (typeof candidate.message === "string") return candidate.message;
        if (typeof candidate.error === "string") return candidate.error;
    }
    return error == null ? "" : String(error);
}

export function isUnauthorizedDownloadError(error?: unknown): boolean {
    const message = downloadErrorMessage(error).toLowerCase();
    return message.includes("unauthorized") || message.includes("403") || message.includes("401") || message.includes("err_unauthorized");
}

/** Retry one download after refreshing the token, whether Wails resolves or rejects the first unauthorized call. */
export async function runWithUnauthorizedRetry<T>(options: UnauthorizedRetryOptions<T>): Promise<T> {
    const retry = async (): Promise<T> => {
        if (options.shouldStop?.()) throw new Error("Download cancelled");
        const refreshedToken = await options.refreshToken();
        if (options.shouldStop?.()) throw new Error("Download cancelled");
        return options.execute(refreshedToken);
    };

    let firstResult: T;
    try {
        firstResult = await options.execute(options.initialToken);
    }
    catch (error) {
        if (!isUnauthorizedDownloadError(error)) throw error;
        return retry();
    }

    if (!options.isUnauthorizedResult(firstResult)) return firstResult;
    return retry();
}

/** Build only the tracks that still need a download while retaining source indices. */
export function buildBatchDownloadWorkItems(
    trackPathInfo: readonly BatchDownloadSourceEntry[],
    existingTrackIds: ReadonlySet<string>,
): BatchDownloadWorkItem[] {
    return trackPathInfo
        .map((entry, sourceIndex) => ({ ...entry, sourceIndex }))
        .filter(({ track }) => !existingTrackIds.has(track.spotify_id || ""));
}

/** Apply worker files by original source index, never by completion order. */
export function reduceBatchFilePaths(
    initialFilePaths: readonly string[],
    results: readonly BatchDownloadWorkerResult[],
): string[] {
    const filePaths = Array.from(initialFilePaths);
    for (const result of results) {
        const { response, item } = result;
        if (response.success && response.file && !response.suspicious) {
            filePaths[item.sourceIndex] = response.file;
        }
    }
    return filePaths;
}
