# Integrated Download Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add metadata refresh for existing files, suspicious-duration detection, detailed batch summaries, and integrated yt-dlp redownload for suspicious tracks.

**Architecture:** Keep Spotify download behavior intact, but extract reusable backend helpers for metadata refresh, duration validation, suspicious file moves, and yt-dlp execution. Frontend batch orchestration keeps path calculation and queue state, then records structured per-track results and opens a reusable summary dialog.

**Tech Stack:** Go/Wails backend, React/TypeScript frontend, ffprobe/ffmpeg, managed yt-dlp binary downloaded from GitHub releases.

---

## File Structure

- Create `backend/download_validation.go`: pure duration threshold and suspicious move helpers.
- Create `backend/download_validation_test.go`: tests for duration threshold and collision-safe suspicious moves.
- Create `backend/ytdlp.go`: yt-dlp path, GitHub asset selection, install/update, and download command execution.
- Create `backend/ytdlp_test.go`: release asset selection tests.
- Create `backend/track_metadata_update.go`: shared metadata payload builder, cover/lyrics/genre refresh, and YouTube replacement orchestration.
- Create `backend/track_metadata_update_test.go`: focused tests for metadata payload construction.
- Create `app_metadata_update.go`: Wails-facing request/response types and app methods.
- Modify `app.go`: add duration fields to `DownloadResponse` and return validation status for single downloads.
- Modify `app_download_helpers.go`: expose shared request-to-metadata helpers.
- Modify `frontend/src/types/api.ts`: add settings and batch summary/redownload API types.
- Modify `frontend/src/lib/settings.ts`: add `updateMetadataForExistingFiles`.
- Modify `frontend/src/components/SettingsPage.tsx`: add the setting switch near metadata embed options.
- Create `frontend/src/lib/batch-summary.ts`: pure summary aggregation helpers.
- Create `frontend/src/lib/batch-summary.test.ts` if a TS test runner is available; otherwise keep pure helpers and validate through TypeScript build.
- Create `frontend/src/components/BatchDownloadSummaryDialog.tsx`: detailed summary and YouTube redownload action.
- Modify `frontend/src/components/TrackList.tsx`: render suspicious warning status.
- Modify `frontend/src/components/PlaylistInfo.tsx`, `AlbumInfo.tsx`, and `ArtistInfo.tsx`: add `Update All Metadata` button prop.
- Modify `frontend/src/App.tsx`: hold summary dialog state and pass new handlers.
- Modify `frontend/src/lib/api.ts`: wrap new Wails methods.
- Modify `frontend/src/hooks/useDownload.ts`: wire metadata refresh, suspicious detection, summary aggregation, and YouTube redownload.

## Task 1: Backend Duration Validation

**Files:**
- Create: `backend/download_validation.go`
- Test: `backend/download_validation_test.go`

- [ ] **Step 1: Write failing tests**

```go
package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsSuspiciousDurationUsesStrictThreeSecondThreshold(t *testing.T) {
	tests := []struct {
		name     string
		expected float64
		actual   float64
		want     bool
	}{
		{name: "exactly three seconds is not suspicious", expected: 180, actual: 183, want: false},
		{name: "more than three seconds is suspicious", expected: 180, actual: 183.01, want: true},
		{name: "negative delta more than three seconds is suspicious", expected: 180, actual: 176.9, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSuspiciousDuration(tt.expected, tt.actual)
			if got != tt.want {
				t.Fatalf("IsSuspiciousDuration(%v, %v) = %v, want %v", tt.expected, tt.actual, got, tt.want)
			}
		})
	}
}

func TestMoveSuspiciousOriginalAvoidsFilenameCollisions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Song.mp3")
	if err := os.WriteFile(src, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	suspiciousDir := filepath.Join(dir, "Suspicious")
	if err := os.MkdirAll(suspiciousDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(suspiciousDir, "Song.mp3"), []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}

	movedPath, err := MoveSuspiciousOriginal(src, suspiciousDir)
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Base(movedPath) != "Song (1).mp3" {
		t.Fatalf("moved basename = %q, want Song (1).mp3", filepath.Base(movedPath))
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source still exists or stat failed unexpectedly: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run: `go test ./backend -run "Test(IsSuspiciousDuration|MoveSuspiciousOriginal)" -count=1`

Expected: FAIL because `IsSuspiciousDuration` and `MoveSuspiciousOriginal` are undefined.

- [ ] **Step 3: Implement minimal validation helpers**

Add `backend/download_validation.go`:

```go
package backend

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const SuspiciousDurationThresholdSeconds = 3.0

func IsSuspiciousDuration(expectedSeconds, actualSeconds float64) bool {
	if expectedSeconds <= 0 || actualSeconds <= 0 {
		return false
	}
	return math.Abs(actualSeconds-expectedSeconds) > SuspiciousDurationThresholdSeconds
}

func CollisionSafePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func MoveSuspiciousOriginal(srcPath, suspiciousDir string) (string, error) {
	if err := os.MkdirAll(suspiciousDir, 0755); err != nil {
		return "", err
	}
	targetPath := CollisionSafePath(filepath.Join(suspiciousDir, filepath.Base(srcPath)))
	if err := MoveFileWithFallback(srcPath, targetPath); err != nil {
		return "", err
	}
	return targetPath, nil
}
```

- [ ] **Step 4: Run test to verify GREEN**

Run: `go test ./backend -run "Test(IsSuspiciousDuration|MoveSuspiciousOriginal)" -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/download_validation.go backend/download_validation_test.go
git commit -m "feat: add suspicious duration helpers"
```

## Task 2: Backend Metadata Payload and Refresh

**Files:**
- Create: `backend/track_metadata_update.go`
- Test: `backend/track_metadata_update_test.go`
- Modify: `app_download_helpers.go`
- Create: `app_metadata_update.go`

- [ ] **Step 1: Write failing metadata payload test**

```go
package backend

import "testing"

func TestBuildTrackMetadataPayloadMapsRequestFields(t *testing.T) {
	req := TrackMetadataUpdateRequest{
		TrackID:      "spotify-track",
		TrackName:    "Title",
		ArtistName:   "Artist",
		AlbumName:    "Album",
		AlbumArtist:  "Album Artist",
		ReleaseDate:  "2026-06-09",
		TrackNumber:  7,
		TotalTracks:  12,
		DiscNumber:   2,
		TotalDiscs:   3,
		Copyright:    "Copyright",
		Publisher:    "Publisher",
		ISRC:         "ISRC123",
		Genre:        "Pop",
	}

	got := BuildTrackMetadataPayload(req)

	if got.Title != req.TrackName || got.Artist != req.ArtistName || got.Album != req.AlbumName {
		t.Fatalf("basic metadata mismatch: %#v", got)
	}
	if got.URL != "https://open.spotify.com/track/spotify-track" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.TrackNumber != 7 || got.TotalTracks != 12 || got.DiscNumber != 2 || got.TotalDiscs != 3 {
		t.Fatalf("number metadata mismatch: %#v", got)
	}
	if got.ISRC != req.ISRC || got.Genre != req.Genre {
		t.Fatalf("extended metadata mismatch: %#v", got)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run: `go test ./backend -run TestBuildTrackMetadataPayloadMapsRequestFields -count=1`

Expected: FAIL because the request type and builder are undefined.

- [ ] **Step 3: Implement metadata update types and payload builder**

Add `backend/track_metadata_update.go` with `TrackMetadataUpdateRequest`, `TrackMetadataUpdateResult`, `BuildTrackMetadataPayload`, and `UpdateTrackMetadata`. `UpdateTrackMetadata` should fetch cover art when requested, fetch genre/ISRC through existing helpers when `EmbedGenre` is true, fetch lyrics through `LyricsClient` when `EmbedLyrics` is true, call `EmbedMetadata`, then call `FinalizeTaggedAudioFile`.

- [ ] **Step 4: Run metadata test**

Run: `go test ./backend -run TestBuildTrackMetadataPayloadMapsRequestFields -count=1`

Expected: PASS.

- [ ] **Step 5: Add Wails-facing app methods**

Add `app_metadata_update.go` exposing:

```go
func (a *App) UpdateTrackMetadata(req backend.TrackMetadataUpdateRequest) (backend.TrackMetadataUpdateResult, error)
func (a *App) UpdateTracksMetadata(reqs []backend.TrackMetadataUpdateRequest) ([]backend.TrackMetadataUpdateResult, error)
```

These methods should call backend functions and never panic on one failed item.

- [ ] **Step 6: Run backend tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/track_metadata_update.go backend/track_metadata_update_test.go app_metadata_update.go
git commit -m "feat: add metadata refresh backend"
```

## Task 3: yt-dlp Runtime Management

**Files:**
- Create: `backend/ytdlp.go`
- Test: `backend/ytdlp_test.go`
- Modify: `app_metadata_update.go`

- [ ] **Step 1: Write failing asset selection tests**

```go
package backend

import "testing"

func TestSelectYtDlpAssetForWindows(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "yt-dlp", BrowserDownloadURL: "linux"},
		{Name: "yt-dlp.exe", BrowserDownloadURL: "windows"},
	}
	got, ok := SelectYtDlpAsset("windows", assets)
	if !ok || got.BrowserDownloadURL != "windows" {
		t.Fatalf("got %#v, %v", got, ok)
	}
}

func TestSelectYtDlpAssetForUnix(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "yt-dlp.exe", BrowserDownloadURL: "windows"},
		{Name: "yt-dlp", BrowserDownloadURL: "unix"},
	}
	got, ok := SelectYtDlpAsset("linux", assets)
	if !ok || got.BrowserDownloadURL != "unix" {
		t.Fatalf("got %#v, %v", got, ok)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run: `go test ./backend -run TestSelectYtDlpAsset -count=1`

Expected: FAIL because types/functions are undefined.

- [ ] **Step 3: Implement yt-dlp module**

Implement `backend/ytdlp.go`:

- `GitHubReleaseAsset`
- `YtDlpStatus`
- `SelectYtDlpAsset`
- `GetYtDlpDir`
- `GetYtDlpPath`
- `CheckYtDlpInstalled`
- `EnsureYtDlpInstalledOrUpdated`
- `DownloadWithYtDlp`

Use `https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest` for latest release metadata. Store binary under the existing app-managed runtime directory returned by `GetFFmpegDir`'s parent pattern.

- [ ] **Step 4: Run yt-dlp tests**

Run: `go test ./backend -run TestSelectYtDlpAsset -count=1`

Expected: PASS.

- [ ] **Step 5: Add app wrappers**

Expose:

```go
func (a *App) CheckYtDlpInstalled() (backend.YtDlpStatus, error)
func (a *App) EnsureYtDlpInstalledOrUpdated() (backend.YtDlpStatus, error)
```

- [ ] **Step 6: Commit**

```bash
git add backend/ytdlp.go backend/ytdlp_test.go app_metadata_update.go
git commit -m "feat: manage yt-dlp runtime"
```

## Task 4: YouTube Redownload Backend

**Files:**
- Modify: `backend/track_metadata_update.go`
- Test: `backend/track_metadata_update_test.go`
- Modify: `app_metadata_update.go`

- [ ] **Step 1: Write failing redownload path test**

Add a test for building a YouTube query:

```go
func TestBuildYtDlpSearchQueryUsesArtistAndTitle(t *testing.T) {
	got := BuildYtDlpSearchQuery("Artist", "Song")
	want := "ytsearch1:Artist - Song audio"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run: `go test ./backend -run TestBuildYtDlpSearchQueryUsesArtistAndTitle -count=1`

Expected: FAIL because function is undefined.

- [ ] **Step 3: Implement redownload request/result**

Add:

- `SuspiciousRedownloadRequest`
- `SuspiciousRedownloadResult`
- `BuildYtDlpSearchQuery`
- `RedownloadSuspiciousTrackFromYouTube`
- `RedownloadSuspiciousTracksFromYouTube`

Each redownload moves the original into `Suspicious`, downloads to the original base path, embeds metadata, probes duration, and returns `replaced`, `still_suspicious`, or `failed`.

- [ ] **Step 4: Add app wrapper**

Expose:

```go
func (a *App) RedownloadSuspiciousTracksFromYouTube(reqs []backend.SuspiciousRedownloadRequest) ([]backend.SuspiciousRedownloadResult, error)
```

- [ ] **Step 5: Run backend tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/track_metadata_update.go backend/track_metadata_update_test.go app_metadata_update.go
git commit -m "feat: redownload suspicious tracks with yt-dlp"
```

## Task 5: Download Response Validation

**Files:**
- Modify: `app.go`
- Modify: `frontend/src/types/api.ts`
- Modify: `frontend/src/lib/api.ts`

- [ ] **Step 1: Add backend test for validation helper**

Add an app-level helper test that constructs a downloaded duration and Spotify duration and expects suspicious details when delta is greater than 3 seconds.

- [ ] **Step 2: Run RED**

Run: `go test . -run TestBuildDownloadValidationResult -count=1`

Expected: FAIL.

- [ ] **Step 3: Implement response fields**

Extend `DownloadResponse`:

- `ExpectedDurationSeconds float64`
- `ActualDurationSeconds float64`
- `DurationDeltaSeconds float64`
- `Suspicious bool`
- `ValidationWarning string`

After successful new downloads, call `backend.GetAudioDuration` and compare against `req.Duration`.

- [ ] **Step 4: Run tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app.go app_download_helpers.go app_download_helpers_test.go frontend/src/types/api.ts frontend/src/lib/api.ts
git commit -m "feat: report suspicious download validation"
```

## Task 6: Frontend Settings and Summary Model

**Files:**
- Modify: `frontend/src/lib/settings.ts`
- Create: `frontend/src/lib/batch-summary.ts`
- Modify: `frontend/src/types/api.ts`
- Modify: `frontend/src/components/SettingsPage.tsx`

- [ ] **Step 1: Add setting type/default**

Add `updateMetadataForExistingFiles: boolean` to `Settings`, `DEFAULT_SETTINGS`, and `normalizeSettingsData`.

- [ ] **Step 2: Add summary helper**

Create `frontend/src/lib/batch-summary.ts` exporting:

- `BatchTrackResult`
- `BatchDownloadSummary`
- `createEmptyBatchSummary`
- `addBatchResult`

- [ ] **Step 3: Add Settings UI switch**

Add a switch labeled `Update Existing Metadata` near metadata embed switches.

- [ ] **Step 4: Run frontend build**

Run: `pnpm --dir frontend run build`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/lib/settings.ts frontend/src/lib/batch-summary.ts frontend/src/types/api.ts frontend/src/components/SettingsPage.tsx
git commit -m "feat: add metadata refresh setting and summary model"
```

## Task 7: Summary Dialog and Collection Buttons

**Files:**
- Create: `frontend/src/components/BatchDownloadSummaryDialog.tsx`
- Modify: `frontend/src/components/PlaylistInfo.tsx`
- Modify: `frontend/src/components/AlbumInfo.tsx`
- Modify: `frontend/src/components/ArtistInfo.tsx`
- Modify: `frontend/src/components/TrackList.tsx`
- Modify: `frontend/src/App.tsx`

- [ ] **Step 1: Implement dialog component**

Dialog shows counters and detail sections for failures, skipped, metadata updated, suspicious, and YouTube results. If suspicious entries exist, it renders `Redownload suspicious from YouTube`.

- [ ] **Step 2: Wire collection props**

Add `onUpdateAllMetadata` and `isBulkUpdatingMetadata` props to collection components. Add an icon-only outline button with tooltip `Update All Metadata`.

- [ ] **Step 3: Add suspicious row status**

Pass a `suspiciousTracks` map/set into `TrackList` and show `AlertTriangle` where appropriate.

- [ ] **Step 4: Run frontend build**

Run: `pnpm --dir frontend run build`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/components/BatchDownloadSummaryDialog.tsx frontend/src/components/PlaylistInfo.tsx frontend/src/components/AlbumInfo.tsx frontend/src/components/ArtistInfo.tsx frontend/src/components/TrackList.tsx frontend/src/App.tsx
git commit -m "feat: add batch summary UI"
```

## Task 8: Frontend Batch Pipeline Wiring

**Files:**
- Modify: `frontend/src/hooks/useDownload.ts`
- Modify: `frontend/src/lib/api.ts`
- Modify: `frontend/src/types/api.ts`
- Modify: `frontend/src/App.tsx`

- [ ] **Step 1: Add API wrappers**

Wrap `UpdateTrackMetadata`, `UpdateTracksMetadata`, `RedownloadSuspiciousTracksFromYouTube`, and yt-dlp status methods in `frontend/src/lib/api.ts`.

- [ ] **Step 2: Wire metadata refresh for existing files**

In `handleDownloadAll` and `handleDownloadSelected`, if `settings.updateMetadataForExistingFiles` is true, call backend metadata update for existing files instead of only skipping.

- [ ] **Step 3: Wire Update All Metadata**

Add `handleUpdateAllMetadata` to `useDownload`, accepting tracks, collection name, and album flag. It performs existence checks, updates existing files, records missing files as skipped/missing, and opens the summary.

- [ ] **Step 4: Wire suspicious status**

When a normal download response has `suspicious`, add it to `suspiciousTracks` and summary details.

- [ ] **Step 5: Wire YouTube redownload**

Add `handleRedownloadSuspiciousFromYouTube`, pass suspicious descriptors to backend, merge returned results into summary, and update track status sets.

- [ ] **Step 6: Run frontend build**

Run: `pnpm --dir frontend run build`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add frontend/src/hooks/useDownload.ts frontend/src/lib/api.ts frontend/src/types/api.ts frontend/src/App.tsx
git commit -m "feat: wire integrated batch pipeline"
```

## Task 9: Generated Wails Bindings and Final Verification

**Files:**
- Modify generated files under `frontend/wailsjs/` if Wails generation updates them.

- [ ] **Step 1: Regenerate Wails bindings**

Run: `wails generate module`

Expected: generated JS/TS model files include new app methods and request/response types.

- [ ] **Step 2: Run backend test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 3: Run frontend build**

Run: `pnpm --dir frontend run build`

Expected: PASS.

- [ ] **Step 4: Run full app build if practical**

Run: `wails build`

Expected: build succeeds. If local toolchain lacks Wails, record the blocker and keep `go test` plus frontend build as verification.

- [ ] **Step 5: Commit generated bindings and fixes**

```bash
git add .
git commit -m "chore: regenerate wails bindings"
```

## Self-Review

- Spec coverage: metadata refresh, bulk metadata button, suspicious duration threshold, summary UI, suspicious file moves, yt-dlp management, YouTube redownload, and verification are each represented in tasks.
- Placeholder scan: no `TODO`/`TBD` placeholders are intentionally left in the plan.
- Type consistency: backend request names are introduced before frontend wrappers use them.
