import { useEffect, useRef, useSyncExternalStore } from "react";
import { addDownloadJob, cancelDownloadItem, cancelDownloadQueue, downloadTrack, markDownloadItemFailed, skipDownloadItem } from "@/lib/api";
import { isUnauthorizedDownloadError, runWithUnauthorizedRetry } from "@/lib/batch-download";
import {
    buildSpotifyCandidate,
    type DownloadQueueContext,
    type MediaJobPayload,
    type MediaJobResult,
} from "@/lib/download-jobs";
import {
    createDynamicDownloadScheduler,
    type DownloadJob,
    type DownloadQueueSnapshot,
    type DownloadSchedulerSnapshot,
    type EnqueueDecision,
} from "@/lib/download-queue-manager";
import type { Settings } from "@/lib/settings";
import type { DownloadRequest, DownloadResponse, TrackMetadata } from "@/types/api";

type RequestSeed = Omit<DownloadRequest, "session_token" | "item_id" | "queue_id" | "force">;

export interface CollectionDownloadArgs {
    settings: Settings;
    playlistName?: string;
    playlistOwner?: string;
    isAlbum?: boolean;
    m3u8OutputDir?: string;
    sourceIndexFor?: (track: TrackMetadata, arrayIndex: number) => number;
    requestFor?: (track: TrackMetadata, sourceIndex: number) => RequestSeed;
    existingFileFor?: (track: TrackMetadata, sourceIndex: number) => string | undefined;
    updateExistingFile?: (track: TrackMetadata, sourceIndex: number, filePath: string) => Promise<DownloadResponse>;
}

export interface TrackDownloadArgs extends CollectionDownloadArgs {
    sourceIndex?: number;
}

export interface DownloadQueueApi {
    addDownloadJob(queueId: string, jobId: string, spotifyId: string, trackName: string, artistName: string, albumName: string): Promise<string>;
    cancelDownloadItem(itemId: string): Promise<void>;
    cancelDownloadQueue(queueId: string): Promise<number>;
    downloadTrack(request: DownloadRequest): Promise<DownloadResponse>;
    ensureValidToken(force?: boolean): Promise<string>;
    markDownloadItemFailed(itemId: string, error: string): Promise<void>;
    skipDownloadItem(itemId: string, filePath: string): Promise<void>;
}

export interface UseDownloadQueuesResult {
    snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>;
    enqueueAll(queue: DownloadQueueContext, tracks: TrackMetadata[], args: CollectionDownloadArgs): Promise<EnqueueDecision[]>;
    enqueueSelected(queue: DownloadQueueContext, tracks: TrackMetadata[], selectedIds: string[], args: CollectionDownloadArgs): Promise<EnqueueDecision[]>;
    retryTrack(queue: DownloadQueueContext, track: TrackMetadata, args: TrackDownloadArgs): Promise<EnqueueDecision>;
    stopQueue(queueId: string): Promise<number>;
}

type ExecuteSpotify = (job: DownloadJob<MediaJobPayload, MediaJobResult>, signal: AbortSignal) => Promise<MediaJobResult>;

export interface DownloadQueueHarnessOptions {
    api?: Partial<DownloadQueueApi>;
    executeSpotify?: ExecuteSpotify;
    onQueueIdle?: (
        queue: DownloadQueueSnapshot<MediaJobPayload, MediaJobResult>,
        snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>,
        args: CollectionDownloadArgs | undefined,
    ) => void | Promise<void>;
}

export interface DownloadQueueHarness {
    snapshot(): DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>;
    enqueueAll: UseDownloadQueuesResult["enqueueAll"];
    enqueueSelected: UseDownloadQueuesResult["enqueueSelected"];
    retryTrack: UseDownloadQueuesResult["retryTrack"];
    stopQueue: UseDownloadQueuesResult["stopQueue"];
    subscribe(listener: () => void): () => void;
    dispose(): Promise<void>;
}

const defaultApi: DownloadQueueApi = {
    addDownloadJob,
    cancelDownloadItem,
    cancelDownloadQueue,
    downloadTrack,
    ensureValidToken: async (force?: boolean) => {
        const { ensureValidToken } = await import("@/lib/token-manager");
        return await ensureValidToken(force);
    },
    markDownloadItemFailed,
    skipDownloadItem,
};

function defaultRequest(track: TrackMetadata, settings: Settings, sourceIndex: number): RequestSeed {
    return {
        track_id: track.spotify_id || "",
        track_name: track.name || "",
        artist_name: track.artists || "",
        album_name: track.album_name || "",
        album_artist: track.album_artist || track.artists || "",
        release_date: track.release_date || "",
        cover_url: track.images || "",
        album_track_number: track.track_number || 0,
        disc_number: track.disc_number || 0,
        total_tracks: track.total_tracks || 0,
        spotify_total_discs: track.total_discs || 0,
        copyright: track.copyright || "",
        publisher: track.publisher || "",
        duration: track.duration_ms || 0,
        output_dir: settings.downloadPath,
        collection_dir: settings.downloadPath,
        audio_format: settings.audioFormat,
        filename_format: settings.filenameTemplate,
        track_number: settings.trackNumber,
        position: sourceIndex + 1,
        use_album_track_number: false,
        spotify_id: track.spotify_id,
        embed_lyrics: settings.embedLyrics,
        embed_max_quality_cover: settings.embedMaxQualityCover,
        use_first_artist_only: settings.useFirstArtistOnly,
        use_single_genre: settings.useSingleGenre,
        embed_genre: settings.embedGenre,
    };
}

function cancelledResponse(error: unknown): DownloadResponse {
    return {
        success: false,
        message: "Download cancelled",
        error: error instanceof Error ? error.message : String(error || "Download cancelled"),
    };
}

function isCancellation(error: unknown): boolean {
    const value = error instanceof Error ? error.message : String(error || "");
    const message = value.toLowerCase();
    return message.includes("cancel") || message.includes("abort");
}

function classifySpotifyResult(response: DownloadResponse) {
    if (response.success) {
        if (response.suspicious) return "suspicious" as const;
        if (response.already_exists) return "skipped" as const;
        return "completed" as const;
    }
    return isCancellation(response.error || response.message) ? "cancelled" as const : "failed" as const;
}

export function createDownloadQueueHarness(options: DownloadQueueHarnessOptions = {}): DownloadQueueHarness {
    const api: DownloadQueueApi = { ...defaultApi, ...options.api };
    const requestByJobId = new Map<string, RequestSeed>();
    const existingPathByJobId = new Map<string, string>();
    const argsByQueueId = new Map<string, CollectionDownloadArgs>();
    const busyQueueIds = new Set<string>();
    const idleWorkByQueueId = new Map<string, Promise<void>>();
    const executeSpotify: ExecuteSpotify = async (job, signal) => {
        let backendItemId = "";
        let downloadStarted = false;
        let backendCancellation: Promise<void> | undefined;
        const cancelRegisteredItem = (): Promise<void> => {
            backendCancellation ??= (async () => {
                try {
                    await api.cancelDownloadItem(backendItemId);
                }
                catch {
                    // Backend cancellation is best effort; keep the local result cancelled.
                }
            })();
            return backendCancellation;
        };
        const settleRegisteredItem = async (settle: () => Promise<void>): Promise<void> => {
            if (signal.aborted) throw new Error("Download cancelled");
            const cancelOnAbort = () => void cancelRegisteredItem();
            signal.addEventListener("abort", cancelOnAbort, { once: true });
            try {
                await settle();
            }
            finally {
                signal.removeEventListener("abort", cancelOnAbort);
            }
            if (signal.aborted) {
                await cancelRegisteredItem();
                throw new Error("Download cancelled");
            }
        };
        try {
            if (options.executeSpotify) {
                return await options.executeSpotify(job, signal);
            }
            if (signal.aborted) {
                return cancelledResponse("Download cancelled");
            }
            const request = requestByJobId.get(job.id);
            if (!request) {
                throw new Error(`Missing request for download job ${job.id}`);
            }
            const track = job.payload.track;
            const args = argsByQueueId.get(job.queueId);
            backendItemId = await api.addDownloadJob(
                job.queueId,
                job.id,
                track.spotify_id ?? "",
                track.name || "",
                track.artists || "",
                track.album_name || "",
            );
            scheduler.setBackendItemId(job.id, backendItemId);
            if (signal.aborted) {
                throw new Error("Download cancelled");
            }
            const existingFilePath = existingPathByJobId.get(job.id);
            if (existingFilePath) {
                if (args?.settings.updateMetadataForExistingFiles && args.updateExistingFile) {
                    const metadataResult = await args.updateExistingFile(track, job.sourceIndex, existingFilePath);
                    if (signal.aborted) {
                        throw new Error("Download cancelled");
                    }
                    if (!metadataResult.success) {
                        await settleRegisteredItem(() => api.markDownloadItemFailed(
                            backendItemId,
                            metadataResult.error || metadataResult.message || "Metadata update failed",
                        ));
                        return metadataResult;
                    }
                    await settleRegisteredItem(() => api.skipDownloadItem(backendItemId, existingFilePath));
                    return {
                        ...metadataResult,
                        file: metadataResult.file || existingFilePath,
                        already_exists: true,
                        metadata_updated: true,
                    };
                }
                await settleRegisteredItem(() => api.skipDownloadItem(backendItemId, existingFilePath));
                return {
                    success: true,
                    message: "File already exists",
                    file: existingFilePath,
                    already_exists: true,
                };
            }
            const initialToken = await api.ensureValidToken(false);
            if (signal.aborted) {
                throw new Error("Download cancelled");
            }
            return await runWithUnauthorizedRetry({
                initialToken,
                execute: async (sessionToken) => {
                    if (signal.aborted) {
                        throw new Error("Download cancelled");
                    }
                    downloadStarted = true;
                    return await api.downloadTrack({
                        ...request,
                        session_token: sessionToken,
                        queue_id: job.queueId,
                        item_id: backendItemId,
                        force: job.force,
                    });
                },
                refreshToken: async () => await api.ensureValidToken(true),
                isUnauthorizedResult: (result) => !result.success && isUnauthorizedDownloadError(result.error),
                shouldStop: () => signal.aborted,
            });
        }
        catch (error) {
            const cancelled = signal.aborted || isCancellation(error);
            if (backendItemId && !downloadStarted) {
                try {
                    if (cancelled) {
                        await cancelRegisteredItem();
                    }
                    else {
                        await settleRegisteredItem(() => api.markDownloadItemFailed(
                            backendItemId,
                            error instanceof Error ? error.message : String(error),
                        ));
                    }
                }
                catch {
                    // Backend cleanup is best effort; preserve the original scheduler result.
                }
            }
            if (cancelled || signal.aborted) {
                return cancelledResponse(error);
            }
            throw error;
        }
    };

    const scheduler = createDynamicDownloadScheduler<MediaJobPayload, MediaJobResult>({
        concurrency: 3,
        execute: executeSpotify,
        classifyResult: classifySpotifyResult,
        cancelBackendQueue: api.cancelDownloadQueue,
    });
    const scheduleQueueIdle = (queue: DownloadQueueSnapshot<MediaJobPayload, MediaJobResult>, snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>): void => {
        const callback = options.onQueueIdle;
        if (!callback) {
            return;
        }
        const previous = idleWorkByQueueId.get(queue.id) ?? Promise.resolve();
        const work = previous.then(
            async () => await callback(queue, snapshot, argsByQueueId.get(queue.id)),
            async () => await callback(queue, snapshot, argsByQueueId.get(queue.id)),
        );
        idleWorkByQueueId.set(queue.id, work);
        void work.then(
            () => {
                if (idleWorkByQueueId.get(queue.id) === work) idleWorkByQueueId.delete(queue.id);
            },
            () => {
                if (idleWorkByQueueId.get(queue.id) === work) idleWorkByQueueId.delete(queue.id);
            },
        );
    };

    const unsubscribeIdleWatcher = scheduler.subscribe(() => {
        const snapshot = scheduler.getSnapshot();
        for (const queue of Object.values(snapshot.queues)) {
            const busy = queue.counts.active > 0 || queue.counts.queued > 0;
            if (busy) {
                busyQueueIds.add(queue.id);
            }
            else if (busyQueueIds.delete(queue.id)) {
                scheduleQueueIdle(queue, snapshot);
            }
        }
    });

    const enqueue = (
        queue: DownloadQueueContext,
        track: TrackMetadata,
        sourceIndex: number,
        args: CollectionDownloadArgs,
        policy: "unseen-only" | "retry-terminal",
    ): EnqueueDecision => {
        const candidate = buildSpotifyCandidate({
            queue,
            track,
            sourceIndex,
            settings: args.settings,
            playlistName: args.playlistName,
            playlistOwner: args.playlistOwner,
            isAlbum: args.isAlbum,
            position: sourceIndex + 1,
        });
        argsByQueueId.set(queue.id, args);
        requestByJobId.set(candidate.id, args.requestFor?.(track, sourceIndex) ?? defaultRequest(track, args.settings, sourceIndex));
        const existingFilePath = args.existingFileFor?.(track, sourceIndex);
        if (existingFilePath) {
            existingPathByJobId.set(candidate.id, existingFilePath);
        }
        const decision = scheduler.enqueue(candidate, policy);
        if (!decision.added) {
            requestByJobId.delete(candidate.id);
            existingPathByJobId.delete(candidate.id);
        }
        else if (decision.jobId && decision.jobId !== candidate.id) {
            const request = requestByJobId.get(candidate.id);
            requestByJobId.delete(candidate.id);
            if (request) requestByJobId.set(decision.jobId, request);
            const existingPath = existingPathByJobId.get(candidate.id);
            existingPathByJobId.delete(candidate.id);
            if (existingPath) existingPathByJobId.set(decision.jobId, existingPath);
        }
        return decision;
    };

    const notifyQueueIdleIfSettled = (queueId: string): void => {
        const snapshot = scheduler.getSnapshot();
        const queue = snapshot.queues[queueId];
        if (queue && queue.counts.active === 0 && queue.counts.queued === 0) {
            scheduleQueueIdle(queue, snapshot);
        }
    };

    return {
        snapshot: () => scheduler.getSnapshot(),
        enqueueAll: async (queue, tracks, args) => {
            const decisions = tracks.map((track, arrayIndex) =>
                enqueue(queue, track, args.sourceIndexFor?.(track, arrayIndex) ?? arrayIndex, args, "unseen-only"));
            notifyQueueIdleIfSettled(queue.id);
            return decisions;
        },
        enqueueSelected: async (queue, tracks, selectedIds, args) => {
            const selected = new Set(selectedIds);
            const decisions = tracks.flatMap((track, arrayIndex) => selected.has(track.spotify_id || "")
                ? [enqueue(queue, track, args.sourceIndexFor?.(track, arrayIndex) ?? arrayIndex, args, "unseen-only")]
                : []);
            notifyQueueIdleIfSettled(queue.id);
            return decisions;
        },
        retryTrack: async (queue, track, args) => {
            const decision = enqueue(queue, track, args.sourceIndex ?? args.sourceIndexFor?.(track, 0) ?? 0, args, "retry-terminal");
            notifyQueueIdleIfSettled(queue.id);
            return decision;
        },
        stopQueue: async (queueId) => await scheduler.cancelQueue(queueId),
        subscribe: (listener) => scheduler.subscribe(listener),
        dispose: async () => {
            unsubscribeIdleWatcher();
            requestByJobId.clear();
            existingPathByJobId.clear();
            argsByQueueId.clear();
            idleWorkByQueueId.clear();
            await scheduler.dispose();
        },
    };
}

/* eslint-disable react-hooks/refs -- this deterministic lazy initialization keeps one scheduler per hook mount. */
export function useDownloadQueues(options: DownloadQueueHarnessOptions = {}): UseDownloadQueuesResult {
    const harnessRef = useRef<DownloadQueueHarness | null>(null);
    if (harnessRef.current === null) {
        harnessRef.current = createDownloadQueueHarness(options);
    }
    const harness = harnessRef.current;
    useEffect(() => () => {
        void harness.dispose();
    }, [harness]);
    const snapshot = useSyncExternalStore(harness.subscribe, harness.snapshot, harness.snapshot);
    return {
        snapshot,
        enqueueAll: harness.enqueueAll,
        enqueueSelected: harness.enqueueSelected,
        retryTrack: harness.retryTrack,
        stopQueue: harness.stopQueue,
    };
}
/* eslint-enable react-hooks/refs */
