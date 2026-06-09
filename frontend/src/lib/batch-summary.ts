export type BatchTrackStatus =
    | "downloaded"
    | "skipped"
    | "metadata_updated"
    | "failed"
    | "duplicate_skipped"
    | "suspicious"
    | "youtube_replaced"
    | "youtube_failed"
    | "youtube_still_suspicious";

export interface BatchTrackResult {
    id: string;
    name: string;
    artists: string;
    status: BatchTrackStatus;
    file?: string;
    error?: string;
    reason?: string;
    expectedDurationSeconds?: number;
    actualDurationSeconds?: number;
    durationDeltaSeconds?: number;
    movedOriginalPath?: string;
    replacementPath?: string;
    validationWarning?: string;
}

export interface BatchDownloadSummary {
    title: string;
    downloaded: number;
    skipped: number;
    metadataUpdated: number;
    failed: number;
    duplicateSkipped: number;
    suspicious: number;
    youtubeReplaced: number;
    youtubeFailed: number;
    youtubeStillSuspicious: number;
    results: BatchTrackResult[];
}

export function createEmptyBatchSummary(title: string): BatchDownloadSummary {
    return {
        title,
        downloaded: 0,
        skipped: 0,
        metadataUpdated: 0,
        failed: 0,
        duplicateSkipped: 0,
        suspicious: 0,
        youtubeReplaced: 0,
        youtubeFailed: 0,
        youtubeStillSuspicious: 0,
        results: [],
    };
}

export function addBatchResult(summary: BatchDownloadSummary, result: BatchTrackResult): BatchDownloadSummary {
    const next: BatchDownloadSummary = {
        ...summary,
        results: [...summary.results, result],
    };

    switch (result.status) {
        case "downloaded":
            next.downloaded++;
            break;
        case "skipped":
            next.skipped++;
            break;
        case "metadata_updated":
            next.metadataUpdated++;
            break;
        case "failed":
            next.failed++;
            break;
        case "duplicate_skipped":
            next.duplicateSkipped++;
            break;
        case "suspicious":
            next.suspicious++;
            break;
        case "youtube_replaced":
            next.youtubeReplaced++;
            break;
        case "youtube_failed":
            next.youtubeFailed++;
            break;
        case "youtube_still_suspicious":
            next.youtubeStillSuspicious++;
            break;
    }

    return next;
}

export function hasProblemDetails(summary: BatchDownloadSummary): boolean {
    return summary.failed > 0 ||
        summary.duplicateSkipped > 0 ||
        summary.suspicious > 0 ||
        summary.youtubeFailed > 0 ||
        summary.youtubeStillSuspicious > 0;
}
