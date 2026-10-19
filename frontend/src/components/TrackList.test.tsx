import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { TrackMetadata } from "@/types/api";
import type { MediaJobPayload, MediaJobResult } from "@/lib/download-jobs";
import type { DownloadQueueSnapshot } from "@/lib/download-queue-manager";
import { selectCollectionQueueSnapshot } from "@/lib/download-queue-selectors";
import { TrackList } from "./TrackList";

const track: TrackMetadata = {
    spotify_id: "track-1",
    name: "Track One",
    artists: "Artist",
    album_name: "Album",
    duration_ms: 180_000,
    images: "",
    release_date: "2026",
    track_number: 1,
    external_urls: "https://open.spotify.com/track/track-1",
};

function queue(status: "queued" | "active"): DownloadQueueSnapshot<MediaJobPayload, MediaJobResult> {
    return {
        id: "playlist:a",
        title: "Collection A",
        jobs: [{
            id: "job-1",
            queueId: "playlist:a",
            queueTitle: "Collection A",
            trackKey: "spotify:track-1",
            sourceIndex: 0,
            kind: "spotify-download",
            payload: {
                track,
                settings: {} as MediaJobPayload["settings"],
                queue: { id: "playlist:a", title: "Collection A", kind: "playlist" },
                isAlbum: false,
                position: 1,
            },
            status,
            force: false,
            enqueuedAt: 1,
        }],
        counts: { total: 1, terminal: 0, active: status === "active" ? 1 : 0, queued: status === "queued" ? 1 : 0, completed: 0, failed: 0 },
        progress: 0,
    };
}

function emptyQueue(): DownloadQueueSnapshot<MediaJobPayload, MediaJobResult> {
    return {
        id: "playlist:a",
        title: "Collection A",
        jobs: [],
        counts: { total: 0, terminal: 0, active: 0, queued: 0, completed: 0, failed: 0 },
        progress: 0,
    };
}

interface LegacyTrackState {
    downloadedTracks?: Set<string>;
    failedTracks?: Set<string>;
    skippedTracks?: Set<string>;
    suspiciousTracks?: Map<string, {
        expectedDurationSeconds?: number;
        actualDurationSeconds?: number;
        durationDeltaSeconds?: number;
    }>;
}

function renderTrackList(
    downloadQueue?: DownloadQueueSnapshot<MediaJobPayload, MediaJobResult>,
    legacy: LegacyTrackState = {},
): string {
    return renderToStaticMarkup(<TrackList
        tracks={[track]}
        searchQuery=""
        sortBy="default"
        selectedTracks={[]}
        downloadedTracks={legacy.downloadedTracks ?? new Set()}
        failedTracks={legacy.failedTracks ?? new Set()}
        skippedTracks={legacy.skippedTracks ?? new Set()}
        suspiciousTracks={legacy.suspiciousTracks}
        downloadQueue={downloadQueue}
        downloadItemsByJobId={new Map()}
        currentPage={1}
        itemsPerPage={50}
        onToggleTrack={vi.fn()}
        onToggleSelectAll={vi.fn()}
        onDownloadTrack={vi.fn()}
        onPageChange={vi.fn()}
    />);
}

describe("TrackList media status", () => {
    it.each(["suspicious", "failed", "cancelled", "unavailable"] as const)(
        "renders the complete %s diagnostic in the row without opening a tooltip",
        (status) => {
            const diagnostic = `Download diagnostic: ${"provider details and retry context; ".repeat(30)}\nFinal reason: source is unavailable.`;
            const snapshot = queue("active");
            snapshot.jobs[0].status = status;
            if (status === "suspicious") {
                snapshot.jobs[0].result = { success: true, message: "Downloaded", validation_warning: diagnostic };
            } else {
                snapshot.jobs[0].error = diagnostic;
            }

            const markup = renderTrackList(snapshot);

            expect(markup).toContain(`>${diagnostic}</span>`);
        },
    );

    it("names the disabled queued media button", () => {
        const markup = renderTrackList(queue("queued"));

        expect(markup).toContain("disabled");
        expect(markup).toContain('aria-label="Queued"');
    });

    it("retains global media status for an ad-hoc list without a collection queue", () => {
        const markup = renderTrackList(undefined, {
            downloadedTracks: new Set(["track-1"]),
        });

        expect(markup).toContain('aria-label="Downloaded"');
    });

    it("ignores global media status for a rendered collection before first enqueue", () => {
        const collectionQueue = selectCollectionQueueSnapshot({
            queues: {},
            activeCount: 0,
            peakActive: 0,
        }, {
            id: "playlist:a",
            title: "Collection A",
            kind: "playlist",
        });
        const markup = renderTrackList(collectionQueue, {
            downloadedTracks: new Set(["track-1"]),
        });

        expect(markup).toContain('aria-label="Download Track"');
    });

    it.each([
        ["downloaded", { downloadedTracks: new Set(["track-1"]) }],
        ["failed", { failedTracks: new Set(["track-1"]) }],
        ["skipped", { skippedTracks: new Set(["track-1"]) }],
        ["suspicious", { suspiciousTracks: new Map([["track-1", {}]]) }],
    ] satisfies Array<[string, LegacyTrackState]>) (
        "ignores global %s state when the current collection has no attempt",
        (_name, legacy) => {
            const markup = renderTrackList(emptyQueue(), legacy);

            expect(markup).toContain('aria-label="Download Track"');
        },
    );
});
