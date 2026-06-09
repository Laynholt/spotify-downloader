# Integrated Download Pipeline Design

## Context

SpotiDownloader currently downloads Spotify-sourced tracks through the SpotiDownloader API, embeds metadata during download, skips existing files during batch downloads, and exposes bulk actions for lyrics and covers. The new work extends that pipeline with metadata refreshes for existing files, suspicious-download detection, end-of-run summaries, and YouTube/yt-dlp redownload for suspicious tracks.

## Goals

- Allow existing audio files to be refreshed with the same embedded metadata the normal download pipeline writes.
- Add a bulk metadata refresh action near the existing bulk lyrics and cover actions.
- Detect likely wrong downloads by comparing Spotify track duration to the downloaded file duration.
- Show a detailed playlist/album/artist batch summary after a run.
- Offer a one-click redownload path for suspicious tracks through a managed yt-dlp binary.

## Non-Goals

- No manual user-provided YouTube URL flow in the first implementation.
- No replacement of the normal SpotiDownloader API path for all downloads.
- No cloud service or persistent remote state.
- No permanent deletion of suspicious originals.

## Metadata Refresh Behavior

Add a setting named `updateMetadataForExistingFiles`.

When this setting is enabled during a batch download, files that already exist are not treated as a pure skip. The app finds the existing file and refreshes all metadata currently written by the normal pipeline:

- title, artist, album, album artist, release date/year
- track number, total tracks, disc number, total discs
- Spotify URL/ID metadata where supported
- copyright and publisher
- cover art, respecting the max-quality cover setting
- genre, respecting the genre settings
- embedded lyrics, respecting the embedded lyrics setting

The result should be counted separately as `metadata updated`, not `downloaded`.

Add a separate bulk action named `Update All Metadata` near `Download All Lyrics` and `Download All Covers`. It runs the same metadata refresh logic over the visible collection without attempting normal audio downloads.

## Suspicious Detection

Spotify metadata already exposes `duration_ms`. The backend already has `GetAudioDuration` via FLAC parsing or ffprobe.

A track is suspicious when:

```text
abs(downloaded_audio_duration_seconds - spotify_duration_seconds) > 3
```

Only successfully downloaded audio files are checked. Existing files are checked only when they are being refreshed or explicitly included in an update/check operation. If duration probing fails, the track is not marked suspicious; it is recorded as a warning in the summary details.

Suspicious tracks should be visible in frontend state with a warning status distinct from failed/skipped/downloaded.

## Suspicious File Handling

When a newly downloaded file is marked suspicious, leave it in place until the user chooses YouTube redownload. This keeps the initial batch non-destructive.

When the user chooses `Redownload suspicious from YouTube`:

1. Create a `Suspicious` folder inside the collection output folder.
2. Move each suspicious original into that folder.
3. Download the YouTube replacement to the original expected output path, without a filename suffix.
4. Re-embed the full Spotify metadata set into the YouTube-downloaded replacement.
5. Re-run duration validation against Spotify duration.
6. Report each redownload as replaced, still suspicious, or failed.

If a moved suspicious filename already exists in the `Suspicious` folder, keep both files by adding a numeric suffix to the moved original.

## yt-dlp Management

The app manages yt-dlp similarly to FFmpeg, but as an independent runtime component.

- Store yt-dlp in the app-managed runtime directory, not in the repository.
- Before YouTube redownload, check whether the binary exists.
- Support a user-visible install/update progress path.
- Download the latest platform-specific yt-dlp release from GitHub when missing or stale.
- Keep version metadata locally so the app can decide whether to refresh.
- If GitHub/latest-version checks fail but an existing yt-dlp binary is present, allow using the existing binary and surface a warning.

The implementation should isolate this into a backend module, for example `backend/ytdlp.go`, instead of mixing yt-dlp logic into the main download hook.

## YouTube Search and Download

The first implementation uses yt-dlp search instead of a custom YouTube API key:

```text
ytsearch1:<artist> - <title> audio
```

The downloaded format should be converted or extracted to match the user's selected audio format where feasible:

- For MP3 output, prefer best audio extraction to MP3 when ffmpeg is available.
- For M4A/FLAC output, use yt-dlp plus ffmpeg conversion if necessary.
- If the selected target cannot be produced, fail that redownload item with a clear error.

After download, the same metadata embedding and duration validation pipeline runs.

## Batch Summary UI

Replace the final short toast for batch downloads with a dialog-style summary for playlist, album, and artist batch runs.

The summary shows counters:

- downloaded
- skipped existing
- metadata updated
- failed
- duplicate skipped
- suspicious
- YouTube replaced, when applicable
- YouTube failed, when applicable

The dialog includes scrollable or collapsible detail sections for:

- failed tracks with error message
- suspicious tracks with expected/actual duration and file path
- skipped duplicates with duplicate reason
- metadata update failures
- YouTube redownload results

If suspicious tracks exist, the dialog includes `Redownload suspicious from YouTube`. The action is disabled while another batch operation is running.

## UI Placement

In collection header actions:

- Keep `Download All` as the primary command.
- Keep `Download Selected` as secondary when tracks are selected.
- Add `Update All Metadata` as an icon-only outline button near `Download All Lyrics` and `Download All Covers`.
- Add the metadata refresh setting in Settings near other metadata/embed options.

Track rows can show a warning icon for suspicious items. Tooltip text should include the expected and actual duration.

## Data Flow

1. Frontend builds per-track output path info using existing filename/folder settings.
2. Frontend calls existing batch file existence checks.
3. For each existing item:
   - If `updateMetadataForExistingFiles` is disabled, count as skipped.
   - If enabled, call backend metadata refresh for the existing file and count as updated or failed.
4. For each missing item:
   - Download normally.
   - Embed metadata as today.
   - Probe duration.
   - Mark suspicious if the delta is greater than 3 seconds.
5. At the end, frontend stores a structured batch summary and opens the summary dialog.
6. If the user triggers YouTube redownload:
   - Frontend sends suspicious item descriptors to backend.
   - Backend moves originals, downloads via yt-dlp, embeds metadata, validates duration, and returns structured results.
   - Frontend updates statuses and reopens or updates the summary.

## Backend API Shape

Add backend-facing request/response types for:

- `UpdateTrackMetadata(filePath, track metadata, settings subset)`
- `UpdateTracksMetadata(batch request)`
- `RedownloadSuspiciousTracksFromYouTube(batch request)`
- `EnsureYtDlpInstalledOrUpdated`
- `CheckYtDlpInstalled`

The exact generated Wails names can follow existing app method naming.

## Error Handling

- Metadata refresh failures do not stop the whole batch; they are counted and listed.
- Duration probe failures become warnings unless the audio file is missing/unreadable.
- yt-dlp missing/update failure blocks YouTube redownload but does not invalidate the completed normal download batch.
- Moving a suspicious original must happen before writing the replacement. If the move fails, skip that item and report a failure.
- If YouTube replacement fails after the original was moved, leave the original in `Suspicious` and report the failure with both paths.

## Testing

Backend tests:

- duration comparison threshold marks only differences greater than 3 seconds
- metadata refresh builds the same metadata payload used by normal download
- suspicious move path creates collision-safe filenames
- yt-dlp release asset selection for Windows/macOS/Linux
- YouTube redownload result handling for success, failed download, and still-suspicious replacement

Frontend tests or focused unit coverage where available:

- batch summary counter aggregation
- suspicious status rendering
- settings persistence for `updateMetadataForExistingFiles`
- bulk metadata button wiring

Manual verification:

- batch download with all-new tracks
- batch download with existing files and metadata refresh enabled
- forced suspicious duration case
- YouTube redownload flow with a managed yt-dlp binary
- failure when yt-dlp cannot be installed

## Implementation Notes

The current `useDownload.ts` already contains substantial batch orchestration. The implementation should extract reusable summary aggregation and per-track result helpers where it reduces complexity, but avoid broad unrelated refactors.

The backend already has metadata embedding, cover fetching, lyrics embedding, genre lookup, ffprobe duration probing, FFmpeg installation, and path collision helpers. The new implementation should reuse those existing pieces instead of duplicating tagging logic.
