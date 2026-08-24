import { describe, expect, it } from "vitest";
import type { backend } from "../../wailsjs/go/models";
import type { SpotifyMetadataResponse } from "@/types/api";
import type { MediaJobPayload, MediaJobResult } from "./download-jobs";
import type {
    DownloadJob,
    DownloadJobStatus,
    DownloadQueueSnapshot,
    DownloadSchedulerSnapshot,
} from "./download-queue-manager";
import {
    selectCollectionQueue,
    selectCollectionQueueSnapshot,
    selectLatestTrackAttempt,
    selectQueueStatus,
    type LoadedSpotifyMetadata,
} from "./download-queue-selectors";

const queueId = "playlist:p1";

function job(
    id: string,
    trackKey: string,
    status: DownloadJobStatus,
    overrides: Partial<DownloadJob<MediaJobPayload, MediaJobResult>> = {},
): DownloadJob<MediaJobPayload, MediaJobResult> {
    return {
        id,
        queueId,
        queueTitle: "Playlist One",
        trackKey,
        sourceIndex: 0,
        kind: "spotify-download",
        payload: {
            track: {
                spotify_id: trackKey.replace("spotify:", ""),
                name: trackKey,
                artists: "Artist",
                album_name: "Album",
                duration_ms: 180_000,
                images: "",
                release_date: "2026",
                track_number: 1,
                external_urls: "",
            },
            settings: {} as MediaJobPayload["settings"],
            queue: { id: queueId, title: "Playlist One", kind: "playlist" },
            isAlbum: false,
            position: 1,
        },
        status,
        force: false,
        enqueuedAt: 1,
        ...overrides,
    };
}

function queue(jobs: ReadonlyArray<DownloadJob<MediaJobPayload, MediaJobResult>>): DownloadQueueSnapshot<MediaJobPayload, MediaJobResult> {
    return {
        id: queueId,
        title: "Playlist One",
        jobs,
        // Deliberately stale: selectors must derive live values from jobs.
        counts: { total: 0, terminal: 0, active: 0, queued: 0, completed: 0, failed: 0 },
        progress: 0,
    };
}

function backendItem(values: Partial<backend.DownloadItem>): backend.DownloadItem {
    return values as backend.DownloadItem;
}

function playlistMetadata(title: string): SpotifyMetadataResponse {
    return {
        playlist_info: {
            name: "",
            tracks: { total: 0 },
            followers: { total: 0 },
            owner: { display_name: "Owner", name: title, images: "" },
        },
        track_list: [],
    };
}

describe("download queue selectors", () => {
    it("creates collection scope before its first scheduler queue exists", () => {
        const scheduler: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult> = {
            queues: {},
            activeCount: 0,
            peakActive: 0,
        };
        const collection = {
            id: "playlist:a",
            title: "Collection A",
            kind: "playlist" as const,
        };

        expect(selectCollectionQueueSnapshot(scheduler, collection)).toEqual({
            id: "playlist:a",
            title: "Collection A",
            jobs: [],
            counts: { total: 0, terminal: 0, active: 0, queued: 0, completed: 0, failed: 0 },
            progress: 0,
        });
        expect(selectCollectionQueueSnapshot(scheduler, undefined)).toBeUndefined();
    });

    it("keeps the rendered collection identity when the editable input changes to another type", () => {
        const loadedA: LoadedSpotifyMetadata = {
            data: playlistMetadata("Collection A"),
            sourceUrl: "https://open.spotify.com/playlist/a",
        };
        const draftInput = "https://open.spotify.com/album/b";

        expect(draftInput).not.toBe(loadedA.sourceUrl);
        expect(selectCollectionQueue(loadedA)).toEqual({
            id: "playlist:a",
            title: "Collection A",
            kind: "playlist",
        });
    });

    it("replaces collection identity atomically when cached history B is loaded", () => {
        const loadedA: LoadedSpotifyMetadata = {
            data: playlistMetadata("Same Name"),
            sourceUrl: "https://open.spotify.com/playlist/a",
        };
        const loadedB: LoadedSpotifyMetadata = {
            data: playlistMetadata("Same Name"),
            sourceUrl: "https://open.spotify.com/playlist/b",
        };

        expect(selectCollectionQueue(loadedA)?.id).toBe("playlist:a");
        expect(selectCollectionQueue(loadedB)?.id).toBe("playlist:b");
    });

    it("returns queued, all three active rows, and every terminal row state", () => {
        const jobs = [
            job("active-1", "spotify:active-1", "active"),
            job("active-2", "spotify:active-2", "active"),
            job("active-3", "spotify:active-3", "active"),
            job("queued", "spotify:queued", "queued"),
            job("completed", "spotify:completed", "completed"),
            job("skipped", "spotify:skipped", "skipped"),
            job("suspicious", "spotify:suspicious", "suspicious"),
            job("failed", "spotify:failed", "failed"),
            job("cancelled", "spotify:cancelled", "cancelled"),
            job("unavailable", "spotify:unavailable", "unavailable", { error: "Spotify track ID is missing" }),
        ];
        const snapshot = queue(jobs);

        expect(jobs.map(candidate => selectLatestTrackAttempt(snapshot, candidate.trackKey))).toEqual([
            { jobId: "active-1", status: "active" },
            { jobId: "active-2", status: "active" },
            { jobId: "active-3", status: "active" },
            { jobId: "queued", status: "queued" },
            { jobId: "completed", status: "completed" },
            { jobId: "skipped", status: "skipped" },
            { jobId: "suspicious", status: "suspicious" },
            { jobId: "failed", status: "failed" },
            { jobId: "cancelled", status: "cancelled" },
            { jobId: "unavailable", status: "unavailable", error: "Spotify track ID is missing" },
        ]);
    });

    it("uses the latest appended attempt for a track", () => {
        const snapshot = queue([
            job("attempt-1", "spotify:track", "completed", { enqueuedAt: 30 }),
            job("attempt-2", "spotify:track", "failed", { enqueuedAt: 10, error: "old failure" }),
            job("attempt-3", "spotify:track", "active", { enqueuedAt: 20 }),
        ]);

        expect(selectLatestTrackAttempt(snapshot, "spotify:track")).toEqual({
            jobId: "attempt-3",
            status: "active",
        });
        expect(selectLatestTrackAttempt(snapshot, "spotify:missing")).toBeUndefined();
    });

    it("joins backend phase data by job ID only when the queue also matches", () => {
        const snapshot = queue([job("attempt-2", "spotify:track", "active")]);
        const matching = new Map<string, backend.DownloadItem>([["attempt-2", backendItem({
            id: "attempt-2",
            queue_id: queueId,
            phase: "audio-download",
            detail: "segment 4",
            downloaded_bytes: 500,
            total_bytes: 1_000,
            speed: 2.5,
            warning: "slow mirror",
        })]]);

        expect(selectLatestTrackAttempt(snapshot, "spotify:track", matching)).toEqual({
            jobId: "attempt-2",
            status: "active",
            phase: "audio-download",
            detail: "segment 4",
            downloadedBytes: 500,
            totalBytes: 1_000,
            speedMbps: 2.5,
            warning: "slow mirror",
        });

        const otherQueue = new Map<string, backend.DownloadItem>([["attempt-2", backendItem({
            id: "attempt-2",
            queue_id: "playlist:p2",
            phase: "wrong-queue-phase",
            speed: 99,
            downloaded_bytes: 999,
            total_bytes: 1_000,
        })]]);
        expect(selectLatestTrackAttempt(snapshot, "spotify:track", otherQueue)).toEqual({
            jobId: "attempt-2",
            status: "active",
        });
    });

    it("omits unknown byte totals and preserves a zero per-item speed", () => {
        const snapshot = queue([job("attempt", "spotify:track", "active")]);
        const items = new Map<string, backend.DownloadItem>([["attempt", backendItem({
            id: "attempt",
            queue_id: queueId,
            downloaded_bytes: 500,
            total_bytes: 0,
            speed: 0,
        })]]);

        expect(selectLatestTrackAttempt(snapshot, "spotify:track", items)).toEqual({
            jobId: "attempt",
            status: "active",
            speedMbps: 0,
        });
    });

    it("derives queue counts and dynamic percentage from jobs without leaking another queue", () => {
        const playlistOne = queue([
            job("done-1", "spotify:done-1", "completed"),
            job("done-2", "spotify:done-2", "skipped"),
            job("active-1", "spotify:active-1", "active"),
            job("active-2", "spotify:active-2", "active"),
            job("active-3", "spotify:active-3", "active"),
        ]);
        const playlistTwo = {
            ...queue([job("other-failure", "spotify:other", "failed")]),
            id: "playlist:p2",
        };
        const snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult> = {
            queues: { [queueId]: playlistOne, "playlist:p2": playlistTwo },
            activeCount: 3,
            peakActive: 3,
        };

        expect(selectQueueStatus(snapshot, queueId)).toMatchObject({
            label: "2 / 5 · 3 active · 0 queued · 0 failed",
            progress: 40,
            terminal: 2,
            total: 5,
            active: 3,
            queued: 0,
            failed: 0,
        });
    });
});
