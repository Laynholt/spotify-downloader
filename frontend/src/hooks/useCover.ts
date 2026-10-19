import { useState, useRef } from "react";
import { toast } from "sonner";
import { DownloadCover } from "../../wailsjs/go/main/App";
import { getSettingsWithDefaults } from "@/lib/settings";
import { buildBatchTrackPathInfo, normalizeReleaseDate } from "@/lib/track-paths";
import type { TrackMetadata } from "@/types/api";
import { logger } from "@/lib/logger";
export const useCover = () => {
    const [downloadingCoverTrack, setDownloadingCoverTrack] = useState<string | null>(null);
    const [downloadedCovers, setDownloadedCovers] = useState<Set<string>>(new Set());
    const [failedCovers, setFailedCovers] = useState<Set<string>>(new Set());
    const [skippedCovers, setSkippedCovers] = useState<Set<string>>(new Set());
    const [isBulkDownloadingCovers, setIsBulkDownloadingCovers] = useState(false);
    const [coverDownloadProgress, setCoverDownloadProgress] = useState(0);
    const stopBulkDownloadRef = useRef(false);
    const handleDownloadCover = async (coverUrl: string, trackName: string, artistName: string, albumName?: string, playlistName?: string, _isArtistDiscography?: boolean, position?: number, trackId?: string, albumArtist?: string, releaseDate?: string, discNumber?: number, isAlbum?: boolean) => {
        if (!coverUrl) {
            toast.error("No cover URL found");
            return;
        }
        const id = trackId || `${trackName}-${artistName}`;
        logger.info(`downloading cover: ${trackName} - ${artistName}`);
        const settings = await getSettingsWithDefaults();
        setDownloadingCoverTrack(id);
        try {
            const pathInfo = buildBatchTrackPathInfo({
                name: trackName, artists: artistName, album_name: albumName || "",
                album_artist: albumArtist, release_date: releaseDate, disc_number: discNumber,
                track_number: position,
            }, settings, playlistName, isAlbum, position || 0);
            const { targetOutputDir: outputDir, displayArtist, displayAlbumArtist, trackPosition } = pathInfo;
            const response = await DownloadCover({
                cover_url: coverUrl,
                track_name: trackName || "",
                artist_name: displayArtist || "",
                album_name: albumName || "",
                album_artist: displayAlbumArtist || "",
                release_date: normalizeReleaseDate(releaseDate),
                output_dir: outputDir,
                filename_format: settings.filenameTemplate || "{title}",
                track_number: settings.trackNumber,
                position: trackPosition,
                disc_number: discNumber || 0,
            });
            if (response.success) {
                if (response.already_exists) {
                    toast.info("Cover file already exists");
                    setSkippedCovers((prev) => new Set(prev).add(id));
                }
                else {
                    toast.success("Cover downloaded successfully");
                    setDownloadedCovers((prev) => new Set(prev).add(id));
                }
                setFailedCovers((prev) => {
                    const newSet = new Set(prev);
                    newSet.delete(id);
                    return newSet;
                });
            }
            else {
                toast.error(response.error || "Failed to download cover");
                setFailedCovers((prev) => new Set(prev).add(id));
            }
        }
        catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to download cover");
            setFailedCovers((prev) => new Set(prev).add(id));
        }
        finally {
            setDownloadingCoverTrack(null);
        }
    };
    const handleDownloadAllCovers = async (tracks: TrackMetadata[], playlistName?: string, _isArtistDiscography?: boolean, isAlbum?: boolean) => {
        const tracksWithCover = tracks.filter((track) => track.images);
        if (tracksWithCover.length === 0) {
            toast.error("No tracks with cover URL available");
            return;
        }
        const settings = await getSettingsWithDefaults();
        setIsBulkDownloadingCovers(true);
        setCoverDownloadProgress(0);
        stopBulkDownloadRef.current = false;
        let completed = 0;
        let success = 0;
        let failed = 0;
        let skipped = 0;
        const total = tracksWithCover.length;
        for (let i = 0; i < tracksWithCover.length; i++) {
            const track = tracksWithCover[i];
            if (stopBulkDownloadRef.current) {
                toast.info("Cover download stopped by user");
                break;
            }
            const id = track.spotify_id || `${track.name}-${track.artists}`;
            setDownloadingCoverTrack(id);
            setCoverDownloadProgress(Math.round((completed / total) * 100));
            try {
                const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, i + 1);
                const { targetOutputDir: outputDir, displayArtist, displayAlbumArtist, trackPosition } = pathInfo;
                const response = await DownloadCover({
                    cover_url: track.images || "",
                    track_name: track.name || "",
                    artist_name: displayArtist || "",
                    album_name: track.album_name || "",
                    album_artist: displayAlbumArtist || "",
                    release_date: normalizeReleaseDate(track.release_date),
                    output_dir: outputDir,
                    filename_format: settings.filenameTemplate || "{title}",
                    track_number: settings.trackNumber,
                    position: trackPosition,
                    disc_number: track.disc_number || 0,
                });
                if (response.success) {
                    if (response.already_exists) {
                        skipped++;
                        setSkippedCovers((prev) => new Set(prev).add(id));
                    }
                    else {
                        success++;
                        setDownloadedCovers((prev) => new Set(prev).add(id));
                    }
                    setFailedCovers((prev) => {
                        const newSet = new Set(prev);
                        newSet.delete(id);
                        return newSet;
                    });
                }
                else {
                    failed++;
                    setFailedCovers((prev) => new Set(prev).add(id));
                }
            }
            catch (err) {
                failed++;
                logger.error(`error downloading cover: ${track.name} - ${err}`);
                setFailedCovers((prev) => new Set(prev).add(id));
            }
            completed++;
        }
        setIsBulkDownloadingCovers(false);
        setDownloadingCoverTrack(null);
        setCoverDownloadProgress(100);
        toast.info(`Cover download completed: ${success} success, ${skipped} skipped, ${failed} failed`);
    };
    const handleStopCoverDownload = () => {
        stopBulkDownloadRef.current = true;
    };
    const resetCoverState = () => {
        setDownloadedCovers(new Set());
        setFailedCovers(new Set());
        setSkippedCovers(new Set());
    };
    return {
        downloadingCoverTrack,
        downloadedCovers,
        failedCovers,
        skippedCovers,
        isBulkDownloadingCovers,
        coverDownloadProgress,
        handleDownloadCover,
        handleDownloadAllCovers,
        handleStopCoverDownload,
        resetCoverState,
    };
};
