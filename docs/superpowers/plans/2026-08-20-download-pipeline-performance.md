# Download Pipeline Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a portable, cancellable download pipeline that processes three tracks concurrently, overlaps per-track media and metadata work, caches successful metadata across restarts, and exposes independent progress and timings.

**Architecture:** The Go backend owns portable runtime paths, helper processes, metadata caching, per-item state, cancellation, and a structured per-track pipeline. The React frontend owns a fixed three-worker batch scheduler and reduces indexed results into the existing ordered playlist and summary flows. Existing settings, history, and music output locations remain unchanged.

**Tech Stack:** Go 1.26.4, Wails v2.11, React 19, TypeScript 5.9, Vite 7, pnpm 10, Vitest, `golang.org/x/sync/singleflight`, FFmpeg/FFprobe, standalone yt-dlp.

**Spec:** `docs/superpowers/specs/2026-08-20-download-pipeline-performance-design.md`

## Global Constraints

- Full-track worker concurrency is fixed at exactly 3 for batches and 1 for a single-track request.
- Lyrics have one total budget of 5 seconds from backend pipeline start; they never receive a fresh wait after conversion.
- Spotify metadata, ISRC/MusicBrainz, and Chosic track mappings live for 7 days; artist genres live for 30 days.
- Cache only successful, non-empty values; cap the cache at 5,000 entries.
- Keep the standalone yt-dlp release check TTL at exactly 24 hours.
- Store runtime-owned helpers, cache files, and working files only below `SpotiDownloader.runtime` beside `SpotiDownloader.exe`; do not fall back to the user profile, PATH, or system temp directory.
- Bound the optional cover branch to 10 seconds and the optional metadata-enrichment branch to 10 seconds per track; timeout produces a warning, not a failed audio download.
- Keep settings, history, and user-selected music output in their current locations.
- Preserve input order in M3U8 files, result arrays, and batch summaries even when completion order differs.
- A queue item reaches `completed` only after media finalization succeeds.
- Preserve the existing uncommitted 24-hour yt-dlp changes in `backend/ytdlp.go` and `backend/ytdlp_test.go`; Task 2 incorporates them rather than reverting them.
- Do not commit `.codebase-memory` database/WAL files with feature commits; refresh the persisted graph after implementation.

---

## File and responsibility map

### New backend files

- `backend/runtime_paths.go` — resolves and prepares portable `bin/cache/temp` directories and owns the process runtime-path registry.
- `backend/runtime_paths_test.go` — path derivation, write-probe failure, run directory, and stale cleanup tests.
- `backend/metadata_cache.go` — bounded JSON cache, TTL, LRU eviction, atomic persistence, corruption recovery, and singleflight lookup.
- `backend/metadata_cache_test.go` — deterministic clock and concurrency tests for the cache.
- `backend/atomic_replace_windows.go`, `backend/atomic_replace_unix.go` — platform-correct atomic cache replacement.
- `backend/process_windows.go` — context-aware Windows process-tree termination.
- `backend/process_unix.go` — context-aware Unix process-group termination.
- `backend/process_test.go` — cancellation tests for managed child processes.
- `backend/track_pipeline.go` — per-track request/result types, timing recorder, dependency seam, branch coordination, cleanup, and finalization sequencing.
- `backend/track_pipeline_test.go` — deterministic branch overlap, lyrics deadline, failure isolation, duration count, finalization count, and cleanup tests.
- `backend/progress_test.go` — concurrent queue/progress and aggregate-state tests.

### New frontend files

- `frontend/src/lib/download-pool.ts` — reusable fixed-width indexed promise pool.
- `frontend/src/lib/download-pool.test.ts` — concurrency, ordering, stop, and failure-isolation tests.
- `frontend/src/lib/batch-download.ts`, `frontend/src/lib/batch-download.test.ts` — source-indexed work items and ordered output reduction.
- `frontend/src/lib/download-timings.ts`, `frontend/src/lib/download-timings.test.ts` — stable phase, aggregate, and timing formatting.
- `frontend/src/lib/token-manager.test.ts` — concurrent token-refresh deduplication tests.

### Existing files with focused changes

- `app.go`, `app_download_helpers.go`, `app_queue_history.go` — application lifecycle, track-scoped contexts, cancellation binding, structured response, and removal of the old lyrics/duration pass.
- `backend/ffmpeg.go`, `backend/ytdlp.go`, `backend/token_fetcher*.go`, `build.ps1` — portable helper paths and packaging.
- `backend/history.go`, `backend/config.go` — explicitly retain the legacy history location after helper paths move.
- `backend/app_update.go`, `backend/spotidownloader.go`, `backend/metadata.go` — portable temp paths and context-aware media work.
- `backend/spotify_metadata.go`, `backend/musicbrainz.go`, `backend/songlink.go`, `backend/chosic_genre.go` — cached metadata lookups.
- `backend/progress.go` — item-scoped phases, bytes, speed, warnings, timings, cancellation state, and aggregate progress.
- `frontend/package.json`, `frontend/pnpm-lock.yaml` — Vitest runner.
- `frontend/src/hooks/useDownload.ts`, `frontend/src/lib/token-manager.ts`, `frontend/src/lib/batch-summary.ts` — worker-pool orchestration, shared refresh promise, indexed results, and batch timing reduction.
- `frontend/src/hooks/useDownloadProgress.ts`, `frontend/src/hooks/useDownloadQueueData.ts`, `frontend/src/components/DownloadQueue.tsx`, `frontend/src/components/DownloadProgress.tsx`, `frontend/src/components/DownloadProgressToast.tsx`, `frontend/src/components/BatchDownloadSummaryDialog.tsx` — aggregate and per-item UI.
- `README.md` — portable layout and writable-directory troubleshooting.

---

### Task 1: Portable runtime path foundation

**Files:**
- Create: `backend/runtime_paths.go`
- Create: `backend/runtime_paths_test.go`
- Modify: `app.go:17-33`

**Interfaces:**
- Produces: `RuntimePaths`, `ResolveRuntimePaths(executablePath, runID string) (RuntimePaths, error)`, `(*RuntimePaths).Prepare() error`, `(*RuntimePaths).CleanupRun() error`, `InitRuntimePaths() (RuntimePaths, error)`, and `CurrentRuntimePaths() (RuntimePaths, error)`.
- Produces paths: `ExecutableDir`, `RootDir`, `BinDir`, `CacheDir`, `TempDir`, `RunTempDir`, and `MetadataCacheFile`.
- Consumes: only `os.Executable`, `filepath`, process ID/time for the production run ID, and the filesystem.

- [ ] **Step 1: Write failing path-layout and writability tests**

```go
func TestResolveRuntimePathsUsesExecutableDirectory(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "portable", "SpotiDownloader.exe")
	paths, err := ResolveRuntimePaths(exe, "run-42")
	if err != nil { t.Fatal(err) }
	wantRoot := filepath.Join(filepath.Dir(exe), "SpotiDownloader.runtime")
	if paths.RootDir != wantRoot { t.Fatalf("RootDir = %q, want %q", paths.RootDir, wantRoot) }
	if paths.BinDir != filepath.Join(wantRoot, "bin") { t.Fatal(paths.BinDir) }
	if paths.MetadataCacheFile != filepath.Join(wantRoot, "cache", "metadata-cache.json") { t.Fatal(paths.MetadataCacheFile) }
	if paths.RunTempDir != filepath.Join(wantRoot, "temp", "run-42") { t.Fatal(paths.RunTempDir) }
}

func TestPrepareRuntimePathsReturnsPortableWriteError(t *testing.T) {
	paths, _ := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "run")
	if err := os.WriteFile(paths.RootDir, []byte("blocks-directory"), 0600); err != nil { t.Fatal(err) }
	err := paths.Prepare()
	if err == nil || !strings.Contains(err.Error(), "portable runtime directory is not writable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests and confirm the missing API failure**

Run: `go test ./backend -run 'Test(Resolve|Prepare)RuntimePaths' -count=1`

Expected: FAIL because `RuntimePaths` and its constructors do not exist.

- [ ] **Step 3: Implement the path value and process registry**

```go
type RuntimePaths struct {
	ExecutableDir    string
	RootDir          string
	BinDir           string
	CacheDir         string
	TempDir          string
	RunTempDir       string
	MetadataCacheFile string
}

func ResolveRuntimePaths(executablePath, runID string) (RuntimePaths, error) {
	absExe, err := filepath.Abs(executablePath)
	if err != nil { return RuntimePaths{}, err }
	root := filepath.Join(filepath.Dir(absExe), "SpotiDownloader.runtime")
	return RuntimePaths{
		ExecutableDir: filepath.Dir(absExe), RootDir: root,
		BinDir: filepath.Join(root, "bin"), CacheDir: filepath.Join(root, "cache"),
		TempDir: filepath.Join(root, "temp"), RunTempDir: filepath.Join(root, "temp", runID),
		MetadataCacheFile: filepath.Join(root, "cache", "metadata-cache.json"),
	}, nil
}
```

`Prepare` must create `bin`, `cache`, and the current run directory, then create/close/delete `.write-probe` under `RootDir`. Wrap every failure with the exact phrase `portable runtime directory is not writable` and the resolved root path. Store the initialized value behind a `sync.RWMutex`; `CurrentRuntimePaths` returns an explicit initialization error rather than deriving a fallback path.

- [ ] **Step 4: Initialize paths in `NewApp` and surface startup failure in the Wails UI**

```go
type App struct {
	ctx context.Context
	runtimePaths   backend.RuntimePaths
	runtimeInitErr error
}

func NewApp() *App {
	paths, err := backend.InitRuntimePaths()
	return &App{runtimePaths: paths, runtimeInitErr: err}
}
```

In `startup`, if `runtimeInitErr != nil`, show one Wails error dialog with title `SpotiDownloader cannot start`, include the resolved portable-directory error, call `runtime.Quit(ctx)`, and return before history initialization.

- [ ] **Step 5: Pass path tests and existing root tests**

Run: `go test ./backend ./... -run 'Test(Resolve|Prepare)RuntimePaths|TestNormalizeDownloadRequest' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit the runtime path foundation**

```powershell
git add backend/runtime_paths.go backend/runtime_paths_test.go app.go
git commit -m "feat: add portable runtime paths"
```

---

### Task 2: Move helpers and temporary files into the portable runtime

**Files:**
- Modify: `backend/ffmpeg.go:74-128,159-222,332-451`
- Modify: `backend/ffmpeg_release_test.go`
- Modify: `backend/ytdlp.go:16-221,294-348`
- Modify: `backend/ytdlp_test.go`
- Modify: `backend/token_fetcher.go:11-114`
- Modify: `backend/token_fetcher_windows.go`
- Modify: `backend/token_fetcher_unix.go`
- Modify: `backend/config.go`
- Modify: `backend/history.go:35-63`
- Create: `backend/history_path_test.go`
- Modify: `backend/app_update.go:312-398`
- Modify: `backend/spotidownloader.go:908-941`
- Modify: `backend/metadata.go:652-717`
- Modify: `build.ps1:53-70,95-130,170-185`

**Interfaces:**
- Consumes: `CurrentRuntimePaths()` from Task 1.
- Produces: `GetUserDataDir() (string, error)` for history only; helper getters return paths strictly below `RuntimePaths.BinDir`.
- Produces: `CreateRunTemp(pattern string) (*os.File, error)` and `CreateUpdateTempPath(name string) (string, error)` in `runtime_paths.go`.

- [ ] **Step 1: Write failing helper/history separation tests**

```go
func TestHelperPathsUsePortableBinButHistoryRemainsInUserData(t *testing.T) {
	paths, _ := ResolveRuntimePaths(filepath.Join(t.TempDir(), "SpotiDownloader.exe"), "test")
	if err := paths.Prepare(); err != nil { t.Fatal(err) }
	installRuntimePathsForTest(t, paths)
	ffmpeg, _ := GetFFmpegPath()
	ytdlp, _ := GetYtDlpPath()
	if filepath.Dir(ffmpeg) != paths.BinDir { t.Fatalf("ffmpeg path: %s", ffmpeg) }
	if filepath.Dir(ytdlp) != paths.BinDir { t.Fatalf("yt-dlp path: %s", ytdlp) }
	dataDir, err := GetUserDataDir()
	if err != nil { t.Fatal(err) }
	if strings.HasPrefix(dataDir, paths.RootDir) { t.Fatalf("history moved into runtime: %s", dataDir) }
}
```

Add assertions that `yt-dlp.update-check` is under `BinDir`, that all `CreateRunTemp` files are under `RunTempDir`, that stale run directories are removed without removing the current run, and that helper getters do not consult `exec.LookPath`.

- [ ] **Step 2: Run focused tests and observe the old profile paths**

Run: `go test ./backend -run 'TestHelperPathsUsePortableBin|TestShouldSkipYtDlpReleaseCheck|TestRecordYtDlpReleaseCheck' -count=1`

Expected: the new portable path test FAILS; the already-written 24-hour TTL tests PASS.

- [ ] **Step 3: Redirect FFmpeg, FFprobe, yt-dlp, and their download staging**

Replace `GetFFmpegDir` and `GetYtDlpDir` implementations with `CurrentRuntimePaths().BinDir`. `GetFFmpegPath`, `GetFFprobePath`, and `GetYtDlpPath` return only the portable binary path; remove PATH lookup. Change verified helper downloads from `os.CreateTemp("")` to `CreateRunTemp`.

Keep these existing constants and behavior intact:

```go
const (
	ytDlpUpdateCheckFilename = "yt-dlp.update-check"
	ytDlpUpdateCheckTTL      = 24 * time.Hour
)
```

- [ ] **Step 4: Separate history and externalize `get_token`**

Add `GetUserDataDir` with the current `%USERPROFILE%/.spotidownloader` behavior and make `InitHistoryDB` use it. Replace the embedded token contract with a platform filename contract:

```go
// token_fetcher_windows.go
func tokenHelperName() string { return "get_token.exe" }

// token_fetcher_unix.go
func tokenHelperName() string { return "get_token" }
```

`FetchSessionTokenWithParams` resolves `filepath.Join(paths.BinDir, tokenHelperName())`, verifies it is a non-empty regular file, and returns `portable helper is missing: <path>` when absent. Remove `go:embed` and binary extraction.

- [ ] **Step 5: Route every discovered runtime temp file**

Use `CreateRunTemp` for covers, FFmpeg archives, and extracted artwork. Put updater scripts directly under `RuntimePaths.TempDir` rather than `RunTempDir`, because they must survive application shutdown long enough to replace/restart the executable. For the macOS updater, replace `mktemp -d` with a precomputed mount directory below `RuntimePaths.TempDir`. Startup stale cleanup removes old updater scripts, updater mount directories, and abandoned run directories by age, but never the current run directory.

- [ ] **Step 6: Package the token helper beside the built executable**

After the Wails build succeeds, make `build.ps1` create `build/bin/SpotiDownloader.runtime/bin` and copy `backend/bin/get_token.exe` there. The built layout assertion is:

```powershell
$runtimeBinDir = Join-Path $scriptRoot "build\bin\SpotiDownloader.runtime\bin"
New-Item -ItemType Directory -Force $runtimeBinDir | Out-Null
Copy-Item -LiteralPath $embeddedHelperExe -Destination (Join-Path $runtimeBinDir "get_token.exe") -Force
```

- [ ] **Step 7: Run helper, history, and temp tests**

Run: `go test ./backend -run 'Test(HelperPaths|ShouldSkipYtDlp|RecordYtDlp|RuntimeTemp|HistoryPath|SelectYtDlp|SelectFFmpeg)' -count=1`

Expected: PASS, including the exact 24-hour boundary.

- [ ] **Step 8: Commit portable helper storage, including the preserved TTL work**

```powershell
git add backend/runtime_paths.go backend/runtime_paths_test.go backend/ffmpeg.go backend/ffmpeg_release_test.go backend/ytdlp.go backend/ytdlp_test.go backend/token_fetcher.go backend/token_fetcher_windows.go backend/token_fetcher_unix.go backend/config.go backend/history.go backend/history_path_test.go backend/app_update.go backend/spotidownloader.go backend/metadata.go build.ps1
git commit -m "feat: store runtime helpers beside executable"
```

---

### Task 3: Persistent metadata cache with TTL, atomic writes, and singleflight

**Files:**
- Create: `backend/metadata_cache.go`
- Create: `backend/metadata_cache_test.go`
- Create: `backend/atomic_replace_windows.go`
- Create: `backend/atomic_replace_unix.go`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `app.go:17-38`

**Interfaces:**
- Consumes: `RuntimePaths.MetadataCacheFile`.
- Produces: `CacheOptions`, `MetadataCache`, `OpenMetadataCache(path string, opts CacheOptions) (*MetadataCache, []string)`, `(*MetadataCache).Close() error`, `CacheOutcome`, and generic `CachedLookup[T]`.

```go
type CacheOptions struct {
	MaxEntries int
	Now        func() time.Time
	WriteDelay time.Duration
	ReplaceFile func(tempPath, targetPath string) error
	OnWarning  func(message string)
}

type CacheOutcome string
const (
	CacheHit CacheOutcome = "hit"
	CacheMiss CacheOutcome = "miss"
	CacheShared CacheOutcome = "shared"
)

func CachedLookup[T any](ctx context.Context, cache *MetadataCache, key string, ttl time.Duration, cacheable func(T) bool, load func(context.Context) (T, error)) (T, CacheOutcome, error)
```

- [ ] **Step 1: Add `x/sync` and write failing cache tests**

Run: `go get golang.org/x/sync@latest`

Write table tests using an injected clock for pre-expiry, exact-expiry, and post-expiry behavior. Add this simultaneous-loader test:

```go
func TestCachedLookupSingleflight(t *testing.T) {
	cache := openTestCache(t)
	var calls atomic.Int32
	loader := func(context.Context) (string, error) {
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		return "USRC17607839", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, _, err := CachedLookup(context.Background(), cache, "isrc:track", 7*24*time.Hour, func(v string) bool { return v != "" }, loader)
			if err != nil || value == "" { t.Errorf("value=%q err=%v", value, err) }
		}()
	}
	wg.Wait()
	if calls.Load() != 1 { t.Fatalf("loader calls = %d", calls.Load()) }
}
```

Also test that empty values/errors are absent on the next lookup, 5,001 inserts evict to 5,000, a corrupt JSON file is renamed to `metadata-cache.json.corrupt-<timestamp>`, and interrupted writes cannot replace the valid primary file. A read/rename/write failure must call `OnWarning` and keep serving an in-memory cache rather than preventing downloads.

- [ ] **Step 2: Run cache tests and confirm the missing implementation**

Run: `go test ./backend -run 'Test(Cache|CachedLookup)' -count=1`

Expected: FAIL to compile because cache types are absent.

- [ ] **Step 3: Implement the in-memory store and JSON envelope**

```go
type cacheEntry struct {
	Value      json.RawMessage `json:"value"`
	ExpiresAt  time.Time       `json:"expires_at"`
	LastAccess time.Time       `json:"last_access"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

type MetadataCache struct {
	mu sync.Mutex
	path string
	entries map[string]cacheEntry
	maxEntries int
	now func() time.Time
	group singleflight.Group
	dirty chan struct{}
	stop chan struct{}
	done chan struct{}
}
```

Use `singleflight.Group.DoChan` so a waiting caller can return on `ctx.Done()`. The loader result is persisted only when `cacheable(value)` is true. A shared non-cacheable error is returned to all waiters but not stored.

- [ ] **Step 4: Implement coalesced atomic persistence and corruption recovery**

One writer goroutine drains `dirty`, waits `WriteDelay`, snapshots under the mutex, writes `<path>.tmp`, calls `Sync`, closes it, and atomically replaces the primary file. Implement production `atomicReplaceFile` with `os.Rename` on Unix and `windows.MoveFileEx` using `MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH` on Windows; inject it through `CacheOptions.ReplaceFile` in failure tests. `Close` flushes synchronously and waits for `done`. On parse failure, attempt to rename the primary file before starting empty; if backup or persistence fails, emit `OnWarning` and continue in memory. Evict expired entries first, then the smallest `LastAccess` until `len(entries) <= MaxEntries`.

- [ ] **Step 5: Attach cache lifecycle to `App`**

Add `metadataCache *backend.MetadataCache` to `App`. Open the cache after portable paths initialize, install the pointer in a process registry through `backend.InstallMetadataCache(cache)`, log returned cache warnings without blocking startup, and close it during `shutdown` before cleaning the run directory. Only the portable-directory write probe from Task 1 may block startup. `backend.CurrentMetadataCache()` returns the installed shared pointer and never creates a second store.

- [ ] **Step 6: Run deterministic and concurrent cache tests**

Run: `go test ./backend -run 'Test(Cache|CachedLookup)' -count=10`

Expected: PASS on all ten runs.

- [ ] **Step 7: Commit the cache core**

```powershell
git add backend/metadata_cache.go backend/metadata_cache_test.go backend/atomic_replace_windows.go backend/atomic_replace_unix.go go.mod go.sum app.go
git commit -m "feat: add persistent metadata cache"
```

---

### Task 4: Cache Spotify, ISRC/MusicBrainz, and Chosic genre lookups

**Files:**
- Modify: `app.go:149-180`
- Modify: `backend/spotidownloader.go:31-45,156-258`
- Modify: `backend/spotify_metadata.go`
- Modify: `backend/musicbrainz.go:69-192`
- Modify: `backend/songlink.go:424-430`
- Modify: `backend/chosic_genre.go:24-191`
- Modify: `backend/track_metadata_update.go:112-182`
- Create: `backend/metadata_lookup_cache_test.go`

**Interfaces:**
- Consumes: `*MetadataCache` and `CachedLookup[T]` from Task 3.
- Preserves: `NewSpotiDownloader(sessionToken string) *SpotiDownloader`; it reads the already-installed shared cache through `CurrentMetadataCache()`.
- Produces context-aware `fetchTaggingMetadata(ctx context.Context, cache *MetadataCache, req TaggingMetadataRequest) (taggingMetadataResult, []string)`.

- [ ] **Step 1: Write local-server cache integration tests**

Use `httptest.Server` counters to assert two calls for the same track perform one Spotify request, one ISRC/MusicBrainz chain, one Chosic track request, and one artist request. Advance the injected clock by 8 days and assert the 7-day values reload while the artist genre remains cached; advance to 31 days and assert the genre reloads.

```go
if got := spotifyCalls.Load(); got != 1 { t.Fatalf("spotify calls = %d", got) }
if got := chosicTrackCalls.Load(); got != 1 { t.Fatalf("chosic track calls = %d", got) }
if got := chosicArtistCalls.Load(); got != 1 { t.Fatalf("chosic artist calls = %d", got) }
```

- [ ] **Step 2: Run the integration test and confirm repeated calls**

Run: `go test ./backend -run TestMetadataLookupCacheTTLsAndSingleflight -count=1`

Expected: FAIL because the second lookup reaches the local server.

- [ ] **Step 3: Add the exact cache keys and TTLs**

```go
const (
	trackMetadataTTL = 7 * 24 * time.Hour
	artistGenreTTL   = 30 * 24 * time.Hour
)

func spotifyCacheKey(trackID string) string { return "spotify:" + trackID }
func isrcCacheKey(trackID string) string { return "isrc:" + trackID }
func chosicTrackCacheKey(trackID string) string { return "chosic-track:" + trackID }
func artistGenreCacheKey(artistID string) string { return "genre:" + artistID }
```

Cache the Spotify response used by `GetSpotifyMetadata`; cache a combined ISRC/MusicBrainz result under `isrc:<track-id>`; cache Chosic track-to-artist separately from the artist genre list. Treat trimmed empty strings and empty genre slices as non-cacheable.

- [ ] **Step 4: Propagate context and cache through metadata clients**

Replace request creation with `http.NewRequestWithContext`. Store the process cache pointer in `SpotiDownloader`. Add `FetchMusicBrainzMetadataContext(ctx, ...)`, context variants for SongLink and Chosic calls, and keep background wrappers only for existing non-download callers. Convert `fetchTaggingMetadataInBackground` into a synchronous context-aware function whose internal independent lookups may still run concurrently; the outer track pipeline will own its goroutine and timing. `UpdateTrackMetadata` calls it with the process cache and its existing non-track background context until that API receives cancellation in a later scoped change.

- [ ] **Step 5: Remove the redundant unconditional frontend enrichment dependency from the backend path**

Keep the frontend's available track fields as the base request. `GetSpotifyMetadata` remains available for `enrichTracksReleaseDates`, but repeated calls now hit the backend cache. Do not add another metadata fetch inside `DownloadTrack` when required base fields are already present.

- [ ] **Step 6: Pass cache integration and existing metadata tests**

Run: `go test ./backend -run 'TestMetadataLookupCache|Test.*Metadata|Test.*Chosic' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit cached metadata clients**

```powershell
git add app.go backend/spotidownloader.go backend/spotify_metadata.go backend/musicbrainz.go backend/songlink.go backend/chosic_genre.go backend/track_metadata_update.go backend/metadata_lookup_cache_test.go
git commit -m "feat: cache track metadata lookups"
```

---

### Task 5: Item-scoped progress, phases, warnings, and aggregate state

**Files:**
- Modify: `backend/progress.go:10-269`
- Create: `backend/progress_test.go`
- Modify: `app_queue_history.go:9-42`

**Interfaces:**
- Produces: `DownloadPhase`, `PipelineTimings`, extended `DownloadItem`, and `DownloadQueueInfo.ActiveCount`.
- Produces functions: `UpdateItemPhase`, `UpdateItemTransfer`, `AddItemWarning`, `CancelDownloadItem`, and item-aware `NewProgressWriterWithID`.

```go
type DownloadPhase string
const (
	PhaseQueued DownloadPhase = "queued"
	PhasePreparing DownloadPhase = "preparing"
	PhaseDownloading DownloadPhase = "downloading"
	PhaseConverting DownloadPhase = "converting"
	PhaseFinalizing DownloadPhase = "finalizing"
	PhaseCompleted DownloadPhase = "completed"
	PhaseFailed DownloadPhase = "failed"
	PhaseCancelled DownloadPhase = "cancelled"
)

type PipelineTimings struct {
	PreparationMS int64 `json:"preparation_ms"`
	DownloadMS int64 `json:"download_ms"`
	ConversionMS int64 `json:"conversion_ms"`
	CoverMS int64 `json:"cover_ms"`
	MetadataMS int64 `json:"metadata_ms"`
	LyricsMS int64 `json:"lyrics_ms"`
	DurationProbeMS int64 `json:"duration_probe_ms"`
	FinalizationMS int64 `json:"finalization_ms"`
	TotalMS int64 `json:"total_ms"`
}
```

- [ ] **Step 1: Write concurrent queue-state tests**

Start three items, update each from separate goroutines, and assert distinct bytes/speeds/phases. Then complete one, fail one, cancel one, and assert `ActiveCount == 0`, `CompletedCount == 1`, `FailedCount == 1`, and `CancelledCount == 1`. Add a regression assertion that starting item B does not reset item A.

- [ ] **Step 2: Run progress tests under the race detector**

Run: `go test -race ./backend -run 'TestConcurrentItemProgress|TestStartingItemDoesNotResetAnother' -count=1`

Expected: FAIL against the global `currentItemID`/progress implementation.

- [ ] **Step 3: Extend queue items and remove track reliance on global current state**

Add JSON fields `phase`, `detail`, `downloaded_bytes`, `total_bytes`, `warning`, and `timings` to `DownloadItem`. Add `active_count` and `cancelled_count` to `DownloadQueueInfo`. All state mutation happens under `downloadQueueLock`; no function reads a process-global `currentItemID` for track progress.

```go
func UpdateItemTransfer(id string, downloaded, total int64, speedMBps float64)
func UpdateItemPhase(id string, phase DownloadPhase, detail string)
func AddItemWarning(id, warning string)
func CompleteDownloadItem(id, filePath string, finalSize float64, timings PipelineTimings)
```

- [ ] **Step 4: Make the progress writer item-explicit**

Construct it with `NewProgressWriterWithID(writer, itemID, totalBytes)`. Its `Write` method updates only that ID and records actual byte counts. Keep any FFmpeg-installer progress in a separately named installer state; do not reuse a queue item's state.

- [ ] **Step 5: Derive aggregate progress from the queue**

`GetDownloadQueue` calculates active count, summed active speed, and total downloaded bytes from a locked snapshot. `GetDownloadProgress` maps that aggregate into the legacy toast structure so existing consumers keep working until Task 12.

- [ ] **Step 6: Pass progress and race tests**

Run: `go test -race ./backend -run 'TestConcurrentItemProgress|TestStartingItemDoesNotResetAnother|TestDownloadQueueAggregate' -count=10`

Expected: PASS with no race report.

- [ ] **Step 7: Commit item-scoped progress**

```powershell
git add backend/progress.go backend/progress_test.go app_queue_history.go
git commit -m "feat: track progress per download item"
```

---

### Task 6: Track cancellation through HTTP and helper process trees

**Files:**
- Create: `backend/process_windows.go`
- Create: `backend/process_unix.go`
- Create: `backend/process_test.go`
- Modify: `backend/http_client.go`
- Modify: `backend/spotidownloader.go:260-327,672-746,908-941`
- Modify: `backend/ffmpeg.go`
- Modify: `backend/metadata.go:837-877,1159-1238`
- Modify: `backend/ytdlp.go:223-269`
- Modify: `backend/lyrics.go:79-329`

**Interfaces:**
- Produces: `newManagedCommand(ctx context.Context, name string, args ...string) *exec.Cmd`.
- Produces context variants: `DownloadWithYtDlpContext`, `ConvertAudioContext`, `GetAudioDurationContext`, and `EmbedMetadataContext`; legacy public wrappers call them with `context.Background()` outside the track pipeline.

- [ ] **Step 1: Write cancellation tests for HTTP and managed commands**

Use an `httptest.Server` handler that blocks on `r.Context().Done()` and a long-running child command. Cancel the context and assert both return within one second with `context.Canceled` or a wrapped cancellation error.

```go
cancel()
select {
case err := <-done:
	if !errors.Is(err, context.Canceled) { t.Fatalf("err = %v", err) }
case <-time.After(time.Second):
	t.Fatal("operation ignored cancellation")
}
```

- [ ] **Step 2: Run focused cancellation tests**

Run: `go test ./backend -run 'TestManagedCommandCancellation|TestDownloadRequestCancellation' -count=1`

Expected: FAIL because current requests and `exec.Command` calls ignore the track context.

- [ ] **Step 3: Implement platform process-tree cancellation**

On Windows, start a new process group and override `cmd.Cancel` to invoke `taskkill /PID <pid> /T /F` with a hidden window. On Unix, start a new process group and send `SIGKILL` to `-pid`. Return `os.ErrProcessDone` when cancellation races with normal exit.

- [ ] **Step 4: Thread context through track-path operations**

Use `http.NewRequestWithContext` for audio, cover, metadata, lyrics, and helper downloads. Replace track-path `exec.Command` with `newManagedCommand` for conversion, FFprobe, M4A tag writing, and yt-dlp. Preserve background wrappers for unrelated metadata-editor operations so this task does not expand their API surface.

- [ ] **Step 5: Pass cancellation and existing helper tests**

Run: `go test ./backend -run 'TestManagedCommandCancellation|TestDownloadRequestCancellation|TestSelectYtDlp|Test.*FFmpeg' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit cancellation plumbing**

```powershell
git add backend/process_windows.go backend/process_unix.go backend/process_test.go backend/http_client.go backend/spotidownloader.go backend/ffmpeg.go backend/metadata.go backend/ytdlp.go backend/lyrics.go
git commit -m "feat: cancel download network and helper work"
```

---

### Task 7: One duration probe and one media finalization pass

**Files:**
- Modify: `backend/metadata.go:20-38,76-248,498-637,794-932,1159-1238`
- Modify: `backend/metadata_test.go`
- Modify: `app_download_helpers.go:225-232`

**Interfaces:**
- Consumes: `GetAudioDurationContext` and `EmbedMetadataContext` from Task 6.
- Produces: `ValidateLyricsDuration(lyrics string, durationSeconds float64) (string, error)` and `FinalizeMedia(ctx context.Context, filePath string, metadata Metadata, coverPath string) error`.

- [ ] **Step 1: Write failing validation and single-save tests**

Add a lyrics test that passes duration directly and therefore cannot invoke FFprobe. Extend MP3/FLAC/M4A metadata tests so `Metadata{Lyrics: "[00:01.00]line"}` is written together with normal tags and cover. For M4A, assert the FFmpeg argument list contains `-metadata lyrics=...` in the same command that maps the cover.

- [ ] **Step 2: Run metadata tests**

Run: `go test ./backend -run 'TestValidateLyricsDurationUsesProvidedDuration|TestFinalizeMediaWrites.*InOnePass' -count=1`

Expected: FAIL because `validateLyricsDuration` probes the file and lyrics use a separate writer.

- [ ] **Step 3: Refactor lyrics validation into a pure duration consumer**

```go
func ValidateLyricsDuration(lyrics string, durationSeconds float64) (string, error) {
	durationMS := int64(durationSeconds * 1000)
	// Retain only valid LRC timestamps at or before durationMS.
}
```

Remove the `GetAudioDuration` call from lyrics validation. Keep the existing timestamp parsing and line filtering rules.

- [ ] **Step 4: Make `Metadata.Lyrics` part of every format's normal save**

MP3 and FLAC already have format-specific tag structures; set their lyrics field before the single close/save. Add the M4A `lyrics=` argument before its sole FFmpeg invocation. `FinalizeMedia` calls exactly one format writer and treats its error as fatal.

- [ ] **Step 5: Reuse measured duration for suspicious-file validation**

Add a duration-consuming helper while temporarily preserving the old probing wrapper so this intermediate commit still builds before App switches to the new pipeline:

```go
func validateDownloadedDuration(filename string, expectedDurationMS int, actualDurationSeconds float64) DownloadResponse
```

The existing `validateDownloadedFileDuration(filename, expectedDurationMS)` wrapper may call `GetAudioDuration` only until Task 9. Task 9 switches the production download path to `validateDownloadedDuration` and removes the wrapper, leaving one probe in the complete pipeline.

- [ ] **Step 6: Pass metadata and duration tests**

Run: `go test ./backend ./... -run 'TestValidateLyricsDuration|TestFinalizeMedia|TestEmbedMetadata|Test.*Duration' -count=1`

Expected: PASS.

- [ ] **Step 7: Commit one-pass finalization**

```powershell
git add backend/metadata.go backend/metadata_test.go app_download_helpers.go
git commit -m "perf: finalize media tags in one pass"
```

---

### Task 8: Concurrent per-track backend pipeline

**Files:**
- Create: `backend/track_pipeline.go`
- Create: `backend/track_pipeline_test.go`
- Modify: `backend/spotidownloader.go:31-45,748-941`

**Interfaces:**
- Consumes: cache, item progress, context-aware helpers, and one-pass finalization from Tasks 3-7.
- Produces: `TrackPipelineRequest`, `TrackPipelineResult`, and `(*SpotiDownloader).RunTrackPipeline(ctx context.Context, req TrackPipelineRequest) (TrackPipelineResult, error)`.
- Preserves the existing 25-argument `DownloadTrack` method until Task 9 so every intermediate commit compiles; Task 9 removes it after switching the App call site.

```go
type TrackPipelineRequest struct {
	ItemID string
	TrackID string
	OutputDir string
	AudioFormat string
	FilenameFormat string
	IncludeTrackNumber bool
	Position int
	UseAlbumTrackNumber bool
	PlaylistName string
	PlaylistOwner string
	UseFirstArtistOnly bool
	EmbedMaxQualityCover bool
	EmbedGenre bool
	UseSingleGenre bool
	EmbedLyrics bool
	ExpectedDurationMS int
	CoverURL string
	BaseMetadata Metadata
}

type TrackPipelineResult struct {
	File string
	AlreadyExists bool
	DurationSeconds float64
	Warnings []string
	CacheOutcomes map[string]CacheOutcome
	Timings PipelineTimings
}
```

- [ ] **Step 1: Build deterministic dependency fakes and failing overlap tests**

Define an unexported `trackPipelineDeps` with function fields for target resolution, audio download, conversion, cover, tagging metadata, lyrics, duration, and finalization. Tests block each branch on channels and record start/end times. Assert audio, cover, metadata, and lyrics have all started before releasing audio.

```go
for _, branch := range []string{"audio", "cover", "metadata", "lyrics"} {
	select {
	case <-started[branch]:
	case <-time.After(time.Second): t.Fatalf("%s did not start", branch)
	}
}
```

Add cases for a 5-second injected deadline clock, late lyrics discarded, duration called once, finalizer called once, finalizer error marks failure, and success/failure/cancellation remove the track temp directory.

- [ ] **Step 2: Run pipeline tests and confirm serialized behavior is absent**

Run: `go test ./backend -run 'TestTrackPipeline' -count=1`

Expected: FAIL because `track_pipeline.go` does not exist.

- [ ] **Step 3: Compute output/existing-file state and source link before starting remote branches**

Normalize paths, build the final filename once, check all possible output extensions, and immediately return `AlreadyExists` without starting cover/metadata/lyrics when a valid file exists. Record the pipeline start before resolving the source link, then resolve a valid audio target. If source-link resolution consumes the whole 5-second lyrics budget, skip starting lyrics and record the deadline warning.

- [ ] **Step 4: Start buffered branch goroutines under one track context**

Create the track temp directory from `RuntimePaths.RunTempDir/<sanitized-item-id>` and defer its removal. Download, convert, probe, and finalize the media inside that directory; do not create intermediate media beside the user's final output. Start cover, tagging metadata, and lyrics channels with capacity one after source-link resolution. Derive cover and metadata child contexts with exact 10-second deadlines. Start audio acquisition in the main path at the same time as those goroutines so retry/session-token behavior remains straightforward. Every goroutine sends exactly once, even on cancellation.

- [ ] **Step 5: Enforce the absolute lyrics deadline**

```go
pipelineStarted := time.Now()
lyricsDeadline := pipelineStarted.Add(5 * time.Second)
lyricsCtx, cancelLyrics := context.WithDeadline(ctx, lyricsDeadline)
defer cancelLyrics()
```

After conversion and the single duration probe, wait only until `lyricsDeadline`. If it has passed, add warning `lyrics deadline exceeded; finalized without lyrics`, cancel the lyrics branch, and continue. A late result is drained/discarded without reopening the file.

- [ ] **Step 6: Finalize exactly once and publish terminal state afterward**

Merge base metadata with optional enrichment, validate ready lyrics against the measured duration, await the bounded cover/metadata outcomes, set phase `finalizing`, and call `FinalizeMedia` once on the runtime staging file. Publish the finalized file to the user output path only after that succeeds; if cross-volume publication falls back to copying, remove the destination on copy/cancellation error. Return warnings, cache outcomes, and overlapping phase timings; do not call `CompleteDownloadItem` inside the pipeline until finalization and publication both succeed.

- [ ] **Step 7: Pass pipeline tests repeatedly**

Run: `go test -race ./backend -run 'TestTrackPipeline' -count=10`

Expected: PASS with no race, leak, second duration call, or second finalization call.

- [ ] **Step 8: Commit the concurrent track pipeline**

```powershell
git add backend/track_pipeline.go backend/track_pipeline_test.go backend/spotidownloader.go
git commit -m "perf: overlap per-track download work"
```

---

### Task 9: App-level track contexts, cancellation API, and structured timings

**Files:**
- Modify: `app.go:17-38,98-147,223-328`
- Modify: `app_download_helpers.go:1-232`
- Modify: `app_download_helpers_test.go`
- Modify: `app_queue_history.go`

**Interfaces:**
- Consumes: `TrackPipelineRequest/Result` from Task 8.
- Produces Wails binding: `CancelActiveDownloads() int`.
- Extends `DownloadResponse` with `Warnings []string`, `CacheOutcomes map[string]backend.CacheOutcome`, and `Timings backend.PipelineTimings`.

- [ ] **Step 1: Write registry and completion-order tests**

Use an injected pipeline function that blocks on `ctx.Done()`. Start three `App.DownloadTrack` calls, invoke `CancelActiveDownloads`, assert it returns `3`, and assert all three queue items become cancelled. Add a regression test where finalization blocks and verify the item is not completed until it is released.

- [ ] **Step 2: Run root-package tests**

Run: `go test . -run 'TestCancelActiveDownloads|TestDownloadCompletesAfterFinalization' -count=1`

Expected: FAIL because App has no active-context registry.

- [ ] **Step 3: Add an App-owned context registry**

```go
type App struct {
	// existing fields
	activeMu sync.Mutex
	activeDownloads map[string]context.CancelFunc
}

func (a *App) beginTrack(itemID string) (context.Context, func())
func (a *App) CancelActiveDownloads() int
```

`beginTrack` derives from `a.ctx`, stores the cancel function by item ID, and returns an idempotent cleanup that removes the entry and cancels resources. `shutdown` calls `CancelActiveDownloads` before cache/history/temp cleanup.

- [ ] **Step 4: Replace old App orchestration with one pipeline call**

Remove `startLyricsFetch`, `embedLyricsIfReady`, `finishLyricsEmbeddingAsync`, and the second file-duration probe from the normal download path. Build `backend.TrackPipelineRequest` from `DownloadRequest`, call the pipeline with the item context, then perform suspicious-file movement using `result.DurationSeconds`.

- [ ] **Step 5: Map errors, warnings, timings, and terminal queue state once**

On `errors.Is(err, context.Canceled)`, call `CancelDownloadItem`. On other required errors, call `FailDownloadItem`. On success, attach every warning to the queue item, call `CompleteDownloadItem` with timings, enqueue history asynchronously, and return the structured response.

- [ ] **Step 6: Regenerate Wails bindings and pass App tests**

Run: `wails generate module`

Run: `go test . ./backend -run 'TestCancelActiveDownloads|TestDownloadCompletesAfterFinalization|TestNormalizeDownloadRequest' -count=1`

Expected: PASS. Generated `frontend/wailsjs` files remain ignored and are not staged.

- [ ] **Step 7: Commit App orchestration**

```powershell
git add app.go app_download_helpers.go app_download_helpers_test.go app_queue_history.go
git commit -m "feat: cancel active track pipelines"
```

---

### Task 10: Frontend test runner, indexed worker pool, and token singleflight

**Files:**
- Modify: `frontend/package.json`
- Modify: `frontend/pnpm-lock.yaml`
- Create: `frontend/src/lib/download-pool.ts`
- Create: `frontend/src/lib/download-pool.test.ts`
- Modify: `frontend/src/lib/token-manager.ts:6-56`
- Create: `frontend/src/lib/token-manager.test.ts`

**Interfaces:**
- Produces: `runBoundedPool<T, R>(options: PoolOptions<T, R>): Promise<PoolResult<R>[]>`.
- Produces result status union: `fulfilled | rejected | cancelled` with original `index`.
- Preserves existing `ensureValidToken(forceRefresh?: boolean): Promise<string>` signature.

- [ ] **Step 1: Install Vitest and add the test script**

Run: `pnpm add -D vitest` from `frontend`.

Set `"test": "vitest run"` in `frontend/package.json`.

- [ ] **Step 2: Write failing pool tests**

```ts
it("runs at most three tasks and preserves indexed results", async () => {
  let active = 0;
  let peak = 0;
  const results = await runBoundedPool({
    items: [40, 10, 30, 5, 20], concurrency: 3,
    shouldStop: () => false,
    worker: async (delay, index) => {
      active++; peak = Math.max(peak, active);
      await new Promise(resolve => setTimeout(resolve, delay));
      active--; return `track-${index}`;
    },
  });
  expect(peak).toBe(3);
  expect(results.map(r => r.index)).toEqual([0, 1, 2, 3, 4]);
});
```

Add stop coverage where active tasks settle but unclaimed indices are `cancelled`, and rejection coverage where another task still fulfills.

- [ ] **Step 3: Implement the pool with one shared synchronous index cursor**

```ts
export type PoolResult<R> =
  | { index: number; status: "fulfilled"; value: R }
  | { index: number; status: "rejected"; reason: unknown }
  | { index: number; status: "cancelled" };

export interface PoolOptions<T, R> {
  items: readonly T[];
  concurrency: number;
  shouldStop: () => boolean;
  worker: (item: T, index: number) => Promise<R>;
  onSettled?: (result: PoolResult<R>) => void;
}
```

Create `Math.min(concurrency, items.length)` loops. Each loop claims `nextIndex++` before awaiting. After loops settle, fill every unclaimed slot with `cancelled`, call `onSettled`, and return the dense array in index order.

- [ ] **Step 4: Replace token polling with a shared refresh promise**

Use `let tokenFetchPromise: Promise<string> | null = null`. A caller needing refresh returns the existing promise; the creator assigns the fetch promise and clears it in `finally`. Add a test with 20 concurrent calls and a mocked Wails fetcher; expect exactly one invocation and identical tokens.

- [ ] **Step 5: Run frontend unit tests and build**

Run from `frontend`: `pnpm test && pnpm run build`

Expected: PASS.

- [ ] **Step 6: Commit frontend concurrency primitives**

```powershell
git add frontend/package.json frontend/pnpm-lock.yaml frontend/src/lib/download-pool.ts frontend/src/lib/download-pool.test.ts frontend/src/lib/token-manager.ts frontend/src/lib/token-manager.test.ts
git commit -m "feat: add bounded frontend download pool"
```

---

### Task 11: Run batch downloads through three full-track workers

**Files:**
- Modify: `frontend/src/hooks/useDownload.ts:868-1151,1346-1350`
- Modify: `frontend/src/lib/batch-summary.ts`
- Modify: `frontend/src/types/api.ts`
- Create: `frontend/src/lib/batch-download.ts`
- Create: `frontend/src/lib/batch-download.test.ts`

**Interfaces:**
- Consumes: `runBoundedPool`, `CancelActiveDownloads`, structured `DownloadResponse`, and source-indexed `trackPathInfo`.
- Produces: batch results with `cancelled` outcome, `elapsedMs`, and `peakActive`.

- [ ] **Step 1: Extract work-item construction and write a pure ordering regression test**

Move source-indexed work-item construction and ordered file reduction into `batch-download.ts`. Represent each missing track as:

```ts
interface BatchDownloadWorkItem {
  track: TrackMetadata;
  pathInfo: BatchTrackPathInfo;
  sourceIndex: number;
}
```

Test that a source list with existing items at indices 1 and 4 plus out-of-order worker completions still produces final file paths `[0,1,2,3,4]` with no completion-order reshuffle.

- [ ] **Step 2: Run the focused frontend tests**

Run from `frontend`: `pnpm test -- download-pool`

Expected: the new work-item/order assertion FAILS until batch helpers use source indices.

- [ ] **Step 3: Replace the sequential `for/await` loop**

Create work items directly from `trackPathInfo.map((entry, sourceIndex) => ...)`, filter existing IDs, fetch one initial token, and call:

```ts
const poolResults = await runBoundedPool({
  items: workItems,
  concurrency: 3,
  shouldStop: () => shouldStopDownloadRef.current,
  worker: downloadBatchWorkItem,
  onSettled: applyBatchResult,
});
```

`downloadBatchWorkItem` performs the existing unauthorized retry once using `ensureValidToken(true)`. It returns data rather than mutating summary counters. `applyBatchResult` performs all counter, React state, logger, suspicious-redownload, and `finalFilePaths[sourceIndex]` updates synchronously.

- [ ] **Step 4: Make Stop cancel backend work and stop assignment**

```ts
const handleStopDownload = async () => {
  shouldStopDownloadRef.current = true;
  const cancelled = await CancelActiveDownloads();
  logger.info(`download stop requested; cancelling ${cancelled} active track(s)`);
  toast.info("Stopping download...");
};
```

Pool results that were never claimed become `cancelled`; do not increment downloaded/failed counts for them. Interrupted backend calls map to the same cancelled outcome.

- [ ] **Step 5: Preserve ordered M3U8 and summary output**

Use `finalFilePaths.filter(Boolean)` only after all pool workers settle. Build the summary by source index, not callback order. Track peak active workers inside the pool callback and record batch wall-clock elapsed time using `performance.now()`.

- [ ] **Step 6: Run frontend tests, lint, and build**

Run from `frontend`: `pnpm test && pnpm run lint && pnpm run build`

Expected: PASS.

- [ ] **Step 7: Commit batch worker integration**

```powershell
git add frontend/src/hooks/useDownload.ts frontend/src/lib/batch-summary.ts frontend/src/types/api.ts frontend/src/lib/batch-download.ts frontend/src/lib/batch-download.test.ts
git commit -m "perf: download three tracks concurrently"
```

---

### Task 12: Display per-item phases and log detailed timings

**Files:**
- Modify: `frontend/src/hooks/useDownloadProgress.ts`
- Modify: `frontend/src/hooks/useDownloadQueueData.ts`
- Modify: `frontend/src/components/DownloadQueue.tsx:112-517`
- Modify: `frontend/src/components/DownloadProgress.tsx`
- Modify: `frontend/src/components/DownloadProgressToast.tsx`
- Modify: `frontend/src/components/BatchDownloadSummaryDialog.tsx:26-262`
- Modify: `frontend/src/hooks/useDownload.ts`
- Create: `frontend/src/lib/download-timings.ts`
- Create: `frontend/src/lib/download-timings.test.ts`

**Interfaces:**
- Consumes generated Wails fields: `active_count`, item `phase/detail/downloaded_bytes/total_bytes/warning/timings`, and response `warnings/cache_outcomes/timings`.
- Produces no new screen; all visibility remains in the queue, toast/progress surface, Debug Logger, and existing batch summary dialog.

- [ ] **Step 1: Add formatter unit tests**

Extract `formatPipelineTimings` and test this stable output shape:

```ts
expect(formatPipelineTimings({ download_ms: 1200, metadata_ms: 800, total_ms: 1700 }))
  .toBe("download 1.20s, metadata 0.80s, total 1.70s");
```

Test aggregate text `3 active · 7/12 settled` and the phase-label mapping for every backend phase including cancelled.

- [ ] **Step 2: Run frontend tests and observe missing fields/formatters**

Run from `frontend`: `pnpm test -- timings progress`

Expected: FAIL until formatters and generated model usage are added.

- [ ] **Step 3: Update queue and aggregate progress rendering**

For each active item, render the main phase, subordinate detail, byte progress when total is known, speed, and warning. Render terminal error/cancelled text once. The top surface shows `active_count` and aggregate settled count; it does not combine unknown totals into a false percentage.

- [ ] **Step 4: Log per-track and batch timings**

After each response settles, log one line with track identity, formatted phases, warnings, and the explicit `cache_outcomes` map (`spotify`, `isrc`, `chosic-track`, and `genre` when requested). At batch completion log:

```ts
logger.info(`batch timing: total ${(elapsedMs / 1000).toFixed(2)}s, peak ${peakActive} active, ${successCount} downloaded, ${skippedCount} skipped, ${errorCount} failed, ${cancelledCount} cancelled`);
```

- [ ] **Step 5: Extend the existing batch summary dialog**

Add compact rows for elapsed time, peak active tracks, and cancelled count. Do not add a diagnostics route or new page.

- [ ] **Step 6: Regenerate bindings and run UI verification**

Run: `wails generate module`

Run from `frontend`: `pnpm test && pnpm run lint && pnpm run build`

Expected: PASS.

- [ ] **Step 7: Commit progress and diagnostics UI**

```powershell
git add frontend/src/hooks/useDownloadProgress.ts frontend/src/hooks/useDownloadQueueData.ts frontend/src/components/DownloadQueue.tsx frontend/src/components/DownloadProgress.tsx frontend/src/components/DownloadProgressToast.tsx frontend/src/components/BatchDownloadSummaryDialog.tsx frontend/src/hooks/useDownload.ts frontend/src/lib/download-timings.ts frontend/src/lib/download-timings.test.ts
git commit -m "feat: show concurrent download phases and timings"
```

---

### Task 13: Full regression, deterministic performance comparison, and documentation

**Files:**
- Modify: `README.md`
- Modify tests only if a verification command exposes a real regression; commit the narrowly scoped correction with its regression test before continuing.

**Interfaces:**
- Consumes every prior task.
- Produces a verified portable build at `build/bin/SpotiDownloader.exe` with `build/bin/SpotiDownloader.runtime/bin/get_token.exe`.

- [ ] **Step 1: Document the exact portable layout and failure mode**

Add this README section:

```markdown
### Portable runtime files

SpotiDownloader stores downloaded helpers, metadata cache, and working files in
`SpotiDownloader.runtime` beside the application executable. Keep the executable
in a writable directory. Running it from a protected directory such as
`Program Files` produces a startup error instead of writing hidden fallback
files into the user profile.

`yt-dlp.exe` is checked for a newer official release at most once every 24 hours.
Settings, history, and downloaded music keep their existing locations.
```

- [ ] **Step 2: Run all backend and root tests with fresh results**

Run: `go test ./... -count=1`

Expected: PASS with zero failures.

Run: `go test -race ./backend -count=1`

Expected: PASS with no race report.

- [ ] **Step 3: Run static analysis**

Run: `go vet ./...`

Expected: exit code 0 with no diagnostics.

- [ ] **Step 4: Run all frontend verification**

Run from `frontend`: `pnpm test && pnpm run lint && pnpm run build`

Expected: unit tests PASS, ESLint exits 0, and Vite production build succeeds.

- [ ] **Step 5: Run deterministic concurrency/performance evidence**

Run: `go test ./backend -run 'TestTrackPipeline(BranchesOverlap|LyricsDeadline|CancellationCleanup)' -count=20 -v`

Run from `frontend`: `pnpm test -- download-pool --reporter=verbose`

Expected evidence: backend branch intervals overlap, lyrics never exceed the absolute 5-second test clock, the pool reports peak active count 3, and indexed results remain ordered. Record serial (`concurrency: 1`) and concurrent (`concurrency: 3`) elapsed values from the same fake-delay fixture in the final handoff; concurrent wall time must be lower without a network-dependent percentage gate.

- [ ] **Step 6: Build the complete Windows application and inspect layout**

Run: `.\build.ps1 -NoHelperRebuild`

Expected files:

```text
build/bin/SpotiDownloader.exe
build/bin/SpotiDownloader.runtime/bin/get_token.exe
```

Launch the built executable once from `build/bin` and verify it creates `SpotiDownloader.runtime/cache` and `SpotiDownloader.runtime/temp` beside itself. Confirm no new helper/cache/temp file appears under `%USERPROFILE%/.spotidownloader`; the existing history database there is allowed and must remain usable.

- [ ] **Step 7: Refresh the persisted code graph**

Invoke `index_repository` with:

```text
repo_path = F:\Data\Code\JavaScript\SpotiDownloader
name = SpotiDownloader
mode = full
persistence = true
```

Expected: the refreshed graph includes `RuntimePaths`, `MetadataCache`, `TrackPipelineRequest`, `runBoundedPool`, and the new cancellation path. Leave database/WAL artifacts outside feature commits.

- [ ] **Step 8: Commit documentation after all verification passes**

```powershell
git add README.md
git commit -m "docs: describe portable download runtime"
```

- [ ] **Step 9: Review the final diff and commit sequence**

Run: `git status --short`

Run: `git log --oneline --decorate -15`

Run: `git diff bbe056a..HEAD --check`

Expected: no accidental profile/cache binaries, no generated `frontend/wailsjs` files, no whitespace errors, and one focused commit per task.
