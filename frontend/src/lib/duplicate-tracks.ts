import type { TrackMetadata } from "@/types/api";

export interface DuplicateTrackEntry {
    index: number;
    track: TrackMetadata;
}

interface DuplicateTrackLogger {
    warning: (message: string) => void;
}

function normalizeDuplicateToken(value?: string): string {
    return (value || "").trim().toLowerCase().replace(/\s+/g, " ");
}

function getDurationBucket(durationMs?: number): string {
    if (!durationMs || durationMs <= 0) {
        return "";
    }

    return Math.round(durationMs / 1000).toString();
}

function buildExactDuplicateTrackKey(track: TrackMetadata): string {
    return track.spotify_id ? `spotify:${track.spotify_id}` : "";
}

function buildLikelyDuplicateTrackKey(track: TrackMetadata): string {
    return [
        "meta",
        normalizeDuplicateToken(track.artists),
        normalizeDuplicateToken(track.name),
        getDurationBucket(track.duration_ms),
    ].join("|");
}

function buildDuplicateTrackGroups(
    tracks: TrackMetadata[],
    buildKey: (track: TrackMetadata) => string,
): DuplicateTrackEntry[][] {
    const groups = new Map<string, DuplicateTrackEntry[]>();

    tracks.forEach((track, index) => {
        const key = buildKey(track);
        if (!key) {
            return;
        }

        const entries = groups.get(key) || [];
        entries.push({ index: index + 1, track });
        groups.set(key, entries);
    });

    return Array.from(groups.values()).filter((entries) => entries.length > 1);
}

function describeDuplicateTrack(entry: DuplicateTrackEntry): string {
    const track = entry.track;
    const trackId = track.spotify_id ? ` [${track.spotify_id}]` : "";
    const albumInfo = track.album_name ? ` (${track.album_name})` : "";
    return `#${entry.index}: ${track.name || "Unknown title"} - ${track.artists || "Unknown artist"}${albumInfo}${trackId}`;
}

export function logDuplicateTracks(logger: DuplicateTrackLogger, tracks: TrackMetadata[], scopeLabel: string) {
    const exactGroups = buildDuplicateTrackGroups(tracks, buildExactDuplicateTrackKey);
    const exactEntrySets = new Set(exactGroups.map((entries) => entries.map((entry) => entry.index).join(",")));

    const likelyGroups = buildDuplicateTrackGroups(tracks, buildLikelyDuplicateTrackKey)
        .filter((entries) => !exactEntrySets.has(entries.map((entry) => entry.index).join(",")));

    const allGroups = [
        ...exactGroups.map((entries) => ({ kind: "exact" as const, entries })),
        ...likelyGroups.map((entries) => ({ kind: "likely" as const, entries })),
    ];

    if (allGroups.length === 0) {
        return;
    }

    const duplicateTrackCount = allGroups.reduce((sum, group) => sum + group.entries.length, 0);
    logger.warning(`found ${allGroups.length} duplicate track group(s) in ${scopeLabel} covering ${duplicateTrackCount} entries`);

    allGroups.forEach((group, groupIndex) => {
        logger.warning(
            `${group.kind} duplicate group ${groupIndex + 1} in ${scopeLabel}: ${group.entries.map(describeDuplicateTrack).join(" | ")}`,
        );
    });
}
