package backend

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
				f.deps.download = func(context.Context, YtDlpDownloadRequest) YtDlpDownloadResult {
					f.record("download")
					f.assertOriginalUntouched()
					return YtDlpDownloadResult{Stage: YtDlpStageDownload, Error: "network lost", Diagnostics: "download details"}
				}
			case "outside-path":
				f.deps.download = func(_ context.Context, req YtDlpDownloadRequest) YtDlpDownloadResult {
					f.record("download")
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
				f.deps.download = func(inner context.Context, _ YtDlpDownloadRequest) YtDlpDownloadResult {
					f.record("download")
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
