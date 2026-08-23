import type { DownloadResponse, TrackMetadata } from "@/types/api";
import type { DownloadJob, DownloadJobStatus } from "./download-queue-manager";
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

export interface TrackSourceEntry {
    track: TrackMetadata;
    sourceIndex: number;
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

/** Filter selected tracks without losing their positions in the source playlist. */
export function selectTracksWithSourceIndices(
    tracks: readonly TrackMetadata[],
    selectedIds: readonly string[],
): TrackSourceEntry[] {
    const selected = new Set(selectedIds);
    return tracks.flatMap((track, sourceIndex) => selected.has(track.spotify_id || "")
        ? [{ track, sourceIndex }]
        : []);
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

/** Keep the newest playable result for each original source position. */
export function orderedQueueFilePaths<TPayload>(
    jobs: readonly DownloadJob<TPayload, DownloadResponse>[],
): string[] {
    const paths = new Map<number, string>();
    for (const job of jobs) {
        const file = job.result?.file;
        if ((job.status === "completed" || job.status === "skipped") && file) {
            paths.set(job.sourceIndex, file);
        }
    }
    return [...paths.entries()]
        .sort(([left], [right]) => left - right)
        .map(([, file]) => file);
}

const terminalQueueStatuses = new Set<DownloadJobStatus>([
    "completed",
    "skipped",
    "failed",
    "suspicious",
    "cancelled",
    "unavailable",
]);

/** Keep the latest terminal attempt for each track, using array order to break timestamp ties. */
export function latestTerminalQueueJobs<TPayload, TResult>(
    jobs: readonly DownloadJob<TPayload, TResult>[],
): DownloadJob<TPayload, TResult>[] {
    const latestByTrack = new Map<string, DownloadJob<TPayload, TResult>>();
    for (const job of jobs) {
        if (!terminalQueueStatuses.has(job.status)) continue;
        const previous = latestByTrack.get(job.trackKey);
        if (!previous || job.enqueuedAt >= previous.enqueuedAt) {
            latestByTrack.set(job.trackKey, job);
        }
    }
    return [...latestByTrack.values()]
        .sort((left, right) => left.sourceIndex - right.sourceIndex || left.enqueuedAt - right.enqueuedAt);
}
