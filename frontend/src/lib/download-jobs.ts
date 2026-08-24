import type { DownloadResponse, SuspiciousRedownloadRequest, SuspiciousRedownloadResult, TrackMetadata } from "@/types/api";
import type { DownloadJobCandidate } from "./download-queue-manager";
import type { Settings } from "./settings";

export type DownloadQueueKind = "playlist" | "album" | "artist" | "adhoc";

export interface DownloadQueueContext {
    id: string;
    title: string;
    kind: DownloadQueueKind;
}

export interface SpotifyDownloadPayload {
    track: TrackMetadata;
    settings: Settings;
    queue: DownloadQueueContext;
    playlistName?: string;
    playlistOwner?: string;
    isAlbum: boolean;
    position: number;
}

export interface YouTubeRedownloadPayload extends SpotifyDownloadPayload {
    request: SuspiciousRedownloadRequest;
    sourceResultId: string;
}

export type MediaJobPayload = SpotifyDownloadPayload | YouTubeRedownloadPayload;
export type MediaJobResult = DownloadResponse | SuspiciousRedownloadResult;

export interface BuildSpotifyCandidateArgs {
    queue: DownloadQueueContext;
    track: TrackMetadata;
    sourceIndex: number;
    settings: Settings;
    playlistName?: string;
    playlistOwner?: string;
    isAlbum?: boolean;
    position?: number;
}

export interface SpotifyCollectionReference {
    kind: Exclude<DownloadQueueKind, "adhoc">;
    id: string;
}

export function parseSpotifyCollectionURL(value: string): SpotifyCollectionReference | undefined {
    const input = value.trim();
    const uri = /^spotify:(playlist|album|artist):([^:/?#]+)$/i.exec(input);
    if (uri) {
        return { kind: uri[1].toLowerCase() as SpotifyCollectionReference["kind"], id: uri[2] };
    }

    let url: URL;
    try {
        url = new URL(input.startsWith("open.spotify.com/") ? `https://${input}` : input);
    }
    catch {
        return undefined;
    }
    const parts = url.pathname.split("/").filter(Boolean);
    if (url.protocol !== "https:" || url.hostname !== "open.spotify.com" || parts.length !== 2) {
        return undefined;
    }
    const [kind, id] = parts;
    if ((kind !== "playlist" && kind !== "album" && kind !== "artist") || !id) {
        return undefined;
    }
    return { kind, id };
}

export function makeQueueId(kind: DownloadQueueKind, sourceId?: string): string {
    if (kind === "adhoc") {
        return "adhoc:single";
    }

    const source = sourceId?.trim();
    if (!source) {
        throw new Error(`${kind} source ID is required`);
    }
    const parsed = parseSpotifyCollectionURL(source);
    if (parsed) {
        if (parsed.kind !== kind) {
            throw new Error(`${kind} URL identifies ${parsed.kind}, not ${kind}`);
        }
        return `${kind}:${parsed.id}`;
    }
    if (source.includes("://") || source.startsWith("spotify:") || source.includes("spotify.com/")) {
        throw new Error(`invalid ${kind} Spotify URL`);
    }
    return `${kind}:${source}`;
}

export function makeTrackKey(track: TrackMetadata, sourceIndex: number): string {
    const spotifyID = track.spotify_id?.trim();
    return spotifyID ? `spotify:${spotifyID}` : `source:${sourceIndex}`;
}

export function buildSpotifyCandidate(
    args: BuildSpotifyCandidateArgs,
): DownloadJobCandidate<SpotifyDownloadPayload> {
    const { queue, track, settings, sourceIndex } = args;
    const playlistName = args.playlistName ?? (queue.kind === "playlist" ? queue.title : undefined);
    return {
        id: crypto.randomUUID(),
        queueId: queue.id,
        queueTitle: queue.title,
        trackKey: makeTrackKey(track, sourceIndex),
        sourceIndex,
        kind: "spotify-download",
        payload: {
            track,
            settings,
            queue,
            ...(playlistName === undefined ? {} : { playlistName }),
            ...(args.playlistOwner === undefined ? {} : { playlistOwner: args.playlistOwner }),
            isAlbum: args.isAlbum ?? queue.kind === "album",
            position: args.position ?? sourceIndex + 1,
        },
        ...(!track.spotify_id?.trim() ? { unavailableReason: "Spotify track ID is missing" } : {}),
    };
}

export function buildYouTubeCandidate(
    queue: DownloadQueueContext,
    sourceResultId: string,
    request: SuspiciousRedownloadRequest,
    sourceIndex: number,
): DownloadJobCandidate<YouTubeRedownloadPayload> {
    const metadata = request.metadata;
    return {
        id: crypto.randomUUID(),
        queueId: queue.id,
        queueTitle: queue.title,
        trackKey: `youtube:${sourceResultId}`,
        sourceIndex,
        kind: "youtube-redownload",
        payload: {
            request,
            sourceResultId,
            track: {
                spotify_id: metadata.track_id,
                name: metadata.track_name || "Unknown track",
                artists: metadata.artist_name || "",
                album_name: metadata.album_name || "",
            } as TrackMetadata,
            settings: {} as Settings,
            queue,
            isAlbum: queue.kind === "album",
            position: sourceIndex + 1,
        },
    };
}
