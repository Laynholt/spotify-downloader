import { useEffect, useState, useRef } from "react";
import { ensureYtDlpInstalledOrUpdated, fetchSpotifyMetadata, redownloadSuspiciousTracksFromYouTube, updateTrackMetadata } from "@/lib/api";
import { CheckFilesExistence, CreateM3U8File } from "../../wailsjs/go/main/App";
import { getSettingsWithDefaults, parseTemplate, type Settings, type TemplateData } from "@/lib/settings";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { joinPath, sanitizePath, getFirstArtist } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { logDuplicateTracks } from "@/lib/duplicate-tracks";
import { addBatchResult, createEmptyBatchSummary, type BatchDownloadSummary, type BatchTrackResult, type BatchTrackStatus } from "@/lib/batch-summary";
import type { DownloadResponse, SuspiciousRedownloadRequest, SuspiciousRedownloadResult, TrackMetadata, TrackMetadataUpdateRequest, TrackMetadataUpdateResult } from "@/types/api";
import { latestTerminalQueueJobs, orderedQueueFilePaths, selectTracksWithSourceIndices } from "@/lib/batch-download";
import { useDownloadQueues, type CollectionDownloadArgs } from "@/hooks/useDownloadQueues";
import type { DownloadQueueContext, MediaJobPayload, MediaJobResult } from "@/lib/download-jobs";
import type { DownloadJob } from "@/lib/download-queue-manager";
export interface BatchTrackPathInfo {
    displayArtist: string;
    displayAlbumArtist: string;
    baseOutputDir: string;
    targetOutputDir: string;
    relativePath: string;
    trackPosition: number;
    useAlbumTrackNumber: boolean;
}
export interface SuspiciousTrackInfo {
    expectedDurationSeconds?: number;
    actualDurationSeconds?: number;
    durationDeltaSeconds?: number;
}

export interface QueueOwnedBatchSummary extends BatchDownloadSummary {
    queueId: string;
}

export function withBatchSummaryQueue(summary: BatchDownloadSummary, queueId: string): QueueOwnedBatchSummary {
    return { ...summary, queueId };
}

function extensionForAudioFormat(audioFormat: string): string {
    switch (audioFormat.toLowerCase()) {
        case "flac":
            return ".flac";
        case "m4a":
            return ".m4a";
        default:
            return ".mp3";
    }
}

function splitRelativePath(relativePath: string): string[] {
    return relativePath.split(/[\\/]+/).filter((part) => part.trim().length > 0);
}

function getExistenceRootDir(pathInfo: BatchTrackPathInfo | undefined, settings: Settings, playlistName?: string, isAlbum?: boolean): string {
    if (!pathInfo) {
        return settings.downloadPath;
    }

    if (!playlistName || isAlbum) {
        return settings.downloadPath;
    }

    if (settings.createPlaylistFolder) {
        return pathInfo.baseOutputDir;
    }

    const safePlaylistName = sanitizePath(playlistName.replace(/\//g, " "), settings.operatingSystem);
    const relativeParts = splitRelativePath(pathInfo.relativePath);
    if (relativeParts.length > 0 && relativeParts[0] === safePlaylistName) {
        return joinPath(settings.operatingSystem, pathInfo.baseOutputDir, relativeParts[0]);
    }

    return pathInfo.targetOutputDir;
}

function normalizeReleaseDate(releaseDate?: string): string {
    if (!releaseDate) {
        return "";
    }
    const trimmedReleaseDate = releaseDate.trim();
    if (!trimmedReleaseDate) {
        return "";
    }
    const dateWithoutTime = trimmedReleaseDate.split("T")[0] || trimmedReleaseDate;
    return dateWithoutTime.split(" ")[0] || dateWithoutTime;
}
function getReleaseYear(releaseDate?: string): string {
    const normalizedReleaseDate = normalizeReleaseDate(releaseDate);
    return normalizedReleaseDate.length >= 4 ? normalizedReleaseDate.slice(0, 4) : "";
}
function folderTemplateNeedsReleaseDate(settings: Settings): boolean {
    const folderTemplate = settings.folderTemplate || "";
    return folderTemplate.includes("{year}") || folderTemplate.includes("{date}");
}
async function enrichTrackReleaseDate(track: TrackMetadata, settings: Settings): Promise<TrackMetadata> {
    const normalizedReleaseDate = normalizeReleaseDate(track.release_date);
    if (!folderTemplateNeedsReleaseDate(settings)) {
        return normalizedReleaseDate === (track.release_date || "")
            ? track
            : { ...track, release_date: normalizedReleaseDate };
    }
    if (normalizedReleaseDate) {
        return normalizedReleaseDate === (track.release_date || "")
            ? track
            : { ...track, release_date: normalizedReleaseDate };
    }
    if (!track.spotify_id) {
        return track;
    }
    try {
        const trackURL = `https://open.spotify.com/track/${track.spotify_id}`;
        const trackMetadata = await fetchSpotifyMetadata(trackURL, false, 0, 10);
        if ("track" in trackMetadata && trackMetadata.track?.release_date) {
            const enrichedReleaseDate = normalizeReleaseDate(trackMetadata.track.release_date);
            if (enrichedReleaseDate) {
                return {
                    ...track,
                    release_date: enrichedReleaseDate,
                };
            }
        }
    }
    catch {
        // Ignore release date enrichment failures and fall back to the existing track fields.
    }
    return track;
}
async function enrichTracksReleaseDates(tracks: TrackMetadata[], settings: Settings): Promise<TrackMetadata[]> {
    if (!folderTemplateNeedsReleaseDate(settings)) {
        return tracks;
    }
    return await Promise.all(tracks.map((track) => enrichTrackReleaseDate(track, settings)));
}
function buildBatchTrackPathInfo(track: TrackMetadata, settings: Settings, playlistName: string | undefined, isAlbum: boolean | undefined, fallbackPosition: number): BatchTrackPathInfo {
    const os = settings.operatingSystem;
    const placeholder = "__SLASH_PLACEHOLDER__";
    const folderTemplate = settings.folderTemplate || "";
    const finalTrackNumber = track.track_number || 0;
    const hasSubfolder = folderTemplate.trim() !== "";
    const trackPosition = hasSubfolder && finalTrackNumber > 0 ? finalTrackNumber : fallbackPosition;
    const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : (track.artists || "");
    const displayAlbumArtist = settings.useFirstArtistOnly && track.album_artist
        ? getFirstArtist(track.album_artist)
        : (track.album_artist || track.artists || "");
    const templateData: TemplateData = {
        artist: displayArtist.replace(/\//g, placeholder),
        album: (track.album_name || "").replace(/\//g, placeholder),
        album_artist: displayAlbumArtist.replace(/\//g, placeholder),
        title: (track.name || "").replace(/\//g, placeholder),
        track: trackPosition,
        disc: track.disc_number,
        year: getReleaseYear(track.release_date),
        date: normalizeReleaseDate(track.release_date),
        playlist: playlistName?.replace(/\//g, placeholder),
    };
    const useAlbumSubfolder = folderTemplate.includes("{album}") ||
        folderTemplate.includes("{album_artist}") ||
        folderTemplate.includes("{playlist}");
    let baseOutputDir = settings.downloadPath;
    if (settings.createPlaylistFolder &&
        playlistName &&
        (!isAlbum || !useAlbumSubfolder)) {
        baseOutputDir = joinPath(os, baseOutputDir, sanitizePath(playlistName.replace(/\//g, " "), os));
    }
    let targetOutputDir = baseOutputDir;
    let relativePath = "";
    if (folderTemplate) {
        const folderPath = parseTemplate(folderTemplate, templateData);
        if (folderPath) {
            const parts = folderPath.split("/").filter((p: string) => p.trim());
            const sanitizedParts = parts.map((part: string) => {
                const sanitizedPart = part.replace(new RegExp(placeholder, "g"), " ");
                return sanitizePath(sanitizedPart, os);
            });
            relativePath = sanitizedParts.join(os === "Windows" ? "\\" : "/");
            for (const part of sanitizedParts) {
                targetOutputDir = joinPath(os, targetOutputDir, part);
            }
        }
    }
    return {
        displayArtist,
        displayAlbumArtist,
        baseOutputDir,
        targetOutputDir,
        relativePath,
        trackPosition,
        useAlbumTrackNumber: hasSubfolder,
    };
}
function buildMetadataUpdateRequest(track: TrackMetadata, settings: Settings, pathInfo: BatchTrackPathInfo, filePath: string): TrackMetadataUpdateRequest {
    return {
        file_path: filePath,
        track_id: track.spotify_id || "",
        track_name: track.name || "",
        artist_name: track.artists || "",
        album_name: track.album_name || "",
        album_artist: track.album_artist || pathInfo.displayAlbumArtist || track.artists || "",
        release_date: normalizeReleaseDate(track.release_date),
        cover_url: track.images || "",
        track_number: track.track_number || pathInfo.trackPosition || 0,
        total_tracks: track.total_tracks || 0,
        disc_number: track.disc_number || 0,
        total_discs: track.total_discs || 0,
        copyright: track.copyright || "",
        publisher: track.publisher || "",
        genre: track.genre || "",
        duration: track.duration_ms || 0,
        embed_lyrics: settings.embedLyrics,
        embed_max_quality_cover: settings.embedMaxQualityCover,
        use_single_genre: settings.useSingleGenre,
        embed_genre: settings.embedGenre,
    };
}
function buildExistenceCheck(track: TrackMetadata, pathInfo: BatchTrackPathInfo, settings: Settings) {
    return {
        spotify_id: track.spotify_id || "",
        track_name: track.name || "",
        artist_name: pathInfo.displayArtist,
        album_name: track.album_name || "",
        album_artist: pathInfo.displayAlbumArtist,
        release_date: normalizeReleaseDate(track.release_date),
        track_number: track.track_number || 0,
        disc_number: track.disc_number || 0,
        position: pathInfo.trackPosition,
        use_album_track_number: pathInfo.useAlbumTrackNumber,
        filename_format: settings.filenameTemplate || "",
        include_track_number: settings.trackNumber || false,
        audio_format: settings.audioFormat || "mp3",
        relative_path: pathInfo.relativePath,
    };
}
function logExistencePrecheck(context: string, outputDir: string, rootDir: string, results: Array<{ exists: boolean; track_name?: string; artist_name?: string; file_path?: string }>) {
    const existing = results.filter((result) => result.exists);
    logger.info(`${context}: precheck found ${existing.length}/${results.length} existing files`);
    logger.debug(`${context}: outputDir=${outputDir || "(empty)"}, rootDir=${rootDir || "(empty)"}`);
    for (const result of existing.slice(0, 5)) {
        logger.debug(`${context}: existing ${result.track_name || "Unknown track"} - ${result.artist_name || ""} -> ${result.file_path || "(no path)"}`);
    }
    if (existing.length > 5) {
        logger.debug(`${context}: ${existing.length - 5} more existing matches omitted`);
    }
}
async function findExistingFilePaths(
    tracks: TrackMetadata[],
    settings: Settings,
    playlistName?: string,
    isAlbum?: boolean,
    context = "batch download",
    sourceIndices?: readonly number[],
): Promise<Map<number, string>> {
    const entries = tracks.map((track, arrayIndex) => {
        const sourceIndex = sourceIndices?.[arrayIndex] ?? arrayIndex;
        return {
            track,
            sourceIndex,
            pathInfo: buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, sourceIndex + 1),
        };
    });
    const candidates = entries.filter(({ track }) => Boolean(track.spotify_id));
    if (candidates.length === 0) {
        return new Map();
    }
    const outputDir = entries[0]?.pathInfo.baseOutputDir || settings.downloadPath;
    const existenceRootDir = getExistenceRootDir(entries[0]?.pathInfo, settings, playlistName, isAlbum);
    const existenceChecks = candidates.map(({ track, pathInfo }) => buildExistenceCheck(track, pathInfo, settings));
    const existenceResults = await CheckFilesExistence(outputDir, existenceRootDir, settings.audioFormat, existenceChecks);
    logExistencePrecheck(context, outputDir, existenceRootDir, existenceResults);
    const existingFilePaths = new Map<number, string>();
    for (const [resultIndex, result] of existenceResults.entries()) {
        const candidate = candidates[resultIndex];
        if (candidate && result.exists && result.file_path) {
            existingFilePaths.set(candidate.sourceIndex, result.file_path);
        }
    }
    return existingFilePaths;
}
function buildExpectedReplacementFilePath(track: TrackMetadata, settings: Settings, pathInfo: BatchTrackPathInfo): string {
    const filenameTemplate = settings.filenameTemplate || "{title} - {artist}";
    const templateData: TemplateData = {
        artist: pathInfo.displayArtist || track.artists || "",
        album: track.album_name || "",
        album_artist: pathInfo.displayAlbumArtist || track.album_artist || track.artists || "",
        title: track.name || "",
        track: pathInfo.trackPosition,
        disc: track.disc_number || 1,
        year: getReleaseYear(track.release_date),
        date: normalizeReleaseDate(track.release_date),
    };
    const filenameBase = sanitizePath(parseTemplate(filenameTemplate, templateData), settings.operatingSystem);
    return joinPath(settings.operatingSystem, pathInfo.targetOutputDir, `${filenameBase}${extensionForAudioFormat(settings.audioFormat)}`);
}
function durationInfoFromDownload(response: DownloadResponse): SuspiciousTrackInfo {
    return {
        expectedDurationSeconds: response.expected_duration_seconds,
        actualDurationSeconds: response.actual_duration_seconds,
        durationDeltaSeconds: response.duration_delta_seconds,
    };
}
function durationInfoFromMetadata(result: TrackMetadataUpdateResult): SuspiciousTrackInfo {
    return {
        expectedDurationSeconds: result.expected_duration_seconds,
        actualDurationSeconds: result.actual_duration_seconds,
        durationDeltaSeconds: result.duration_delta_seconds,
    };
}
function batchResult(track: TrackMetadata, status: BatchTrackStatus, details: Partial<BatchTrackResult> = {}): BatchTrackResult {
    return {
        id: track.spotify_id || `${track.name}-${track.artists}`,
        name: track.name || "Unknown track",
        artists: track.artists || "",
        status,
        ...details,
    };
}
function downloadResultToBatch(track: TrackMetadata, response: DownloadResponse): BatchTrackResult {
    return batchResult(track, response.metadata_updated ? "metadata_updated" : response.already_exists ? "skipped" : "downloaded", {
        file: response.file,
        reason: response.metadata_updated || !response.already_exists ? undefined : "File already exists",
        warnings: response.warnings,
        cacheOutcomes: response.cache_outcomes,
        timings: response.timings,
    });
}
function suspiciousDownloadResult(track: TrackMetadata, response: DownloadResponse): BatchTrackResult {
    return batchResult(track, "suspicious", {
        file: response.file,
        movedOriginalPath: response.moved_original_path,
        replacementPath: response.replacement_path,
        validationWarning: response.validation_warning,
        warnings: response.warnings,
        cacheOutcomes: response.cache_outcomes,
        timings: response.timings,
        ...durationInfoFromDownload(response),
    });
}
function suspiciousMetadataResult(track: TrackMetadata, result: TrackMetadataUpdateResult): BatchTrackResult {
    return batchResult(track, "suspicious", {
        file: result.file_path,
        validationWarning: result.validation_warning,
        ...durationInfoFromMetadata(result),
    });
}
function redownloadResultToBatch(request: SuspiciousRedownloadRequest, result: SuspiciousRedownloadResult): BatchTrackResult {
    const metadata = request.metadata;
    const status: BatchTrackStatus = result.success && result.status === "replaced"
        ? "youtube_replaced"
        : result.status === "still_suspicious"
            ? "youtube_still_suspicious"
            : "youtube_failed";
    return {
        id: metadata.track_id || result.track_id || metadata.file_path,
        name: metadata.track_name || "Unknown track",
        artists: metadata.artist_name || "",
        status,
        error: result.error,
        movedOriginalPath: result.moved_original_path,
        replacementPath: result.replacement_path,
        validationWarning: result.validation_warning,
        expectedDurationSeconds: result.expected_duration_seconds,
        actualDurationSeconds: result.actual_duration_seconds,
        durationDeltaSeconds: result.duration_delta_seconds,
    };
}
export function queuePeakActive(jobs: ReadonlyArray<{ startedAt?: number; finishedAt?: number }>): number {
    const events: Array<[time: number, order: number, delta: number]> = [];
    for (const job of jobs) {
        if (job.startedAt === undefined) continue;
        events.push([job.startedAt, 0, 1]);
        if (job.finishedAt !== undefined) {
            events.push([job.finishedAt, job.finishedAt === job.startedAt ? 1 : -1, -1]);
        }
    }
    events.sort(([leftTime, leftOrder], [rightTime, rightOrder]) => leftTime - rightTime || leftOrder - rightOrder);
    let active = 0;
    let peak = 0;
    for (const [, , delta] of events) {
        active += delta;
        peak = Math.max(peak, active);
    }
    return peak;
}

export function takeUnloggedActiveJobs<TJob extends { id: string; status: string }>(
    jobs: readonly TJob[],
    loggedIds: Set<string>,
): TJob[] {
    const newlyActive: TJob[] = [];
    for (const job of jobs) {
        if (job.status === "active" && !loggedIds.has(job.id)) {
            loggedIds.add(job.id);
            newlyActive.push(job);
        }
    }
    return newlyActive;
}

export interface QueueCompatibilityLog {
    level: "info" | "warning" | "error";
    message: string;
}

export function queueTerminalLog(
    job: Pick<DownloadJob<MediaJobPayload, MediaJobResult>, "status" | "payload" | "result" | "error">,
): QueueCompatibilityLog {
    const track = job.payload.track;
    const identity = `${track.name || "Unknown track"} - ${track.artists || ""}`;
    const detail = job.error || job.result?.error || "Download failed";
    switch (job.status) {
        case "completed":
            return { level: "info", message: `downloaded: ${identity}` };
        case "skipped":
            return { level: "info", message: `skipped: ${identity}` };
        case "suspicious":
            return { level: "warning", message: `suspicious: ${identity} - ${detail}` };
        case "cancelled":
            return { level: "info", message: `cancelled: ${identity} - ${job.error || "Download cancelled"}` };
        case "unavailable":
            return { level: "error", message: `unavailable: ${identity} - ${detail}` };
        default:
            return { level: "error", message: `failed: ${identity} - ${detail}` };
    }
}

export function batchCompletionLog(summary: BatchDownloadSummary): string {
    return `batch complete: ${summary.downloaded} downloaded, ${summary.skipped} skipped, ${summary.metadataUpdated} metadata updated, ${summary.failed} failed, ${summary.cancelled} cancelled; elapsed ${(summary.elapsedMs / 1000).toFixed(2)}s; peak active ${summary.peakActive}`;
}

function writeCompatibilityLog(entry: QueueCompatibilityLog): void {
    if (entry.level === "error") logger.error(entry.message);
    else if (entry.level === "warning") logger.warning(entry.message);
    else logger.info(entry.message);
}

export function useDownload() {
    const [downloadProgress, setDownloadProgress] = useState<number>(0);
    const [isDownloading, setIsDownloading] = useState(false);
    const [downloadingTrack, setDownloadingTrack] = useState<string | null>(null);
    const [bulkDownloadType, setBulkDownloadType] = useState<"all" | "selected" | null>(null);
    const [downloadedTracks, setDownloadedTracks] = useState<Set<string>>(new Set());
    const [failedTracks, setFailedTracks] = useState<Set<string>>(new Set());
    const [skippedTracks, setSkippedTracks] = useState<Set<string>>(new Set());
    const [currentDownloadInfo, setCurrentDownloadInfo] = useState<{
        name: string;
        artists: string;
    } | null>(null);
    const [suspiciousTracks, setSuspiciousTracks] = useState<Map<string, SuspiciousTrackInfo>>(new Map());
    const [batchSummary, setBatchSummary] = useState<QueueOwnedBatchSummary | null>(null);
    const [isBatchSummaryOpen, setIsBatchSummaryOpen] = useState(false);
    const [isBatchSummaryMinimized, setIsBatchSummaryMinimized] = useState(false);
    const [isBulkUpdatingMetadata, setIsBulkUpdatingMetadata] = useState(false);
    const [isRedownloadingSuspicious, setIsRedownloadingSuspicious] = useState(false);
    const shouldStopDownloadRef = useRef(false);
    const latestQueueIdRef = useRef<string | null>(null);
    const loggedActiveJobIdsRef = useRef<Set<string>>(new Set());
    const loggedTerminalJobIdsRef = useRef<Set<string>>(new Set());
    const suspiciousRedownloadRequestsRef = useRef<Map<string, Map<string, SuspiciousRedownloadRequest>>>(new Map());
    const failedRedownloadRequestsRef = useRef<Map<string, Map<string, SuspiciousRedownloadRequest>>>(new Map());
    const openBatchSummary = (summary: BatchDownloadSummary, queueId: string) => {
        setBatchSummary(withBatchSummaryQueue(summary, queueId));
        setIsBatchSummaryMinimized(false);
        setIsBatchSummaryOpen(true);
    };
    const minimizeBatchSummary = () => {
        setIsBatchSummaryOpen(false);
        setIsBatchSummaryMinimized(batchSummary !== null);
    };
    const restoreBatchSummary = () => {
        if (!batchSummary) {
            return;
        }
        setIsBatchSummaryMinimized(false);
        setIsBatchSummaryOpen(true);
    };
    const closeBatchSummary = () => {
        setIsBatchSummaryOpen(false);
        setIsBatchSummaryMinimized(false);
    };
    const recordSuspiciousTrack = (trackID: string, info: SuspiciousTrackInfo) => {
        if (!trackID) {
            return;
        }
        setSuspiciousTracks((prev) => {
            const next = new Map(prev);
            next.set(trackID, info);
            return next;
        });
    };
    const clearSuspiciousTrack = (trackID: string) => {
        if (!trackID) {
            return;
        }
        setSuspiciousTracks((prev) => {
            const next = new Map(prev);
            next.delete(trackID);
            return next;
        });
    };
    const redownloadTrackKey = (track: TrackMetadata) => track.spotify_id || `${track.name || ""}-${track.artists || ""}`;
    const queueSuspiciousRedownload = (queueId: string, track: TrackMetadata, settings: Settings, pathInfo: BatchTrackPathInfo, filePath?: string, replacementPath?: string) => {
        if (!filePath) {
            return;
        }
        const metadataFilePath = replacementPath || filePath;
        const queueRequests = suspiciousRedownloadRequestsRef.current.get(queueId) || new Map<string, SuspiciousRedownloadRequest>();
        queueRequests.set(redownloadTrackKey(track), {
            original_file_path: filePath,
            replacement_file_path: replacementPath,
            collection_dir: pathInfo.baseOutputDir,
            audio_format: settings.audioFormat,
            metadata: buildMetadataUpdateRequest(track, settings, pathInfo, metadataFilePath),
        });
        suspiciousRedownloadRequestsRef.current.set(queueId, queueRequests);
    };
    const queueFailedRedownload = (queueId: string, track: TrackMetadata, settings: Settings, pathInfo: BatchTrackPathInfo) => {
        const replacementPath = buildExpectedReplacementFilePath(track, settings, pathInfo);
        const queueRequests = failedRedownloadRequestsRef.current.get(queueId) || new Map<string, SuspiciousRedownloadRequest>();
        queueRequests.set(redownloadTrackKey(track), {
            original_file_path: "",
            replacement_file_path: replacementPath,
            collection_dir: pathInfo.baseOutputDir,
            audio_format: settings.audioFormat,
            metadata: buildMetadataUpdateRequest(track, settings, pathInfo, replacementPath),
        });
        failedRedownloadRequestsRef.current.set(queueId, queueRequests);
    };
    const isQueueTerminal = (status: string) => status === "completed" ||
        status === "skipped" ||
        status === "failed" ||
        status === "suspicious" ||
        status === "cancelled" ||
        status === "unavailable";
    const fallbackQueue = (playlistName?: string, isAlbum?: boolean): DownloadQueueContext => {
        if (!playlistName) {
            return { id: "adhoc:single", title: "Downloads", kind: "adhoc" };
        }
        const kind = isAlbum ? "album" : "playlist";
        return {
            id: kind + ":" + encodeURIComponent(playlistName),
            title: playlistName,
            kind,
        };
    };
    const collectionArgsFor = (
        settings: Settings,
        tracks: TrackMetadata[],
        playlistName?: string,
        isAlbum?: boolean,
        existingFilePaths?: ReadonlyMap<number, string>,
        sourceIndices?: readonly number[],
    ): CollectionDownloadArgs => {
        const firstTrack = tracks[0];
        const firstSourceIndex = sourceIndices?.[0] ?? 0;
        const firstPath = firstTrack
            ? buildBatchTrackPathInfo(firstTrack, settings, playlistName, isAlbum, firstSourceIndex + 1)
            : undefined;
        return {
            settings,
            playlistName,
            isAlbum,
            m3u8OutputDir: firstPath?.baseOutputDir || settings.downloadPath,
            sourceIndexFor: (_track, arrayIndex) => sourceIndices?.[arrayIndex] ?? arrayIndex,
            existingFileFor: existingFilePaths
                ? (_track, sourceIndex) => existingFilePaths.get(sourceIndex)
                : undefined,
            updateExistingFile: existingFilePaths
                ? async (track, sourceIndex, filePath) => {
                    const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, sourceIndex + 1);
                    const result = await updateTrackMetadata(buildMetadataUpdateRequest(track, settings, pathInfo, filePath));
                    return {
                        success: result.success,
                        message: result.message,
                        error: result.error,
                        file: filePath,
                        already_exists: result.success,
                        metadata_updated: result.success,
                        suspicious: result.suspicious,
                        validation_warning: result.validation_warning,
                        expected_duration_seconds: result.expected_duration_seconds,
                        actual_duration_seconds: result.actual_duration_seconds,
                        duration_delta_seconds: result.duration_delta_seconds,
                    };
                }
                : undefined,
            requestFor: (track, sourceIndex) => {
                const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, sourceIndex + 1);
                return {
                    track_id: track.spotify_id || "",
                    track_name: track.name || "",
                    artist_name: track.artists || "",
                    album_name: track.album_name || "",
                    album_artist: track.album_artist || track.artists || "",
                    release_date: normalizeReleaseDate(track.release_date),
                    cover_url: track.images || "",
                    album_track_number: track.track_number || 0,
                    disc_number: track.disc_number || 0,
                    total_tracks: track.total_tracks || 0,
                    spotify_total_discs: track.total_discs || 0,
                    copyright: track.copyright || "",
                    publisher: track.publisher || "",
                    duration: track.duration_ms || 0,
                    output_dir: pathInfo.targetOutputDir,
                    collection_dir: pathInfo.baseOutputDir,
                    audio_format: settings.audioFormat,
                    filename_format: settings.filenameTemplate,
                    track_number: settings.trackNumber,
                    position: pathInfo.trackPosition,
                    use_album_track_number: pathInfo.useAlbumTrackNumber,
                    spotify_id: track.spotify_id,
                    embed_lyrics: settings.embedLyrics,
                    embed_max_quality_cover: settings.embedMaxQualityCover,
                    use_first_artist_only: settings.useFirstArtistOnly,
                    use_single_genre: settings.useSingleGenre,
                    embed_genre: settings.embedGenre,
                };
            },
        };
    };
    const downloadQueues = useDownloadQueues({
        onQueueIdle: async (queue, snapshot, args) => {
            const allTerminalJobs = queue.jobs.filter((job) => isQueueTerminal(job.status));
            const terminalJobs = latestTerminalQueueJobs(queue.jobs);
            if (terminalJobs.length === 0) {
                return;
            }

            for (const job of allTerminalJobs) {
                if (!loggedTerminalJobIdsRef.current.has(job.id)) {
                    loggedTerminalJobIdsRef.current.add(job.id);
                    writeCompatibilityLog(queueTerminalLog(job));
                }
            }

            let summary = createEmptyBatchSummary((args?.playlistName || queue.title) + " download summary");
            for (const job of terminalJobs) {
                const track = job.payload.track;
                const response = job.result;
                if (job.status === "completed" || job.status === "skipped") {
                    summary = addBatchResult(summary, response
                        ? downloadResultToBatch(track, response)
                        : batchResult(track, job.status === "skipped" ? "skipped" : "downloaded"));
                }
                else if (job.status === "suspicious") {
                    summary = addBatchResult(summary, response
                        ? suspiciousDownloadResult(track, response)
                        : batchResult(track, "suspicious", { error: job.error }));
                }
                else if (job.status === "cancelled") {
                    summary = addBatchResult(summary, batchResult(track, "cancelled", {
                        error: job.error,
                        reason: "Download cancelled",
                    }));
                }
                else {
                    summary = addBatchResult(summary, batchResult(track, "failed", {
                        error: job.error || response?.error || (job.status === "unavailable" ? "Spotify track ID is missing" : "Download failed"),
                    }));
                }
            }
            const earliestEnqueuedAt = Math.min(...allTerminalJobs.map((job) => job.enqueuedAt));
            const latestFinishedAt = Math.max(...allTerminalJobs.map((job) => job.finishedAt || job.enqueuedAt));
            const elapsedMs = Math.max(0, latestFinishedAt - earliestEnqueuedAt);
            summary = { ...summary, elapsedMs, peakActive: queuePeakActive(queue.jobs) };
            openBatchSummary(summary, queue.id);

            const downloaded = new Set<string>();
            const skipped = new Set<string>();
            const failed = new Set<string>();
            suspiciousRedownloadRequestsRef.current.set(queue.id, new Map());
            failedRedownloadRequestsRef.current.set(queue.id, new Map());
            for (const job of terminalJobs) {
                const track = job.payload.track;
                const trackID = track.spotify_id || "";
                if (!trackID) {
                    continue;
                }
                if (job.status === "completed" || job.status === "skipped" || job.status === "suspicious") {
                    downloaded.add(trackID);
                    if (job.status === "skipped" && !job.result?.metadata_updated) {
                        skipped.add(trackID);
                    }
                    if (job.status === "suspicious" && job.result) {
                        const pathInfo = buildBatchTrackPathInfo(track, job.payload.settings, job.payload.playlistName, job.payload.isAlbum, job.payload.position);
                        recordSuspiciousTrack(trackID, durationInfoFromDownload(job.result));
                        queueSuspiciousRedownload(queue.id, track, job.payload.settings, pathInfo, job.result.file, job.result.replacement_path);
                    }
                    else {
                        clearSuspiciousTrack(trackID);
                    }
                }
                else if (job.status === "failed" || job.status === "unavailable") {
                    failed.add(trackID);
                    const pathInfo = buildBatchTrackPathInfo(track, job.payload.settings, job.payload.playlistName, job.payload.isAlbum, job.payload.position);
                    queueFailedRedownload(queue.id, track, job.payload.settings, pathInfo);
                }
            }
            logger.info(batchCompletionLog(summary));
            setDownloadedTracks((previous) => new Set([...previous, ...downloaded]));
            setSkippedTracks((previous) => {
                const next = new Set(previous);
                for (const trackID of downloaded) next.delete(trackID);
                for (const trackID of skipped) next.add(trackID);
                return next;
            });
            setFailedTracks((previous) => {
                const next = new Set(previous);
                for (const trackID of downloaded) next.delete(trackID);
                for (const trackID of failed) next.add(trackID);
                return next;
            });
            setDownloadProgress(Math.round(queue.progress * 100));
            setDownloadingTrack(null);
            setCurrentDownloadInfo(null);

            const hasBusyQueue = Object.values(snapshot.queues)
                .some((candidateQueue) => candidateQueue.counts.active > 0 || candidateQueue.counts.queued > 0);
            if (!hasBusyQueue) {
                setIsDownloading(false);
                setBulkDownloadType(null);
            }
            if (args?.settings.createM3u8File && queue.id.startsWith("playlist:") &&
                args.playlistName && args.m3u8OutputDir) {
                const paths = orderedQueueFilePaths(queue.jobs);
                if (paths.length > 0) {
                    try {
                        await CreateM3U8File(args.playlistName, args.m3u8OutputDir, paths);
                        toast.success("M3U8 playlist created");
                    }
                    catch (error) {
                        logger.error("failed to create m3u8 playlist: " + String(error));
                        toast.error("Failed to create M3U8 playlist: " + String(error));
                    }
                }
            }
        },
    });
    useEffect(() => {
        const queues = Object.values(downloadQueues.snapshot.queues);
        const newlyActiveJobs = takeUnloggedActiveJobs(
            queues.flatMap((queue) => queue.jobs),
            loggedActiveJobIdsRef.current,
        );
        for (const job of newlyActiveJobs) {
            const track = job.payload.track;
            logger.info(`starting download: ${track.name || "Unknown track"} - ${track.artists || ""}`);
        }
        const preferredQueue = latestQueueIdRef.current
            ? downloadQueues.snapshot.queues[latestQueueIdRef.current]
            : undefined;
        const targetQueue = preferredQueue && (preferredQueue.counts.active > 0 || preferredQueue.counts.queued > 0)
            ? preferredQueue
            : queues.find((candidateQueue) => candidateQueue.counts.active > 0 || candidateQueue.counts.queued > 0) || preferredQueue;
        const activeJob = targetQueue?.jobs.find((job) => job.status === "active") ||
            targetQueue?.jobs.find((job) => job.status === "queued");
        if (!targetQueue || !activeJob) {
            return;
        }
        const track = activeJob.payload.track;
        setIsDownloading(true);
        setDownloadProgress(Math.round(targetQueue.progress * 100));
        setDownloadingTrack(track.spotify_id || null);
        setCurrentDownloadInfo({ name: track.name || "Unknown track", artists: track.artists || "" });
    }, [downloadQueues.snapshot]);
    const handleDownloadTrack = async (
        track: TrackMetadata,
        playlistName?: string,
        _isArtistDiscography?: boolean,
        isAlbum?: boolean,
        position?: number,
        queueContext?: DownloadQueueContext,
    ) => {
        const settings = await getSettingsWithDefaults();
        const queue = queueContext || fallbackQueue(playlistName, isAlbum);
        const [enrichedTrack] = await enrichTracksReleaseDates([track], settings);
        const resolvedTrack = enrichedTrack || track;
        const sourceIndex = Math.max(0, (position || 1) - 1);
        const sourceIndices = [sourceIndex];
        const existingFilePaths = await findExistingFilePaths([resolvedTrack], settings, playlistName, isAlbum, "track download", sourceIndices);
        latestQueueIdRef.current = queue.id;
        setIsDownloading(true);
        setBulkDownloadType(null);
        setDownloadingTrack(resolvedTrack.spotify_id || null);
        await downloadQueues.retryTrack(queue, resolvedTrack, {
            ...collectionArgsFor(settings, [resolvedTrack], playlistName, isAlbum, existingFilePaths, sourceIndices),
            sourceIndex,
        });
    };
    const handleDownloadSelected = async (
        selectedTracks: string[],
        allTracks: TrackMetadata[],
        playlistName?: string,
        isAlbum?: boolean,
        queueContext?: DownloadQueueContext,
    ) => {
        if (selectedTracks.length === 0) {
            toast.error("No tracks selected");
            return;
        }
        const settings = await getSettingsWithDefaults();
        const queue = queueContext || fallbackQueue(playlistName, isAlbum);
        const selectedEntries = selectTracksWithSourceIndices(allTracks, selectedTracks);
        const selectedTrackObjects = selectedEntries.map((entry) => entry.track);
        const sourceIndices = selectedEntries.map((entry) => entry.sourceIndex);
        const enrichedTracks = await enrichTracksReleaseDates(selectedTrackObjects, settings);
        const existingFilePaths = await findExistingFilePaths(enrichedTracks, settings, playlistName, isAlbum, "selected download", sourceIndices);
        latestQueueIdRef.current = queue.id;
        setIsDownloading(true);
        setBulkDownloadType("selected");
        setDownloadProgress(0);
        const decisions = await downloadQueues.enqueueSelected(
            queue,
            enrichedTracks,
            selectedTracks,
            collectionArgsFor(settings, enrichedTracks, playlistName, isAlbum, existingFilePaths, sourceIndices),
        );
        if (!decisions.some((decision) => decision.added)) {
            toast.info("Selected tracks are already represented in this queue");
        }
    };
    const handleDownloadAll = async (
        tracks: TrackMetadata[],
        playlistName?: string,
        isAlbum?: boolean,
        queueContext?: DownloadQueueContext,
    ) => {
        if (tracks.length === 0) {
            toast.error("No tracks available for download");
            return;
        }
        const settings = await getSettingsWithDefaults();
        const queue = queueContext || fallbackQueue(playlistName, isAlbum);
        const enrichedTracks = await enrichTracksReleaseDates(tracks, settings);
        const existingFilePaths = await findExistingFilePaths(enrichedTracks, settings, playlistName, isAlbum, "all download");
        logDuplicateTracks(logger, enrichedTracks, playlistName || "batch download");
        latestQueueIdRef.current = queue.id;
        setIsDownloading(true);
        setBulkDownloadType("all");
        setDownloadProgress(0);
        const decisions = await downloadQueues.enqueueAll(
            queue,
            enrichedTracks,
            collectionArgsFor(settings, enrichedTracks, playlistName, isAlbum, existingFilePaths),
        );
        if (!decisions.some((decision) => decision.added)) {
            toast.info("Tracks are already represented in this queue");
        }
    };
    const handleUpdateAllMetadata = async (tracks: TrackMetadata[], playlistName?: string, isAlbum?: boolean) => {
        const tracksWithId = tracks.filter((track) => track.spotify_id);
        if (tracksWithId.length === 0) {
            toast.error("No tracks available for metadata update");
            return;
        }
        const settings = await getSettingsWithDefaults();
        latestQueueIdRef.current = "metadata";
        setIsBulkUpdatingMetadata(true);
        setDownloadProgress(0);
        suspiciousRedownloadRequestsRef.current.set("metadata", new Map());
        failedRedownloadRequestsRef.current.set("metadata", new Map());
        let summary = createEmptyBatchSummary(`${playlistName || "Tracks"} metadata update summary`);
        try {
            const enrichedTracks = await enrichTracksReleaseDates(tracksWithId, settings);
            const trackPathInfo = enrichedTracks.map((track, index) => ({
                track,
                pathInfo: buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, index + 1),
            }));
            const outputDir = trackPathInfo[0]?.pathInfo.baseOutputDir || settings.downloadPath;
            const existenceRootDir = getExistenceRootDir(trackPathInfo[0]?.pathInfo, settings, playlistName, isAlbum);
            const existenceChecks = trackPathInfo.map(({ track, pathInfo }) => buildExistenceCheck(track, pathInfo, settings));
            const existenceResults = await CheckFilesExistence(outputDir, existenceRootDir, settings.audioFormat, existenceChecks);
            const existingFilePaths = new Map<string, string>();
            for (const result of existenceResults) {
                if (result.exists) {
                    existingFilePaths.set(result.spotify_id, result.file_path || "");
                }
            }
            let completed = 0;
            for (const { track, pathInfo } of trackPathInfo) {
                const trackID = track.spotify_id || "";
                const filePath = existingFilePaths.get(trackID) || "";
                setDownloadingTrack(trackID);
                setCurrentDownloadInfo({ name: track.name, artists: pathInfo.displayArtist });
                if (!filePath) {
                    setSkippedTracks((prev) => new Set(prev).add(trackID));
                    summary = addBatchResult(summary, batchResult(track, "skipped", { reason: "File missing" }));
                }
                else {
                    try {
                        const result = await updateTrackMetadata(buildMetadataUpdateRequest(track, settings, pathInfo, filePath));
                        if (result.success) {
                            setDownloadedTracks((prev) => new Set(prev).add(trackID));
                            setFailedTracks((prev) => {
                                const newSet = new Set(prev);
                                newSet.delete(trackID);
                                return newSet;
                            });
                            summary = addBatchResult(summary, batchResult(track, "metadata_updated", { file: filePath }));
                            if (result.suspicious) {
                                recordSuspiciousTrack(trackID, durationInfoFromMetadata(result));
                                queueSuspiciousRedownload("metadata", track, settings, pathInfo, filePath);
                                summary = addBatchResult(summary, suspiciousMetadataResult(track, result));
                            }
                            else {
                                clearSuspiciousTrack(trackID);
                            }
                        }
                        else {
                            setFailedTracks((prev) => new Set(prev).add(trackID));
                            summary = addBatchResult(summary, batchResult(track, "failed", { file: filePath, error: result.error || result.message }));
                        }
                    }
                    catch (err) {
                        setFailedTracks((prev) => new Set(prev).add(trackID));
                        summary = addBatchResult(summary, batchResult(track, "failed", { file: filePath, error: err instanceof Error ? err.message : String(err) }));
                    }
                }
                completed++;
                setDownloadProgress(Math.min(100, Math.round((completed / trackPathInfo.length) * 100)));
            }
            openBatchSummary(summary, "metadata");
        }
        finally {
            setDownloadingTrack(null);
            setCurrentDownloadInfo(null);
            setIsBulkUpdatingMetadata(false);
        }
    };
    const handleRedownloadSuspiciousFromYouTube = async (includeFailedTracks: boolean = false) => {
        const queueId = batchSummary?.queueId ?? latestQueueIdRef.current;
        const suspiciousRequests = queueId ? suspiciousRedownloadRequestsRef.current.get(queueId) : undefined;
        const failedRequests = queueId ? failedRedownloadRequestsRef.current.get(queueId) : undefined;
        const queuedRequests = [
            ...(suspiciousRequests
                ? [...suspiciousRequests.entries()].map(([key, request]) => ({ request, key, source: "suspicious" as const }))
                : []),
            ...(includeFailedTracks
                ? failedRequests
                    ? [...failedRequests.entries()].map(([key, request]) => ({ request, key, source: "failed" as const }))
                    : []
                : []),
        ];
        if (queuedRequests.length === 0) {
            toast.info(includeFailedTracks ? "No tracks available for YouTube redownload" : "No suspicious tracks available for YouTube redownload");
            return;
        }
        setIsRedownloadingSuspicious(true);
        setIsDownloading(true);
        setBulkDownloadType(null);
        setDownloadProgress(0);
        setCurrentDownloadInfo({ name: "Preparing yt-dlp", artists: "" });
        shouldStopDownloadRef.current = false;
        try {
            toast.info("Preparing yt-dlp...");
            const status = await ensureYtDlpInstalledOrUpdated();
            if (status.error) {
                toast.error(status.error);
                return;
            }
            let nextSummary = batchSummary || createEmptyBatchSummary("YouTube redownload summary");
            const results: SuspiciousRedownloadResult[] = [];
            for (let i = 0; i < queuedRequests.length; i++) {
                if (shouldStopDownloadRef.current) {
                    toast.info(`YouTube redownload stopped. ${i} of ${queuedRequests.length} processed.`);
                    break;
                }
                const { request } = queuedRequests[i];
                setCurrentDownloadInfo({
                    name: request.metadata.track_name || "Unknown track",
                    artists: request.metadata.artist_name || "YouTube redownload",
                });
                setDownloadProgress(Math.round((i / queuedRequests.length) * 100));
                const [backendResult] = await redownloadSuspiciousTracksFromYouTube([request]);
                const result = backendResult || {
                    success: false,
                    status: "failed",
                    track_id: request.metadata.track_id,
                    error: "YouTube redownload returned no result",
                };
                results.push(result);
                nextSummary = addBatchResult(nextSummary, redownloadResultToBatch(request, result));
                const trackID = request.metadata.track_id || result.track_id || "";
                if (result.success && result.status === "replaced") {
                    clearSuspiciousTrack(trackID);
                    setDownloadedTracks((prev) => new Set(prev).add(trackID));
                    setFailedTracks((prev) => {
                        const newSet = new Set(prev);
                        newSet.delete(trackID);
                        return newSet;
                    });
                }
                else if (result.status === "still_suspicious") {
                    recordSuspiciousTrack(trackID, {
                        expectedDurationSeconds: result.expected_duration_seconds,
                        actualDurationSeconds: result.actual_duration_seconds,
                        durationDeltaSeconds: result.duration_delta_seconds,
                    });
                }
                else {
                    setFailedTracks((prev) => new Set(prev).add(trackID));
                }
                setDownloadProgress(Math.round(((i + 1) / queuedRequests.length) * 100));
            }
            for (let index = 0; index < queuedRequests.length; index++) {
                const queued = queuedRequests[index];
                const result = results[index];
                if (queued && result?.success && result.status === "replaced") {
                    const requests = queued.source === "suspicious" ? suspiciousRequests : failedRequests;
                    requests?.delete(queued.key);
                }
            }
            openBatchSummary(nextSummary, queueId || "redownload");
        }
        catch (err) {
            toast.error(err instanceof Error ? err.message : "YouTube redownload failed");
        }
        finally {
            setIsRedownloadingSuspicious(false);
            setIsDownloading(false);
            setBulkDownloadType(null);
            setDownloadingTrack(null);
            setCurrentDownloadInfo(null);
            shouldStopDownloadRef.current = false;
        }
    };
    const handleStopDownload = async (queueId?: string) => {
        const targetQueueId = queueId || latestQueueIdRef.current;
        if (!targetQueueId) {
            return;
        }
        const cancelled = await downloadQueues.stopQueue(targetQueueId);
        logger.info("download stop requested for queue " + targetQueueId + "; cancelling " + cancelled + " local attempt(s)");
        toast.info("Stopping download queue...");
    };
    const resetDownloadedTracks = () => {
        setDownloadedTracks(new Set());
        setFailedTracks(new Set());
        setSkippedTracks(new Set());
        setSuspiciousTracks(new Map());
        suspiciousRedownloadRequestsRef.current.clear();
        failedRedownloadRequestsRef.current.clear();
    };
    return {
        downloadProgress,
        isDownloading,
        downloadingTrack,
        bulkDownloadType,
        downloadedTracks,
        failedTracks,
        skippedTracks,
        suspiciousTracks,
        batchSummary,
        isBatchSummaryOpen,
        isBatchSummaryMinimized,
        isBulkUpdatingMetadata,
        isRedownloadingSuspicious,
        currentDownloadInfo,
        downloadQueueSnapshot: downloadQueues.snapshot,
        minimizeBatchSummary,
        restoreBatchSummary,
        closeBatchSummary,
        handleDownloadTrack,
        handleDownloadSelected,
        handleDownloadAll,
        handleUpdateAllMetadata,
        handleRedownloadSuspiciousFromYouTube,
        handleStopDownload,
        stopQueue: downloadQueues.stopQueue,
        resetDownloadedTracks,
    };
}
