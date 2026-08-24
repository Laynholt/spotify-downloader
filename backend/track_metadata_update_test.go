package backend

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildTrackMetadataPayloadMapsRequestFields(t *testing.T) {
	req := TrackMetadataUpdateRequest{
		TrackID: "spotify-track", TrackName: "Title", ArtistName: "Artist", AlbumName: "Album", AlbumArtist: "Album Artist",
		ReleaseDate: "2026-06-09", TrackNumber: 7, TotalTracks: 12, DiscNumber: 2, TotalDiscs: 3,
		Copyright: "Copyright", Publisher: "Publisher", ISRC: "ISRC123", Genre: "Pop",
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

func TestBuildYtDlpSearchQueryUsesArtistAndTitle(t *testing.T) {
	if got, want := BuildYtDlpSearchQuery("Artist", "Song"), "ytsearch1:Artist - Song audio"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

type replacementCallCounts struct {
	download, metadata, finalize, probe, move, publish, restore int
}

type replacementFixture struct {
	t               *testing.T
	request         SuspiciousRedownloadRequest
	deps            suspiciousRedownloadDeps
	jobTempDir      string
	youtubeDir      string
	keepPath        string
	originalPath    string
	replacementPath string
	backupPath      string
	stagedPath      string
	expectOriginal  bool
	calls           replacementCallCounts
	order           []string
}

func newReplacementFixture(t *testing.T) *replacementFixture {
	t.Helper()
	root := t.TempDir()
	runTempDir := filepath.Join(root, "run")
	collectionDir := filepath.Join(root, "Playlist")
	originalPath := filepath.Join(collectionDir, "Song.mp3")
	backupPath := filepath.Join(collectionDir, SuspiciousTracksDirName, "Song.mp3")
	jobTempDir := filepath.Join(runTempDir, sanitizeTrackPipelineID("youtube-job"))
	youtubeDir := filepath.Join(jobTempDir, "youtube")
	keepPath := filepath.Join(runTempDir, "keep.txt")
	if err := os.MkdirAll(collectionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runTempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepPath, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, RuntimePaths{RunTempDir: runTempDir})

	f := &replacementFixture{
		t: t, jobTempDir: jobTempDir, youtubeDir: youtubeDir, keepPath: keepPath,
		originalPath: originalPath, replacementPath: originalPath, backupPath: backupPath, expectOriginal: true,
		request: SuspiciousRedownloadRequest{
			QueueID: "playlist:p1", JobID: "youtube-job", OriginalFilePath: originalPath, ReplacementFilePath: originalPath,
			CollectionDir: collectionDir, AudioFormat: "mp3",
			Metadata: TrackMetadataUpdateRequest{TrackID: "spotify-track", TrackName: "Song", ArtistName: "Artist", TrackNumber: 301, DiscNumber: 0, Duration: 180000},
		},
	}
	f.deps = suspiciousRedownloadDeps{
		download: func(ctx context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
			f.record("download")
			f.captureOperationDir(req.OutputBase)
			f.assertOriginalUntouched()
			if err := ctx.Err(); err != nil {
				return YtDlpDownloadResult{Stage: YtDlpStageDownload, Error: err.Error()}
			}
			f.stagedPath = req.OutputBase + ".mp3"
			if err := os.MkdirAll(filepath.Dir(f.stagedPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.stagedPath, []byte("replacement"), 0o644); err != nil {
				t.Fatal(err)
			}
			return YtDlpDownloadResult{Success: true, File: f.stagedPath, Stage: YtDlpStageConversion, Diagnostics: "yt diagnostics"}
		},
		prepareMetadata: func(ctx context.Context, req TrackMetadataUpdateRequest, tempDir string) (Metadata, string, error) {
			f.record("metadata")
			f.assertOriginalUntouched()
			if err := ctx.Err(); err != nil {
				return Metadata{}, "", err
			}
			if req.FilePath != f.stagedPath || tempDir != f.youtubeDir {
				t.Fatalf("metadata inputs = file %q temp %q, want %q / %q", req.FilePath, tempDir, f.stagedPath, f.youtubeDir)
			}
			if req.TrackNumber != 1 || req.DiscNumber != 1 {
				t.Fatalf("fallback numbers = track %d disc %d, want 1/1", req.TrackNumber, req.DiscNumber)
			}
			return BuildTrackMetadataPayload(req), "", nil
		},
		finalize: func(ctx context.Context, path string, _ Metadata, _ string) error {
			f.record("finalize")
			f.assertOriginalUntouched()
			if path != f.stagedPath {
				t.Fatalf("finalize path = %q, want %q", path, f.stagedPath)
			}
			return ctx.Err()
		},
		probeDuration: func(ctx context.Context, path string) (float64, error) {
			f.record("probe")
			f.assertOriginalUntouched()
			if path != f.stagedPath {
				t.Fatalf("probe path = %q, want %q", path, f.stagedPath)
			}
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return 180, nil
		},
		moveOriginal: func(src, dir string) (string, error) {
			f.record("move")
			f.assertOriginalUntouched()
			return MoveSuspiciousOriginal(src, dir)
		},
		publish: func(ctx context.Context, src, dst string) error {
			f.record("publish")
			return publishTrackPipelineFile(ctx, src, dst)
		},
		restoreOriginal: func(src, dst string) error {
			f.record("restore")
			return MoveFileWithFallback(src, dst)
		},
	}
	return f
}

func (f *replacementFixture) captureOperationDir(outputBase string) {
	f.youtubeDir = filepath.Dir(outputBase)
	f.jobTempDir = filepath.Dir(f.youtubeDir)
}

func (f *replacementFixture) record(name string) {
	f.order = append(f.order, name)
	switch name {
	case "download":
		f.calls.download++
	case "metadata":
		f.calls.metadata++
	case "finalize":
		f.calls.finalize++
	case "probe":
		f.calls.probe++
	case "move":
		f.calls.move++
	case "publish":
		f.calls.publish++
	case "restore":
		f.calls.restore++
	}
}

func (f *replacementFixture) assertOriginalUntouched() {
	f.t.Helper()
	if !f.expectOriginal {
		assertPathAbsent(f.t, f.originalPath)
		return
	}
	assertFileContents(f.t, f.originalPath, "original")
}

func (f *replacementFixture) moveOriginalToSuspicious() {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.backupPath), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := MoveFileWithFallback(f.originalPath, f.backupPath); err != nil {
		f.t.Fatal(err)
	}
	f.originalPath = f.backupPath
	f.request.OriginalFilePath = f.backupPath
}

func TestYouTubeReplacementFailurePointsPreserveOriginal(t *testing.T) {
	tests := []struct {
		name, failAt, wantStatus, wantStage string
		wantSuccess                         bool
		wantCalls                           replacementCallCounts
	}{
		{name: "yt-dlp failure", failAt: "download", wantStatus: "failed", wantStage: string(YtDlpStageDownload), wantCalls: replacementCallCounts{download: 1}},
		{name: "yt-dlp path outside job temp", failAt: "outside-path", wantStatus: "failed", wantStage: string(YtDlpStageConversion), wantCalls: replacementCallCounts{download: 1}},
		{name: "metadata failure", failAt: "metadata", wantStatus: "failed", wantStage: "metadata", wantCalls: replacementCallCounts{download: 1, metadata: 1}},
		{name: "finalization failure", failAt: "finalize", wantStatus: "failed", wantStage: "finalization", wantCalls: replacementCallCounts{download: 1, metadata: 1, finalize: 1}},
		{name: "duration probe failure", failAt: "probe", wantStatus: "failed", wantStage: "validation", wantCalls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1}},
		{name: "suspicious validation", failAt: "suspicious", wantStatus: "still_suspicious", wantStage: "validation", wantCalls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1}},
		{name: "move-original failure", failAt: "move", wantStatus: "failed", wantStage: "publication", wantCalls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1, move: 1}},
		{name: "publication failure before move", failAt: "publish-before-move", wantStatus: "failed", wantStage: "publication", wantCalls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1, publish: 1}},
		{name: "publication failure after move", failAt: "publish-after-move", wantStatus: "failed", wantStage: "publication", wantCalls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1, move: 1, publish: 1, restore: 1}},
		{name: "cancellation", failAt: "cancel", wantStatus: "cancelled", wantStage: string(YtDlpStageDownload), wantCalls: replacementCallCounts{download: 1}},
		{name: "success", wantSuccess: true, wantStatus: "replaced", wantStage: string(YtDlpStageConversion), wantCalls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1, move: 1, publish: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReplacementFixture(t)
			ctx := context.Background()
			switch tt.failAt {
			case "download":
				f.deps.download = func(_ context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
					f.record("download")
					f.captureOperationDir(req.OutputBase)
					f.assertOriginalUntouched()
					return YtDlpDownloadResult{Stage: YtDlpStageDownload, Error: "network lost", Diagnostics: "download details"}
				}
			case "outside-path":
				f.deps.download = func(_ context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
					f.record("download")
					f.captureOperationDir(req.OutputBase)
					f.assertOriginalUntouched()
					f.stagedPath = req.OutputBase + ".mp3"
					if err := os.WriteFile(f.stagedPath, []byte("replacement"), 0o644); err != nil {
						f.t.Fatal(err)
					}
					return YtDlpDownloadResult{Success: true, File: f.originalPath, Stage: YtDlpStageConversion}
				}
			case "metadata":
				f.deps.prepareMetadata = func(context.Context, TrackMetadataUpdateRequest, string) (Metadata, string, error) {
					f.record("metadata")
					f.assertOriginalUntouched()
					return Metadata{}, "", errors.New("metadata unavailable")
				}
			case "finalize":
				f.deps.finalize = func(context.Context, string, Metadata, string) error {
					f.record("finalize")
					f.assertOriginalUntouched()
					return errors.New("tag write failed")
				}
			case "probe":
				f.deps.probeDuration = func(context.Context, string) (float64, error) {
					f.record("probe")
					f.assertOriginalUntouched()
					return 0, errors.New("ffprobe failed")
				}
			case "suspicious":
				f.deps.probeDuration = func(context.Context, string) (float64, error) {
					f.record("probe")
					f.assertOriginalUntouched()
					return 184, nil
				}
			case "move":
				f.deps.moveOriginal = func(string, string) (string, error) {
					f.record("move")
					f.assertOriginalUntouched()
					return "", errors.New("move denied")
				}
			case "publish-before-move":
				f.moveOriginalToSuspicious()
				f.deps.publish = func(context.Context, string, string) error { f.record("publish"); return errors.New("disk full") }
			case "publish-after-move":
				f.deps.publish = func(context.Context, string, string) error { f.record("publish"); return errors.New("disk full") }
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				f.deps.download = func(inner context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
					f.record("download")
					f.captureOperationDir(req.OutputBase)
					f.assertOriginalUntouched()
					cancel()
					<-inner.Done()
					return YtDlpDownloadResult{Stage: YtDlpStageDownload, Error: inner.Err().Error()}
				}
			}

			got := runSuspiciousRedownloadWithDeps(ctx, f.request, f.deps)
			if got.Success != tt.wantSuccess || got.Status != tt.wantStatus || (tt.wantStage != "" && got.Stage != tt.wantStage) {
				t.Fatalf("result = %#v, want success=%v status=%q stage=%q", got, tt.wantSuccess, tt.wantStatus, tt.wantStage)
			}
			if f.calls != tt.wantCalls {
				t.Fatalf("calls = %#v, want %#v; order %v", f.calls, tt.wantCalls, f.order)
			}
			assertPathAbsent(t, f.jobTempDir)
			assertFileContents(t, f.keepPath, "keep")
			if tt.wantSuccess {
				assertFileContents(t, f.replacementPath, "replacement")
				assertFileContents(t, f.backupPath, "original")
				if got.MovedOriginalPath != f.backupPath || got.ReplacementPath != f.replacementPath || got.Diagnostics != "yt diagnostics" {
					t.Fatalf("success result = %#v", got)
				}
			} else {
				f.assertOriginalUntouched()
			}
			if tt.failAt == "suspicious" && (!got.Suspicious || got.DurationDeltaSeconds != 4) {
				t.Fatalf("validation result = %#v", got)
			}
			if tt.failAt == "download" && got.Diagnostics != "download details" {
				t.Fatalf("diagnostics = %q", got.Diagnostics)
			}
			if tt.failAt == "publish-after-move" {
				assertPathAbsent(t, f.backupPath)
			}
			if tt.failAt == "publish-before-move" {
				assertPathAbsent(t, f.replacementPath)
			}
		})
	}
}

func TestYouTubeReplacementCancellationDuringDurationProbeWinsBeforeClassification(t *testing.T) {
	f := newReplacementFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	f.deps.probeDuration = func(inner context.Context, _ string) (float64, error) {
		f.record("probe")
		f.assertOriginalUntouched()
		cancel()
		<-inner.Done()
		return 184, nil
	}

	got := runSuspiciousRedownloadWithDeps(ctx, f.request, f.deps)
	if got.Success || got.Status != "cancelled" || got.Stage != "validation" || got.Suspicious {
		t.Fatalf("result = %#v", got)
	}
	wantCalls := replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1}
	if f.calls != wantCalls {
		t.Fatalf("calls = %#v, want %#v", f.calls, wantCalls)
	}
	assertFileContents(t, f.originalPath, "original")
	assertPathAbsent(t, f.backupPath)
	assertPathAbsent(t, f.jobTempDir)
}

func TestYouTubeReplacementPublishesAlreadyMovedAndMissingOriginals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*replacementFixture)
		calls replacementCallCounts
		moved bool
	}{
		{
			name:  "already moved original",
			setup: func(f *replacementFixture) { f.moveOriginalToSuspicious() },
			calls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1, publish: 1},
			moved: true,
		},
		{
			name: "failed track without original",
			setup: func(f *replacementFixture) {
				if err := os.Remove(f.originalPath); err != nil {
					f.t.Fatal(err)
				}
				f.expectOriginal = false
				f.request.OriginalFilePath = ""
			},
			calls: replacementCallCounts{download: 1, metadata: 1, finalize: 1, probe: 1, publish: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newReplacementFixture(t)
			tt.setup(f)
			got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
			if !got.Success || got.Status != "replaced" || f.calls != tt.calls {
				t.Fatalf("result = %#v, calls = %#v", got, f.calls)
			}
			assertFileContents(t, f.replacementPath, "replacement")
			if tt.moved {
				assertFileContents(t, f.backupPath, "original")
				if got.MovedOriginalPath != f.backupPath {
					t.Fatalf("moved original = %q, want %q", got.MovedOriginalPath, f.backupPath)
				}
			} else {
				assertPathAbsent(t, f.backupPath)
				if got.MovedOriginalPath != "" {
					t.Fatalf("moved original = %q, want empty", got.MovedOriginalPath)
				}
			}
			assertPathAbsent(t, f.jobTempDir)
		})
	}
}

func TestYouTubeReplacementRestoresOriginalAfterPublishFailure(t *testing.T) {
	f := newReplacementFixture(t)
	f.deps.publish = func(context.Context, string, string) error {
		f.record("publish")
		if err := os.WriteFile(f.replacementPath, []byte("partial"), 0o644); err != nil {
			t.Fatal(err)
		}
		return errors.New("disk full")
	}
	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if got.Success || got.Stage != "publication" {
		t.Fatalf("result = %#v", got)
	}
	assertFileContents(t, f.originalPath, "original")
	assertPathAbsent(t, f.backupPath)
}

func TestYouTubeReplacementReportsRollbackFailure(t *testing.T) {
	f := newReplacementFixture(t)
	f.deps.publish = func(context.Context, string, string) error { f.record("publish"); return errors.New("disk full") }
	f.deps.restoreOriginal = func(string, string) error { f.record("restore"); return errors.New("restore denied") }
	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if got.Success || got.Status != "failed" || got.Stage != "rollback" || !strings.Contains(got.Error, "disk full") || !strings.Contains(got.Error, "restore denied") {
		t.Fatalf("result = %#v", got)
	}
	assertPathAbsent(t, f.originalPath)
	assertFileContents(t, f.backupPath, "original")
}

func TestYouTubeReplacementRestoresExistingDistinctDestinationAfterPartialPublishFailure(t *testing.T) {
	f := newReplacementFixture(t)
	destination := filepath.Join(filepath.Dir(f.originalPath), "Published.mp3")
	if err := os.WriteFile(destination, []byte("existing destination"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.replacementPath = destination
	f.request.ReplacementFilePath = destination
	f.deps.publish = func(context.Context, string, string) error {
		f.record("publish")
		if err := os.WriteFile(destination, []byte("partial replacement"), 0o644); err != nil {
			t.Fatal(err)
		}
		return errors.New("disk full")
	}

	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if got.Success || got.Stage != "publication" {
		t.Fatalf("result = %#v", got)
	}
	assertFileContents(t, f.originalPath, "original")
	assertFileContents(t, destination, "existing destination")
	assertPathAbsent(t, f.backupPath)
	assertNoRollbackFiles(t, destination)
}

func TestYouTubeReplacementJoinsBothDestinationAndOriginalRestoreFailures(t *testing.T) {
	f := newReplacementFixture(t)
	destination := filepath.Join(filepath.Dir(f.originalPath), "Published.mp3")
	if err := os.WriteFile(destination, []byte("existing destination"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.replacementPath = destination
	f.request.ReplacementFilePath = destination
	f.deps.publish = func(context.Context, string, string) error {
		f.record("publish")
		return errors.New("publish failed")
	}
	f.deps.restoreOriginal = func(_, dst string) error {
		f.record("restore")
		switch filepath.Clean(dst) {
		case filepath.Clean(destination):
			return errors.New("destination restore denied")
		case filepath.Clean(f.originalPath):
			return errors.New("original restore denied")
		default:
			return errors.New("unexpected restore destination")
		}
	}

	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if got.Success || got.Stage != "rollback" || !strings.Contains(got.Error, "publish failed") ||
		!strings.Contains(got.Error, "destination restore denied") || !strings.Contains(got.Error, "original restore denied") {
		t.Fatalf("result = %#v", got)
	}
	if f.calls.restore != 2 {
		t.Fatalf("restore calls = %d, want 2", f.calls.restore)
	}
}

func TestYouTubeReplacementRestoresSameDestinationAlreadyInSuspicious(t *testing.T) {
	f := newReplacementFixture(t)
	f.moveOriginalToSuspicious()
	f.replacementPath = f.originalPath
	f.request.ReplacementFilePath = f.originalPath
	f.deps.publish = func(context.Context, string, string) error {
		f.record("publish")
		if err := os.WriteFile(f.replacementPath, []byte("partial replacement"), 0o644); err != nil {
			t.Fatal(err)
		}
		return errors.New("disk full")
	}

	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if got.Success || got.Stage != "publication" {
		t.Fatalf("result = %#v", got)
	}
	assertFileContents(t, f.originalPath, "original")
	assertNoRollbackFiles(t, f.replacementPath)
}

func TestYouTubeReplacementPreservesSameDestinationAlreadyInSuspiciousOnSuccess(t *testing.T) {
	f := newReplacementFixture(t)
	f.moveOriginalToSuspicious()
	destination := f.originalPath
	if runtime.GOOS == "windows" {
		destination = strings.ToUpper(destination)
	}
	f.replacementPath = destination
	f.request.ReplacementFilePath = destination

	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if !got.Success || got.Status != "replaced" || got.MovedOriginalPath == "" {
		t.Fatalf("result = %#v", got)
	}
	assertFileContents(t, destination, "replacement")
	assertFileContents(t, got.MovedOriginalPath, "original")
	assertNoRollbackFiles(t, destination)
}

func TestSameFilesystemPathRecognizesAliases(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "Song.mp3")
	if err := os.WriteFile(original, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	hardLink := filepath.Join(dir, "Song-link.mp3")
	if err := os.Link(original, hardLink); err != nil {
		t.Fatal(err)
	}
	if !sameFilesystemPath(original, hardLink) {
		t.Fatal("hard links to the same file were treated as distinct")
	}
	if !sameFilesystemPath(original, filepath.Join(dir, ".", "Song.mp3")) {
		t.Fatal("clean aliases to the same path were treated as distinct")
	}
	if runtime.GOOS == "windows" && !sameFilesystemPath(original, strings.ToUpper(original)) {
		t.Fatal("Windows case alias was treated as distinct")
	}
}

func TestYouTubeReplacementPreservesOriginalWhenDestinationIsHardLinkAlias(t *testing.T) {
	f := newReplacementFixture(t)
	destination := filepath.Join(filepath.Dir(f.originalPath), "Song-alias.mp3")
	if err := os.Link(f.originalPath, destination); err != nil {
		t.Fatal(err)
	}
	f.replacementPath = destination
	f.request.ReplacementFilePath = destination
	f.deps.publish = func(_ context.Context, src, dst string) error {
		f.record("publish")
		if err := os.WriteFile(dst, []byte("replacement"), 0o644); err != nil {
			return err
		}
		return os.Remove(src)
	}

	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if !got.Success || got.Status != "replaced" {
		t.Fatalf("result = %#v", got)
	}
	assertFileContents(t, destination, "replacement")
	assertFileContents(t, got.MovedOriginalPath, "original")
	assertNoRollbackFiles(t, destination)
}

func TestYouTubeReplacementUsesUniqueOperationDirectoriesForDuplicateEmptyAndCollidingIDs(t *testing.T) {
	root := t.TempDir()
	runTempDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runTempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(runTempDir, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, RuntimePaths{RunTempDir: runTempDir})

	jobIDs := []string{"duplicate", "duplicate", "", "", "a:b", "a?b"}
	started := make(chan string, len(jobIDs))
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i, jobID := range jobIDs {
		deps := defaultSuspiciousRedownloadDeps
		deps.download = func(_ context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
			started <- req.OutputBase
			<-release
			return YtDlpDownloadResult{Stage: YtDlpStageDownload, Error: "stop after path capture"}
		}
		req := SuspiciousRedownloadRequest{
			JobID: jobID, ReplacementFilePath: filepath.Join(root, "destinations", fmt.Sprintf("%d.mp3", i)),
			Metadata: TrackMetadataUpdateRequest{TrackID: "same-track", ArtistName: "Artist", TrackName: "Song"},
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			runSuspiciousRedownloadWithDeps(context.Background(), req, deps)
		}()
	}

	operationDirs := make(map[string]struct{}, len(jobIDs))
	for range jobIDs {
		select {
		case outputBase := <-started:
			operationDir := filepath.Dir(filepath.Dir(outputBase))
			if filepath.Dir(operationDir) != filepath.Clean(runTempDir) {
				t.Fatalf("operation directory %q is outside run temp %q", operationDir, runTempDir)
			}
			operationDirs[operationDir] = struct{}{}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for operation directories")
		}
	}
	if len(operationDirs) != len(jobIDs) {
		t.Fatalf("unique operation directories = %d, want %d: %v", len(operationDirs), len(jobIDs), operationDirs)
	}
	close(release)
	wg.Wait()
	for operationDir := range operationDirs {
		assertPathAbsent(t, operationDir)
	}
	assertFileContents(t, keepPath, "keep")
}

func TestYouTubeReplacementRejectsDestinationInsideOperationDirectoryBeforeDownload(t *testing.T) {
	root := t.TempDir()
	runTempDir := filepath.Join(root, "run")
	operationDir := filepath.Join(runTempDir, "controlled-operation")
	if err := os.MkdirAll(runTempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keepPath := filepath.Join(runTempDir, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, RuntimePaths{RunTempDir: runTempDir})

	downloadCalled := false
	deps := defaultSuspiciousRedownloadDeps
	deps.makeOperationDir = func(_, _ string) (string, error) {
		if err := os.Mkdir(operationDir, 0o755); err != nil {
			return "", err
		}
		return operationDir, nil
	}
	deps.download = func(context.Context, YtDlpDownloadRequest) YtDlpDownloadResult {
		downloadCalled = true
		return YtDlpDownloadResult{Success: true}
	}
	req := SuspiciousRedownloadRequest{
		JobID: "job", ReplacementFilePath: filepath.Join(operationDir, "must-not-publish.mp3"),
		Metadata: TrackMetadataUpdateRequest{TrackID: "track", ArtistName: "Artist", TrackName: "Song"},
	}

	got := runSuspiciousRedownloadWithDeps(context.Background(), req, deps)
	if got.Success || got.Stage != "preparation" || !strings.Contains(got.Error, "operation directory") {
		t.Fatalf("result = %#v", got)
	}
	if downloadCalled {
		t.Fatal("download started for a destination inside the operation directory")
	}
	assertPathAbsent(t, operationDir)
	assertFileContents(t, keepPath, "keep")
}

func TestYouTubeReplacementRejectsExternalStagedMediaSymlinkBeforeMetadata(t *testing.T) {
	f := newReplacementFixture(t)
	externalPath := filepath.Join(filepath.Dir(filepath.Dir(f.originalPath)), "external.mp3")
	if err := os.WriteFile(externalPath, []byte("external audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.deps.download = func(_ context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
		f.record("download")
		f.captureOperationDir(req.OutputBase)
		f.assertOriginalUntouched()
		f.stagedPath = req.OutputBase + ".mp3"
		if err := os.Symlink(externalPath, f.stagedPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return YtDlpDownloadResult{Success: true, File: f.stagedPath, Stage: YtDlpStageConversion}
	}

	got := runSuspiciousRedownloadWithDeps(context.Background(), f.request, f.deps)
	if got.Success || got.Stage != string(YtDlpStageConversion) || !strings.Contains(strings.ToLower(got.Error), "symlink") {
		t.Fatalf("result = %#v", got)
	}
	if f.calls != (replacementCallCounts{download: 1}) {
		t.Fatalf("calls = %#v, want download only", f.calls)
	}
	assertFileContents(t, f.originalPath, "original")
	assertFileContents(t, externalPath, "external audio")
	assertPathAbsent(t, f.jobTempDir)
}

func TestYouTubeReplacementSerializesOnlyPublicationForConcurrentSameOriginal(t *testing.T) {
	root := t.TempDir()
	runTempDir := filepath.Join(root, "run")
	collectionDir := filepath.Join(root, "Playlist")
	originalPath := filepath.Join(collectionDir, "Song.mp3")
	if err := os.MkdirAll(runTempDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(collectionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	installRuntimePathsForTest(t, RuntimePaths{RunTempDir: runTempDir})

	type labeledResult struct {
		label  string
		result SuspiciousRedownloadResult
	}
	probeReached := make(chan string, 2)
	publishReached := make(chan string)
	releaseFirstPublish := make(chan struct{})
	results := make(chan labeledResult, 2)
	run := func(label string) {
		deps := defaultSuspiciousRedownloadDeps
		deps.download = func(_ context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
			path := req.OutputBase + ".mp3"
			if err := os.WriteFile(path, []byte(label), 0o644); err != nil {
				t.Errorf("write %s staged media: %v", label, err)
				return YtDlpDownloadResult{Stage: YtDlpStageDownload, Error: err.Error()}
			}
			return YtDlpDownloadResult{Success: true, File: path, Stage: YtDlpStageConversion}
		}
		deps.prepareMetadata = func(context.Context, TrackMetadataUpdateRequest, string) (Metadata, string, error) {
			return Metadata{Title: "Song"}, "", nil
		}
		deps.finalize = func(context.Context, string, Metadata, string) error { return nil }
		deps.probeDuration = func(context.Context, string) (float64, error) {
			probeReached <- label
			return 180, nil
		}
		deps.publish = func(ctx context.Context, src, dst string) error {
			publishReached <- label
			if label == "first" {
				<-releaseFirstPublish
			}
			return publishTrackPipelineFile(ctx, src, dst)
		}
		req := SuspiciousRedownloadRequest{
			JobID: label, OriginalFilePath: originalPath, ReplacementFilePath: originalPath, CollectionDir: collectionDir,
			Metadata: TrackMetadataUpdateRequest{TrackID: label, ArtistName: "Artist", TrackName: "Song", Duration: 180000},
		}
		results <- labeledResult{label: label, result: runSuspiciousRedownloadWithDeps(context.Background(), req, deps)}
	}

	go run("first")
	waitForLabel(t, probeReached, "first")
	waitForLabel(t, publishReached, "first")
	go run("second")
	waitForLabel(t, probeReached, "second")

	publicationLocked := !youtubePublicationMu.TryLock()
	if !publicationLocked {
		youtubePublicationMu.Unlock()
	}
	close(releaseFirstPublish)
	waitForLabel(t, publishReached, "second")

	collected := make([]labeledResult, 0, 2)
	for len(collected) < 2 {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent replacements")
		}
	}
	if !publicationLocked {
		t.Fatal("first publication did not hold the publication lock")
	}

	byLabel := make(map[string]SuspiciousRedownloadResult, 2)
	for _, item := range collected {
		byLabel[item.label] = item.result
	}
	if !byLabel["first"].Success || !byLabel["second"].Success {
		t.Fatalf("results = %#v", byLabel)
	}
	if sameFilesystemPath(byLabel["first"].MovedOriginalPath, byLabel["second"].MovedOriginalPath) {
		t.Fatalf("original backups collided: %#v", byLabel)
	}
	assertFileContents(t, originalPath, "second")
	assertFileContents(t, byLabel["first"].MovedOriginalPath, "original")
	assertFileContents(t, byLabel["second"].MovedOriginalPath, "first")
	assertNoRollbackFiles(t, originalPath)
}

func waitForLabel(t *testing.T, labels <-chan string, want string) {
	t.Helper()
	select {
	case got := <-labels:
		if got != want {
			t.Fatalf("label = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
	}
}

func assertNoRollbackFiles(t *testing.T, destination string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(destination), "."+filepath.Base(destination)+".youtube-rollback-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("rollback files remain: %v", matches)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("contents of %q = %q, want %q", path, data, want)
	}
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("path %q exists or stat failed unexpectedly: %v", path, err)
	}
}
