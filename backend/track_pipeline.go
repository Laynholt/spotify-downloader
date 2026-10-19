package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	trackPipelineLyricsBudget = 5 * time.Second
	trackPipelineBranchBudget = 10 * time.Second
)

// TrackPipelineRequest is the complete input needed by one track worker. The
// basic identity is carried in BaseMetadata so the pipeline can calculate the
// destination before any optional network branch is started.
type TrackPipelineRequest struct {
	ItemID               string
	TrackID              string
	Force                bool
	OutputDir            string
	AudioFormat          string
	FilenameFormat       string
	IncludeTrackNumber   bool
	Position             int
	UseAlbumTrackNumber  bool
	PlaylistName         string
	PlaylistOwner        string
	UseFirstArtistOnly   bool
	EmbedMaxQualityCover bool
	EmbedGenre           bool
	UseSingleGenre       bool
	EmbedLyrics          bool
	ExpectedDurationMS   int
	CoverURL             string
	BaseMetadata         Metadata
}

// TrackPipelineResult contains terminal output and phase diagnostics. A
// successful result has File set; AlreadyExists results intentionally skip all
// optional branches and have no duration probe.
type TrackPipelineResult struct {
	File            string
	AlreadyExists   bool
	DurationSeconds float64
	Warnings        []string
	CacheOutcomes   map[string]CacheOutcome
	Timings         PipelineTimings
}

// trackPipelineDeps holds every side effect of RunTrackPipeline so backend
// tests can replace them (see installPipelineDepsForTest).
type trackPipelineDeps struct {
	resolveTarget func(ctx context.Context, s *SpotiDownloader, req TrackPipelineRequest) (url, ext string, err error)
	downloadAudio func(ctx context.Context, s *SpotiDownloader, url, base, ext, itemID string) (string, error)
	convertAudio  func(ctx context.Context, path, base, format string) (string, error)
	downloadCover func(ctx context.Context, s *SpotiDownloader, url, dir string, maxQuality bool) (string, error)
	metadata      func(ctx context.Context, cache *MetadataCache, req TaggingMetadataRequest) (taggingMetadataResult, []string, map[string]CacheOutcome)
	lyrics        func(ctx context.Context, req TrackPipelineRequest, expectedDurationMS int) (string, error)
	probeDuration func(ctx context.Context, path string) (float64, error)
	finalizeMedia func(ctx context.Context, path string, metadata Metadata, coverPath string) error
	publish       func(ctx context.Context, src, dst string) error
	now           func() time.Time
	newTimer      func(time.Duration) (<-chan time.Time, func() bool)
	withTimeout   func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	withDeadline  func(context.Context, time.Time) (context.Context, context.CancelFunc)
}

var pipelineDeps = trackPipelineDeps{
	resolveTarget: trackPipelineResolveTarget,
	downloadAudio: func(ctx context.Context, s *SpotiDownloader, url, base, ext, itemID string) (string, error) {
		return s.downloadAudio(ctx, url, base, ext, itemID)
	},
	convertAudio: convertDownloadedAudioContext,
	downloadCover: func(ctx context.Context, s *SpotiDownloader, url, dir string, maxQuality bool) (string, error) {
		return s.downloadCover(ctx, url, dir, maxQuality)
	},
	metadata:      fetchTaggingMetadataWithOutcomes,
	lyrics:        trackPipelineLyrics,
	probeDuration: GetAudioDurationContext,
	finalizeMedia: FinalizeMedia,
	publish:       publishTrackPipelineFile,
	now:           time.Now,
	newTimer: func(d time.Duration) (<-chan time.Time, func() bool) {
		timer := time.NewTimer(d)
		return timer.C, timer.Stop
	},
	withTimeout:  context.WithTimeout,
	withDeadline: context.WithDeadline,
}

type trackPipelineAudioResult struct {
	path         string
	downloadMS   int64
	conversionMS int64
	err          error
}

type trackPipelineCoverResult struct {
	path    string
	err     error
	elapsed int64
}

type trackPipelineMetadataResult struct {
	tagging       taggingMetadataResult
	warnings      []string
	cacheOutcomes map[string]CacheOutcome
	elapsed       int64
	err           error
}

type trackPipelineLyricsResult struct {
	lyrics      string
	err         error
	elapsed     int64
	completedAt time.Time
}

func (s *SpotiDownloader) RunTrackPipeline(ctx context.Context, req TrackPipelineRequest) (result TrackPipelineResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}

	deps := pipelineDeps
	now := deps.now
	pipelineStarted := now()
	warnings := make([]string, 0, 6)
	cacheOutcomes := make(map[string]CacheOutcome)
	result.CacheOutcomes = cacheOutcomes
	defer func() {
		result.Warnings = append([]string(nil), warnings...)
		result.CacheOutcomes = cacheOutcomes
		result.Timings.TotalMS = trackPipelineElapsedMS(now, pipelineStarted)
	}()

	if s == nil {
		s = NewSpotiDownloader("")
	}
	itemID := sanitizeTrackPipelineID(req.ItemID)
	if req.ItemID != "" {
		StartDownloadItem(req.ItemID)
		UpdateItemPhase(req.ItemID, PhasePreparing, "resolving download target")
	}

	outputDir := NormalizePath(req.OutputDir)
	if strings.TrimSpace(outputDir) == "" {
		outputDir = GetDefaultMusicPath()
	}
	if err = os.MkdirAll(outputDir, 0755); err != nil {
		return result, fmt.Errorf("create output directory: %w", err)
	}

	filenameArtist := req.BaseMetadata.Artist
	filenameAlbumArtist := req.BaseMetadata.AlbumArtist
	if req.UseFirstArtistOnly {
		filenameArtist = GetFirstArtist(filenameArtist)
		filenameAlbumArtist = GetFirstArtist(filenameAlbumArtist)
	}
	filenameBase := BuildFilename(req.BaseMetadata.Title, filenameArtist, req.BaseMetadata.Album, filenameAlbumArtist,
		req.BaseMetadata.Date, req.BaseMetadata.DiscNumber, req.FilenameFormat, req.IncludeTrackNumber, req.Position,
		req.UseAlbumTrackNumber, req.PlaylistName, req.PlaylistOwner)
	filenameBase = SanitizeFilename(filenameBase)
	if strings.TrimSpace(filenameBase) == "" {
		return result, errors.New("track filename is empty")
	}
	outputBasePath := filepath.Join(outputDir, filenameBase)

	// Existing-state detection is intentionally before link resolution and all
	// optional branches. A non-empty file is the only valid existing artifact.
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

	if err = ctx.Err(); err != nil {
		return trackPipelineFailure(&result, req, err, warnings)
	}
	lyricsDeadline := pipelineStarted.Add(trackPipelineLyricsBudget)
	downloadURL, fallbackExt, resolveErr := deps.resolveTarget(ctx, s, req)
	if resolveErr != nil {
		return trackPipelineFailure(&result, req, fmt.Errorf("failed to get download link: %w", resolveErr), warnings)
	}
	if strings.TrimSpace(downloadURL) == "" {
		return trackPipelineFailure(&result, req, errors.New("no download link available"), warnings)
	}
	if req.ItemID != "" {
		UpdateItemPhase(req.ItemID, PhaseDownloading, "audio and optional branches running")
	}
	result.Timings.PreparationMS = trackPipelineElapsedMS(now, pipelineStarted)

	paths, pathErr := CurrentRuntimePaths()
	if pathErr != nil {
		return trackPipelineFailure(&result, req, pathErr, warnings)
	}
	trackDir := filepath.Join(paths.RunTempDir, itemID)
	if err = os.MkdirAll(trackDir, 0755); err != nil {
		return trackPipelineFailure(&result, req, fmt.Errorf("create track temp directory: %w", err), warnings)
	}
	// Every branch uses a buffered, single-send channel. Cancellation can
	// therefore release a caller without leaving a worker blocked on send.
	audioCh := make(chan trackPipelineAudioResult, 1)
	coverCh := make(chan trackPipelineCoverResult, 1)
	metadataCh := make(chan trackPipelineMetadataResult, 1)
	lyricsCh := make(chan trackPipelineLyricsResult, 1)
	branchCtx, cancelBranches := context.WithCancel(ctx)
	var branchWG sync.WaitGroup
	defer func() {
		// Cancel all branch contexts before removing staged media, then wait for
		// context-aware workers to finish any in-flight writes. This keeps the
		// per-item runtime directory from being removed while a production
		// branch still owns a staged file.
		cancelBranches()
		branchWG.Wait()
		// A lyrics result that completed after the absolute deadline is already
		// semantically discarded; drain its single buffered send before cleanup.
		select {
		case <-lyricsCh:
		default:
		}
		_ = os.RemoveAll(trackDir)
	}()

	coverStartedAt := now()
	metadataStartedAt := now()
	lyricsStartedAt := now()
	coverCtx, cancelCover := deps.withTimeout(branchCtx, trackPipelineBranchBudget)
	defer cancelCover()
	metadataCtx, cancelMetadata := deps.withTimeout(branchCtx, trackPipelineBranchBudget)
	defer cancelMetadata()
	lyricsCtx, cancelLyrics := deps.withDeadline(branchCtx, lyricsDeadline)
	defer cancelLyrics()

	audioBasePath := filepath.Join(trackDir, filenameBase)
	branchWG.Add(1)
	go func() {
		defer branchWG.Done()
		started := now()
		path, audioErr := deps.downloadAudio(branchCtx, s, downloadURL, audioBasePath, fallbackExt, req.ItemID)
		downloadMS := trackPipelineElapsedMS(now, started)
		if audioErr != nil {
			audioCh <- trackPipelineAudioResult{err: audioErr, downloadMS: downloadMS}
			return
		}
		if req.ItemID != "" {
			UpdateItemPhase(req.ItemID, PhaseConverting, "audio conversion")
		}
		conversionStarted := now()
		converted, conversionErr := deps.convertAudio(branchCtx, path, audioBasePath, req.AudioFormat)
		audioCh <- trackPipelineAudioResult{path: converted, downloadMS: downloadMS, conversionMS: trackPipelineElapsedMS(now, conversionStarted), err: conversionErr}
	}()

	if strings.TrimSpace(req.CoverURL) != "" {
		branchWG.Add(1)
		go func() {
			defer branchWG.Done()
			path, coverErr := deps.downloadCover(coverCtx, s, req.CoverURL, trackDir, req.EmbedMaxQualityCover)
			coverCh <- trackPipelineCoverResult{path: path, err: coverErr, elapsed: trackPipelineElapsedBetween(coverStartedAt, now())}
		}()
	} else {
		coverCh <- trackPipelineCoverResult{}
	}

	metadataNeeded := req.EmbedGenre || strings.TrimSpace(req.BaseMetadata.Date) == "" || strings.TrimSpace(req.BaseMetadata.ISRC) == ""
	if metadataNeeded {
		branchWG.Add(1)
		go func() {
			defer branchWG.Done()
			tagging, metadataWarnings, outcomes := deps.metadata(metadataCtx, s.metadataCache, TaggingMetadataRequest{
				TrackID: req.TrackID, TrackName: req.BaseMetadata.Title, ArtistName: req.BaseMetadata.Artist,
				AlbumName: req.BaseMetadata.Album, ReleaseDate: req.BaseMetadata.Date,
				UseSingleGenre: req.UseSingleGenre, EmbedGenre: req.EmbedGenre,
			})
			metadataCh <- trackPipelineMetadataResult{tagging: tagging, warnings: metadataWarnings, cacheOutcomes: outcomes,
				elapsed: trackPipelineElapsedBetween(metadataStartedAt, now())}
		}()
	} else {
		metadataCh <- trackPipelineMetadataResult{}
	}

	lyricsStarted := req.EmbedLyrics && now().Before(lyricsDeadline)
	if lyricsStarted {
		branchWG.Add(1)
		go func() {
			defer branchWG.Done()
			lyrics, lyricsErr := deps.lyrics(lyricsCtx, req, req.ExpectedDurationMS)
			completedAt := now()
			lyricsCh <- trackPipelineLyricsResult{lyrics: lyrics, err: lyricsErr, completedAt: completedAt,
				elapsed: trackPipelineElapsedBetween(lyricsStartedAt, completedAt)}
		}()
	} else {
		if req.EmbedLyrics {
			warnings = append(warnings, "lyrics deadline exceeded; finalized without lyrics")
		}
		lyricsCh <- trackPipelineLyricsResult{}
	}

	var audioOutcome trackPipelineAudioResult
	select {
	case audioOutcome = <-audioCh:
	case <-ctx.Done():
		cancelBranches()
		return trackPipelineFailure(&result, req, ctx.Err(), warnings)
	}
	result.Timings.DownloadMS = audioOutcome.downloadMS
	result.Timings.ConversionMS = audioOutcome.conversionMS
	if audioOutcome.err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trackPipelineFailure(&result, req, ctxErr, warnings)
		}
		return trackPipelineFailure(&result, req, fmt.Errorf("audio acquisition failed: %w", audioOutcome.err), warnings)
	}
	if err = ctx.Err(); err != nil {
		return trackPipelineFailure(&result, req, err, warnings)
	}
	if strings.TrimSpace(audioOutcome.path) == "" {
		return trackPipelineFailure(&result, req, errors.New("audio acquisition returned an empty path"), warnings)
	}

	probeStarted := now()
	duration, durationErr := deps.probeDuration(branchCtx, audioOutcome.path)
	result.Timings.DurationProbeMS = trackPipelineElapsedMS(now, probeStarted)
	if durationErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trackPipelineFailure(&result, req, ctxErr, warnings)
		}
		// A file FFprobe cannot read is not audio (empty body, HTML error page);
		// publishing it would make every later run skip it as "already exists".
		if !errors.Is(durationErr, ErrFFmpegUnavailable) {
			return trackPipelineFailure(&result, req, fmt.Errorf("downloaded file is not readable audio: %w", durationErr), warnings)
		}
		warnings = append(warnings, fmt.Sprintf("duration probe failed: %v", durationErr))
		duration = 0
	}
	result.DurationSeconds = duration

	var lyricsOutcome trackPipelineLyricsResult
	if lyricsStarted {
		var candidate trackPipelineLyricsResult
		received := false
		// Audio usually outlasts the deadline, so first take a result that is
		// already waiting; its completion time decides whether it was in time.
		select {
		case candidate = <-lyricsCh:
			received = true
		default:
		}
		if remaining := lyricsDeadline.Sub(now()); !received && remaining > 0 {
			timerC, stopTimer := deps.newTimer(remaining)
			select {
			case candidate = <-lyricsCh:
				stopTimer()
				received = true
			case <-timerC:
			case <-ctx.Done():
				stopTimer()
				return trackPipelineFailure(&result, req, ctx.Err(), warnings)
			}
		}
		if received && (candidate.completedAt.IsZero() || candidate.completedAt.Before(lyricsDeadline)) {
			lyricsOutcome = candidate
		} else {
			cancelLyrics()
			warnings = append(warnings, "lyrics deadline exceeded; finalized without lyrics")
			result.Timings.LyricsMS = trackPipelineElapsedMS(now, lyricsStartedAt)
			if received {
				result.Timings.LyricsMS = candidate.elapsed
			}
		}
	} else {
		lyricsOutcome = <-lyricsCh
	}
	if lyricsOutcome.err != nil && !errors.Is(lyricsOutcome.err, context.Canceled) && !errors.Is(lyricsOutcome.err, context.DeadlineExceeded) {
		warnings = append(warnings, fmt.Sprintf("lyrics unavailable: %v", lyricsOutcome.err))
	}
	if result.Timings.LyricsMS == 0 {
		result.Timings.LyricsMS = lyricsOutcome.elapsed
	}

	var coverOutcome trackPipelineCoverResult
	select {
	case coverOutcome = <-coverCh:
	case <-coverCtx.Done():
		coverOutcome = trackPipelineCoverResult{err: coverCtx.Err(), elapsed: trackPipelineElapsedMS(now, coverStartedAt)}
	case <-ctx.Done():
		cancelBranches()
		return trackPipelineFailure(&result, req, ctx.Err(), warnings)
	}
	result.Timings.CoverMS = coverOutcome.elapsed
	if coverOutcome.err != nil {
		if errors.Is(coverOutcome.err, context.Canceled) && ctx.Err() != nil {
			return trackPipelineFailure(&result, req, ctx.Err(), warnings)
		}
		if errors.Is(coverOutcome.err, context.DeadlineExceeded) {
			warnings = append(warnings, "cover retrieval timed out")
		} else {
			warnings = append(warnings, fmt.Sprintf("cover retrieval failed: %v", coverOutcome.err))
		}
	}
	var metadataOutcome trackPipelineMetadataResult
	select {
	case metadataOutcome = <-metadataCh:
	case <-metadataCtx.Done():
		metadataOutcome = trackPipelineMetadataResult{err: metadataCtx.Err(), elapsed: trackPipelineElapsedMS(now, metadataStartedAt)}
	case <-ctx.Done():
		cancelBranches()
		return trackPipelineFailure(&result, req, ctx.Err(), warnings)
	}
	result.Timings.MetadataMS = metadataOutcome.elapsed
	if metadataOutcome.err != nil {
		if errors.Is(metadataOutcome.err, context.Canceled) && ctx.Err() != nil {
			return trackPipelineFailure(&result, req, ctx.Err(), warnings)
		}
		if errors.Is(metadataOutcome.err, context.DeadlineExceeded) {
			warnings = append(warnings, "metadata enrichment timed out")
		} else {
			warnings = append(warnings, fmt.Sprintf("metadata enrichment failed: %v", metadataOutcome.err))
		}
	}
	warnings = append(warnings, metadataOutcome.warnings...)
	for key, outcome := range metadataOutcome.cacheOutcomes {
		cacheOutcomes[key] = outcome
	}

	metadata := mergeTrackPipelineMetadata(req.BaseMetadata, metadataOutcome)
	if lyricsOutcome.err == nil && strings.TrimSpace(lyricsOutcome.lyrics) != "" {
		validated, validationErr := ValidateLyricsDuration(lyricsOutcome.lyrics, duration)
		if validationErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return trackPipelineFailure(&result, req, ctxErr, warnings)
			}
			warnings = append(warnings, fmt.Sprintf("lyrics validation failed: %v", validationErr))
		} else {
			metadata.Lyrics = validated
		}
	}

	if req.ItemID != "" {
		UpdateItemPhase(req.ItemID, PhaseFinalizing, "writing metadata, cover, and lyrics")
	}
	finalizeStarted := now()
	if err = deps.finalizeMedia(branchCtx, audioOutcome.path, metadata, coverOutcome.path); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trackPipelineFailure(&result, req, ctxErr, warnings)
		}
		return trackPipelineFailure(&result, req, fmt.Errorf("finalize media: %w", err), warnings)
	}
	result.Timings.FinalizationMS = trackPipelineElapsedMS(now, finalizeStarted)

	finalPath := audioOutputPath(outputBasePath, audioOutcome.path, req.AudioFormat)
	if req.ItemID != "" {
		UpdateItemPhase(req.ItemID, PhaseFinalizing, "publishing finalized media")
	}
	if err = deps.publish(ctx, audioOutcome.path, finalPath); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trackPipelineFailure(&result, req, ctxErr, warnings)
		}
		return trackPipelineFailure(&result, req, fmt.Errorf("publish finalized media: %w", err), warnings)
	}

	result.File = finalPath
	if _, statErr := os.Stat(finalPath); statErr == nil {
		result.Timings.TotalMS = trackPipelineElapsedMS(now, pipelineStarted)
	} else if req.ItemID != "" {
		return trackPipelineFailure(&result, req, fmt.Errorf("published file missing: %w", statErr), warnings)
	}
	return result, nil
}

func trackPipelineFailure(result *TrackPipelineResult, req TrackPipelineRequest, err error, warnings []string) (TrackPipelineResult, error) {
	if err == nil {
		err = errors.New("track pipeline failed")
	}
	if req.ItemID != "" {
		for _, warning := range warnings {
			AddItemWarning(req.ItemID, warning)
		}
	}
	return *result, err
}

func trackPipelineElapsedMS(now func() time.Time, started time.Time) int64 {
	if now == nil {
		now = time.Now
	}
	elapsed := now().Sub(started).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func trackPipelineElapsedBetween(started, completed time.Time) int64 {
	elapsed := completed.Sub(started).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	return elapsed
}

func sanitizeTrackPipelineID(itemID string) string {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		itemID = "track"
	}
	itemID = SanitizeFilename(itemID)
	itemID = strings.Trim(strings.TrimSpace(itemID), ".")
	if itemID == "" {
		return "track"
	}
	return itemID
}

func audioOutputPath(outputBasePath, stagedPath, audioFormat string) string {
	ext := strings.ToLower(filepath.Ext(stagedPath))
	if ext != ".mp3" && ext != ".flac" && ext != ".m4a" {
		ext = preferredAudioExtension(audioFormat)
	}
	return outputBasePath + ext
}

// mergeTrackPipelineMetadata fills tags the request left empty from the
// tagging lookup.
func mergeTrackPipelineMetadata(base Metadata, optional trackPipelineMetadataResult) Metadata {
	metadata := base
	tagging := optional.tagging
	if strings.TrimSpace(metadata.Date) == "" {
		metadata.Date = strings.TrimSpace(tagging.ReleaseDate)
	}
	if strings.TrimSpace(metadata.ReleaseDate) == "" {
		metadata.ReleaseDate = strings.TrimSpace(tagging.ReleaseDate)
	}
	if strings.TrimSpace(metadata.ISRC) == "" {
		metadata.ISRC = strings.TrimSpace(tagging.ISRC)
	}
	if strings.TrimSpace(metadata.Genre) == "" {
		metadata.Genre = strings.TrimSpace(tagging.Genre)
	}
	return metadata
}

func trackPipelineResolveTarget(ctx context.Context, s *SpotiDownloader, req TrackPipelineRequest) (string, string, error) {
	resp, err := s.getDownloadLink(ctx, req.TrackID)
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(req.AudioFormat, "flac") && resp.LinkFlac != "" {
		return resp.LinkFlac, ".flac", nil
	}
	if resp.Link == "" {
		return "", "", errors.New("no download link available")
	}
	return resp.Link, ".mp3", nil
}

func trackPipelineLyrics(ctx context.Context, req TrackPipelineRequest, _ int) (string, error) {
	client := NewLyricsClient()
	duration := req.ExpectedDurationMS / 1000
	resp, _, err := client.FetchLyricsAllSourcesContext(ctx, req.TrackID, req.BaseMetadata.Title, req.BaseMetadata.Artist, duration)
	if err != nil {
		return "", err
	}
	return client.ConvertToLRC(resp, req.BaseMetadata.Title, req.BaseMetadata.Artist), nil
}

func publishTrackPipelineFile(ctx context.Context, srcPath, dstPath string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return err
	}
	if err := os.Rename(srcPath, dstPath); err == nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = os.Remove(dstPath)
			return ctxErr
		}
		return nil
	}

	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat source file: %w", err)
	}
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return fmt.Errorf("open destination file: %w", err)
	}
	_, copyErr := copyWithContext(ctx, dst, src)
	closeErr := dst.Close()
	// Windows cannot delete the source below while it is still open.
	_ = src.Close()
	if copyErr != nil || closeErr != nil || ctx.Err() != nil {
		_ = os.Remove(dstPath)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if copyErr != nil {
			return fmt.Errorf("copy file contents: %w", copyErr)
		}
		return fmt.Errorf("close destination file: %w", closeErr)
	}
	if err := os.Chmod(dstPath, info.Mode()); err != nil && !os.IsPermission(err) {
		_ = os.Remove(dstPath)
		return fmt.Errorf("set destination file mode: %w", err)
	}
	if err := os.Remove(srcPath); err != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("remove source file: %w", err)
	}
	return nil
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return copied, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			copied += int64(written)
			if writeErr != nil {
				return copied, writeErr
			}
			if written != n {
				return copied, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return copied, nil
			}
			return copied, readErr
		}
	}
}
