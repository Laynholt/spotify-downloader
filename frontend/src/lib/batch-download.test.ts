import { describe, expect, it } from "vitest";
import type { TrackMetadata } from "@/types/api";
import {
    buildBatchDownloadWorkItems,
    isUnauthorizedDownloadError,
    reduceBatchFilePaths,
    type BatchDownloadWorkItem,
} from "./batch-download";

function track(index: number): TrackMetadata {
    return {
        spotify_id: `track-${index}`,
        name: `Track ${index}`,
        artists: `Artist ${index}`,
        album_name: "Album",
        duration_ms: 1000,
        release_date: "2026-01-01",
        track_number: index + 1,
        images: "",
        external_urls: "",
    };
}

function pathInfo() {
    return {
        displayArtist: "Artist",
        displayAlbumArtist: "Artist",
        baseOutputDir: "downloads",
        targetOutputDir: "downloads",
        relativePath: "",
        trackPosition: 1,
        useAlbumTrackNumber: false,
    };
}

describe("batch download source ordering", () => {
    it("keeps existing source indices and reduces out-of-order completions into source order", () => {
        const sourceEntries = Array.from({ length: 5 }, (_, sourceIndex) => ({
            track: track(sourceIndex),
            pathInfo: pathInfo(),
        }));
        const existingTrackIds = new Set(["track-1", "track-4"]);
        const workItems = buildBatchDownloadWorkItems(sourceEntries, existingTrackIds);

        expect(workItems.map((item) => item.sourceIndex)).toEqual([0, 2, 3]);

        const completions: BatchDownloadWorkItem[] = [
            workItems[2],
            workItems[0],
            workItems[1],
        ];
        const finalFilePaths = reduceBatchFilePaths(
            ["file-0", "existing-1", "file-2", "file-3", "existing-4"],
            completions.map((item) => ({
                item,
                response: {
                    success: true,
                    message: "Download completed successfully",
                    file: `file-${item.sourceIndex}`,
                },
            })),
        );

        expect(finalFilePaths).toEqual(["file-0", "existing-1", "file-2", "file-3", "existing-4"]);
    });

    it.each(["401", "403", "unauthorized", "ERR_UNAUTHORIZED"])("recognizes %s as an unauthorized response", (error) => {
        expect(isUnauthorizedDownloadError(error)).toBe(true);
    });

    it("does not treat an ordinary download failure as unauthorized", () => {
        expect(isUnauthorizedDownloadError("network timeout")).toBe(false);
    });
});
