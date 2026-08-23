# Dynamic Playlist Download Queues Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace fixed playlist batches with independent session-only queues sharing three global workers, expose accurate per-track/per-playlist progress, and make selected YouTube retries reliable through portable Deno, cancellation, diagnostics, and transactional publication.

**Architecture:** A framework-free TypeScript scheduler owns queue ordering, fairness, and immutable snapshots for the lifetime of `useDownload`; Go remains the cancellable executor and authoritative transfer-progress source for one job. Spotify and YouTube attempts use the same scheduler and backend job identity, while queue-aware App methods preserve global shutdown cancellation and add cancellation scoped to one queue.

**Tech Stack:** Go 1.x, Wails v2, React 19, TypeScript, Vitest, yt-dlp executable, portable Deno executable, FFmpeg/FFprobe executables, GitHub Releases API.

**Spec:** `docs/superpowers/specs/2026-08-23-dynamic-playlist-download-queues-design.md`

## Global Constraints

- Keep queues in memory only; do not persist or restore them across application restarts.
- Enforce one global maximum of exactly three active media jobs across all queues.
- Schedule FIFO within each queue and round-robin across non-empty queues.
- Use queue IDs `playlist:<spotify-playlist-id>`, `album:<spotify-album-id>`, `artist:<spotify-artist-id>`, and `adhoc:single`.
- Do not silently filter a requested track without a Spotify ID; represent it as terminal `unavailable`.
- Repeated individual clicks may create a forced attempt only after the latest attempt is terminal; queued/active duplicates are no-ops.
- Repeated `Download All`/`Download Selected` enqueues only tracks never represented in that queue session.
- Preserve the existing 24-hour helper release-check TTL, including the exact stale boundary.
- Store Deno beside yt-dlp in `SpotiDownloader.runtime/bin`; keep settings and history in their existing locations.
- Download Deno only from official GitHub release hosts, verify the matching checksum, reject archive traversal, and replace the executable atomically.
- Invoke yt-dlp with explicit absolute Deno and FFmpeg paths and kill its full process tree on cancellation.
- Return bounded diagnostics through Wails; write complete yt-dlp output only to the backend debug log.
- Stage every replacement below the current portable run temp directory and preserve or restore the original file on every failure path.
- Preserve compatibility wrappers for existing public Wails/backend entry points where practical.
- Use TDD for every production change and do not modify the user-owned `.codebase-memory/` directory.

---

## File and Responsibility Map

| File | Responsibility |
|---|---|
| `frontend/src/lib/download-queue-manager.ts` | Framework-free global scheduler, per-queue FIFO state, round-robin claiming, dedupe/retry policy, cancellation, immutable snapshots. |
| `frontend/src/lib/download-queue-manager.test.ts` | Deterministic scheduler, concurrency, fairness, retry, cancellation, and aggregate tests. |
| `frontend/src/lib/download-jobs.ts` | Stable queue/track keys and typed Spotify/YouTube job payload builders. |
| `frontend/src/lib/download-jobs.test.ts` | Queue-key, missing-ID, represented-track, and force-attempt tests. |
| `frontend/src/hooks/useDownload.ts` | Own one scheduler instance, execute jobs, reduce results, build M3U8 files, expose queue-aware callbacks. |
| `frontend/src/hooks/useDownloadQueueData.ts` | Join backend progress by queue/job ID and expose queue-specific active details. |
| `frontend/src/types/api.ts` | Queue context, queue snapshot, force flag, YouTube stage/diagnostic response types. |
| `frontend/src/App.tsx` | Derive stable collection IDs from the active Spotify URL and pass queue context to collection views. |
| `frontend/src/components/PlaylistInfo.tsx` | Per-playlist status bar and `Stop this queue`. |
| `frontend/src/components/AlbumInfo.tsx`, `frontend/src/components/ArtistInfo.tsx` | Pass stable album/artist queue contexts through existing download controls. |
| `frontend/src/components/TrackList.tsx` | Latest-attempt icons/phases and per-track retry enablement. |
| `frontend/src/components/BatchDownloadSummaryDialog.tsx` | Per-result YouTube selection, Select all/Clear, selected-count action. |
| `frontend/src/lib/batch-summary.ts` | Stable summary-result IDs and selection helpers. |
| `backend/progress.go` | Queue ID on every backend item while preserving unique attempt/job ID mutations. |
| `backend/progress_test.go` | Queue isolation, aggregate, and repeated-attempt regression coverage. |
| `app.go`, `app_download_helpers.go` | Queue ID and force flag on Spotify download requests; queue-aware active context registration. |
| `app_queue_history.go` | Wails registration and cancellation for one queue plus global shutdown cancellation. |
| `app_download_helpers_test.go` | App queue registration, force propagation, and scoped cancellation tests. |
| `backend/track_pipeline.go`, `backend/track_pipeline_test.go` | Forced retry bypass of existing-file short circuit while retaining safe staged publication. |
| `backend/deno.go`, `backend/deno_test.go` | Portable Deno release selection, checksum verification, extraction, TTL, update, and cancellation. |
| `backend/ytdlp.go`, `backend/ytdlp_test.go` | Explicit Deno args, bounded structured diagnostics, full debug logging, context-aware helper startup. |
| `backend/track_metadata_update.go`, `backend/track_metadata_update_test.go` | Context-aware metadata finalization and transactional YouTube replacement/rollback. |
| `app_metadata_update.go` | Queue-aware, cancellable single YouTube-redownload Wails method; legacy bulk wrapper retained. |
| `README.md` | Portable Deno/yt-dlp runtime behavior and troubleshooting. |

---

### Task 1: Framework-Free Dynamic Queue Scheduler

**Files:**
- Create: `frontend/src/lib/download-queue-manager.ts`
- Create: `frontend/src/lib/download-queue-manager.test.ts`

**Interfaces:**
- Consumes: no application state; injected `execute`, `classifyResult`, and `cancelBackendQueue` functions.
- Produces:

```ts
export type DownloadJobKind = "spotify-download" | "youtube-redownload";
export type DownloadJobStatus =
  | "queued" | "active" | "completed" | "skipped" | "failed"
  | "suspicious" | "cancelled" | "unavailable";
export type EnqueuePolicy = "unseen-only" | "retry-terminal";

export interface DownloadJobCandidate<TPayload> {
  id: string;
  queueId: string;
  queueTitle: string;
  trackKey: string;
  sourceIndex: number;
  kind: DownloadJobKind;
  payload: TPayload;
  unavailableReason?: string;
}

export interface DownloadJob<TPayload, TResult> extends DownloadJobCandidate<TPayload> {
  status: DownloadJobStatus;
  force: boolean;
  backendItemId?: string;
  result?: TResult;
  error?: string;
  enqueuedAt: number;
  startedAt?: number;
  finishedAt?: number;
}

export interface QueueCounts {
  total: number;
  terminal: number;
  active: number;
  queued: number;
  completed: number;
  failed: number;
}

export interface DownloadQueueSnapshot<TPayload, TResult> {
  id: string;
  title: string;
  jobs: ReadonlyArray<DownloadJob<TPayload, TResult>>;
  counts: QueueCounts;
  progress: number;
}

export interface DownloadSchedulerSnapshot<TPayload, TResult> {
  queues: Readonly<Record<string, DownloadQueueSnapshot<TPayload, TResult>>>;
  activeCount: number;
  peakActive: number;
}

export interface EnqueueDecision {
  added: boolean;
  jobId?: string;
  reason?: "already-represented" | "already-in-flight";
}

export interface DynamicDownloadScheduler<TPayload, TResult> {
  enqueue(candidate: DownloadJobCandidate<TPayload>, policy: EnqueuePolicy): EnqueueDecision;
  setBackendItemId(jobId: string, backendItemId: string): void;
  cancelQueue(queueId: string): Promise<number>;
  getSnapshot(): DownloadSchedulerSnapshot<TPayload, TResult>;
  subscribe(listener: () => void): () => void;
  dispose(): Promise<void>;
}
```

- [ ] **Step 1: Write failing scheduler tests**

Create deterministic deferred promises and cover dynamic append, per-queue FIFO, round-robin order, global peak `3`, `unseen-only`, `retry-terminal`, unavailable jobs, and selected-queue cancellation:

```ts
it("round-robins dynamically appended queues with a global peak of three", async () => {
  const started: string[] = [];
  const gates = new Map<string, ReturnType<typeof deferred<Result>>>();
  const scheduler = createDynamicDownloadScheduler<JobPayload, Result>({
    concurrency: 3,
    execute: async (job) => {
      started.push(job.id);
      const gate = deferred<Result>();
      gates.set(job.id, gate);
      return gate.promise;
    },
    classifyResult: (result) => result.status,
    cancelBackendQueue: vi.fn(async () => 0),
    now: monotonicTestClock(),
  });

  enqueueMany(scheduler, "playlist:a", ["a1", "a2", "a3", "a4"]);
  enqueueMany(scheduler, "playlist:b", ["b1", "b2"]);
  expect(started).toEqual(["a1", "a2", "a3"]);
  gates.get("a1")!.resolve({ status: "completed" });
  await flushPromises();
  expect(started[3]).toBe("b1");
  expect(scheduler.getSnapshot().peakActive).toBe(3);
});
```

- [ ] **Step 2: Run the tests to verify RED**

Run: `rtk pnpm --dir frontend test -- download-queue-manager`

Expected: FAIL because `./download-queue-manager` does not exist.

- [ ] **Step 3: Implement the minimal scheduler**

Use a `Map<string, MutableQueue>`, a queue-order array, one rotation cursor, and synchronous `claimNext()` calls. Publish snapshots only after cloning jobs and freezing nested arrays/objects. On execution settlement, classify exactly once, decrement `activeCount`, advance scheduling, and never keep an independent count that can diverge from job statuses.

```ts
function isTerminal(status: DownloadJobStatus): boolean {
  return status === "completed" || status === "skipped" || status === "failed" ||
    status === "suspicious" || status === "cancelled" || status === "unavailable";
}

function nextRunnableQueue(): MutableQueue | undefined {
  for (let checked = 0; checked < queueOrder.length; checked++) {
    const index = (rotationCursor + checked) % queueOrder.length;
    const queue = queues.get(queueOrder[index]);
    if (queue?.jobs.some((job) => job.status === "queued")) {
      rotationCursor = (index + 1) % queueOrder.length;
      return queue;
    }
  }
  return undefined;
}
```

For `retry-terminal`, reject only when the latest matching attempt is queued/active; set the new attempt's `force` to `true`. For `unseen-only`, reject when any matching attempt exists. Convert `unavailableReason` to an immediate unavailable terminal job without calling `execute`.

- [ ] **Step 4: Run focused and full frontend tests**

Run: `rtk pnpm --dir frontend test -- download-queue-manager`

Expected: PASS with deterministic ordering and no timers.

Run: `rtk pnpm --dir frontend test`

Expected: all frontend tests PASS.

- [ ] **Step 5: Commit**

```powershell
rtk git add frontend/src/lib/download-queue-manager.ts frontend/src/lib/download-queue-manager.test.ts
rtk git commit -m "feat: add dynamic download queue scheduler"
```

---

### Task 2: Backend Queue Identity and Queue-Scoped Cancellation

**Files:**
- Modify: `backend/progress.go`
- Modify: `backend/progress_test.go`
- Modify: `app.go`
- Modify: `app_queue_history.go`
- Modify: `app_download_helpers.go`
- Modify: `app_download_helpers_test.go`

**Interfaces:**
- Consumes: unique frontend-generated `jobID` and stable `queueID`.
- Produces:

```go
func AddQueueItem(queueID, id, trackName, artistName, albumName, spotifyID string) error
func AddToQueue(id, trackName, artistName, albumName, spotifyID string) // compatibility: adhoc:single
func (a *App) AddDownloadJob(queueID, jobID, spotifyID, trackName, artistName, albumName string) (string, error)
func (a *App) CancelDownloadQueue(queueID string) int
func (a *App) CancelActiveDownloads() int
```

`backend.DownloadItem` gains `QueueID string \`json:"queue_id"\``. `DownloadRequest` gains `QueueID string \`json:"queue_id,omitempty"\``.

- [ ] **Step 1: Write failing backend/App tests**

```go
func TestQueueIDsAndAttemptsStayIsolated(t *testing.T) {
    ClearAllDownloads()
    t.Cleanup(ClearAllDownloads)
    if err := AddQueueItem("playlist:a", "job-a-1", "Track", "Artist", "Album", "spotify-1"); err != nil { t.Fatal(err) }
    if err := AddQueueItem("playlist:b", "job-b-1", "Track", "Artist", "Album", "spotify-1"); err != nil { t.Fatal(err) }
    StartDownloadItem("job-a-1")
    UpdateItemPhase("job-a-1", PhaseDownloading, "audio")
    queue := GetDownloadQueue()
    if queue.Queue[0].QueueID != "playlist:a" || queue.Queue[1].QueueID != "playlist:b" {
        t.Fatalf("queue IDs lost: %#v", queue.Queue)
    }
    if queue.Queue[1].Phase != PhaseQueued {
        t.Fatalf("second attempt mutated: %#v", queue.Queue[1])
    }
}
```

Add an App test that registers active jobs in `playlist:a` and `playlist:b`, calls `CancelDownloadQueue("playlist:a")`, asserts exactly the two A contexts are cancelled, and asserts B remains live. Also assert global `CancelActiveDownloads()` cancels the remaining context.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk go test ./backend . -run 'TestQueueIDsAndAttemptsStayIsolated|TestCancelDownloadQueueCancelsOnlySelectedQueue' -count=1`

Expected: compile FAIL because `AddQueueItem`, `QueueID`, and `CancelDownloadQueue` are missing.

- [ ] **Step 3: Implement queue identity and active-entry storage**

Replace the two parallel active maps with one value type keyed by unique job ID:

```go
type activeDownload struct {
    QueueID string
    Context context.Context
    Cancel  context.CancelFunc
}

type App struct {
    // existing fields...
    activeMu        sync.Mutex
    activeDownloads map[string]activeDownload
}
```

Change `beginTrack` to `beginTrack(itemID, queueID string)`. If `queueID` is empty, normalize it to `adhoc:single`. `CancelDownloadQueue` must snapshot/delete matching entries under the lock, unlock, then call cancels. `CancelActiveDownloads` must snapshot/delete all entries and invoke cancels after unlocking. Do not directly mutate terminal progress from either cancellation method; the owning App execution maps the context result exactly once.

`AddQueueItem` rejects empty or duplicate job IDs while holding `downloadQueueLock`, then appends `DownloadItem{ID: id, QueueID: normalizedQueueID, ...}`. Keep `AddToQueue` as a compatibility wrapper calling `AddQueueItem("adhoc:single", ...)`; its existing timestamp-based callers remain unique. Generate new frontend job IDs with `crypto.randomUUID()`.

- [ ] **Step 4: Run focused, full, and race-capable tests**

Run: `rtk go test ./backend . -run 'TestQueueIDsAndAttemptsStayIsolated|TestCancelDownloadQueueCancelsOnlySelectedQueue|TestCancelActiveDownloads' -count=20`

Expected: PASS with no deadlock or cross-queue cancellation.

Run: `rtk go test ./... -count=1`

Expected: all Go tests PASS.

Run: `rtk go test -race ./backend . -run 'TestQueueIDsAndAttemptsStayIsolated|TestCancelDownloadQueueCancelsOnlySelectedQueue' -count=10`

Expected: PASS when CGO and a compiler are available; otherwise record the exact environment failure and continue with high-count non-race tests.

- [ ] **Step 5: Commit**

```powershell
rtk git add backend/progress.go backend/progress_test.go app.go app_queue_history.go app_download_helpers.go app_download_helpers_test.go
rtk git commit -m "feat: associate download progress with queues"
```

---

### Task 3: Queue Job Builders and Forced Spotify Attempts

**Files:**
- Create: `frontend/src/lib/download-jobs.ts`
- Create: `frontend/src/lib/download-jobs.test.ts`
- Modify: `frontend/src/types/api.ts`
- Modify: `backend/track_pipeline.go`
- Modify: `backend/track_pipeline_test.go`
- Modify: `app.go`
- Modify: `app_download_helpers.go`
- Modify: `app_download_helpers_test.go`

**Interfaces:**
- Consumes: scheduler `DownloadJobCandidate`, collection type/Spotify URL, `TrackMetadata`, resolved settings.
- Produces:

```ts
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
export type MediaJobPayload = SpotifyDownloadPayload;
export type MediaJobResult = DownloadResponse;
export function makeQueueId(kind: DownloadQueueKind, sourceId?: string): string;
export function makeTrackKey(track: TrackMetadata, sourceIndex: number): string;
export function buildSpotifyCandidate(args: BuildSpotifyCandidateArgs):
  DownloadJobCandidate<SpotifyDownloadPayload>;
```

Go request types gain `Force bool \`json:"force,omitempty"\`` and `QueueID string`; `backend.TrackPipelineRequest` gains `Force bool`.

- [ ] **Step 1: Write failing job-builder and force tests**

```ts
it("turns a missing Spotify ID into a stable unavailable candidate", () => {
  const candidate = buildSpotifyCandidate({
    queue: { id: "playlist:p1", title: "Mix", kind: "playlist" },
    track: { name: "Local", artists: "Artist" } as TrackMetadata,
    sourceIndex: 4,
    settings,
  });
  expect(candidate.trackKey).toBe("source:4");
  expect(candidate.unavailableReason).toBe("Spotify track ID is missing");
});
```

```go
func TestTrackPipelineForceBypassesExistingOutput(t *testing.T) {
    req, deps := existingOutputPipelineFixture(t)
    req.Force = true
    var resolverCalls atomic.Int32
    deps.resolveTarget = func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error) {
        resolverCalls.Add(1)
        return "source", ".mp3", nil
    }
    _, _ = runTrackPipelineWithDeps(t, req, deps)
    if resolverCalls.Load() != 1 { t.Fatalf("resolver calls = %d", resolverCalls.Load()) }
}
```

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk pnpm --dir frontend test -- download-jobs`

Expected: FAIL because the job-builder module is missing.

Run: `rtk go test ./backend . -run 'TestTrackPipelineForceBypassesExistingOutput|TestDownloadTrackPropagatesQueueAndForce' -count=1`

Expected: compile FAIL because `Force` and `QueueID` are missing.

- [ ] **Step 3: Implement stable keys and force propagation**

Parse the active Spotify URL with a strict helper accepting `open.spotify.com/{playlist|album|artist}/{id}` and `spotify:{kind}:{id}`. `makeQueueId("adhoc")` returns exactly `adhoc:single`; collection kinds require a non-empty source ID and throw a descriptive error if parsing failed.

Guard only the existing-output scan in `RunTrackPipeline`:

```go
if !req.Force {
    for _, ext := range PossibleAudioExtensions(req.AudioFormat) {
        candidate := outputBasePath + ext
        if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Size() > 0 {
            result.File = candidate
            result.AlreadyExists = true
            return result, nil
        }
    }
}
```

Propagate `DownloadRequest.Force` and `DownloadRequest.QueueID` through `buildTrackPipelineRequest` and `beginTrack(itemID, queueID)`. A forced attempt still stages under `RunTempDir/<job-id>` and uses the existing `publishTrackPipelineFile`; do not write directly to the destination. Extend the force regression to seed old destination bytes, complete the fake pipeline, and assert the destination contains the new staged bytes while no staged file remains.

- [ ] **Step 4: Run focused and full tests**

Run: `rtk pnpm --dir frontend test -- download-jobs download-queue-manager`

Expected: PASS.

Run: `rtk go test ./backend . -run 'TestTrackPipelineExistingOutput|TestTrackPipelineForce|TestDownloadTrackPropagatesQueueAndForce' -count=10`

Expected: PASS; normal requests skip existing files and forced requests execute/publish.

Run: `rtk go test ./... -count=1`

Expected: all Go tests PASS.

- [ ] **Step 5: Commit**

```powershell
rtk git add frontend/src/lib/download-jobs.ts frontend/src/lib/download-jobs.test.ts frontend/src/types/api.ts backend/track_pipeline.go backend/track_pipeline_test.go app.go app_download_helpers.go app_download_helpers_test.go
rtk git commit -m "feat: build queue jobs and force track retries"
```

---

### Task 4: Integrate the Scheduler into `useDownload`

**Files:**
- Create: `frontend/src/hooks/useDownloadQueues.ts`
- Create: `frontend/src/hooks/useDownloadQueues.test.ts`
- Modify: `frontend/src/hooks/useDownload.ts`
- Modify: `frontend/src/lib/batch-download.ts`
- Modify: `frontend/src/lib/batch-download.test.ts`
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/types/api.ts`

**Interfaces:**
- Consumes: scheduler from Task 1, Spotify payloads from Task 3, Wails `AddDownloadJob`, `DownloadTrack`, and `CancelDownloadQueue`.
- Produces:

```ts
export interface UseDownloadQueuesResult {
  snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>;
  enqueueAll(queue: DownloadQueueContext, tracks: TrackMetadata[], args: CollectionDownloadArgs): Promise<EnqueueDecision[]>;
  enqueueSelected(queue: DownloadQueueContext, tracks: TrackMetadata[], selectedIds: string[], args: CollectionDownloadArgs): Promise<EnqueueDecision[]>;
  retryTrack(queue: DownloadQueueContext, track: TrackMetadata, args: TrackDownloadArgs): Promise<EnqueueDecision>;
  stopQueue(queueId: string): Promise<number>;
}
```

- [ ] **Step 1: Write failing orchestration tests with injected Wails functions**

Test that a second enqueue while three jobs are blocked increases queue total immediately; `Download All` does not duplicate represented terminal tracks; an individual retry creates a forced job; every candidate including a missing-ID candidate appears; and cancelling queue A never cancels queue B.

```ts
it("keeps a playlist queue live while more tracks are appended", async () => {
  const harness = createDownloadQueueHarness({ executeSpotify: blockedExecutor });
  await harness.enqueueAll(playlistA, firstThree, args);
  expect(harness.snapshot().queues[playlistA.id].counts.active).toBe(3);
  await harness.retryTrack(playlistA, fourthTrack, args);
  expect(harness.snapshot().queues[playlistA.id].counts.total).toBe(4);
  expect(harness.snapshot().queues[playlistA.id].counts.queued).toBe(1);
});
```

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk pnpm --dir frontend test -- useDownloadQueues batch-download`

Expected: FAIL because `useDownloadQueues` and queue-aware Wails adapters are missing.

- [ ] **Step 3: Implement one scheduler instance and Spotify executor**

Create the scheduler once with `useRef`; subscribe through `useSyncExternalStore`. The Spotify executor must:

1. call `AddDownloadJob(queue.id, job.id, track.spotify_id ?? "", ...)`;
2. store the returned backend item ID on the job through a scheduler `setBackendItemId(job.id, id)` method added with its test in Task 1's module;
3. call the existing unauthorized retry helper around `DownloadTrack`;
4. pass `queue_id`, `item_id`, and `force` in `DownloadRequest`;
5. map resolved/rejected/cancelled responses to exactly one terminal scheduler status.

Delete media-batch calls to `ClearAllDownloads` and stop using `runBoundedPool` for Spotify media jobs. Keep `runBoundedPool` for unrelated fixed utilities only.

When a queue changes from non-idle to idle, derive its summary from terminal jobs. For playlist M3U8 output, keep the latest successful/skipped path for each source index, sort by source index, and regenerate the playlist after each idle transition; dynamic appends therefore update rather than corrupt the ordered playlist.

- [ ] **Step 4: Run focused tests, then the frontend suite**

Run: `rtk pnpm --dir frontend test -- useDownloadQueues download-queue-manager download-jobs batch-download`

Expected: PASS, including unauthorized retry and stop-during-refresh cases.

Run: `rtk pnpm --dir frontend test`

Expected: all frontend tests PASS.

Run: `rtk pnpm --dir frontend run build`

Expected: TypeScript and Vite build PASS.

- [ ] **Step 5: Commit**

```powershell
rtk git add frontend/src/hooks/useDownloadQueues.ts frontend/src/hooks/useDownloadQueues.test.ts frontend/src/hooks/useDownload.ts frontend/src/lib/batch-download.ts frontend/src/lib/batch-download.test.ts frontend/src/lib/api.ts frontend/src/types/api.ts frontend/src/lib/download-queue-manager.ts frontend/src/lib/download-queue-manager.test.ts
rtk git commit -m "feat: enqueue Spotify downloads dynamically"
```

---

### Task 5: Per-Collection Status and Latest-Attempt Track UI

**Files:**
- Create: `frontend/src/lib/download-queue-selectors.ts`
- Create: `frontend/src/lib/download-queue-selectors.test.ts`
- Modify: `frontend/src/hooks/useDownloadQueueData.ts`
- Modify: `frontend/src/hooks/useDownloadProgress.ts`
- Modify: `frontend/src/components/TrackList.tsx`
- Modify: `frontend/src/components/PlaylistInfo.tsx`
- Modify: `frontend/src/components/AlbumInfo.tsx`
- Modify: `frontend/src/components/ArtistInfo.tsx`
- Modify: `frontend/src/components/DownloadProgress.tsx`
- Modify: `frontend/src/components/DownloadProgressToast.tsx`
- Modify: `frontend/src/components/DownloadQueue.tsx`
- Modify: `frontend/src/App.tsx`

**Interfaces:**
- Consumes: immutable scheduler snapshot plus backend `DownloadItem.queue_id`/`id` polling.
- Produces:

```ts
export interface TrackAttemptView {
  jobId: string;
  status: DownloadJobStatus;
  phase?: string;
  detail?: string;
  downloadedBytes?: number;
  totalBytes?: number;
  speedMbps?: number;
  warning?: string;
  error?: string;
}
export function selectQueueStatus(snapshot: DownloadSchedulerSnapshot<MediaJobPayload, MediaJobResult>, queueId: string): QueueStatusView;
export function selectLatestTrackAttempt(queue: DownloadQueueSnapshot<MediaJobPayload, MediaJobResult>, trackKey: string): TrackAttemptView | undefined;
```

- [ ] **Step 1: Write failing selector tests**

Cover all three concurrent active rows, queued, completed, skipped, suspicious, failed, cancelled, unavailable, latest-attempt-wins, backend phase join by job ID, and dynamic aggregate percentage:

```ts
expect(selectQueueStatus(snapshotAfterAppend, "playlist:p1")).toMatchObject({
  label: "2 / 5 · 3 active · 0 queued · 0 failed",
  progress: 40,
});
```

- [ ] **Step 2: Run selector tests to verify RED**

Run: `rtk pnpm --dir frontend test -- download-queue-selectors`

Expected: FAIL because the selector module is missing.

- [ ] **Step 3: Implement selectors and replace scalar UI state**

Derive terminal/total from queue jobs. Join backend items through `Map<jobID, DownloadItem>` and ignore backend items from other queues. Remove media use of scalar `downloadingTrack`; keep separate lyrics-only and cover-only indicators.

Pass queue contexts from `App.tsx` by parsing the retained `spotifyUrl`. Add a collection-local status bar to playlist/album/artist surfaces and label its action `Stop this queue`. Keep `Download All` enabled during unrelated work. Disable an individual row only when its latest attempt is `queued` or `active`.

Render the agreed icons and active phase/detail. Display bytes/percent only when `total_bytes > 0`; never substitute aggregate speed into a row whose item speed is zero.

- [ ] **Step 4: Run tests, lint, and build**

Run: `rtk pnpm --dir frontend test -- download-queue-selectors download-timings`

Expected: PASS.

Run: `rtk pnpm --dir frontend run lint`

Expected: zero errors; record only pre-existing warnings.

Run: `rtk pnpm --dir frontend run build`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
rtk git add frontend/src/lib/download-queue-selectors.ts frontend/src/lib/download-queue-selectors.test.ts frontend/src/hooks/useDownloadQueueData.ts frontend/src/hooks/useDownloadProgress.ts frontend/src/components/TrackList.tsx frontend/src/components/PlaylistInfo.tsx frontend/src/components/AlbumInfo.tsx frontend/src/components/ArtistInfo.tsx frontend/src/components/DownloadProgress.tsx frontend/src/components/DownloadProgressToast.tsx frontend/src/components/DownloadQueue.tsx frontend/src/App.tsx
rtk git commit -m "feat: show per-collection queue progress"
```

---

### Task 6: Selective YouTube Retry Selection

**Files:**
- Modify: `frontend/src/lib/batch-summary.ts`
- Create: `frontend/src/lib/batch-summary.test.ts`
- Modify: `frontend/src/components/BatchDownloadSummaryDialog.tsx`
- Modify: `frontend/src/hooks/useDownload.ts`

**Interfaces:**
- Consumes: eligible failed/suspicious `BatchTrackResult` rows.
- Produces:

```ts
export function youtubeEligibleResultIds(summary: BatchDownloadSummary): string[];
export function normalizeYoutubeSelection(summary: BatchDownloadSummary, selected: ReadonlySet<string>): Set<string>;
```

`BatchDownloadSummaryDialogProps` gains:

```ts
selectedYoutubeResultIds: ReadonlySet<string>;
onToggleYoutubeResult(id: string): void;
onSelectAllYoutube(): void;
onClearYoutube(): void;
onRedownloadSelectedFromYouTube(ids: string[]): void;
```

- [ ] **Step 1: Write failing selection tests**

```ts
it("keeps only eligible selected rows and defaults all eligible rows on open", () => {
  const summary = summaryWithStatuses(["failed", "suspicious", "downloaded"]);
  expect(youtubeEligibleResultIds(summary)).toEqual(["failed-id", "suspicious-id"]);
  expect([...normalizeYoutubeSelection(summary, new Set())]).toEqual(["failed-id", "suspicious-id"]);
});
```

Add a reducer test proving only checked IDs become candidate requests and unchecked rows remain in the summary.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk pnpm --dir frontend test -- batch-summary`

Expected: FAIL because the selection helpers are missing.

- [ ] **Step 3: Implement controlled selection UI**

Give every result a stable attempt/result ID. On summary open, initialize selection to all eligible IDs. Render a semantic checkbox on each eligible failed/suspicious row, plus `Select all` and `Clear` buttons. Render `Redownload selected via YouTube (N)` and disable it at zero. The callback receives only the checked IDs and must not remove unchecked results.

- [ ] **Step 4: Run tests, lint, and build**

Run: `rtk pnpm --dir frontend test -- batch-summary download-queue-selectors`

Expected: PASS.

Run: `rtk pnpm --dir frontend run lint`

Expected: zero errors.

Run: `rtk pnpm --dir frontend run build`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
rtk git add frontend/src/lib/batch-summary.ts frontend/src/lib/batch-summary.test.ts frontend/src/components/BatchDownloadSummaryDialog.tsx frontend/src/hooks/useDownload.ts
rtk git commit -m "feat: select tracks for YouTube retry"
```

---

### Task 7: Portable Deno Runtime Manager

**Files:**
- Create: `backend/deno.go`
- Create: `backend/deno_test.go`
- Modify: `backend/runtime_paths.go`
- Modify: `backend/runtime_paths_test.go`

**Interfaces:**
- Consumes: `CurrentRuntimePaths().BinDir`, managed commands, context-aware HTTP.
- Produces:

```go
type DenoStatus struct {
    Installed bool   `json:"installed"`
    Path      string `json:"path,omitempty"`
    Version   string `json:"version,omitempty"`
    Latest    string `json:"latest,omitempty"`
    Updated   bool   `json:"updated,omitempty"`
    Warning   string `json:"warning,omitempty"`
    Error     string `json:"error,omitempty"`
}

func SelectDenoAsset(goos, goarch string, assets []GitHubReleaseAsset) (archive GitHubReleaseAsset, checksum GitHubReleaseAsset, ok bool)
func EnsureDenoInstalledOrUpdatedContext(ctx context.Context) (DenoStatus, error)
func EnsureDenoInstalledOrUpdated() (DenoStatus, error)
func GetDenoPath() (string, error)
```

- [ ] **Step 1: Write failing Deno tests**

Use `httptest.Server` release JSON and in-memory ZIP/checksum fixtures. Test Windows amd64/arm64, Linux amd64/arm64, Darwin amd64/arm64 asset names; exact 24-hour marker boundary; matching checksum parsing; checksum mismatch; traversal entry; missing/empty executable; cancellation during download; atomic replacement preserving an installed binary on invalid update; partial temp cleanup.

```go
func TestShouldSkipDenoReleaseCheckExact24HourBoundary(t *testing.T) {
    now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
    marker := writeDenoMarker(t, now.Add(-24*time.Hour))
    if shouldSkipDenoReleaseCheck(marker, now, true) {
        t.Fatal("exactly 24 hours must trigger a release check")
    }
}
```

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk go test ./backend -run 'Test.*Deno' -count=1`

Expected: compile FAIL because Deno symbols are missing.

- [ ] **Step 3: Implement selection, download, validation, and atomic install**

Use release API `https://api.github.com/repos/denoland/deno/releases/latest`. Select these exact archives and their `<archive>.sha256sum` assets:

```go
var denoAssets = map[string]string{
    "windows/amd64": "deno-x86_64-pc-windows-msvc.zip",
    "windows/arm64": "deno-aarch64-pc-windows-msvc.zip",
    "linux/amd64":   "deno-x86_64-unknown-linux-gnu.zip",
    "linux/arm64":   "deno-aarch64-unknown-linux-gnu.zip",
    "darwin/amd64":  "deno-x86_64-apple-darwin.zip",
    "darwin/arm64":  "deno-aarch64-apple-darwin.zip",
}
```

Allow HTTPS redirects only among `github.com`, `api.github.com`, `objects.githubusercontent.com`, and `release-assets.githubusercontent.com`. Download archive/checksum below `RuntimePaths.RunTempDir`, parse the checksum line matching the exact archive filename, verify SHA-256 before opening ZIP, reject any entry except the expected root-level `deno`/`deno.exe`, and validate the staged regular non-empty executable with `newManagedCommand(ctx, stagedPath, "--version")`.

Install with same-directory staged rename: rename current binary to `.bak`, rename staged executable to final, validate final, restore `.bak` on failure, then remove backup. Write the release-check marker only after a successful check/install.

- [ ] **Step 4: Run focused, repeated, and full tests**

Run: `rtk go test ./backend -run 'Test.*Deno' -count=10`

Expected: all Deno tests PASS with no leaked temp artifacts.

Run: `rtk go test ./... -count=1`

Expected: all Go tests PASS.

Run: `rtk go vet ./...`

Expected: exit 0.

- [ ] **Step 5: Commit**

```powershell
rtk git add backend/deno.go backend/deno_test.go backend/runtime_paths.go backend/runtime_paths_test.go
rtk git commit -m "feat: manage portable Deno runtime"
```

---

### Task 8: yt-dlp Deno Arguments and Structured Diagnostics

**Files:**
- Modify: `backend/ytdlp.go`
- Modify: `backend/ytdlp_test.go`
- Modify: `app_metadata_update.go`

**Interfaces:**
- Consumes: `EnsureDenoInstalledOrUpdatedContext`, `GetFFmpegPath`, `newManagedCommand`.
- Produces:

```go
type YtDlpStage string

const (
    YtDlpStageReleaseCheck YtDlpStage = "release_check"
    YtDlpStageRuntime      YtDlpStage = "runtime"
    YtDlpStageSearch       YtDlpStage = "search"
    YtDlpStageDownload     YtDlpStage = "media_download"
    YtDlpStageConversion   YtDlpStage = "conversion"
)

type YtDlpDownloadResult struct {
    Success     bool       `json:"success"`
    File        string     `json:"file,omitempty"`
    Stage       YtDlpStage `json:"stage,omitempty"`
    Error       string     `json:"error,omitempty"`
    Diagnostics string     `json:"diagnostics,omitempty"`
    Output      string     `json:"output,omitempty"` // bounded compatibility alias
}

func BuildYtDlpArgs(req YtDlpDownloadRequest, denoPath, ffmpegPath string) []string
```

- [ ] **Step 1: Write failing yt-dlp tests**

Test exact args include `--js-runtimes deno:<absolute-path>` and `--ffmpeg-location <absolute-dir>`; Deno installation runs before yt-dlp; cancellation returns `context.Canceled` semantics and kills the helper tree; a 50KB fake stderr/output is fully delivered to an injected debug logger but response diagnostics/output are capped to 4096 bytes and identify the failing stage.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk go test ./backend -run 'Test(BuildYtDlpArgs|DownloadWithYtDlp.*Deno|YtDlpDiagnostics)' -count=1`

Expected: compile/test FAIL because the args builder, stage, and diagnostics are missing.

- [ ] **Step 3: Implement explicit runtime args and bounded diagnostics**

Call `EnsureYtDlpInstalledOrUpdatedContext(ctx)` and `EnsureDenoInstalledOrUpdatedContext(ctx)` before building args. Require absolute regular executable paths. Append:

```go
args = append(args,
    "--js-runtimes", "deno:"+denoPath,
    "--ffmpeg-location", filepath.Dir(ffmpegPath),
)
```

Run one managed command and capture combined output. Log the full output through an injectable `ytDlpDebugLogf` defaulting to `log.Printf`. Derive stage from the last known operation and produce a whitespace-normalized last-4096-byte diagnostic string. Do not return unbounded output through Wails.

- [ ] **Step 4: Run focused and full tests**

Run: `rtk go test ./backend -run 'Test(BuildYtDlpArgs|DownloadWithYtDlp|YtDlpDiagnostics|ManagedCommandCancellation)' -count=20`

Expected: PASS with bounded responses and no surviving descendant marker.

Run: `rtk go test ./... -count=1`

Expected: all Go tests PASS.

- [ ] **Step 5: Commit**

```powershell
rtk git add backend/ytdlp.go backend/ytdlp_test.go app_metadata_update.go
rtk git commit -m "feat: run yt-dlp with portable Deno diagnostics"
```

---

### Task 9: Transactional YouTube Replacement Backend

**Files:**
- Modify: `backend/track_metadata_update.go`
- Modify: `backend/track_metadata_update_test.go`
- Modify: `app_metadata_update.go`
- Modify: `app.go`
- Modify: `app_queue_history.go`
- Create: `app_metadata_update_test.go`

**Interfaces:**
- Consumes: queue/job IDs, context-aware yt-dlp result, `FinalizeMedia`, duration probe, portable run temp, atomic publisher.
- Produces:

```go
type SuspiciousRedownloadRequest struct {
    QueueID            string                     `json:"queue_id,omitempty"`
    JobID              string                     `json:"job_id,omitempty"`
    OriginalFilePath   string                     `json:"original_file_path"`
    ReplacementFilePath string                    `json:"replacement_file_path,omitempty"`
    CollectionDir      string                     `json:"collection_dir,omitempty"`
    AudioFormat        string                     `json:"audio_format,omitempty"`
    Metadata           TrackMetadataUpdateRequest `json:"metadata"`
}

type SuspiciousRedownloadResult struct {
    // existing fields...
    Stage       string `json:"stage,omitempty"`
    Diagnostics string `json:"diagnostics,omitempty"`
}

func RedownloadSuspiciousTrackFromYouTubeContext(ctx context.Context, req SuspiciousRedownloadRequest) SuspiciousRedownloadResult
func RedownloadSuspiciousTrackFromYouTube(req SuspiciousRedownloadRequest) SuspiciousRedownloadResult
func (a *App) RedownloadTrackFromYouTube(req backend.SuspiciousRedownloadRequest) (backend.SuspiciousRedownloadResult, error)
```

- [ ] **Step 1: Write failure-point-first transaction tests**

Create injected dependency seams for yt-dlp, metadata preparation/finalization, duration probe, move original, publish replacement, and restore original. Use a table covering: yt-dlp failure, metadata failure, suspicious validation, move-original failure, publication failure before move, publication failure after move, cancellation, and success.

```go
func TestYouTubeReplacementRestoresOriginalAfterPublishFailure(t *testing.T) {
    fixture := newReplacementFixture(t)
    fixture.deps.publish = func(context.Context, string, string) error {
        return errors.New("disk full")
    }
    got := runSuspiciousRedownloadWithDeps(context.Background(), fixture.request, fixture.deps)
    if got.Success || got.Stage != "publication" { t.Fatalf("result = %#v", got) }
    assertFileContents(t, fixture.originalPath, "original")
    assertPathAbsent(t, fixture.backupPath)
}
```

Add App tests proving queue-scoped cancellation reaches this context and maps to `cancelled`, not `failed`.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk go test ./backend . -run 'TestYouTubeReplacement|TestRedownloadTrackFromYouTube' -count=1`

Expected: compile/test FAIL because the context method, transaction deps, and Wails method are missing.

- [ ] **Step 3: Implement the transaction without touching the original early**

Create `RunTempDir/<job-id>/youtube`, download there, resolve the actual produced media path, build metadata with context-aware tagging/lyrics/cover helpers, call `FinalizeMedia(ctx, stagedMedia, metadata, coverPath)` once, probe duration once, and classify before moving the original.

Publication order:

```go
if originalNeedsBackup {
    movedOriginal, err = moveOriginal(originalPath, suspiciousDir)
    if err != nil { return fail("publication", err) }
}
if err = publish(ctx, stagedMedia, replacementPath); err != nil {
    if movedOriginal != "" {
        if restoreErr := restoreOriginal(movedOriginal, originalPath); restoreErr != nil {
            return fail("rollback", errors.Join(err, restoreErr))
        }
    }
    return fail("publication", err)
}
```

Defer cancellation, wait for context-aware helper work, then remove only the job temp directory. Register the App operation with `beginTrack(req.JobID, req.QueueID)`. Keep the old bulk method as a Background compatibility wrapper that calls the single function sequentially; the frontend will no longer use it.

- [ ] **Step 4: Run focused, repeated, and full tests**

Run: `rtk go test ./backend . -run 'TestYouTubeReplacement|TestRedownloadTrackFromYouTube' -count=20`

Expected: every failure point preserves/restores the original and success publishes exactly once.

Run: `rtk go test ./... -count=1`

Expected: all Go tests PASS.

Run: `rtk go vet ./...`

Expected: exit 0.

- [ ] **Step 5: Commit**

```powershell
rtk git add backend/track_metadata_update.go backend/track_metadata_update_test.go app_metadata_update.go app_metadata_update_test.go app.go app_queue_history.go
rtk git commit -m "fix: publish YouTube replacements transactionally"
```

---

### Task 10: Run Selected YouTube Attempts Through the Global Scheduler

**Files:**
- Modify: `frontend/src/lib/download-jobs.ts`
- Modify: `frontend/src/lib/download-jobs.test.ts`
- Modify: `frontend/src/hooks/useDownloadQueues.ts`
- Modify: `frontend/src/hooks/useDownloadQueues.test.ts`
- Modify: `frontend/src/hooks/useDownload.ts`
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/types/api.ts`
- Modify: `frontend/src/components/BatchDownloadSummaryDialog.tsx`

**Interfaces:**
- Consumes: selected result IDs from Task 6 and Wails `RedownloadTrackFromYouTube` from Task 9.
- Produces:

```ts
export interface YouTubeRedownloadPayload {
  request: SuspiciousRedownloadRequest;
  sourceResultId: string;
}
export type MediaJobPayload = SpotifyDownloadPayload | YouTubeRedownloadPayload;
export type MediaJobResult = DownloadResponse | SuspiciousRedownloadResult;
```

`UseDownloadQueuesResult` gains:

```ts
enqueueYouTubeRetries(queue: DownloadQueueContext, selectedResultIds: string[], summary: BatchDownloadSummary): Promise<EnqueueDecision[]>;
```

- [ ] **Step 1: Write failing shared-scheduler tests**

Start two blocked Spotify jobs in queue A, enqueue two selected YouTube jobs in queue B, and assert the global active count never exceeds three. Assert an unselected failed row produces no job, cancellation reports `cancelled`, a successful replacement removes only its source problem row, and a failed/still-suspicious result remains eligible.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `rtk pnpm --dir frontend test -- useDownloadQueues download-jobs batch-summary`

Expected: FAIL because the YouTube payload/executor path is not wired.

- [ ] **Step 3: Extend the executor with an exhaustive job-kind switch**

```ts
switch (job.kind) {
  case "spotify-download":
    return executeSpotifyJob(job, signal);
  case "youtube-redownload":
    return executeYouTubeJob(job, signal);
}
```

The YouTube executor must pass `queue_id` and `job_id`, call `RedownloadTrackFromYouTube`, map `status === "cancelled"` to scheduler `cancelled`, preserve `stage`/`diagnostics`, and never invoke the old bulk Wails method. `Stop this queue` uses the same scheduler cancellation path for both job kinds.

Update the summary incrementally from scheduler terminal results. Keep failed, still-suspicious, cancelled, and unselected rows; remove only successfully replaced rows.

- [ ] **Step 4: Run frontend and Go integration suites**

Run: `rtk pnpm --dir frontend test -- useDownloadQueues download-queue-manager download-jobs batch-summary`

Expected: PASS with a peak active count of three.

Run: `rtk pnpm --dir frontend test`

Expected: all frontend tests PASS.

Run: `rtk go test ./... -count=1`

Expected: all Go tests PASS.

Run: `rtk pnpm --dir frontend run build`

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
rtk git add frontend/src/lib/download-jobs.ts frontend/src/lib/download-jobs.test.ts frontend/src/hooks/useDownloadQueues.ts frontend/src/hooks/useDownloadQueues.test.ts frontend/src/hooks/useDownload.ts frontend/src/lib/api.ts frontend/src/types/api.ts frontend/src/components/BatchDownloadSummaryDialog.tsx
rtk git commit -m "feat: schedule selected YouTube retries"
```

---

### Task 11: Bindings, Documentation, Packaging, and End-to-End Verification

**Files:**
- Modify: `README.md`
- Modify if generated files are tracked: `frontend/wailsjs/go/main/App.d.ts`
- Modify if generated files are tracked: `frontend/wailsjs/go/main/App.js`
- Modify if generated files are tracked: `frontend/wailsjs/go/models.ts`
- Modify only if packaging verification exposes a missing runtime directory rule: `build.ps1`

**Interfaces:**
- Consumes: all previous tasks.
- Produces: verified Wails bindings, documented portable runtime, packaged executable, and smoke evidence.

- [ ] **Step 1: Add a binding/API shape test before regeneration**

Add a focused TypeScript test importing the Wails methods used by `frontend/src/lib/api.ts` and asserting the wrappers accept queue/job IDs. The test should fail until Wails bindings are regenerated after the Go methods exist.

- [ ] **Step 2: Run the binding test to verify RED, then regenerate**

Run: `rtk pnpm --dir frontend test -- api`

Expected before regeneration: TypeScript compile FAIL for missing `AddDownloadJob`, `CancelDownloadQueue`, or `RedownloadTrackFromYouTube` exports.

Run: `rtk wails generate module`

Expected: exit 0 and bindings expose the three new methods and new response fields.

- [ ] **Step 3: Document runtime and troubleshooting**

Add a README section stating:

```markdown
### YouTube fallback runtime

The first YouTube retry lazily installs official `yt-dlp` and Deno executables
under `SpotiDownloader.runtime/bin` beside the application. Release checks are
cached for 24 hours. Downloads use the packaged FFmpeg path and an explicit
`--js-runtimes deno:<path>` argument. If a retry fails, open the queue result to
see its stage and bounded diagnostics; the complete yt-dlp output remains in the
application debug log.
```

Do not add Deno to the source tree or user profile; packaging creates the portable bin directory and runtime installation remains lazy.

- [ ] **Step 4: Run the complete verification matrix**

Run:

```powershell
rtk gofmt -w app.go app_download_helpers.go app_queue_history.go app_metadata_update.go backend/progress.go backend/track_pipeline.go backend/deno.go backend/ytdlp.go backend/track_metadata_update.go
rtk go test ./... -count=1
rtk go vet ./...
rtk go build ./...
rtk powershell -NoProfile -Command "$env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'; go build ./..."
rtk pnpm --dir frontend test
rtk pnpm --dir frontend run lint
rtk pnpm --dir frontend run build
rtk wails generate module
rtk git diff --check
```

Expected: all tests/builds PASS; lint has zero errors; only documented baseline warnings may remain.

Attempt:

```powershell
rtk powershell -NoProfile -Command "$env:CGO_ENABLED='1'; go test -race ./backend . -run 'Test.*(Queue|Deno|YtDlp|YouTubeReplacement)' -count=10"
```

Expected: PASS when GCC/Clang is installed; otherwise record the exact missing-compiler output and run the same regex without `-race` at `-count=100`.

- [ ] **Step 5: Package and run smoke checks**

Run: `rtk powershell -NoProfile -ExecutionPolicy Bypass -File .\build.ps1 -NoHelperRebuild`

Expected: `build/bin/SpotiDownloader.exe` and `build/bin/SpotiDownloader.runtime/bin` exist. Launch the built executable from `build/bin`, trigger one YouTube retry, and verify `deno.exe` appears beside yt-dlp, `deno --version` succeeds, the yt-dlp command uses Deno and portable FFmpeg, and no helper/cache/temp files appear under the user profile.

Manual scheduler smoke:

1. Start playlist A with at least six tracks.
2. Navigate to playlist B and start at least three tracks.
3. Confirm global active count never exceeds three and B receives a worker before A drains.
4. Return to A and append an individual terminal retry; confirm total grows and the row becomes queued/active.
5. Stop A; confirm B continues.
6. Select a subset of failed/suspicious rows for YouTube retry; confirm only checked rows enter the queue and originals survive any forced failure.

Real helper smoke uses a disposable destination under the portable run temp directory and a public-domain/authorized query. Remove only that disposable output after verification.

- [ ] **Step 6: Commit**

```powershell
rtk git add README.md build.ps1
rtk git add -u -- frontend/wailsjs/go
rtk git commit -m "docs: verify dynamic queue runtime"
```

If generated bindings and `build.ps1` are unchanged/ignored, stage only `README.md` and commit it with the same message.

---

## Final Acceptance Checklist

- [ ] Returning to a playlist restores its in-memory queue snapshot until application exit.
- [ ] At most three Spotify/YouTube media jobs are active globally.
- [ ] Round-robin scheduling prevents a large queue from monopolizing subsequent slots.
- [ ] Dynamic append grows queue total immediately and does not reset prior attempts.
- [ ] Missing Spotify IDs appear as unavailable instead of disappearing.
- [ ] Every visible row uses its latest attempt, including all concurrent active rows.
- [ ] `Stop this queue` cancels only the selected queue; shutdown cancels all.
- [ ] Individual terminal retry is forced and bypasses existing-file skip through safe staged publication.
- [ ] Selective YouTube UI enqueues exactly the checked results.
- [ ] Deno is checksum-verified, portable, lazily installed, and checked at the exact 24-hour boundary.
- [ ] yt-dlp receives explicit Deno/FFmpeg paths, bounded diagnostics, and tree-aware cancellation.
- [ ] YouTube replacement never destroys the original on download, validation, publication, rollback, or cancellation failure.
- [ ] Go/frontend tests, builds, lint, Wails generation, packaging, and manual navigation smoke pass.
