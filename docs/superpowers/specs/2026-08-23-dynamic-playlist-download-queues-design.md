# Dynamic Playlist Download Queues Design

## Context

SpotiDownloader currently downloads a batch through a fixed frontend pool. The pool receives an immutable item list, `Download All` clears the existing backend queue, and individual download buttons are disabled while a batch is active. The track list exposes one scalar `downloadingTrack`, so concurrent workers are not represented accurately. A track can therefore be actively downloading while its row shows no icon.

YouTube redownload currently runs as a separate sequential workflow. It does not share the normal worker limit, cannot be cancelled through the active-track registry, publishes yt-dlp output too close to the destination path, and discards the useful yt-dlp diagnostic output. The official yt-dlp executable also warns when no supported external JavaScript runtime is available; full YouTube support requires one.

## Goals

- Maintain an independent, dynamic, session-only queue for every playlist.
- Continue all queues in the background while the user navigates between views.
- Enforce one global limit of three active download workers across all queues.
- Allow new tracks and selected YouTube redownloads to be appended while work is active.
- Show accurate per-playlist aggregate progress and per-track latest-attempt state.
- Ensure every enqueued track reaches an explicit terminal or active state; never silently omit one.
- Make YouTube fallback reliable with a portable Deno runtime, cancellation, diagnostics, and transactional publication.
- Allow users to select which failed or suspicious tracks are redownloaded from YouTube.

## Non-goals

- Queues are not persisted across application restarts.
- The design does not add more than three concurrent media workers.
- It does not automatically retry arbitrary non-authentication failures.
- It does not redesign lyrics-only or cover-only batch workflows.

## Architecture

The frontend owns scheduling because it already owns playlist metadata, settings-derived paths, Spotify session-token refresh, summaries, and navigation state. The Go backend remains the executor for an individual media job and the authoritative source for that job's transfer phase, bytes, speed, warnings, timings, and terminal result.

A standalone queue manager is created once by the root `useDownload` hook and survives view changes. It exposes an immutable snapshot subscription for React, while its mutable scheduler state remains outside component render cycles. This keeps scheduling deterministic and makes the manager directly testable without rendering React.

The backend `DownloadItem` gains a queue identifier and job identifier. Existing progress polling returns these fields, allowing the frontend to join backend transfer state to the queue manager's job state. Batch entry points stop calling `ClearAllDownloads`; cleanup becomes an explicit user action or shutdown concern.

## Queue identity

Queue keys use stable source identifiers:

- Spotify playlist: `playlist:<spotify-playlist-id>`.
- Album: `album:<spotify-album-id>`.
- Artist/discography: `artist:<spotify-artist-id>`.
- A single download without a collection: `adhoc:single`.

The primary requirement is independent playlist queues. Album, artist, and ad-hoc keys generalize the same scheduler so existing entry points do not fall back to the old fixed-pool behavior.

Each queue stores its display name and an ordered list of jobs. Each job contains:

- a unique job ID;
- queue ID and source track index;
- Spotify track ID when available;
- job kind: `spotify-download` or `youtube-redownload`;
- track metadata, resolved output settings, and retry inputs;
- status: `queued`, `active`, `completed`, `skipped`, `failed`, `suspicious`, `cancelled`, or `unavailable`;
- backend item ID once execution starts;
- result diagnostics and final file path.

Jobs are attempts, not unique tracks. A later retry creates a new job and increases the queue total. The latest attempt for a track determines the icon shown in the track list, while older attempts remain visible in queue history for the current session.

## Scheduling and fairness

The manager enforces a single global active count of three. Every queue is FIFO. When a worker slot becomes available, the scheduler selects the next non-empty queue using round-robin rotation, claims one queued job synchronously, then advances the rotation cursor. A large playlist therefore cannot permanently monopolize all slots when another playlist receives work.

Enqueueing immediately triggers scheduling. Worker completion updates the job, publishes a new snapshot, releases the global slot, and triggers scheduling again. No polling or timer is used to claim work.

An individual click follows these rules:

- If the same track already has a `queued` or `active` attempt in that queue, no duplicate is added.
- If its latest attempt is terminal, a new forced attempt is appended.
- A forced Spotify attempt bypasses existing-file short-circuiting and replaces the destination through the existing safe publication path.

`Download All` and `Download Selected` add only tracks that have never been represented in the current queue session. They do not duplicate queued, active, or terminal attempts. Terminal tracks are retried through an individual click or the selective summary action.

A track without a Spotify ID becomes an immediate `unavailable` job with an explanatory reason. It contributes to the terminal count rather than disappearing during filtering.

## Progress model

Each playlist status bar is derived from that playlist queue snapshot and shows:

`completed / total · active · queued · failed`

The progress percentage is `terminal / total`. Terminal includes completed, skipped, failed, suspicious, cancelled, and unavailable attempts. Adding a job increases `total` immediately and may reduce the displayed percentage. The bar remains available for the duration of the application session so returning to a playlist restores its state.

The track list no longer uses a scalar `downloadingTrack` for media downloads. It derives the latest attempt for each track in the active queue:

- queued: clock;
- active: spinner plus backend phase/detail;
- completed: green check;
- skipped: yellow file check;
- suspicious: warning;
- failed: red cross;
- cancelled: cancelled indicator;
- unavailable: disabled/error indicator.

Individual download buttons remain enabled during unrelated work. Only a track whose latest attempt is queued or active is disabled.

## Existing-file and publication behavior

Normal batch jobs preserve the existing-file precheck and may finish as `skipped`. A user-initiated retry is marked `force` and must not be skipped solely because a previous output exists.

All replacements are staged beneath the per-run portable temp directory. Final publication uses the existing atomic rename/copy fallback. A completed destination is reported only after finalization and publication succeed.

## YouTube runtime management

The portable helper directory gains `deno.exe` beside `yt-dlp.exe`, `ffmpeg.exe`, and `ffprobe.exe`. Deno is installed lazily when the first YouTube job is executed.

The Deno manager:

- queries the official `denoland/deno` latest GitHub release;
- selects `deno-x86_64-pc-windows-msvc.zip` or `deno-aarch64-pc-windows-msvc.zip` on Windows, with equivalent supported assets on other platforms;
- downloads the matching `.sha256sum` asset and verifies the archive before extraction;
- validates the extracted executable with `deno --version`;
- atomically replaces the portable executable;
- records a 24-hour release-check marker in the portable bin directory;
- uses context-aware HTTP and removes partial files on failure or cancellation.

yt-dlp is invoked with an explicit `--js-runtimes deno:<absolute-path>` argument and the existing explicit FFmpeg location. Official yt-dlp executables already include their required EJS components; the external runtime supplies the JavaScript engine.

## YouTube redownload transaction

A YouTube redownload is a normal queue job and shares the global worker limit. It registers a cancellable backend context just like a Spotify job.

The transaction is:

1. Ensure current yt-dlp and a valid portable Deno runtime.
2. Search and download into a job-specific runtime temp directory.
3. Apply metadata and cover art, probe duration, and classify the result.
4. Prepare the final destination without modifying the existing suspicious file.
5. If the replacement is valid, move the old suspicious file to the suspicious backup directory when necessary and publish the replacement atomically.
6. If publication fails after moving the old file, restore the old file before returning failure.
7. Remove job temp files after all context-aware workers have exited.

The backend returns a structured stage, short message, and bounded diagnostic details. Full yt-dlp output is written to the debug logger; the summary shows the short reason with expandable details. Cancellation is represented as `cancelled`, not `failed`.

## Selective redownload UI

Failed and suspicious summary sections contain a checkbox per eligible track, plus `Select all` and `Clear`. All eligible tracks start selected to preserve the existing default behavior. The action button displays `Redownload selected via YouTube (N)` and is disabled when `N` is zero.

Only selected entries are converted to `youtube-redownload` jobs. Successful replacements leave the problem set; failed, still-suspicious, cancelled, and unselected entries remain available for another attempt.

## Stop and shutdown

Stop marks queued jobs in the selected queue as cancelled and calls the backend active-download cancellation method. Other playlist queues continue unless the UI action explicitly requests stopping all queues. Application shutdown cancels all active jobs, waits for their managed processes, then performs cache and runtime-temp cleanup.

The initial UI will label the playlist action `Stop this queue` to avoid implying that unrelated playlist queues are stopped. The existing global cancellation API remains available for shutdown.

## Error handling and observability

- Every enqueue attempt produces a visible job or a clear no-op reason.
- Scheduler worker rejections are converted into terminal job results exactly once.
- Backend queue mutation uses unique job IDs, never Spotify IDs, so repeated attempts cannot update the wrong entry.
- yt-dlp and Deno errors identify the failing stage: release check, helper download, search, media download, conversion, metadata, validation, or publication.
- Diagnostic output is bounded in Wails responses and unbounded output is kept only in the debug logger.
- Status-bar counts and summary counts are derived from job states rather than maintained as independent counters.

## Testing strategy

### Frontend unit tests

- Dynamic enqueue while workers are active.
- FIFO order within a queue and round-robin fairness across queues.
- A strict global maximum of three active workers.
- Duplicate suppression for queued/active jobs.
- Terminal retry creates a new forced job and increases total.
- `Download All` does not re-add represented tracks.
- Missing Spotify IDs become unavailable terminal jobs.
- Stop cancels queued jobs only in the selected playlist queue.
- Aggregate counts and percentage update when jobs are appended.
- Latest-attempt row status selects queued, all three active workers, and every terminal state.
- Selective YouTube action enqueues only checked entries.

### Backend tests

- Queue progress updates target unique job IDs and preserve queue IDs.
- Deno platform asset selection, checksum parsing, exact 24-hour TTL boundary, atomic replacement, cancellation, and temp cleanup using `httptest` fixtures.
- yt-dlp arguments include the explicit Deno and FFmpeg locations.
- yt-dlp diagnostic output survives failure in the structured result.
- YouTube cancellation kills the full helper process tree.
- Transactional publication retains or restores the original on every failure point.
- A valid replacement is finalized and published exactly once.

### Integration verification

- Go and frontend test suites, Go vet/build, frontend lint/build, Wails binding generation, and Windows packaging.
- A deterministic scheduler fixture demonstrating three-worker speedup without reordering results.
- A real yt-dlp search/download smoke test using the portable Deno and FFmpeg helpers.
- Manual navigation smoke test: start one playlist, switch to another, enqueue more work, return to the first, and verify independent progress with a global peak of three.

## Security and compatibility

- Helper downloads use HTTPS and allow only expected official GitHub asset hosts.
- Deno archives are verified against the matching release checksum before extraction.
- Archive extraction rejects traversal paths and extracts only the expected executable.
- Existing public Wails methods remain as compatibility wrappers where practical; the new queue-aware methods carry job and queue IDs explicitly.
- Settings and history remain in their existing locations; only portable helpers, cache, and temp artifacts live beside the executable.
