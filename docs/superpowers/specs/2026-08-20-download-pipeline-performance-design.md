# Download Pipeline Performance Design

**Date:** 2026-08-20  
**Status:** Approved in discussion; awaiting final document review

## Context

Downloads are currently processed one track at a time. Within each track, several independent operations are also serialized: audio download, cover retrieval, metadata enrichment, lyrics handling, conversion, duration probing, and tag writing. This produces visible idle periods before and after the media transfer and makes a batch take approximately the sum of every track's full latency.

The current implementation also relies on process-wide progress state, so simply starting several existing download calls concurrently would mix progress and cancellation state between tracks. Metadata lookups are repeated across runs, and helper binaries and transient data are stored outside the application directory.

This design introduces bounded concurrency, overlaps independent per-track work, adds a persistent metadata cache, and makes all runtime-owned files portable beside the application executable.

## Goals

- Process up to three complete track pipelines concurrently.
- Preserve the source order in results, playlists, and summary output.
- Overlap independent work inside each track without corrupting tags or progress.
- Remove the fixed post-download wait for lyrics.
- Avoid repeated metadata requests across tracks and application restarts.
- Give every active queue item independent phase, byte, speed, warning, error, and timing state.
- Make Stop cancel active network and conversion work, not only prevent the next track.
- Store helper executables, caches, and temporary files beside the current application executable.
- Keep a standalone, replaceable `yt-dlp.exe` so it can be updated without rebuilding SpotiDownloader.

## Non-goals

- Moving user settings, download history, or the user's music output directory.
- Adding a separate performance or diagnostics screen.
- Automatically deleting legacy files from the user profile.
- Making concurrency unlimited or user-configurable in this iteration.
- Treating lyrics as required for a successful track download.

## Confirmed bottlenecks

The existing code has the following structural delays:

1. The frontend batch loop awaits each `downloadTrack` call before starting the next track.
2. Cover retrieval begins only after audio download and conversion-related work.
3. Tag enrichment contains multiple remote lookups, including sequential Chosic requests.
4. Lyrics embedding can impose a fixed wait after the rest of the track is ready.
5. Duration can be probed more than once for the same track.
6. MP3 metadata and lyrics can be saved in separate passes, rewriting the output file.
7. Progress and current-item state are global rather than associated with a queue item.

## Architecture

The frontend owns a bounded pool of three full-track workers. Each worker invokes one backend track pipeline. The backend owns per-track phases, cancellable operations, temporary files, metadata caching, media finalization, and progress reporting.

The concurrency boundary is intentionally the complete track pipeline rather than only the media transfer. This allows network metadata latency for one track to overlap audio transfer or conversion for another while keeping each track's finalization internally coordinated.

Results are stored by their original input index. Completion order may differ, but all user-visible ordered outputs use the source order.

## Portable runtime layout

All application-managed helper binaries, caches, and temporary working files live in a sibling directory beside the running executable:

```text
SpotiDownloader.exe
SpotiDownloader.runtime/
  bin/
    yt-dlp.exe
    ffmpeg.exe
    ffprobe.exe
    get_token.exe
  cache/
    metadata-cache.json
  temp/
    <run-id>/
      <track-id>/
```

A single `RuntimePaths` component derives the executable directory from `os.Executable`, resolves it to an absolute path, and supplies every runtime path. Runtime paths must never depend on the process working directory.

At startup, the application creates the required directories and performs a small write/delete probe. If the executable directory is not writable, startup fails with a clear message explaining that the portable application must be moved to a writable directory. There is no fallback to `%USERPROFILE%`, `%APPDATA%`, the system temp directory, or another hidden location.

Existing legacy helper files in the user profile are neither used nor deleted automatically. The portable runtime downloads or provisions its own helpers. This avoids silently mutating old installations; legacy cleanup can be documented separately.

The existing 24-hour `yt-dlp` freshness check is retained, but its executable and update marker move under `SpotiDownloader.runtime/bin`. An update replaces only the standalone helper and does not require rebuilding the application.

Each application run uses a unique directory below `temp`. Track-specific temporary files remain isolated even when track titles collide. Successful, failed, and cancelled pipelines clean their own temporary directory. Startup may remove stale run directories that are not owned by the current process.

## Batch worker pool

The frontend batch scheduler has a fixed concurrency of three:

- It starts at most three track pipelines at once.
- When one pipeline settles, the next not-yet-started track may begin.
- It records each result at the track's original index.
- It updates aggregate counts using settled results, independent of completion order.
- Playlist output and batch summaries are generated in original source order.
- A single-track download uses the same pipeline without creating unnecessary additional workers.

When Stop is requested, the scheduler stops assigning new tracks immediately and asks the backend to cancel all active track contexts. Tracks already finalized remain completed. Not-yet-started and interrupted tracks are not counted as completed.

## Per-track pipeline

Once the source link and basic track identity are known, the backend starts these independent branches concurrently:

1. Audio acquisition through `yt-dlp`.
2. Cover retrieval.
3. Spotify/ISRC/genre/tag metadata enrichment.
4. Lyrics retrieval and preparation.

Audio conversion begins as soon as the downloaded media is available. Cover and metadata branches continue in parallel with download and conversion.

After conversion, the application probes duration exactly once. This placement intentionally resolves an earlier design ambiguity: duration must be known before time-synchronised lyrics are validated, but it must not be probed again during final tag writing. The single duration result is reused by lyrics validation and any other consumer.

Finalization waits for the parallel metadata and cover branches to settle within their request timeouts, then writes ordinary tags, the available cover, and any lyrics that met their deadline in one format-specific save/finalization pass. Basic track identity is required; remote enrichment and cover retrieval are optional and produce warnings when unavailable. Every HTTP branch uses the track context and a finite request timeout. A file is marked complete only after finalization succeeds.

The expected phase sequence is:

```text
queued
  -> preparing
  -> downloading
  -> converting
  -> finalizing
  -> completed
```

Parallel branch detail is reported as subordinate status text, for example `downloading · cover ready · metadata pending`, without forcing the main phase to move backwards.

## Lyrics deadline

Lyrics have a total deadline of five seconds measured from the start of the track pipeline. This is not an additional five-second wait after audio finishes.

Finalization may wait only for the unused remainder of that total budget. For example, if conversion is ready two seconds after pipeline start, lyrics may use at most the remaining three seconds; if conversion finishes after the five-second point, there is no lyrics wait.

- If lyrics are ready and valid before finalization, they are included in the single tag-writing pass.
- If the deadline expires first, the track finalizes without lyrics.
- A missed deadline is a warning in the Debug Logger and queue item, not a track failure.
- Late lyrics results are discarded and must not reopen or rewrite a completed media file.
- Cancellation terminates the lyrics request immediately.

## Persistent metadata cache

The cache is stored at `SpotiDownloader.runtime/cache/metadata-cache.json` and is shared by all workers in the process.

Cache categories and time-to-live values are:

| Category | Example key | TTL |
| --- | --- | ---: |
| Spotify track metadata | `spotify:<track-id>` | 7 days |
| ISRC/MusicBrainz enrichment | `isrc:<track-id>` | 7 days |
| Chosic track-to-artist mapping | `chosic-track:<track-id>` | 7 days |
| Artist genre data | `genre:<artist-id>` | 30 days |

Only successful, non-empty results are cached. Errors, timeouts, invalid responses, and empty enrichment results are not cached.

Concurrent requests for the same cache key use singleflight semantics: one caller performs the remote lookup and the others await the same result. This applies before the result is written to persistent storage.

The cache has a maximum of 5,000 entries. Expired entries are removed during normal maintenance; when the size limit is exceeded, the least recently used or oldest eligible entries are evicted. A synchronized in-memory store serves readers. A single writer coalesces dirty updates, persists them through a temporary file followed by an atomic replace, and flushes outstanding changes during orderly shutdown. A crash may lose only updates not yet flushed; it cannot leave a partially written primary cache.

If the cache cannot be parsed, the application renames it to a timestamped corruption backup and starts with an empty cache. Cache failure must not prevent downloading unless the portable runtime directory itself is unwritable.

## Progress and thread safety

Process-wide mutable download progress is replaced with per-item state keyed by queue item ID. Every progress writer receives the item ID explicitly.

Each queue item can expose:

- main phase;
- optional subordinate branch status;
- downloaded and total bytes when known;
- instantaneous or smoothed transfer speed;
- warning and error text;
- start, phase transition, and completion timestamps;
- phase durations for diagnostics.

The top-level progress display derives an aggregate from all queue items and shows the number of active tracks. It does not pretend that three concurrent files form one byte stream when their totals are unknown.

Shared maps and counters are synchronized. There is no single global `currentItemID`, and starting one track must not reset another track's progress.

## Cancellation and external processes

Each active track has its own `context.Context` and cancellation function registered by queue item ID. Network requests are created with that context. FFmpeg, FFprobe, `yt-dlp`, and other helpers are launched through context-aware process execution. On Windows, cancellation terminates the helper process tree, using a process group or Job Object where direct-process cancellation is insufficient.

Cancellation behavior:

- prevent new worker assignments;
- cancel all active track contexts;
- terminate active HTTP requests and child processes;
- remove partial output and temporary files owned by interrupted tracks;
- retain files that had already completed finalization;
- emit one terminal cancelled state per interrupted queue item.

Cleanup is idempotent so error handling and cancellation may both attempt it safely.

## Error handling

A failure in one worker does not stop unrelated tracks. Each track reports its own terminal result, and the batch continues unless the user requests Stop or a process-wide prerequisite fails.

Required audio acquisition, conversion, basic track identity, and final media save failures mark the track failed. Optional enrichment, cover, and lyrics failures produce warnings and allow finalization with the available data. None of these optional branches may cause an unbounded wait.

Portable runtime initialization and missing/unusable mandatory helper binaries are process-wide failures and must be surfaced clearly before a batch is started where possible.

## Observability

The existing queue shows the current phase and progress for each item. The existing Debug Logger records per-track timing data, including:

- preparation;
- audio acquisition;
- conversion;
- cover retrieval;
- metadata enrichment;
- lyrics outcome and deadline;
- duration probe;
- finalization;
- total pipeline time;
- cache hit/miss and singleflight sharing where useful.

At the end of a batch, the logger records total wall-clock time, track counts by outcome, peak active worker count, and summarized phase timings. No new diagnostics screen is added.

Timing collection must be lightweight and must not serialize the workers.

## Data and API changes

Backend download methods accept or create a queue item ID and a track-scoped context. Internal download, progress, HTTP, conversion, probing, and finalization helpers propagate that context and item ID.

Queue item data gains phase, detailed status, warning, timing, and cancellation-aware terminal state fields. Existing frontend consumers should remain compatible through additive fields where practical.

The backend returns a structured per-track result containing the final path, outcome, warnings, and timing summary. The frontend worker pool reduces these results into the existing batch summary and playlist output.

## Testing strategy

### Portable runtime paths

- Derive all paths from a mocked executable path, not the working directory.
- Create the exact `bin`, `cache`, and `temp` layout.
- Reject an unwritable executable directory with the intended user-facing error.
- Verify helper, cache, and temporary paths never fall back to a user-profile directory.

### Cache

- Validate each TTL category at just-before and just-after expiry.
- Never cache errors or empty results.
- Deduplicate simultaneous identical lookups with one underlying call.
- Enforce the 5,000-entry bound and expired-entry cleanup.
- Persist through atomic replacement.
- Back up and recover from a corrupt cache.

### Worker pool and queue

- Never exceed three simultaneous pipelines.
- Start a replacement task when a worker becomes free.
- Preserve source ordering despite out-of-order completion.
- Stop prevents queued work from starting and cancels active work.
- One worker failure does not abort unrelated tracks.

### Pipeline

- Audio, cover, metadata, and lyrics branches overlap.
- Lyrics use a five-second total deadline and add no post-audio fixed delay.
- A late lyrics result cannot rewrite a completed file.
- Duration is probed once and reused.
- Tags, cover, and ready lyrics are committed in one finalization pass.
- A track is completed only after finalization.
- Success, failure, and cancellation all clean track temporary files.

### Progress and cancellation

- Concurrent items maintain independent bytes, speed, phase, warnings, and errors.
- Starting one item does not reset another.
- Active HTTP requests and child processes observe cancellation.
- Completed output survives a later batch Stop.

### Integration and regression

- Use local HTTP test servers and controllable fake helper processes to exercise overlap, timeouts, failures, and cancellation deterministically.
- Run all Go tests, frontend tests, Go vet, frontend production build, and application build.
- Capture instrumented before/after timings on the same representative batch. External network variance prevents a hard percentage gate, but the result must demonstrate a peak of three active pipelines and overlapping phase intervals rather than serial execution.

## Rollout

1. Introduce and test `RuntimePaths`; redirect helpers, cache, and temporary files.
2. Add the persistent cache and singleflight without changing batch concurrency.
3. Refactor progress and cancellation to be item-scoped.
4. Refactor the backend into the concurrent per-track pipeline and one-pass finalization.
5. Enable the frontend worker pool with a fixed concurrency of three.
6. Add timing logs, run the full regression suite, and compare representative batch timings.

The ordering keeps concurrency disabled until shared state, cancellation, and temporary file isolation are safe.

## Acceptance criteria

- At most three complete track pipelines run concurrently, and source order is preserved in outputs.
- All helper binaries, metadata caches, and temporary working files are below `SpotiDownloader.runtime` beside the executable.
- No runtime-owned helper, cache, or temp path falls back to the user profile.
- `yt-dlp.exe` remains independently replaceable and observes the existing 24-hour freshness policy.
- Independent audio, cover, metadata, and lyrics work overlaps within each track.
- Lyrics never add a fixed wait after the audio/conversion path is ready.
- Duration is probed once per track.
- Ordinary tags, cover, and available lyrics are saved in one finalization pass per output format.
- Every queue item has independent progress and terminal state.
- Stop prevents new work, cancels active work, and leaves completed tracks intact.
- Cache TTL, corruption recovery, atomic persistence, size limit, and singleflight behavior are covered by tests.
- Debug logging exposes per-phase and aggregate batch timings.
- Go tests, frontend tests, vet, frontend build, and application build pass.
