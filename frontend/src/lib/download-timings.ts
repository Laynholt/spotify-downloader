import type { PipelineTimings } from "@/types/api";

type TimingValue = number | null | undefined;

const TIMING_FIELDS: Array<[keyof PipelineTimings, string]> = [
    ["preparation_ms", "preparation"],
    ["download_ms", "download"],
    ["conversion_ms", "conversion"],
    ["cover_ms", "cover"],
    ["metadata_ms", "metadata"],
    ["lyrics_ms", "lyrics"],
    ["duration_probe_ms", "duration probe"],
    ["finalization_ms", "finalization"],
    ["total_ms", "total"],
];

function isPositiveTiming(value: TimingValue): value is number {
    return typeof value === "number" && Number.isFinite(value) && value > 0;
}

export function formatPipelineTimings(timings?: Partial<PipelineTimings> | null): string {
    if (!timings) {
        return "";
    }

    return TIMING_FIELDS
        .filter(([field]) => isPositiveTiming(timings[field]))
        .map(([field, label]) => `${label} ${(timings[field] as number / 1000).toFixed(2)}s`)
        .join(", ");
}

export function formatAggregateProgress(activeCount: number, settledCount: number, totalCount: number): string {
    const active = Math.max(0, Math.trunc(Number.isFinite(activeCount) ? activeCount : 0));
    const settled = Math.max(0, Math.trunc(Number.isFinite(settledCount) ? settledCount : 0));
    const total = Math.max(0, Math.trunc(Number.isFinite(totalCount) ? totalCount : 0));
    return total > 0 ? `${active} active · ${settled}/${total} settled` : `${active} active · ${settled} settled`;
}

const PHASE_LABELS: Record<string, string> = {
    queued: "Queued",
    preparing: "Preparing",
    downloading: "Downloading",
    converting: "Converting",
    finalizing: "Finalizing",
    completed: "Completed",
    failed: "Failed",
    cancelled: "Cancelled",
};

export function phaseLabel(phase?: string | null): string {
    const normalized = (phase || "").trim().toLowerCase();
    if (PHASE_LABELS[normalized]) {
        return PHASE_LABELS[normalized];
    }
    if (!normalized) {
        return "Queued";
    }
    return normalized.replace(/[_-]+/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

export function formatCacheOutcomes(cacheOutcomes: Record<string, string> | undefined, includeGenre: boolean): string {
    const keys = ["spotify", "isrc", "chosic-track"];
    if (includeGenre) {
        keys.push("genre");
    }
    const outcomeFor = (key: string): string => {
        if (!cacheOutcomes) {
            return "not reported";
        }
        if (cacheOutcomes[key]) {
            return cacheOutcomes[key];
        }
        const prefixes = key === "genre" ? ["artist-genre:", "genre:"] : [`${key}:`];
        const matchingKey = Object.keys(cacheOutcomes).find((candidate) => prefixes.some((prefix) => candidate.startsWith(prefix)));
        return matchingKey ? cacheOutcomes[matchingKey] : "not reported";
    };
    const explicit = Object.fromEntries(keys.map((key) => [key, outcomeFor(key)]));
    return JSON.stringify(explicit);
}
