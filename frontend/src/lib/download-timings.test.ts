import { describe, expect, it } from "vitest";
import {
    formatCacheOutcomes,
    formatAggregateProgress,
    formatPipelineTimings,
    phaseLabel,
} from "./download-timings";

describe("download timing formatters", () => {
    it("formats the stable non-zero pipeline timing fields in order", () => {
        expect(formatPipelineTimings({ download_ms: 1200, metadata_ms: 800, total_ms: 1700 }))
            .toBe("download 1.20s, metadata 0.80s, total 1.70s");
    });

    it("formats active and settled aggregate counts without inventing a percentage", () => {
        expect(formatAggregateProgress(3, 7, 12)).toBe("3 active · 7/12 settled");
    });

    it("normalizes backend cache keys to the explicit diagnostic categories", () => {
        expect(formatCacheOutcomes({
            "spotify:track-1": "hit",
            "isrc:track-1": "miss",
            "chosic-track:track-1": "shared",
            "artist-genre:artist-1": "hit",
        }, true)).toBe('{"spotify":"hit","isrc":"miss","chosic-track":"shared","genre":"hit"}');
    });

    it.each([
        ["queued", "Queued"],
        ["preparing", "Preparing"],
        ["downloading", "Downloading"],
        ["converting", "Converting"],
        ["finalizing", "Finalizing"],
        ["completed", "Completed"],
        ["failed", "Failed"],
        ["cancelled", "Cancelled"],
    ])("maps backend phase %s to %s", (phase, expected) => {
        expect(phaseLabel(phase)).toBe(expected);
    });
});
