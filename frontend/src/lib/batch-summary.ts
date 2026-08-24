export type BatchTrackStatus =
    | "downloaded"
    | "skipped"
    | "metadata_updated"
    | "failed"
    | "duplicate_skipped"
    | "suspicious"
    | "cancelled"
    | "youtube_replaced"
    | "youtube_failed"
    | "youtube_still_suspicious";

export interface BatchTrackResult {
    id: string;
    name: string;
    artists: string;
    status: BatchTrackStatus;
    youtubeRetryable?: boolean;
    file?: string;
    error?: string;
    reason?: string;
    expectedDurationSeconds?: number;
    actualDurationSeconds?: number;
    durationDeltaSeconds?: number;
    movedOriginalPath?: string;
    replacementPath?: string;
    validationWarning?: string;
    warnings?: string[];
    cacheOutcomes?: Record<string, string>;
    timings?: import("@/types/api").PipelineTimings;
}

export interface BatchDownloadSummary {
    title: string;
    downloaded: number;
    skipped: number;
    metadataUpdated: number;
    failed: number;
    cancelled: number;
    duplicateSkipped: number;
    suspicious: number;
    youtubeReplaced: number;
    youtubeFailed: number;
    youtubeStillSuspicious: number;
    elapsedMs: number;
    peakActive: number;
    results: BatchTrackResult[];
}

export interface YoutubeSummaryState {
    summary: BatchDownloadSummary | null;
    queueId: string | null;
    selectedYoutubeResultIds: Set<string>;
    isOpen: boolean;
}

export function createEmptyBatchSummary(title: string): BatchDownloadSummary {
    return {
        title,
        downloaded: 0,
        skipped: 0,
        metadataUpdated: 0,
        failed: 0,
        cancelled: 0,
        duplicateSkipped: 0,
        suspicious: 0,
        youtubeReplaced: 0,
        youtubeFailed: 0,
        youtubeStillSuspicious: 0,
        elapsedMs: 0,
        peakActive: 0,
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
        case "cancelled":
            next.cancelled++;
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
        summary.cancelled > 0 ||
        summary.duplicateSkipped > 0 ||
        summary.suspicious > 0 ||
        summary.youtubeFailed > 0 ||
        summary.youtubeStillSuspicious > 0;
}

function changeBatchStatusCount(summary: BatchDownloadSummary, status: BatchTrackStatus, change: number): void {
    switch (status) {
        case "downloaded":
            summary.downloaded += change;
            break;
        case "skipped":
            summary.skipped += change;
            break;
        case "metadata_updated":
            summary.metadataUpdated += change;
            break;
        case "failed":
            summary.failed += change;
            break;
        case "cancelled":
            summary.cancelled += change;
            break;
        case "duplicate_skipped":
            summary.duplicateSkipped += change;
            break;
        case "suspicious":
            summary.suspicious += change;
            break;
        case "youtube_replaced":
            summary.youtubeReplaced += change;
            break;
        case "youtube_failed":
            summary.youtubeFailed += change;
            break;
        case "youtube_still_suspicious":
            summary.youtubeStillSuspicious += change;
            break;
    }
}

export function replaceBatchResult(summary: BatchDownloadSummary, resultId: string, replacement: BatchTrackResult): BatchDownloadSummary {
    const index = summary.results.findIndex((result) => result.id === resultId && result.youtubeRetryable);
    if (index < 0) {
        return summary;
    }

    const original = summary.results[index];
    const results = [...summary.results];
    results[index] = { ...replacement, id: resultId, youtubeRetryable: false };
    const next = { ...summary, results };
    changeBatchStatusCount(next, original.status, -1);
    changeBatchStatusCount(next, replacement.status, 1);
    return next;
}

export function youtubeEligibleResultIds(summary: BatchDownloadSummary): string[] {
    return summary.results
        .filter((result) => result.youtubeRetryable && (result.status === "failed" || result.status === "suspicious"))
        .map((result) => result.id);
}

export function resultIdForAttempt(attemptId: string, outcome: string): string {
    return `${attemptId}:${outcome}`;
}

export function initialYoutubeSelection(summary: BatchDownloadSummary): Set<string> {
    return new Set(youtubeEligibleResultIds(summary));
}

export function normalizeYoutubeSelection(summary: BatchDownloadSummary, selected: ReadonlySet<string>): Set<string> {
    const eligible = new Set(youtubeEligibleResultIds(summary));
    return new Set([...selected].filter((id) => eligible.has(id)));
}

export function selectedYoutubeRetryResultIds(summary: BatchDownloadSummary, selected: ReadonlySet<string>): string[] {
    const normalized = normalizeYoutubeSelection(summary, selected);
    return youtubeEligibleResultIds(summary).filter((id) => normalized.has(id));
}

export function openYoutubeSummaryState(state: YoutubeSummaryState, summary: BatchDownloadSummary, queueId: string): YoutubeSummaryState {
    const preserveSelection = state.isOpen && state.queueId === queueId;
    return {
        summary,
        queueId,
        selectedYoutubeResultIds: preserveSelection
            ? normalizeYoutubeSelection(summary, state.selectedYoutubeResultIds)
            : initialYoutubeSelection(summary),
        isOpen: true,
    };
}

export function updateYoutubeSummaryState(state: YoutubeSummaryState, summary: BatchDownloadSummary, queueId: string): YoutubeSummaryState {
    if (state.queueId !== queueId) {
        return state;
    }
    return {
        ...state,
        summary,
        selectedYoutubeResultIds: normalizeYoutubeSelection(summary, state.selectedYoutubeResultIds),
    };
}
