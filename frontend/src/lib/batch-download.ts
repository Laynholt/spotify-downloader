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

export function isUnauthorizedDownloadError(error?: string): boolean {
    const message = (error || "").toLowerCase();
    return message.includes("unauthorized") || message.includes("403") || message.includes("401") || message.includes("err_unauthorized");
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
