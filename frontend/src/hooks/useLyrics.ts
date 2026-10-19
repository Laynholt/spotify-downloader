import { useState, useRef } from "react";
import { toast } from "sonner";
import { DownloadLyrics } from "../../wailsjs/go/main/App";
import { getSettingsWithDefaults } from "@/lib/settings";
import { buildBatchTrackPathInfo, normalizeReleaseDate } from "@/lib/track-paths";
import type { TrackMetadata } from "@/types/api";
import { logger } from "@/lib/logger";
export const useLyrics = () => {
    const [downloadingLyricsTrack, setDownloadingLyricsTrack] = useState<string | null>(null);
    const [downloadedLyrics, setDownloadedLyrics] = useState<Set<string>>(new Set());
    const [failedLyrics, setFailedLyrics] = useState<Set<string>>(new Set());
    const [skippedLyrics, setSkippedLyrics] = useState<Set<string>>(new Set());
    const [isBulkDownloadingLyrics, setIsBulkDownloadingLyrics] = useState(false);
    const [lyricsDownloadProgress, setLyricsDownloadProgress] = useState(0);
    const stopBulkDownloadRef = useRef(false);
    const handleDownloadLyrics = async (spotifyId: string, trackName: string, artistName: string, albumName?: string, playlistName?: string, _isArtistDiscography?: boolean, position?: number, albumArtist?: string, releaseDate?: string, discNumber?: number, isAlbum?: boolean) => {
        if (!spotifyId) {
            toast.error("No Spotify ID found for this track");
            return;
        }
        logger.info(`downloading lyrics: ${trackName} - ${artistName}`);
        const settings = await getSettingsWithDefaults();
        setDownloadingLyricsTrack(spotifyId);
        try {
            const pathInfo = buildBatchTrackPathInfo({
                name: trackName, artists: artistName, album_name: albumName || "",
                album_artist: albumArtist, release_date: releaseDate, disc_number: discNumber,
                track_number: position,
            }, settings, playlistName, isAlbum, position || 0);
            const { targetOutputDir: outputDir, displayArtist, displayAlbumArtist, trackPosition, useAlbumTrackNumber } = pathInfo;
            const response = await DownloadLyrics({
                spotify_id: spotifyId,
                track_name: trackName || "",
                artist_name: displayArtist || "",
                album_name: albumName || "",
                album_artist: displayAlbumArtist || "",
                release_date: normalizeReleaseDate(releaseDate),
                output_dir: outputDir,
                filename_format: settings.filenameTemplate || "{title}",
                track_number: settings.trackNumber,
                position: trackPosition,
                use_album_track_number: useAlbumTrackNumber,
                disc_number: discNumber || 0,
            });
            if (response.success) {
                if (response.already_exists) {
                    toast.info("Lyrics file already exists");
                    setSkippedLyrics((prev) => new Set(prev).add(spotifyId));
                }
                else {
                    toast.success("Lyrics downloaded successfully");
                    setDownloadedLyrics((prev) => new Set(prev).add(spotifyId));
                }
                setFailedLyrics((prev) => {
                    const newSet = new Set(prev);
                    newSet.delete(spotifyId);
                    return newSet;
                });
            }
            else {
                toast.error(response.error || "Failed to download lyrics");
                setFailedLyrics((prev) => new Set(prev).add(spotifyId));
            }
        }
        catch (err) {
            toast.error(err instanceof Error ? err.message : "Failed to download lyrics");
            setFailedLyrics((prev) => new Set(prev).add(spotifyId));
        }
        finally {
            setDownloadingLyricsTrack(null);
        }
    };
    const handleDownloadAllLyrics = async (tracks: TrackMetadata[], playlistName?: string, _isArtistDiscography?: boolean, isAlbum?: boolean) => {
        const tracksWithSpotifyId = tracks.filter((track) => track.spotify_id);
        if (tracksWithSpotifyId.length === 0) {
            toast.error("No tracks with Spotify ID available for lyrics download");
            return;
        }
        const settings = await getSettingsWithDefaults();
        setIsBulkDownloadingLyrics(true);
        setLyricsDownloadProgress(0);
        stopBulkDownloadRef.current = false;
        let completed = 0;
        let success = 0;
        let failed = 0;
        let skipped = 0;
        const total = tracksWithSpotifyId.length;
        for (let i = 0; i < tracksWithSpotifyId.length; i++) {
            const track = tracksWithSpotifyId[i];
            if (stopBulkDownloadRef.current) {
                toast.info("Lyrics download stopped by user");
                break;
            }
            const id = track.spotify_id!;
            setDownloadingLyricsTrack(id);
            setLyricsDownloadProgress(Math.round((completed / total) * 100));
            try {
                const pathInfo = buildBatchTrackPathInfo(track, settings, playlistName, isAlbum, i + 1);
                const { targetOutputDir: outputDir, displayArtist, displayAlbumArtist, trackPosition, useAlbumTrackNumber } = pathInfo;
                const response = await DownloadLyrics({
                    spotify_id: id,
                    track_name: track.name || "",
                    artist_name: displayArtist || "",
                    album_name: track.album_name || "",
                    album_artist: displayAlbumArtist || "",
                    release_date: normalizeReleaseDate(track.release_date),
                    output_dir: outputDir,
                    filename_format: settings.filenameTemplate || "{title}",
                    track_number: settings.trackNumber,
                    position: trackPosition,
                    use_album_track_number: useAlbumTrackNumber,
                    disc_number: track.disc_number || 0,
                });
                if (response.success) {
                    if (response.already_exists) {
                        skipped++;
                        setSkippedLyrics((prev) => new Set(prev).add(id));
                    }
                    else {
                        success++;
                        setDownloadedLyrics((prev) => new Set(prev).add(id));
                    }
                    setFailedLyrics((prev) => {
                        const newSet = new Set(prev);
                        newSet.delete(id);
                        return newSet;
                    });
                }
                else {
                    failed++;
                    setFailedLyrics((prev) => new Set(prev).add(id));
                }
            }
            catch (err) {
                failed++;
                logger.error(`error downloading lyrics: ${track.name} - ${err}`);
                setFailedLyrics((prev) => new Set(prev).add(id));
            }
            completed++;
        }
        setIsBulkDownloadingLyrics(false);
        setDownloadingLyricsTrack(null);
        setLyricsDownloadProgress(100);
        toast.info(`Lyrics download completed: ${success} success, ${skipped} skipped, ${failed} failed`);
    };
    const handleStopLyricsDownload = () => {
        stopBulkDownloadRef.current = true;
    };
    const resetLyricsState = () => {
        setDownloadedLyrics(new Set());
        setFailedLyrics(new Set());
        setSkippedLyrics(new Set());
    };
    return {
        downloadingLyricsTrack,
        downloadedLyrics,
        failedLyrics,
        skippedLyrics,
        isBulkDownloadingLyrics,
        lyricsDownloadProgress,
        handleDownloadLyrics,
        handleDownloadAllLyrics,
        handleStopLyricsDownload,
        resetLyricsState,
    };
};
