import type { backend } from "../../wailsjs/go/models";
import type { MediaJobPayload, MediaJobResult } from "./download-jobs";
import type {
    DownloadJob,
    DownloadJobStatus,
    DownloadQueueSnapshot,
    DownloadSchedulerSnapshot,
} from "./download-queue-manager";

export interface QueueStatusView {
    label: string;
    progress: number;
    terminal: number;
    total: number;
    active: number;
    queued: number;
    failed: number;
    busy: boolean;
}

export interface TrackAttemptView {
    jobId: string;
    status: DownloadJobStatus;
    phase?: string;
    detail?: string;
    downloadedBytes?: number;
    totalBytes?: number;
    speedMbps?: number;
    warning?: string;
    error?: string;
}

function isTerminal(status: DownloadJobStatus): boolean {
    return status === "completed" || status === "skipped" || status === "failed" ||
        status === "suspicious" || status === "cancelled" || status === "unavailable";
}

export function selectQueueStatus(
    snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>,
    queueId: string,
): QueueStatusView {
    const jobs = snapshot.queues[queueId]?.jobs ?? [];
    const total = jobs.length;
    const terminal = jobs.filter(job => isTerminal(job.status)).length;
    const active = jobs.filter(job => job.status === "active").length;
    const queued = jobs.filter(job => job.status === "queued").length;
    const failed = jobs.filter(job => job.status === "failed").length;
    return {
        label: `${terminal} / ${total} · ${active} active · ${queued} queued · ${failed} failed`,
        progress: total === 0 ? 0 : Math.round((terminal / total) * 100),
        terminal,
        total,
        active,
        queued,
        failed,
        busy: active > 0 || queued > 0,
    };
}

export function selectLatestTrackAttempt(
    queue: DownloadQueueSnapshot<MediaJobPayload, MediaJobResult>,
    trackKey: string,
    backendByJobId?: ReadonlyMap<string, backend.DownloadItem>,
): TrackAttemptView | undefined {
    let latest: DownloadJob<MediaJobPayload, MediaJobResult> | undefined;
    for (let index = queue.jobs.length - 1; index >= 0; index--) {
        const candidate = queue.jobs[index];
        if (candidate?.trackKey === trackKey) {
            latest = candidate;
            break;
        }
    }
    if (!latest) {
        return undefined;
    }

    const backendItem = backendByJobId?.get(latest.id);
    const progress = backendItem?.queue_id === queue.id ? backendItem : undefined;
    const warning = progress?.warning || latest.result?.validation_warning || latest.result?.warnings?.[0];
    const error = latest.error || latest.result?.error || progress?.error_message;
    return {
        jobId: latest.id,
        status: latest.status,
        ...(progress?.phase ? { phase: progress.phase } : {}),
        ...(progress?.detail ? { detail: progress.detail } : {}),
        ...(progress && progress.total_bytes > 0
            ? { downloadedBytes: progress.downloaded_bytes, totalBytes: progress.total_bytes }
            : {}),
        ...(progress && Number.isFinite(progress.speed) ? { speedMbps: progress.speed } : {}),
        ...(warning ? { warning } : {}),
        ...(error ? { error } : {}),
    };
}
