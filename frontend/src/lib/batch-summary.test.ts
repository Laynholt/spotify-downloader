import { describe, expect, it } from "vitest";
import {
    addBatchResult,
    createEmptyBatchSummary,
    initialYoutubeSelection,
    normalizeYoutubeSelection,
    openYoutubeSummaryState,
    replaceBatchResult,
    resultIdForAttempt,
    selectedYoutubeRetryResultIds,
    updateYoutubeSummaryState,
    youtubeEligibleResultIds,
    type BatchDownloadSummary,
    type BatchTrackResult,
    type BatchTrackStatus,
} from "./batch-summary";

function resultForTest(id: string, status: BatchTrackStatus, youtubeRetryable = status === "failed" || status === "suspicious"): BatchTrackResult {
    return {
        id,
        name: id,
        artists: "Test artist",
        status,
        youtubeRetryable,
    } as BatchTrackResult;
}

function summaryWithResults(results: ReadonlyArray<readonly [id: string, status: BatchTrackStatus]>): BatchDownloadSummary {
    return results.reduce(
        (summary, [id, status]) => addBatchResult(summary, resultForTest(id, status)),
        createEmptyBatchSummary("Test summary"),
    );
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

    it("only makes candidate-backed failed and suspicious rows eligible", () => {
        const summary = [
            resultForTest("retryable-failure", "failed", true),
            resultForTest("missing-request", "suspicious", false),
            resultForTest("finished", "downloaded", true),
        ].reduce(
            (next, result) => addBatchResult(next, result),
            createEmptyBatchSummary("Test summary"),
        );

        expect(youtubeEligibleResultIds(summary)).toEqual(["retryable-failure"]);
        expect([...initialYoutubeSelection(summary)]).toEqual(["retryable-failure"]);
    });

    it("replaces processed retry rows and updates counters without changing unchecked rows", () => {
        const unchecked = resultForTest("attempt-unchecked", "downloaded", false);
        const summary = [
            resultForTest("attempt-success", "failed", true),
            resultForTest("attempt-failure", "suspicious", true),
            unchecked,
        ].reduce(
            (next, result) => addBatchResult(next, result),
            createEmptyBatchSummary("Test summary"),
        );

        const afterSuccess = replaceBatchResult(summary, "attempt-success", resultForTest("ignored", "youtube_replaced", false));
        const afterFailure = replaceBatchResult(afterSuccess, "attempt-failure", resultForTest("ignored", "youtube_failed", false));

        expect(afterFailure.results).toHaveLength(3);
        expect(afterFailure.results.map((result) => [result.id, result.status])).toEqual([
            ["attempt-success", "youtube_replaced"],
            ["attempt-failure", "youtube_failed"],
            ["attempt-unchecked", "downloaded"],
        ]);
        expect(afterFailure.results[2]).toBe(unchecked);
        expect(afterFailure).toMatchObject({
            downloaded: 1,
            failed: 0,
            suspicious: 0,
            youtubeReplaced: 1,
            youtubeFailed: 1,
        });
    });

    it("does not append a retry ghost when there is no matching candidate row", () => {
        const summary = summaryWithResults([["attempt-failed", "failed"]]);

        expect(replaceBatchResult(summary, "missing-attempt", resultForTest("ignored", "youtube_replaced", false))).toBe(summary);
    });

    it("uses distinct stable result IDs for duplicate Spotify attempts and metadata outcomes", () => {
        const ids = [
            resultIdForAttempt("job-1", "terminal"),
            resultIdForAttempt("job-2", "terminal"),
            resultIdForAttempt("metadata-attempt", "metadata_updated"),
            resultIdForAttempt("metadata-attempt", "suspicious"),
        ];

        expect(ids).toEqual([
            "job-1:terminal",
            "job-2:terminal",
            "metadata-attempt:metadata_updated",
            "metadata-attempt:suspicious",
        ]);
        expect(new Set(ids)).toHaveLength(4);
    });

    it("preserves Clear for an in-place update", () => {
        const summaryA = summaryWithResults([["attempt-a", "failed"]]);
        const openedA = openYoutubeSummaryState({
            summary: null,
            queueId: null,
            selectedYoutubeResultIds: new Set<string>(),
            isOpen: false,
            generation: 0,
        }, summaryA, "queue-a");
        const clearedA = {
            ...openedA,
            selectedYoutubeResultIds: new Set<string>(),
        };
        const updatedA = updateYoutubeSummaryState(clearedA, { ...summaryA, title: "Updated A" }, "queue-a", openedA.generation);

        expect([...openedA.selectedYoutubeResultIds]).toEqual(["attempt-a"]);
        expect([...updatedA.selectedYoutubeResultIds]).toEqual([]);
    });

    it("ignores a gated late queue-A retry after queue B opens", async () => {
        const summaryA = summaryWithResults([["attempt-a", "failed"]]);
        const summaryB = summaryWithResults([["attempt-b", "suspicious"]]);
        const openedA = openYoutubeSummaryState({
            summary: null,
            queueId: null,
            selectedYoutubeResultIds: new Set<string>(),
            isOpen: false,
            generation: 0,
        }, summaryA, "queue-a");
        const queueAGeneration = openedA.generation;
        let currentSummary = openedA;
        let releaseQueueA!: () => void;
        const queueAGate = new Promise<void>((resolve) => {
            releaseQueueA = resolve;
        });
        const delayedQueueAUpdate = (async () => {
            await queueAGate;
            return updateYoutubeSummaryState(currentSummary, { ...summaryA, title: "Queue A retry complete" }, "queue-a", queueAGeneration);
        })();
        currentSummary = openYoutubeSummaryState(openedA, summaryB, "queue-b");

        releaseQueueA();
        const lateA = await delayedQueueAUpdate;

        expect([...currentSummary.selectedYoutubeResultIds]).toEqual(["attempt-b"]);
        expect(lateA).toBe(currentSummary);
    });

    it("opens a same-queue summary as a fresh generation after Clear", () => {
        const summaryA = summaryWithResults([["attempt-a", "failed"]]);
        const summaryB = summaryWithResults([["attempt-b", "suspicious"]]);
        const openedA = openYoutubeSummaryState({
            summary: null,
            queueId: null,
            selectedYoutubeResultIds: new Set<string>(),
            isOpen: false,
            generation: 0,
        }, summaryA, "queue-reused");
        const clearedA = { ...openedA, selectedYoutubeResultIds: new Set<string>() };
        const openedB = openYoutubeSummaryState(clearedA, summaryB, "queue-reused");

        expect(openedB.generation).toBe(2);
        expect([...openedB.selectedYoutubeResultIds]).toEqual(["attempt-b"]);
    });

    it("ignores a gated late retry when a newer generation reuses its queue ID", async () => {
        const summaryA = summaryWithResults([["attempt-a", "failed"]]);
        const summaryB = summaryWithResults([["attempt-b", "suspicious"]]);
        const openedA = openYoutubeSummaryState({
            summary: null,
            queueId: null,
            selectedYoutubeResultIds: new Set<string>(),
            isOpen: false,
            generation: 0,
        }, summaryA, "queue-reused");
        const retryGeneration = openedA.generation;
        let currentSummary = openedA;
        let releaseQueueA!: () => void;
        const queueAGate = new Promise<void>((resolve) => {
            releaseQueueA = resolve;
        });
        const delayedQueueAUpdate = (async () => {
            await queueAGate;
            return updateYoutubeSummaryState(currentSummary, { ...summaryA, title: "Queue A retry complete" }, "queue-reused", retryGeneration);
        })();
        currentSummary = openYoutubeSummaryState(openedA, summaryB, "queue-reused");

        releaseQueueA();
        const lateA = await delayedQueueAUpdate;

        expect([...currentSummary.selectedYoutubeResultIds]).toEqual(["attempt-b"]);
        expect(lateA).toBe(currentSummary);
    });
});
