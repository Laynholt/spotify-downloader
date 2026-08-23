import { describe, expect, it } from "vitest";
import type { TrackMetadata } from "@/types/api";
import {
    buildBatchDownloadWorkItems,
    isUnauthorizedDownloadError,
    orderedQueueFilePaths,
    reduceBatchFilePaths,
    runWithUnauthorizedRetry,
    type BatchDownloadWorkItem,
} from "./batch-download";
import type { DownloadJob } from "./download-queue-manager";

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

    it("keeps the latest successful or skipped file for each source index in source order", () => {
        const jobs = [
            { sourceIndex: 2, status: "completed", result: { success: true, message: "done", file: "third-old" } },
            { sourceIndex: 0, status: "skipped", result: { success: true, message: "exists", file: "first" } },
            { sourceIndex: 2, status: "completed", result: { success: true, message: "done", file: "third-new" } },
            { sourceIndex: 1, status: "failed", result: { success: false, message: "failed" } },
        ] as DownloadJob<unknown, import("@/types/api").DownloadResponse>[];

        expect(orderedQueueFilePaths(jobs)).toEqual(["first", "third-new"]);
    });

    it.each(["401", "403", "unauthorized", "ERR_UNAUTHORIZED"])("recognizes %s as an unauthorized response", (error) => {
        expect(isUnauthorizedDownloadError(error)).toBe(true);
    });

    it("does not treat an ordinary download failure as unauthorized", () => {
        expect(isUnauthorizedDownloadError("network timeout")).toBe(false);
    });

    it("refreshes once when Wails rejects the first download with unauthorized", async () => {
        const tokens: string[] = [];
        let refreshes = 0;

        const result = await runWithUnauthorizedRetry({
            initialToken: "stale-token",
            execute: async (token) => {
                tokens.push(token);
                if (tokens.length === 1) {
                    throw new Error("401 unauthorized");
                }
                return { success: true, token };
            },
            refreshToken: async () => {
                refreshes++;
                return "fresh-token";
            },
            isUnauthorizedResult: () => false,
        });

        expect(result).toEqual({ success: true, token: "fresh-token" });
        expect(tokens).toEqual(["stale-token", "fresh-token"]);
        expect(refreshes).toBe(1);
    });

    it("rethrows a non-unauthorized Wails rejection without refreshing", async () => {
        let attempts = 0;
        let refreshes = 0;

        await expect(runWithUnauthorizedRetry({
            initialToken: "token",
            execute: async () => {
                attempts++;
                throw new Error("network timeout");
            },
            refreshToken: async () => {
                refreshes++;
                return "unused";
            },
            isUnauthorizedResult: () => false,
        })).rejects.toThrow("network timeout");

        expect(attempts).toBe(1);
        expect(refreshes).toBe(0);
    });

    it("does not retry a second unauthorized rejection", async () => {
        let attempts = 0;
        let refreshes = 0;

        await expect(runWithUnauthorizedRetry({
            initialToken: "stale-token",
            execute: async () => {
                attempts++;
                throw "403 unauthorized";
            },
            refreshToken: async () => {
                refreshes++;
                return "fresh-token";
            },
            isUnauthorizedResult: () => false,
        })).rejects.toBe("403 unauthorized");

        expect(attempts).toBe(2);
        expect(refreshes).toBe(1);
    });

    it("also refreshes once for a resolved unauthorized response", async () => {
        const tokens: string[] = [];
        let refreshes = 0;

        const result = await runWithUnauthorizedRetry({
            initialToken: "stale-token",
            execute: async (token) => {
                tokens.push(token);
                return tokens.length === 1
                    ? { success: false, error: "ERR_UNAUTHORIZED" }
                    : { success: true, token };
            },
            refreshToken: async () => {
                refreshes++;
                return "fresh-token";
            },
            isUnauthorizedResult: (response) => !response.success && isUnauthorizedDownloadError(response.error),
        });

        expect(result).toEqual({ success: true, token: "fresh-token" });
        expect(tokens).toEqual(["stale-token", "fresh-token"]);
        expect(refreshes).toBe(1);
    });

    it("does not start the retry when stop is requested during token refresh", async () => {
        let attempts = 0;
        let stopped = false;

        await expect(runWithUnauthorizedRetry({
            initialToken: "stale-token",
            execute: async () => {
                attempts++;
                if (attempts === 1) throw new Error("401 unauthorized");
                return { success: true };
            },
            refreshToken: async () => {
                stopped = true;
                return "fresh-token";
            },
            isUnauthorizedResult: () => false,
            shouldStop: () => stopped,
        })).rejects.toThrow("Download cancelled");

        expect(attempts).toBe(1);
    });
});
