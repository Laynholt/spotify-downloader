import { describe, expect, it } from "vitest";
import {
    createEmptyBatchSummary,
    initialYoutubeSelection,
    normalizeYoutubeSelection,
    selectedYoutubeRetryResultIds,
    youtubeEligibleResultIds,
    type BatchDownloadSummary,
    type BatchTrackStatus,
} from "./batch-summary";

function summaryWithResults(results: ReadonlyArray<readonly [id: string, status: BatchTrackStatus]>): BatchDownloadSummary {
    return {
        ...createEmptyBatchSummary("Test summary"),
        results: results.map(([id, status]) => ({
            id,
            name: id,
            artists: "Test artist",
            status,
        })),
    };
}

describe("YouTube retry selection", () => {
    it("selects every eligible row only when a summary opens", () => {
        const summary = summaryWithResults([
            ["failed-id", "failed"],
            ["suspicious-id", "suspicious"],
            ["downloaded-id", "downloaded"],
        ]);

        expect(youtubeEligibleResultIds(summary)).toEqual(["failed-id", "suspicious-id"]);
        expect([...initialYoutubeSelection(summary)]).toEqual(["failed-id", "suspicious-id"]);
        expect([...normalizeYoutubeSelection(summary, new Set())]).toEqual([]);
    });

    it("keeps Clear empty through normalization and rerendering", () => {
        const summary = summaryWithResults([
            ["failed-id", "failed"],
            ["suspicious-id", "suspicious"],
        ]);
        const cleared = new Set<string>();

        expect([...normalizeYoutubeSelection(summary, cleared)]).toEqual([]);
        expect([...normalizeYoutubeSelection({ ...summary }, cleared)]).toEqual([]);
    });

    it("restores every eligible row when Select all is used", () => {
        const summary = summaryWithResults([
            ["failed-id", "failed"],
            ["suspicious-id", "suspicious"],
            ["downloaded-id", "downloaded"],
        ]);

        expect([...normalizeYoutubeSelection(summary, initialYoutubeSelection(summary))]).toEqual([
            "failed-id",
            "suspicious-id",
        ]);
    });

    it("removes selected rows that are no longer eligible", () => {
        const summary = summaryWithResults([
            ["failed-id", "failed"],
            ["suspicious-id", "suspicious"],
            ["downloaded-id", "downloaded"],
        ]);

        expect([...normalizeYoutubeSelection(summary, new Set([
            "failed-id",
            "downloaded-id",
            "stale-attempt-id",
        ]))]).toEqual(["failed-id"]);
    });

    it("sends only checked stable result IDs without removing unchecked rows", () => {
        const summary = summaryWithResults([
            ["attempt-suspicious", "suspicious"],
            ["attempt-failed", "failed"],
            ["attempt-downloaded", "downloaded"],
        ]);
        const originalResults = summary.results;

        expect(selectedYoutubeRetryResultIds(summary, new Set([
            "attempt-failed",
            "attempt-downloaded",
        ]))).toEqual(["attempt-failed"]);
        expect(summary.results).toBe(originalResults);
        expect(summary.results.map((result) => result.id)).toEqual([
            "attempt-suspicious",
            "attempt-failed",
            "attempt-downloaded",
        ]);
    });
});
