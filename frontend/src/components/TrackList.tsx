import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Download, CheckCircle, XCircle, FileCheck, FileText, ImageDown, Play, Pause, AlertTriangle, Clock } from "lucide-react";
import { Spinner } from "@/components/ui/spinner";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Pagination, PaginationContent, PaginationEllipsis, PaginationItem, PaginationLink, PaginationNext, PaginationPrevious, } from "@/components/ui/pagination";
import type { TrackMetadata } from "@/types/api";
import { usePreview } from "@/hooks/usePreview";
import type { backend } from "../../wailsjs/go/models";
import type { MediaJobPayload, MediaJobResult } from "@/lib/download-jobs";
import { makeTrackKey } from "@/lib/download-jobs";
import type { DownloadQueueSnapshot } from "@/lib/download-queue-manager";
import { selectLatestTrackAttempt, type TrackAttemptView } from "@/lib/download-queue-selectors";
import { formatTransferProgress, phaseLabel } from "@/lib/download-timings";
interface SuspiciousTrackInfo {
    expectedDurationSeconds?: number;
    actualDurationSeconds?: number;
    durationDeltaSeconds?: number;
}
interface TrackListProps {
    tracks: TrackMetadata[];
    searchQuery: string;
    sortBy: string;
    selectedTracks: string[];
    downloadedTracks: Set<string>;
    failedTracks: Set<string>;
    skippedTracks: Set<string>;
    suspiciousTracks?: Map<string, SuspiciousTrackInfo>;
    downloadQueue?: DownloadQueueSnapshot<MediaJobPayload, MediaJobResult>;
    downloadItemsByJobId?: ReadonlyMap<string, backend.DownloadItem>;
    currentPage: number;
    itemsPerPage: number;
    showCheckboxes?: boolean;
    hideAlbumColumn?: boolean;
    folderName?: string;
    isArtistDiscography?: boolean;
    isAlbum?: boolean;
    downloadedLyrics?: Set<string>;
    failedLyrics?: Set<string>;
    skippedLyrics?: Set<string>;
    downloadingLyricsTrack?: string | null;
    downloadedCovers?: Set<string>;
    failedCovers?: Set<string>;
    skippedCovers?: Set<string>;
    downloadingCoverTrack?: string | null;
    onToggleTrack: (id: string) => void;
    onToggleSelectAll: (tracks: TrackMetadata[]) => void;
    onDownloadTrack: (track: TrackMetadata, folderName?: string, isArtistDiscography?: boolean, isAlbum?: boolean, position?: number) => void;
    onDownloadLyrics?: (spotifyId: string, name: string, artists: string, albumName: string, folderName?: string, isArtistDiscography?: boolean, position?: number, albumArtist?: string, releaseDate?: string, discNumber?: number) => void;
    onDownloadCover?: (coverUrl: string, trackName: string, artistName: string, albumName: string, folderName?: string, isArtistDiscography?: boolean, position?: number, trackId?: string, albumArtist?: string, releaseDate?: string, discNumber?: number) => void;
    onPageChange: (page: number) => void;
    onAlbumClick?: (album: {
        id: string;
        name: string;
        external_urls: string;
    }) => void;
    onArtistClick?: (artist: {
        id: string;
        name: string;
        external_urls: string;
    }) => void;
    onTrackClick?: (track: TrackMetadata) => void;
}
export function TrackList({ tracks, searchQuery, sortBy, selectedTracks, downloadedTracks, failedTracks, skippedTracks, suspiciousTracks, downloadQueue, downloadItemsByJobId, currentPage, itemsPerPage, showCheckboxes = false, hideAlbumColumn = false, folderName, isArtistDiscography = false, isAlbum = false, downloadedLyrics, failedLyrics, skippedLyrics, downloadingLyricsTrack, downloadedCovers, failedCovers, skippedCovers, downloadingCoverTrack, onToggleTrack, onToggleSelectAll, onDownloadTrack, onDownloadLyrics, onDownloadCover, onPageChange, onAlbumClick, onArtistClick, onTrackClick, }: TrackListProps) {
    const { playPreview, loadingPreview, playingTrack } = usePreview();
    const useLegacyMediaState = downloadQueue === undefined;
    const sourceIndexByTrack = new Map(tracks.map((track, index) => [track, index]));
    const attemptFor = (track: TrackMetadata): TrackAttemptView | undefined => {
        if (!downloadQueue) {
            return undefined;
        }
        const sourceIndex = sourceIndexByTrack.get(track) ?? 0;
        return selectLatestTrackAttempt(downloadQueue, makeTrackKey(track, sourceIndex), downloadItemsByJobId);
    };
    const isDownloaded = (track: TrackMetadata): boolean => {
        const attempt = attemptFor(track);
        return attempt
            ? attempt.status === "completed" || attempt.status === "skipped" || attempt.status === "suspicious"
            : useLegacyMediaState && Boolean(track.spotify_id && downloadedTracks.has(track.spotify_id));
    };
    const isFailed = (track: TrackMetadata): boolean => {
        const attempt = attemptFor(track);
        return attempt
            ? attempt.status === "failed" || attempt.status === "unavailable"
            : useLegacyMediaState && Boolean(track.spotify_id && failedTracks.has(track.spotify_id));
    };
    let filteredTracks = tracks.filter((track) => {
        if (!searchQuery)
            return true;
        const query = searchQuery.toLowerCase();
        return (track.name.toLowerCase().includes(query) ||
            track.artists.toLowerCase().includes(query) ||
            track.album_name.toLowerCase().includes(query));
    });
    if (sortBy === "title-asc") {
        filteredTracks = [...filteredTracks].sort((a, b) => a.name.localeCompare(b.name));
    }
    else if (sortBy === "title-desc") {
        filteredTracks = [...filteredTracks].sort((a, b) => b.name.localeCompare(a.name));
    }
    else if (sortBy === "artist-asc") {
        filteredTracks = [...filteredTracks].sort((a, b) => a.artists.localeCompare(b.artists));
    }
    else if (sortBy === "artist-desc") {
        filteredTracks = [...filteredTracks].sort((a, b) => b.artists.localeCompare(a.artists));
    }
    else if (sortBy === "duration-asc") {
        filteredTracks = [...filteredTracks].sort((a, b) => a.duration_ms - b.duration_ms);
    }
    else if (sortBy === "duration-desc") {
        filteredTracks = [...filteredTracks].sort((a, b) => b.duration_ms - a.duration_ms);
    }
    else if (sortBy === "plays-asc") {
        filteredTracks = [...filteredTracks].sort((a, b) => {
            const aPlays = a.plays ? parseInt(a.plays, 10) : 0;
            const bPlays = b.plays ? parseInt(b.plays, 10) : 0;
            if (isNaN(aPlays))
                return 1;
            if (isNaN(bPlays))
                return -1;
            return aPlays - bPlays;
        });
    }
    else if (sortBy === "plays-desc") {
        filteredTracks = [...filteredTracks].sort((a, b) => {
            const aPlays = a.plays ? parseInt(a.plays, 10) : 0;
            const bPlays = b.plays ? parseInt(b.plays, 10) : 0;
            if (isNaN(aPlays))
                return 1;
            if (isNaN(bPlays))
                return -1;
            return bPlays - aPlays;
        });
    }
    else if (sortBy === "downloaded") {
        filteredTracks = [...filteredTracks].sort((a, b) => {
            const aDownloaded = isDownloaded(a);
            const bDownloaded = isDownloaded(b);
            return (bDownloaded ? 1 : 0) - (aDownloaded ? 1 : 0);
        });
    }
    else if (sortBy === "not-downloaded") {
        filteredTracks = [...filteredTracks].sort((a, b) => {
            const aDownloaded = isDownloaded(a);
            const bDownloaded = isDownloaded(b);
            return (aDownloaded ? 1 : 0) - (bDownloaded ? 1 : 0);
        });
    }
    else if (sortBy === "failed") {
        filteredTracks = [...filteredTracks].sort((a, b) => {
            const aFailed = isFailed(a);
            const bFailed = isFailed(b);
            return (bFailed ? 1 : 0) - (aFailed ? 1 : 0);
        });
    }
    const totalPages = Math.ceil(filteredTracks.length / itemsPerPage);
    const startIndex = (currentPage - 1) * itemsPerPage;
    const endIndex = startIndex + itemsPerPage;
    const paginatedTracks = filteredTracks.slice(startIndex, endIndex);
    const getPaginationPages = (current: number, total: number): (number | 'ellipsis')[] => {
        if (total <= 10) {
            return Array.from({ length: total }, (_, i) => i + 1);
        }
        const pages: (number | 'ellipsis')[] = [];
        pages.push(1);
        if (current <= 7) {
            for (let i = 2; i <= 10; i++) {
                pages.push(i);
            }
            pages.push('ellipsis');
            pages.push(total);
        }
        else if (current >= total - 7) {
            pages.push('ellipsis');
            for (let i = total - 9; i <= total; i++) {
                pages.push(i);
            }
        }
        else {
            pages.push('ellipsis');
            pages.push(current - 1);
            pages.push(current);
            pages.push(current + 1);
            pages.push('ellipsis');
            pages.push(total);
        }
        return pages;
    };
    const tracksWithId = filteredTracks.filter((track) => track.spotify_id);
    const allSelected = tracksWithId.length > 0 &&
        tracksWithId.every((track) => selectedTracks.includes(track.spotify_id!));
    const formatDuration = (ms: number) => {
        const minutes = Math.floor(ms / 60000);
        const seconds = Math.floor((ms % 60000) / 1000);
        return `${minutes}:${seconds.toString().padStart(2, "0")}`;
    };
    const formatPlays = (plays: string | undefined) => {
        if (!plays)
            return "";
        const num = parseInt(plays, 10);
        if (isNaN(num))
            return plays;
        return num.toLocaleString();
    };
    const formatSuspiciousTooltip = (info?: SuspiciousTrackInfo) => {
        if (!info) {
            return "Suspicious duration";
        }
        const parts = [];
        if (info.expectedDurationSeconds !== undefined) {
            parts.push(`expected ${info.expectedDurationSeconds.toFixed(1)}s`);
        }
        if (info.actualDurationSeconds !== undefined) {
            parts.push(`actual ${info.actualDurationSeconds.toFixed(1)}s`);
        }
        if (info.durationDeltaSeconds !== undefined) {
            parts.push(`delta ${info.durationDeltaSeconds.toFixed(1)}s`);
        }
        return parts.length > 0 ? parts.join(", ") : "Suspicious duration";
    };
    const attemptIcon = (attempt: TrackAttemptView) => {
        switch (attempt.status) {
            case "queued":
                return <Clock className="h-4 w-4 text-muted-foreground"/>;
            case "active":
                return <Spinner />;
            case "completed":
                return <CheckCircle className="h-4 w-4 text-green-500"/>;
            case "skipped":
                return <FileCheck className="h-4 w-4 text-yellow-500"/>;
            case "suspicious":
                return <AlertTriangle className="h-4 w-4 text-yellow-500"/>;
            case "failed":
                return <XCircle className="h-4 w-4 text-red-500"/>;
            case "cancelled":
                return <XCircle className="h-4 w-4 text-orange-500"/>;
            case "unavailable":
                return <XCircle className="h-4 w-4 text-muted-foreground"/>;
        }
    };
    const attemptTooltip = (attempt: TrackAttemptView): string => {
        switch (attempt.status) {
            case "queued":
                return "Queued";
            case "active":
                return `${phaseLabel(attempt.phase || "downloading")}${attempt.detail ? ` · ${attempt.detail}` : ""}`;
            case "completed":
                return "Downloaded";
            case "skipped":
                return "Already exists";
            case "suspicious":
                return attempt.warning || "Suspicious download";
            case "failed":
                return attempt.error || "Failed";
            case "cancelled":
                return attempt.error || "Cancelled";
            case "unavailable":
                return attempt.error || "Unavailable";
        }
    };
    return (<div className="space-y-4">
    <div className="rounded-md border">
      <div className="overflow-x-auto">
        <table className="w-full">
          <thead>
            <tr className="border-b bg-muted/50">
              {showCheckboxes && (<th className="h-12 px-4 text-left align-middle w-12">
                <Checkbox checked={allSelected} onCheckedChange={() => onToggleSelectAll(filteredTracks)}/>
              </th>)}
              <th className="h-12 px-4 text-left align-middle font-medium text-muted-foreground w-12">
                #
              </th>
              <th className="h-12 px-4 text-left align-middle font-medium text-muted-foreground">
                Title
              </th>
              {!hideAlbumColumn && (<th className="h-12 px-4 text-left align-middle font-medium text-muted-foreground hidden md:table-cell">
                Album
              </th>)}
              <th className="h-12 px-4 text-left align-middle font-medium text-muted-foreground hidden lg:table-cell w-24">
                Duration
              </th>
              <th className="h-12 px-4 text-left align-middle font-medium text-muted-foreground hidden xl:table-cell w-32">
                Plays
              </th>
              <th className="h-12 px-4 text-center align-middle font-medium text-muted-foreground w-32">
                Actions
              </th>
            </tr>
          </thead>
          <tbody>
            {paginatedTracks.map((track, index) => {
            const sourceIndex = sourceIndexByTrack.get(track) ?? startIndex + index;
            const attempt = attemptFor(track);
            const attemptBusy = attempt?.status === "queued" || attempt?.status === "active";
            const speedMbps = attempt?.speedMbps ?? 0;
            const transfer = attempt?.totalBytes
                ? formatTransferProgress(attempt.downloadedBytes ?? 0, attempt.totalBytes)
                : "";
            const legacySkipped = useLegacyMediaState && Boolean(track.spotify_id && skippedTracks.has(track.spotify_id));
            const legacyDownloaded = useLegacyMediaState && Boolean(track.spotify_id && downloadedTracks.has(track.spotify_id));
            const legacyFailed = useLegacyMediaState && Boolean(track.spotify_id && failedTracks.has(track.spotify_id));
            const suspiciousInfo = useLegacyMediaState && track.spotify_id ? suspiciousTracks?.get(track.spotify_id) : undefined;
            const isSuspicious = attempt ? attempt.status === "suspicious" : !!suspiciousInfo;
            const mediaLabel = attempt
                ? attemptTooltip(attempt)
                : isSuspicious
                    ? formatSuspiciousTooltip(suspiciousInfo)
                    : legacySkipped
                        ? "Already exists"
                        : legacyDownloaded
                            ? "Downloaded"
                            : legacyFailed
                                ? "Failed"
                                : "Download Track";
            return (<tr key={index} className="border-b transition-colors hover:bg-muted/50">
              {showCheckboxes && (<td className="p-4 align-middle">
                {track.spotify_id && (<Checkbox checked={selectedTracks.includes(track.spotify_id)} onCheckedChange={() => onToggleTrack(track.spotify_id!)}/>)}
              </td>)}
              <td className="p-4 align-middle text-sm text-muted-foreground">
                <div className="flex flex-col items-center gap-0.5">
                  <span>{startIndex + index + 1}</span>
                  {track.status && (track.status === "UP" || track.status === "DOWN" || track.status === "NEW") && (<span className={`text-xs ${track.status === "UP"
                    ? "text-green-500"
                    : track.status === "DOWN"
                        ? "text-red-500"
                        : track.status === "NEW"
                            ? "text-blue-500"
                            : ""}`}>
                    {track.status === "NEW" ? "●" : track.status === "UP" ? "▲" : "▼"}
                  </span>)}
                </div>
              </td>
              <td className="p-4 align-middle">
                <div className="flex items-center gap-3">
                  {track.images && (<img src={track.images} alt={track.name} className="w-10 h-10 rounded object-cover"/>)}
                  <div className="flex flex-col">
                    <div className="flex items-center gap-2">
                      {onTrackClick ? (<span className="font-medium cursor-pointer hover:underline" onClick={() => onTrackClick(track)}>
                        {track.name}
                      </span>) : (<span className="font-medium">{track.name}</span>)}
                      {track.is_explicit && (<span className="inline-flex items-center justify-center bg-red-600 text-white text-[10px] h-4 w-4 rounded shrink-0" title="Explicit">E</span>)}
                      {attempt ? attemptIcon(attempt) : isSuspicious ? (<Tooltip>
                        <TooltipTrigger asChild>
                          <AlertTriangle className="h-4 w-4 text-yellow-500 shrink-0"/>
                        </TooltipTrigger>
                        <TooltipContent>
                          <p>{formatSuspiciousTooltip(suspiciousInfo)}</p>
                        </TooltipContent>
                      </Tooltip>) : legacySkipped ? (<FileCheck className="h-4 w-4 text-yellow-500 shrink-0"/>) : legacyDownloaded ? (<CheckCircle className="h-4 w-4 text-green-500 shrink-0"/>) : legacyFailed ? (<XCircle className="h-4 w-4 text-red-500 shrink-0"/>) : null}
                    </div>
                    <span className="text-sm text-muted-foreground">
                      {track.artists_data && track.artists_data.length > 0 ? ((() => {
                const artistNames = track.artists.split(", ").map(name => name.trim());
                return artistNames.map((name, i) => {
                    const artistData = track.artists_data![i];
                    const hasArtistData = artistData && artistData.id && artistData.external_urls;
                    return (<span key={artistData?.id || i}>
                            {onArtistClick && hasArtistData ? (<span className="cursor-pointer hover:underline" onClick={() => onArtistClick({
                                id: artistData.id,
                                name: name,
                                external_urls: artistData.external_urls,
                            })}>
                              {name}
                            </span>) : (name)}
                            {i < artistNames.length - 1 && ", "}
                          </span>);
                });
            })()) : onArtistClick && track.artist_id && track.artist_url ? (<span className="cursor-pointer hover:underline" onClick={() => onArtistClick({
                    id: track.artist_id!,
                    name: track.artists,
                    external_urls: track.artist_url!,
                })}>
                        {track.artists}
                      </span>) : (track.artists)}
                    </span>
                    {attempt?.status === "active" && (<span className="max-w-[420px] truncate text-xs text-muted-foreground">
                      {phaseLabel(attempt.phase || "downloading")}{attempt.detail ? ` · ${attempt.detail}` : ""}
                    </span>)}
                    {(transfer || speedMbps > 0) && (<span className="text-xs font-mono text-muted-foreground">
                      {transfer}{transfer && speedMbps > 0 ? " · " : ""}
                      {speedMbps > 0 ? `${speedMbps.toFixed(2)} MB/s` : ""}
                    </span>)}
                    {attempt?.warning && (<span className="max-w-[420px] truncate text-xs text-yellow-600 dark:text-yellow-400">{attempt.warning}</span>)}
                    {attempt?.error && (attempt.status === "failed" || attempt.status === "cancelled" || attempt.status === "unavailable") && (<span className="max-w-[420px] truncate text-xs text-red-500">{attempt.error}</span>)}
                  </div>
                </div>
              </td>
              {!hideAlbumColumn && (<td className="p-4 align-middle text-sm text-muted-foreground hidden md:table-cell">
                {onAlbumClick && track.album_id && track.album_url ? (<span className="cursor-pointer hover:underline" onClick={() => onAlbumClick({
                        id: track.album_id!,
                        name: track.album_name,
                        external_urls: track.album_url!,
                    })}>
                  {track.album_name}
                </span>) : (track.album_name)}
              </td>)}
              <td className="p-4 align-middle text-sm text-muted-foreground hidden lg:table-cell">
                {formatDuration(track.duration_ms)}
              </td>
              <td className="p-4 align-middle text-sm text-muted-foreground hidden xl:table-cell">
                {track.plays ? formatPlays(track.plays) : ""}
              </td>
              <td className="p-4 align-middle text-center">
                <div className="flex items-center justify-center gap-1">
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Button onClick={() => onDownloadTrack(track, folderName, isArtistDiscography, isAlbum, sourceIndex + 1)} size="icon" disabled={attemptBusy} aria-label={mediaLabel}>
                        {attempt ? attemptIcon(attempt) : isSuspicious ? (<AlertTriangle className="h-4 w-4"/>) : legacySkipped ? (<FileCheck className="h-4 w-4"/>) : legacyDownloaded ? (<CheckCircle className="h-4 w-4"/>) : legacyFailed ? (<XCircle className="h-4 w-4"/>) : (<Download className="h-4 w-4"/>)}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>
                      <p>{mediaLabel}</p>
                    </TooltipContent>
                  </Tooltip>
                  {track.spotify_id && (<Tooltip>
                    <TooltipTrigger asChild>
                      <Button onClick={() => playPreview(track.spotify_id!, track.name)} size="icon" variant="outline" disabled={loadingPreview === track.spotify_id}>
                        {loadingPreview === track.spotify_id ? (<Spinner />) : playingTrack === track.spotify_id ? (<Pause className="h-4 w-4"/>) : (<Play className="h-4 w-4"/>)}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>
                      <p>{playingTrack === track.spotify_id ? "Stop Preview" : "Play Preview"}</p>
                    </TooltipContent>
                  </Tooltip>)}
                  {track.spotify_id && onDownloadLyrics && (<Tooltip>
                    <TooltipTrigger asChild>
                      <Button onClick={() => onDownloadLyrics(track.spotify_id!, track.name, track.artists, track.album_name, folderName, isArtistDiscography, sourceIndex + 1, track.album_artist, track.release_date, track.disc_number)} size="icon" variant="outline" disabled={downloadingLyricsTrack === track.spotify_id}>
                        {downloadingLyricsTrack === track.spotify_id ? (<Spinner />) : skippedLyrics?.has(track.spotify_id) ? (<FileCheck className="h-4 w-4 text-yellow-500"/>) : downloadedLyrics?.has(track.spotify_id) ? (<CheckCircle className="h-4 w-4 text-green-500"/>) : failedLyrics?.has(track.spotify_id) ? (<XCircle className="h-4 w-4 text-red-500"/>) : (<FileText className="h-4 w-4"/>)}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>
                      {downloadingLyricsTrack === track.spotify_id ? (<p>Downloading lyrics...</p>) : skippedLyrics?.has(track.spotify_id) ? (<p>Lyrics already exists</p>) : downloadedLyrics?.has(track.spotify_id) ? (<p>Lyrics downloaded</p>) : failedLyrics?.has(track.spotify_id) ? (<p>Lyrics failed</p>) : (<p>Download Lyrics</p>)}
                    </TooltipContent>
                  </Tooltip>)}
                  {track.images && onDownloadCover && (<Tooltip>
                    <TooltipTrigger asChild>
                      <Button onClick={() => {
                    const trackId = track.spotify_id || `${track.name}-${track.artists}`;
                    onDownloadCover(track.images, track.name, track.artists, track.album_name, folderName, isArtistDiscography, sourceIndex + 1, trackId, track.album_artist, track.release_date, track.disc_number);
                }} size="icon" variant="outline" disabled={downloadingCoverTrack === (track.spotify_id || `${track.name}-${track.artists}`)}>
                        {downloadingCoverTrack === (track.spotify_id || `${track.name}-${track.artists}`) ? (<Spinner />) : skippedCovers?.has(track.spotify_id || `${track.name}-${track.artists}`) ? (<FileCheck className="h-4 w-4 text-yellow-500"/>) : downloadedCovers?.has(track.spotify_id || `${track.name}-${track.artists}`) ? (<CheckCircle className="h-4 w-4 text-green-500"/>) : failedCovers?.has(track.spotify_id || `${track.name}-${track.artists}`) ? (<XCircle className="h-4 w-4 text-red-500"/>) : (<ImageDown className="h-4 w-4"/>)}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>
                      {downloadingCoverTrack === (track.spotify_id || `${track.name}-${track.artists}`) ? (<p>Downloading cover...</p>) : skippedCovers?.has(track.spotify_id || `${track.name}-${track.artists}`) ? (<p>Cover already exists</p>) : downloadedCovers?.has(track.spotify_id || `${track.name}-${track.artists}`) ? (<p>Cover downloaded</p>) : failedCovers?.has(track.spotify_id || `${track.name}-${track.artists}`) ? (<p>Cover failed</p>) : (<p>Download Cover</p>)}
                    </TooltipContent>
                  </Tooltip>)}
                </div>
              </td>
            </tr>);
        })}
          </tbody>
        </table>
      </div>
    </div>

    {totalPages > 1 && (<Pagination>
      <PaginationContent>
        <PaginationItem>
          <PaginationPrevious href="#" onClick={(e) => {
                e.preventDefault();
                if (currentPage > 1)
                    onPageChange(currentPage - 1);
            }} className={currentPage === 1 ? "pointer-events-none opacity-50" : "cursor-pointer"}/>
        </PaginationItem>

        {getPaginationPages(currentPage, totalPages).map((page, index) => (page === 'ellipsis' ? (<PaginationItem key={`ellipsis-${index}`}>
              <PaginationEllipsis />
            </PaginationItem>) : (<PaginationItem key={page}>
              <PaginationLink href="#" onClick={(e) => {
                    e.preventDefault();
                    onPageChange(page);
                }} isActive={currentPage === page} className="cursor-pointer">
                {page}
              </PaginationLink>
            </PaginationItem>)))}

        <PaginationItem>
          <PaginationNext href="#" onClick={(e) => {
                e.preventDefault();
                if (currentPage < totalPages)
                    onPageChange(currentPage + 1);
            }} className={currentPage === totalPages
                ? "pointer-events-none opacity-50"
                : "cursor-pointer"}/>
        </PaginationItem>
      </PaginationContent>
    </Pagination>)}
  </div>);
}
