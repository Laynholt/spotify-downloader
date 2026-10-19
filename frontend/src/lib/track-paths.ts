import type { TrackMetadata } from "@/types/api";
import { parseTemplate, type Settings, type TemplateData } from "@/lib/settings";
import { joinPath, sanitizePath, getFirstArtist } from "@/lib/utils";

export interface BatchTrackPathInfo {
    displayArtist: string;
    displayAlbumArtist: string;
    baseOutputDir: string;
    targetOutputDir: string;
    relativePath: string;
    trackPosition: number;
    useAlbumTrackNumber: boolean;
}
export function normalizeReleaseDate(releaseDate?: string): string {
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
export function getReleaseYear(releaseDate?: string): string {
    const normalizedReleaseDate = normalizeReleaseDate(releaseDate);
    return normalizedReleaseDate.length >= 4 ? normalizedReleaseDate.slice(0, 4) : "";
}
export function buildBatchTrackPathInfo(track: Pick<TrackMetadata, "name" | "artists" | "album_name"> & Partial<TrackMetadata>, settings: Settings, playlistName: string | undefined, isAlbum: boolean | undefined, fallbackPosition: number): BatchTrackPathInfo {
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
