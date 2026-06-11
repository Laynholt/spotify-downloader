import { useState, useRef } from "react";
import { downloadTrack, ensureYtDlpInstalledOrUpdated, fetchSpotifyMetadata, redownloadSuspiciousTracksFromYouTube, updateTrackMetadata } from "@/lib/api";
import { AddToDownloadQueue, CheckFilesExistence, ClearAllDownloads, CreateM3U8File, MarkDownloadItemFailed, SkipDownloadItem } from "../../wailsjs/go/main/App";
import { getSettingsWithDefaults, isLikelySessionToken, parseTemplate, type Settings, type TemplateData } from "@/lib/settings";
import { ensureValidToken } from "@/lib/token-manager";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { joinPath, sanitizePath, getFirstArtist } from "@/lib/utils";
import { logger } from "@/lib/logger";
import { findDuplicateTrackEntriesByTitle, logDuplicateTracks, type DuplicateTrackEntry } from "@/lib/duplicate-tracks";
import { addBatchResult, createEmptyBatchSummary, type BatchDownloadSummary, type BatchTrackResult, type BatchTrackStatus } from "@/lib/batch-summary";
import type { DownloadResponse, SuspiciousRedownloadRequest, SuspiciousRedownloadResult, TrackMetadata, TrackMetadataUpdateRequest, TrackMetadataUpdateResult } from "@/types/api";
interface CheckFileExistenceRequest {
    spotify_id: string;
    track_name: string;
    artist_name: string;
    album_name?: string;
    album_artist?: string;
    release_date?: string;
    track_number?: number;
    disc_number?: number;
    position?: number;
    use_album_track_number?: boolean;
    filename_format?: string;
    include_track_number?: boolean;
    audio_format?: string;
    relative_path?: string;
}
interface BatchTrackPathInfo {
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
function duplicateTrackResult(entry: DuplicateTrackEntry): BatchTrackResult {
    return batchResult(entry.track, "duplicate_skipped", {
        reason: `Duplicate title in this batch at position #${entry.index}`,
    });
}
function downloadResultToBatch(track: TrackMetadata, response: DownloadResponse): BatchTrackResult {
    return batchResult(track, response.already_exists ? "skipped" : "downloaded", {
        file: response.file,
        reason: response.already_exists ? "File already exists" : undefined,
    });
}
function suspiciousDownloadResult(track: TrackMetadata, response: DownloadResponse): BatchTrackResult {
    return batchResult(track, "suspicious", {
        file: response.file,
        movedOriginalPath: response.moved_original_path,
        replacementPath: response.replacement_path,
        validationWarning: response.validation_warning,
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
function requireSessionToken(sessionToken: string): string {
    if (!isLikelySessionToken(sessionToken)) {
        throw new Error("Session token is missing or invalid");
    }
    return sessionToken;
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
    const [batchSummary, setBatchSummary] = useState<BatchDownloadSummary | null>(null);
    const [isBatchSummaryOpen, setIsBatchSummaryOpen] = useState(false);
    const [isBatchSummaryMinimized, setIsBatchSummaryMinimized] = useState(false);
    const [isBulkUpdatingMetadata, setIsBulkUpdatingMetadata] = useState(false);
    const [isRedownloadingSuspicious, setIsRedownloadingSuspicious] = useState(false);
    const shouldStopDownloadRef = useRef(false);
    const suspiciousRedownloadRequestsRef = useRef<SuspiciousRedownloadRequest[]>([]);
    const failedRedownloadRequestsRef = useRef<SuspiciousRedownloadRequest[]>([]);
    const isUnauthorizedDownloadError = (error?: string) => {
        const msg = (error || "").toLowerCase();
        return msg.includes("unauthorized") || msg.includes("403") || msg.includes("401") || msg.includes("err_unauthorized");
    };
    const openBatchSummary = (summary: BatchDownloadSummary) => {
        setBatchSummary(summary);
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
    const queueSuspiciousRedownload = (track: TrackMetadata, settings: Settings, pathInfo: BatchTrackPathInfo, filePath?: string, replacementPath?: string) => {
        if (!filePath) {
            return;
        }
        const metadataFilePath = replacementPath || filePath;
        suspiciousRedownloadRequestsRef.current.push({
            original_file_path: filePath,
            replacement_file_path: replacementPath,
            collection_dir: pathInfo.baseOutputDir,
            audio_format: settings.audioFormat,
            metadata: buildMetadataUpdateRequest(track, settings, pathInfo, metadataFilePath),
        });
    };
    const queueFailedRedownload = (track: TrackMetadata, settings: Settings, pathInfo: BatchTrackPathInfo) => {
        const replacementPath = buildExpectedReplacementFilePath(track, settings, pathInfo);
        failedRedownloadRequestsRef.current.push({
            original_file_path: "",
            replacement_file_path: replacementPath,
            collection_dir: pathInfo.baseOutputDir,
            audio_format: settings.audioFormat,
            metadata: buildMetadataUpdateRequest(track, settings, pathInfo, replacementPath),
        });
    };
    const downloadWithSpotiDownloader = async (track: TrackMetadata, settings: Settings, playlistName?: string, position?: number, retryCount: number = 0, isAlbum?: boolean, releaseYear?: string) => {
        let finalReleaseDate = normalizeReleaseDate(track.release_date);
        let finalTrackNumber = track.track_number;
        if (track.spotify_id) {
            try {
                const trackURL = `https://open.spotify.com/track/${track.spotify_id}`;
                const trackMetadata = await fetchSpotifyMetadata(trackURL, false, 0, 10);
                if ("track" in trackMetadata && trackMetadata.track) {
                    if (trackMetadata.track.release_date) {
                        finalReleaseDate = normalizeReleaseDate(trackMetadata.track.release_date);
                    }
                    if (trackMetadata.track.track_number > 0) {
                        finalTrackNumber = trackMetadata.track.track_number;
                    }
                }
            }
            catch {
                // Ignore metadata enrichment failures and use the track fields already available.
            }
        }
        const resolvedReleaseDate = finalReleaseDate || normalizeReleaseDate(track.release_date);
        const pathInfo = buildBatchTrackPathInfo({
            ...track,
            release_date: resolvedReleaseDate,
            track_number: finalTrackNumber || 0,
        }, settings, playlistName, isAlbum, position || 0);
        const outputDir = pathInfo.targetOutputDir;
        const existenceRootDir = getExistenceRootDir(pathInfo, settings, playlistName, isAlbum);
        if (track.name && track.artists) {
            try {
                const checkRequest: CheckFileExistenceRequest = {
                    spotify_id: track.spotify_id || "",
                    track_name: track.name,
                    artist_name: pathInfo.displayArtist || "",
                    album_name: track.album_name,
                    album_artist: pathInfo.displayAlbumArtist,
                    release_date: resolvedReleaseDate || "",
                    track_number: finalTrackNumber || 0,
                    disc_number: track.disc_number || 0,
                    position: pathInfo.trackPosition,
                    use_album_track_number: pathInfo.useAlbumTrackNumber,
                    filename_format: settings.filenameTemplate || "",
                    include_track_number: settings.trackNumber || false,
                    audio_format: settings.audioFormat,
                    relative_path: pathInfo.relativePath,
                };
                const existenceResults = await CheckFilesExistence(pathInfo.baseOutputDir, existenceRootDir, settings.audioFormat, [checkRequest]);
                if (existenceResults.length > 0 && existenceResults[0].exists) {
                    return {
                        success: true,
                        message: "File already exists",
                        file: existenceResults[0].file_path || "",
                        already_exists: true,
                    };
                }
            }
            catch (err) {
                console.warn("File existence check failed:", err);
            }
        }
        const sessionToken = await ensureValidToken();
        const itemID = await AddToDownloadQueue(track.spotify_id || "", track.name || "", pathInfo.displayArtist || "", track.album_name || "");
        const response = await downloadTrack({
            track_id: track.spotify_id || "",
            session_token: sessionToken,
            track_name: track.name,
            artist_name: track.artists,
            album_name: track.album_name,
            album_artist: track.album_artist || track.artists,
            release_date: resolvedReleaseDate,
            cover_url: track.images,
            album_track_number: finalTrackNumber || track.track_number,
            disc_number: track.disc_number,
            total_tracks: track.total_tracks,
            spotify_total_discs: track.total_discs,
            copyright: track.copyright,
            publisher: track.publisher,
            duration: track.duration_ms || 0,
            output_dir: outputDir,
            collection_dir: pathInfo.baseOutputDir,
            audio_format: settings.audioFormat,
            filename_format: settings.filenameTemplate,
            use_first_artist_only: settings.useFirstArtistOnly,
            track_number: settings.trackNumber,
            position: pathInfo.trackPosition,
            use_album_track_number: pathInfo.useAlbumTrackNumber,
            spotify_id: track.spotify_id,
            embed_lyrics: settings.embedLyrics,
            embed_max_quality_cover: settings.embedMaxQualityCover,
            item_id: itemID,
            use_single_genre: settings.useSingleGenre,
            embed_genre: settings.embedGenre,
        });
        if (!response.success && retryCount < 2) {
            const errorMsg = response.error?.toLowerCase() || "";
            if (errorMsg.includes("unauthorized") ||
                errorMsg.includes("403") ||
                errorMsg.includes("err_unauthorized")) {
                await ensureValidToken(true);
                return downloadWithSpotiDownloader(track, settings, playlistName, position, retryCount + 1, isAlbum, releaseYear);
            }
        }
        if (!response.success && response.item_id) {
            await MarkDownloadItemFailed(response.item_id, response.error || "Download failed");
        }
        return response;
    };
    const handleDownloadTrack = async (track: TrackMetadata, playlistName?: string, _isArtistDiscography?: boolean, isAlbum?: boolean, position?: number) => {
        const id = track.spotify_id;
        if (!id) {
            toast.error("No ID found for this track");
            return;
        }
        const settings = await getSettingsWithDefaults();
        const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists;
        logger.info(`starting download: ${track.name} - ${displayArtist}`);
        suspiciousRedownloadRequestsRef.current = [];
        failedRedownloadRequestsRef.current = [];
        setDownloadingTrack(id);
        try {
            const response = await downloadWithSpotiDownloader(track, settings, playlistName, position, 0, isAlbum);
            if (response.success) {
                if (response.already_exists) {
                    logger.info(`skipped: ${track.name} - ${displayArtist} (already exists)`);
                    toast.info(response.message);
                    setSkippedTracks((prev) => new Set(prev).add(id));
                }
                else {
                    logger.success(`downloaded: ${track.name} - ${displayArtist}`);
                    if (response.suspicious) {
                        recordSuspiciousTrack(id, durationInfoFromDownload(response));
                        const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, position || 0);
                        queueSuspiciousRedownload(track, settings, pathInfo, response.file, response.replacement_path);
                        toast.warning(response.validation_warning || "Downloaded track is suspicious");
                        let summary = createEmptyBatchSummary(`${track.name || "Track"} download summary`);
                        summary = addBatchResult(summary, suspiciousDownloadResult(track, response));
                        suspiciousRedownloadRequestsRef.current = suspiciousRedownloadRequestsRef.current.slice(-1);
                        openBatchSummary(summary);
                    }
                    else {
                        toast.success(response.message);
                    }
                }
                setDownloadedTracks((prev: Set<string>) => new Set(prev).add(id));
                setFailedTracks((prev: Set<string>) => {
                    const newSet = new Set(prev);
                    newSet.delete(id);
                    return newSet;
                });
            }
            else {
                logger.error(`failed: ${track.name} - ${displayArtist} - ${response.error}`);
                toast.error(response.error || "Download failed");
                const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, position || 0);
                queueFailedRedownload(track, settings, pathInfo);
                setFailedTracks((prev) => new Set(prev).add(id));
            }
        }
        catch (err) {
            logger.error(`error: ${track.name} - ${err}`);
            toast.error(err instanceof Error ? err.message : "Download failed");
            const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, position || 0);
            queueFailedRedownload(track, settings, pathInfo);
            setFailedTracks((prev) => new Set(prev).add(id));
        }
        finally {
            setDownloadingTrack(null);
        }
    };
    const handleDownloadSelected = async (selectedTracks: string[], allTracks: TrackMetadata[], playlistName?: string, isAlbum?: boolean) => {
        if (selectedTracks.length === 0) {
            toast.error("No tracks selected");
            return;
        }
        logger.info(`starting batch download: ${selectedTracks.length} selected tracks`);
        const settings = await getSettingsWithDefaults();
        await ClearAllDownloads();
        setDownloadedTracks(new Set());
        setFailedTracks(new Set());
        setSkippedTracks(new Set());
        setSuspiciousTracks(new Map());
        setIsDownloading(true);
        setBulkDownloadType("selected");
        setDownloadProgress(0);
        const selectedTrackObjects = await enrichTracksReleaseDates(selectedTracks
            .map((id) => allTracks.find((t) => t.spotify_id === id))
            .filter((t): t is TrackMetadata => t !== undefined), settings);
        logDuplicateTracks(logger, selectedTrackObjects, playlistName ? `selection for ${playlistName}` : "selected tracks");
        const duplicateTrackEntries = findDuplicateTrackEntriesByTitle(selectedTrackObjects);
        const selectedTrackPathInfo = selectedTrackObjects.map((track, index) => ({
            track,
            pathInfo: buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, index + 1),
        }));
        const outputDir = selectedTrackPathInfo[0]?.pathInfo.baseOutputDir || settings.downloadPath;
        const existenceRootDir = getExistenceRootDir(selectedTrackPathInfo[0]?.pathInfo, settings, playlistName, isAlbum);
        logger.info(`checking existing files in parallel...`);
        const existenceChecks = selectedTrackPathInfo.map(({ track, pathInfo }) => {
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
                audio_format: settings.audioFormat,
                relative_path: pathInfo.relativePath,
            };
        });
        const existenceResults = await CheckFilesExistence(outputDir, existenceRootDir, settings.audioFormat, existenceChecks);
        logExistencePrecheck("selected download", outputDir, existenceRootDir, existenceResults);
        const existingSpotifyIDs = new Set<string>();
        const existingFilePathsBySpotifyID = new Map<string, string>();
        const finalFilePaths = new Map<string, string>();
        for (const result of existenceResults) {
            if (result.exists) {
                existingSpotifyIDs.add(result.spotify_id);
                existingFilePathsBySpotifyID.set(result.spotify_id, result.file_path || "");
                finalFilePaths.set(result.spotify_id, result.file_path || "");
            }
        }
        logger.info(`found ${existingSpotifyIDs.size} existing files`);
        let summary = createEmptyBatchSummary(`${playlistName || "Selected tracks"} download summary`);
        for (const entry of duplicateTrackEntries) {
            summary = addBatchResult(summary, duplicateTrackResult(entry));
        }
        suspiciousRedownloadRequestsRef.current = [];
        failedRedownloadRequestsRef.current = [];
        let successCount = 0;
        let errorCount = 0;
        let skippedCount = 0;
        let metadataUpdatedCount = 0;
        const total = selectedTracks.length;
        for (const { track, pathInfo } of selectedTrackPathInfo) {
            const trackID = track.spotify_id || "";
            if (existingSpotifyIDs.has(trackID)) {
                const itemID = await AddToDownloadQueue(track.spotify_id || "", track.name || "", pathInfo.displayArtist, track.album_name || "");
                const filePath = existingFilePathsBySpotifyID.get(trackID) || "";
                if (settings.updateMetadataForExistingFiles) {
                    try {
                        const result = await updateTrackMetadata(buildMetadataUpdateRequest(track, settings, pathInfo, filePath));
                        if (result.success) {
                            metadataUpdatedCount++;
                            summary = addBatchResult(summary, batchResult(track, "metadata_updated", { file: filePath }));
                            if (result.suspicious) {
                                recordSuspiciousTrack(trackID, durationInfoFromMetadata(result));
                                queueSuspiciousRedownload(track, settings, pathInfo, filePath);
                                summary = addBatchResult(summary, suspiciousMetadataResult(track, result));
                            }
                            else {
                                clearSuspiciousTrack(trackID);
                            }
                            setDownloadedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                            setFailedTracks((prev: Set<string>) => {
                                const newSet = new Set(prev);
                                newSet.delete(trackID);
                                return newSet;
                            });
                            setTimeout(() => SkipDownloadItem(itemID, filePath), 10);
                        }
                        else {
                            errorCount++;
                            summary = addBatchResult(summary, batchResult(track, "failed", { file: filePath, error: result.error || result.message }));
                            setFailedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                        }
                    }
                    catch (err) {
                        errorCount++;
                        summary = addBatchResult(summary, batchResult(track, "failed", { file: filePath, error: err instanceof Error ? err.message : String(err) }));
                        setFailedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                    }
                }
                else {
                    skippedCount++;
                    summary = addBatchResult(summary, batchResult(track, "skipped", { file: filePath, reason: "File already exists" }));
                    setTimeout(() => SkipDownloadItem(itemID, filePath), 10);
                    setSkippedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                    setDownloadedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                }
                const completedCount = skippedCount + successCount + errorCount + metadataUpdatedCount;
                setDownloadProgress(Math.min(100, Math.round((completedCount / total) * 100)));
            }
        }
        const tracksToDownload = selectedTrackObjects.filter((track) => {
            const trackID = track.spotify_id || "";
            return !existingSpotifyIDs.has(trackID);
        });
        let sessionToken = "";
        if (tracksToDownload.length > 0) {
            try {
                sessionToken = await ensureValidToken();
                requireSessionToken(sessionToken);
            }
            catch (err) {
                logger.error(`failed to fetch session token for batch: ${err}`);
                toast.error(err instanceof Error ? err.message : "Failed to fetch session token");
                setDownloadingTrack(null);
                setCurrentDownloadInfo(null);
                setIsDownloading(false);
                setBulkDownloadType(null);
                shouldStopDownloadRef.current = false;
                return;
            }
        }
        setDownloadProgress(Math.round(((skippedCount + errorCount + metadataUpdatedCount) / total) * 100));
        for (let i = 0; i < tracksToDownload.length; i++) {
            if (shouldStopDownloadRef.current) {
                toast.info(`Download stopped. ${successCount} tracks downloaded, ${tracksToDownload.length - i} remaining.`);
                break;
            }
            const track = tracksToDownload[i];
            const id = track.spotify_id || "";
            const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists;
            setDownloadingTrack(id);
            setCurrentDownloadInfo({ name: track.name, artists: displayArtist || "" });
            const playlistIndex = selectedTracks.indexOf(id) + 1;
            const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, playlistIndex);
            try {
                let response = await downloadTrack({
                    track_id: id,
                    session_token: sessionToken,
                    track_name: track.name || "",
                    artist_name: track.artists,
                    album_name: track.album_name,
                    album_artist: track.album_artist || track.artists,
                    release_date: normalizeReleaseDate(track.release_date),
                    cover_url: track.images,
                    album_track_number: track.track_number,
                    disc_number: track.disc_number,
                    total_tracks: track.total_tracks,
                    spotify_total_discs: track.total_discs,
                    copyright: track.copyright,
                    publisher: track.publisher,
                    duration: track.duration_ms || 0,
                    output_dir: pathInfo.targetOutputDir,
                    collection_dir: pathInfo.baseOutputDir,
                    audio_format: settings.audioFormat,
                    filename_format: settings.filenameTemplate,
                    track_number: settings.trackNumber,
                    position: pathInfo.trackPosition,
                    use_album_track_number: pathInfo.useAlbumTrackNumber,
                    embed_lyrics: settings.embedLyrics,
                    embed_max_quality_cover: settings.embedMaxQualityCover,
                    use_first_artist_only: settings.useFirstArtistOnly,
                    use_single_genre: settings.useSingleGenre,
                    embed_genre: settings.embedGenre,
                });
                if (!response.success && isUnauthorizedDownloadError(response.error)) {
                    sessionToken = await ensureValidToken(true);
                    requireSessionToken(sessionToken);
                    response = await downloadTrack({
                        track_id: id,
                        session_token: sessionToken,
                        track_name: track.name || "",
                        artist_name: track.artists,
                        album_name: track.album_name,
                        album_artist: track.album_artist || track.artists,
                        release_date: normalizeReleaseDate(track.release_date),
                        cover_url: track.images,
                        album_track_number: track.track_number,
                        disc_number: track.disc_number,
                        total_tracks: track.total_tracks,
                        spotify_total_discs: track.total_discs,
                        copyright: track.copyright,
                        publisher: track.publisher,
                        duration: track.duration_ms || 0,
                        output_dir: pathInfo.targetOutputDir,
                        collection_dir: pathInfo.baseOutputDir,
                        audio_format: settings.audioFormat,
                        filename_format: settings.filenameTemplate,
                        track_number: settings.trackNumber,
                        position: pathInfo.trackPosition,
                        use_album_track_number: pathInfo.useAlbumTrackNumber,
                        embed_lyrics: settings.embedLyrics,
                        embed_max_quality_cover: settings.embedMaxQualityCover,
                        use_first_artist_only: settings.useFirstArtistOnly,
                        use_single_genre: settings.useSingleGenre,
                        embed_genre: settings.embedGenre,
                    });
                }
                if (response.success) {
                    if (response.already_exists) {
                        skippedCount++;
                        summary = addBatchResult(summary, downloadResultToBatch(track, response));
                        logger.info(`skipped: ${track.name} - ${displayArtist} (already exists)`);
                        setSkippedTracks((prev) => new Set(prev).add(id));
                    }
                    else {
                        successCount++;
                        summary = addBatchResult(summary, downloadResultToBatch(track, response));
                        logger.success(`downloaded: ${track.name} - ${displayArtist}`);
                        if (response.suspicious) {
                            recordSuspiciousTrack(id, durationInfoFromDownload(response));
                            queueSuspiciousRedownload(track, settings, pathInfo, response.file, response.replacement_path);
                            summary = addBatchResult(summary, suspiciousDownloadResult(track, response));
                        }
                        else {
                            clearSuspiciousTrack(id);
                        }
                    }
                    if (response.file && !response.suspicious) {
                        finalFilePaths.set(id, response.file);
                        finalFilePaths.set(track.spotify_id || id, response.file);
                    }
                    setDownloadedTracks((prev) => new Set(prev).add(id));
                    setFailedTracks((prev) => {
                        const newSet = new Set(prev);
                        newSet.delete(id);
                        return newSet;
                    });
                }
                else {
                    errorCount++;
                    summary = addBatchResult(summary, batchResult(track, "failed", { error: response.error || "Download failed" }));
                    logger.error(`failed: ${track.name} - ${displayArtist}`);
                    queueFailedRedownload(track, settings, pathInfo);
                    setFailedTracks((prev) => new Set(prev).add(id));
                }
            }
            catch (err) {
                errorCount++;
                summary = addBatchResult(summary, batchResult(track, "failed", { error: err instanceof Error ? err.message : String(err) }));
                logger.error(`error: ${track.name} - ${err}`);
                queueFailedRedownload(track, settings, pathInfo);
                setFailedTracks((prev) => new Set(prev).add(id));
            }
            const completedCount = skippedCount + successCount + errorCount + metadataUpdatedCount;
            setDownloadProgress(Math.min(100, Math.round((completedCount / total) * 100)));
        }
        setDownloadingTrack(null);
        setCurrentDownloadInfo(null);
        setIsDownloading(false);
        setBulkDownloadType(null);
        shouldStopDownloadRef.current = false;
        if (settings.createM3u8File && playlistName) {
            const paths = selectedTrackObjects
                .map((t) => finalFilePaths.get(t.spotify_id || "") || "")
                .filter((p) => p !== "");
            if (paths.length > 0) {
                try {
                    logger.info(`creating m3u8 playlist: ${playlistName}`);
                    await CreateM3U8File(playlistName, outputDir, paths);
                    toast.success("M3U8 playlist created");
                }
                catch (err) {
                    logger.error(`failed to create m3u8 playlist: ${err}`);
                    toast.error(`Failed to create M3U8 playlist: ${err}`);
                }
            }
        }
        logger.info(`batch complete: ${successCount} downloaded, ${skippedCount} skipped, ${metadataUpdatedCount} metadata updated, ${errorCount} failed`);
        openBatchSummary(summary);
    };
    const handleDownloadAll = async (tracks: TrackMetadata[], playlistName?: string, isAlbum?: boolean) => {
        const tracksWithId = tracks.filter((track) => track.spotify_id);
        if (tracksWithId.length === 0) {
            toast.error("No tracks available for download");
            return;
        }
        logger.info(`starting batch download: ${tracksWithId.length} tracks`);
        const settings = await getSettingsWithDefaults();
        await ClearAllDownloads();
        setDownloadedTracks(new Set());
        setFailedTracks(new Set());
        setSkippedTracks(new Set());
        setSuspiciousTracks(new Map());
        setIsDownloading(true);
        setBulkDownloadType("all");
        setDownloadProgress(0);
        const enrichedTracksWithId = await enrichTracksReleaseDates(tracksWithId, settings);
        logDuplicateTracks(logger, enrichedTracksWithId, playlistName ? `${isAlbum ? "album" : "playlist"} ${playlistName}` : "batch download");
        const duplicateTrackEntries = findDuplicateTrackEntriesByTitle(enrichedTracksWithId);
        const trackPathInfo = enrichedTracksWithId.map((track, index) => ({
            track,
            pathInfo: buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, index + 1),
        }));
        const outputDir = trackPathInfo[0]?.pathInfo.baseOutputDir || settings.downloadPath;
        const existenceRootDir = getExistenceRootDir(trackPathInfo[0]?.pathInfo, settings, playlistName, isAlbum);
        logger.info(`checking existing files in parallel...`);
        const existenceChecks = trackPathInfo.map(({ track, pathInfo }) => {
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
        });
        const existenceResults = await CheckFilesExistence(outputDir, existenceRootDir, settings.audioFormat, existenceChecks);
        logExistencePrecheck("all download", outputDir, existenceRootDir, existenceResults);
        const finalFilePaths: string[] = new Array(enrichedTracksWithId.length).fill("");
        const existingSpotifyIDs = new Set<string>();
        const existingFilePaths = new Map<string, string>();
        for (let i = 0; i < existenceResults.length; i++) {
            const result = existenceResults[i];
            if (result.exists) {
                existingSpotifyIDs.add(result.spotify_id);
                existingFilePaths.set(result.spotify_id, result.file_path || "");
                finalFilePaths[i] = result.file_path || "";
            }
        }
        logger.info(`found ${existingSpotifyIDs.size} existing files`);
        let summary = createEmptyBatchSummary(`${playlistName || "All tracks"} download summary`);
        for (const entry of duplicateTrackEntries) {
            summary = addBatchResult(summary, duplicateTrackResult(entry));
        }
        suspiciousRedownloadRequestsRef.current = [];
        failedRedownloadRequestsRef.current = [];
        let successCount = 0;
        let errorCount = 0;
        let skippedCount = 0;
        let metadataUpdatedCount = 0;
        const total = enrichedTracksWithId.length;
        for (const { track, pathInfo } of trackPathInfo) {
            const trackID = track.spotify_id || "";
            if (existingSpotifyIDs.has(trackID)) {
                const itemID = await AddToDownloadQueue(trackID, track.name || "", pathInfo.displayArtist, track.album_name || "");
                const filePath = existingFilePaths.get(trackID) || "";
                if (settings.updateMetadataForExistingFiles) {
                    try {
                        const result = await updateTrackMetadata(buildMetadataUpdateRequest(track, settings, pathInfo, filePath));
                        if (result.success) {
                            metadataUpdatedCount++;
                            summary = addBatchResult(summary, batchResult(track, "metadata_updated", { file: filePath }));
                            if (result.suspicious) {
                                recordSuspiciousTrack(trackID, durationInfoFromMetadata(result));
                                queueSuspiciousRedownload(track, settings, pathInfo, filePath);
                                summary = addBatchResult(summary, suspiciousMetadataResult(track, result));
                            }
                            else {
                                clearSuspiciousTrack(trackID);
                            }
                            setDownloadedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                            setFailedTracks((prev: Set<string>) => {
                                const newSet = new Set(prev);
                                newSet.delete(trackID);
                                return newSet;
                            });
                            setTimeout(() => SkipDownloadItem(itemID, filePath), 10);
                        }
                        else {
                            errorCount++;
                            summary = addBatchResult(summary, batchResult(track, "failed", { file: filePath, error: result.error || result.message }));
                            setFailedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                        }
                    }
                    catch (err) {
                        errorCount++;
                        summary = addBatchResult(summary, batchResult(track, "failed", { file: filePath, error: err instanceof Error ? err.message : String(err) }));
                        setFailedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                    }
                }
                else {
                    skippedCount++;
                    summary = addBatchResult(summary, batchResult(track, "skipped", { file: filePath, reason: "File already exists" }));
                    setTimeout(() => SkipDownloadItem(itemID, filePath), 10);
                    setSkippedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                    setDownloadedTracks((prev: Set<string>) => new Set(prev).add(trackID));
                }
                const completedCount = skippedCount + successCount + errorCount + metadataUpdatedCount;
                setDownloadProgress(Math.min(100, Math.round((completedCount / total) * 100)));
            }
        }
        const tracksToDownload = enrichedTracksWithId.filter((track) => {
            const trackID = track.spotify_id || "";
            return !existingSpotifyIDs.has(trackID);
        });
        let sessionToken = "";
        if (tracksToDownload.length > 0) {
            try {
                sessionToken = await ensureValidToken();
                requireSessionToken(sessionToken);
            }
            catch (err) {
                logger.error(`failed to fetch session token for batch: ${err}`);
                toast.error(err instanceof Error ? err.message : "Failed to fetch session token");
                setDownloadingTrack(null);
                setCurrentDownloadInfo(null);
                setIsDownloading(false);
                setBulkDownloadType(null);
                shouldStopDownloadRef.current = false;
                return;
            }
        }
        setDownloadProgress(Math.round(((skippedCount + errorCount + metadataUpdatedCount) / total) * 100));
        for (let i = 0; i < tracksToDownload.length; i++) {
            if (shouldStopDownloadRef.current) {
                toast.info(`Download stopped. ${successCount} tracks downloaded, ${tracksToDownload.length - i} remaining.`);
                break;
            }
            const track = tracksToDownload[i];
            const id = track.spotify_id || "";
            const displayArtist = settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists;
            setDownloadingTrack(id);
            setCurrentDownloadInfo({ name: track.name, artists: displayArtist || "" });
            const playlistIndex = enrichedTracksWithId.findIndex((t) => t.spotify_id === id) + 1;
            const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, playlistIndex);
            try {
                let response = await downloadTrack({
                    track_id: id,
                    session_token: sessionToken,
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
                    embed_lyrics: settings.embedLyrics,
                    embed_max_quality_cover: settings.embedMaxQualityCover,
                    use_first_artist_only: settings.useFirstArtistOnly,
                    use_single_genre: settings.useSingleGenre,
                    embed_genre: settings.embedGenre,
                });
                if (!response.success && isUnauthorizedDownloadError(response.error)) {
                    sessionToken = await ensureValidToken(true);
                    requireSessionToken(sessionToken);
                    response = await downloadTrack({
                        track_id: id,
                        session_token: sessionToken,
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
                        embed_lyrics: settings.embedLyrics,
                        embed_max_quality_cover: settings.embedMaxQualityCover,
                        use_first_artist_only: settings.useFirstArtistOnly,
                        use_single_genre: settings.useSingleGenre,
                        embed_genre: settings.embedGenre,
                    });
                }
                if (response.success) {
                    if (response.already_exists) {
                        skippedCount++;
                        summary = addBatchResult(summary, downloadResultToBatch(track, response));
                        logger.info(`skipped: ${track.name} - ${displayArtist} (already exists)`);
                        setSkippedTracks((prev) => new Set(prev).add(id));
                    }
                    else {
                        successCount++;
                        summary = addBatchResult(summary, downloadResultToBatch(track, response));
                        logger.success(`downloaded: ${track.name} - ${displayArtist}`);
                        if (response.suspicious) {
                            recordSuspiciousTrack(id, durationInfoFromDownload(response));
                            queueSuspiciousRedownload(track, settings, pathInfo, response.file, response.replacement_path);
                            summary = addBatchResult(summary, suspiciousDownloadResult(track, response));
                        }
                        else {
                            clearSuspiciousTrack(id);
                        }
                    }
                    setDownloadedTracks((prev) => new Set(prev).add(id));
                    setFailedTracks((prev) => {
                        const newSet = new Set(prev);
                        newSet.delete(id);
                        return newSet;
                    });
                    if (response.file && !response.suspicious) {
                        finalFilePaths[playlistIndex - 1] = response.file;
                    }
                }
                else {
                    errorCount++;
                    summary = addBatchResult(summary, batchResult(track, "failed", { error: response.error || "Download failed" }));
                    logger.error(`failed: ${track.name} - ${displayArtist}`);
                    queueFailedRedownload(track, settings, pathInfo);
                    setFailedTracks((prev) => new Set(prev).add(id));
                }
            }
            catch (err) {
                errorCount++;
                summary = addBatchResult(summary, batchResult(track, "failed", { error: err instanceof Error ? err.message : String(err) }));
                logger.error(`error: ${track.name} - ${err}`);
                queueFailedRedownload(track, settings, pathInfo);
                setFailedTracks((prev) => new Set(prev).add(id));
            }
            const completedCount = skippedCount + successCount + errorCount + metadataUpdatedCount;
            setDownloadProgress(Math.min(100, Math.round((completedCount / total) * 100)));
        }
        setDownloadingTrack(null);
        setCurrentDownloadInfo(null);
        setIsDownloading(false);
        setBulkDownloadType(null);
        shouldStopDownloadRef.current = false;
        shouldStopDownloadRef.current = false;
        if (settings.createM3u8File && playlistName) {
            try {
                logger.info(`creating m3u8 playlist: ${playlistName}`);
                await CreateM3U8File(playlistName, outputDir, finalFilePaths.filter((p) => p !== ""));
                toast.success("M3U8 playlist created");
            }
            catch (err) {
                logger.error(`failed to create m3u8 playlist: ${err}`);
                toast.error(`Failed to create M3U8 playlist: ${err}`);
            }
        }
        logger.info(`batch complete: ${successCount} downloaded, ${skippedCount} skipped, ${metadataUpdatedCount} metadata updated, ${errorCount} failed`);
        openBatchSummary(summary);
    };
    const handleUpdateAllMetadata = async (tracks: TrackMetadata[], playlistName?: string, isAlbum?: boolean) => {
        const tracksWithId = tracks.filter((track) => track.spotify_id);
        if (tracksWithId.length === 0) {
            toast.error("No tracks available for metadata update");
            return;
        }
        const settings = await getSettingsWithDefaults();
        setIsBulkUpdatingMetadata(true);
        setDownloadProgress(0);
        suspiciousRedownloadRequestsRef.current = [];
        failedRedownloadRequestsRef.current = [];
        let summary = createEmptyBatchSummary(`${playlistName || "Tracks"} metadata update summary`);
        try {
            const enrichedTracks = await enrichTracksReleaseDates(tracksWithId, settings);
            const trackPathInfo = enrichedTracks.map((track, index) => ({
                track,
                pathInfo: buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, index + 1),
            }));
            const outputDir = trackPathInfo[0]?.pathInfo.baseOutputDir || settings.downloadPath;
            const existenceRootDir = getExistenceRootDir(trackPathInfo[0]?.pathInfo, settings, playlistName, isAlbum);
            const existenceChecks = trackPathInfo.map(({ track, pathInfo }) => ({
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
            }));
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
                                queueSuspiciousRedownload(track, settings, pathInfo, filePath);
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
            openBatchSummary(summary);
        }
        finally {
            setDownloadingTrack(null);
            setCurrentDownloadInfo(null);
            setIsBulkUpdatingMetadata(false);
        }
    };
    const handleRedownloadSuspiciousFromYouTube = async (includeFailedTracks: boolean = false) => {
        const queuedRequests = [
            ...suspiciousRedownloadRequestsRef.current.map((request) => ({ request, source: "suspicious" as const })),
            ...(includeFailedTracks
                ? failedRedownloadRequestsRef.current.map((request) => ({ request, source: "failed" as const }))
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
            suspiciousRedownloadRequestsRef.current = suspiciousRedownloadRequestsRef.current.filter((request) => {
                const index = queuedRequests.findIndex((queued) => queued.source === "suspicious" && queued.request === request);
                if (index < 0) {
                    return true;
                }
                const result = results[index];
                return !(result?.success && result.status === "replaced");
            });
            if (includeFailedTracks) {
                failedRedownloadRequestsRef.current = failedRedownloadRequestsRef.current.filter((request) => {
                    const index = queuedRequests.findIndex((queued) => queued.source === "failed" && queued.request === request);
                    if (index < 0) {
                        return true;
                    }
                    const result = results[index];
                    return !(result?.success && result.status === "replaced");
                });
            }
            openBatchSummary(nextSummary);
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
    const handleStopDownload = () => {
        logger.info("download stopped by user");
        shouldStopDownloadRef.current = true;
        toast.info("Stopping download...");
    };
    const resetDownloadedTracks = () => {
        setDownloadedTracks(new Set());
        setFailedTracks(new Set());
        setSkippedTracks(new Set());
        setSuspiciousTracks(new Map());
        suspiciousRedownloadRequestsRef.current = [];
        failedRedownloadRequestsRef.current = [];
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
        minimizeBatchSummary,
        restoreBatchSummary,
        closeBatchSummary,
        handleDownloadTrack,
        handleDownloadSelected,
        handleDownloadAll,
        handleUpdateAllMetadata,
        handleRedownloadSuspiciousFromYouTube,
        handleStopDownload,
        resetDownloadedTracks,
    };
}
