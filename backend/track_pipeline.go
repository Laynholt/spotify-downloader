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

// trackPipelineDeps is deliberately unexported: production callers use the
// real context-aware helpers, while backend tests can replace individual
// operations with deterministic fakes. Fields are any so focused tests can use
// either the full production signature or a smaller function shape; the call
// adapters below accept both without widening the public API.
type trackPipelineDeps struct {
	resolveTarget          any
	resolveDownloadTarget  any
	resolve                any
	downloadAudio          any
	audioDownload          any
	download               any
	convertAudio           any
	convert                any
	convertDownloadedAudio any
	downloadCover          any
	cover                  any
	metadata               any
	fetchMetadata          any
	taggingMetadata        any
	fetchTaggingMetadata   any
	lyrics                 any
	fetchLyrics            any
	probeDuration          any
	duration               any
	durationProbe          any
	finalizeMedia          any
	finalizer              any
	finalize               any
	publish                any
	now                    any
	clock                  any
	newTimer               any
	withTimeout            any
}

// pipelineDeps is the test seam used by the backend package tests. Keep the
// default value separate so a test may overlay only one branch.
var (
	pipelineDeps              trackPipelineDeps
	trackPipelineDepsForTest  trackPipelineDeps
	trackPipelineDependencies trackPipelineDeps
	defaultTrackPipelineDeps  = trackPipelineDeps{
		resolveTarget: trackPipelineResolveTarget,
		downloadAudio: trackPipelineDownloadAudio,
		convertAudio:  trackPipelineConvertAudio,
		downloadCover: trackPipelineDownloadCover,
		metadata:      trackPipelineMetadataWithOutcomes,
		lyrics:        trackPipelineLyrics,
		probeDuration: trackPipelineProbeDuration,
		finalizeMedia: trackPipelineFinalizeMedia,
		publish:       publishTrackPipelineFile,
		now:           time.Now,
	}
)

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
	metadata      Metadata
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

type trackPipelineTimer struct {
	C    <-chan time.Time
	Stop func() bool
}

func (s *SpotiDownloader) RunTrackPipeline(ctx context.Context, req TrackPipelineRequest) (result TrackPipelineResult, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}

	deps := trackPipelineCurrentDeps(s)
	now := trackPipelineNow(deps)
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
	if itemID != "" {
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
	for _, ext := range PossibleAudioExtensions(req.AudioFormat) {
		candidate := outputBasePath + ext
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() && info.Size() > 0 {
			result.File = candidate
			result.AlreadyExists = true
			if req.ItemID != "" {
				SkipDownloadItem(req.ItemID, candidate)
			}
			return result, nil
		}
	}

	if err = ctx.Err(); err != nil {
		return trackPipelineFailure(&result, req, err, warnings)
	}
	lyricsDeadline := pipelineStarted.Add(trackPipelineLyricsBudget)
	downloadURL, fallbackExt, resolveErr := trackPipelineResolveTargetCall(deps, ctx, s, req)
	if resolveErr != nil {
		return trackPipelineFailure(&result, req, fmt.Errorf("failed to get download link: %w", resolveErr), warnings)
	}
	if strings.TrimSpace(downloadURL) == "" {
		return trackPipelineFailure(&result, req, errors.New("no download link available"), warnings)
	}
	if itemID != "" {
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
	coverCtx, cancelCover := trackPipelineWithTimeout(deps, branchCtx, trackPipelineBranchBudget)
	defer cancelCover()
	metadataCtx, cancelMetadata := trackPipelineWithTimeout(deps, branchCtx, trackPipelineBranchBudget)
	defer cancelMetadata()
	lyricsCtx, cancelLyrics := context.WithDeadline(branchCtx, lyricsDeadline)
	defer cancelLyrics()

	audioBasePath := filepath.Join(trackDir, filenameBase)
	branchWG.Add(1)
	go func() {
		defer branchWG.Done()
		started := now()
		path, audioErr := trackPipelineDownloadAudioCall(deps, branchCtx, s, downloadURL, audioBasePath, fallbackExt, req.ItemID)
		downloadMS := trackPipelineElapsedMS(now, started)
		if audioErr != nil {
			audioCh <- trackPipelineAudioResult{err: audioErr, downloadMS: downloadMS}
			return
		}
		if itemID != "" {
			UpdateItemPhase(req.ItemID, PhaseConverting, "audio conversion")
		}
		conversionStarted := now()
		converted, conversionErr := trackPipelineConvertAudioCall(deps, branchCtx, path, audioBasePath, req.AudioFormat)
		audioCh <- trackPipelineAudioResult{path: converted, downloadMS: downloadMS, conversionMS: trackPipelineElapsedMS(now, conversionStarted), err: conversionErr}
	}()

	if strings.TrimSpace(req.CoverURL) != "" {
		branchWG.Add(1)
		go func() {
			defer branchWG.Done()
			path, coverErr := trackPipelineDownloadCoverCall(deps, coverCtx, s, req.CoverURL, trackDir, req.EmbedMaxQualityCover)
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
			metadata := trackPipelineMetadataCall(deps, metadataCtx, s, req)
			metadata.elapsed = trackPipelineElapsedBetween(metadataStartedAt, now())
			metadataCh <- metadata
		}()
	} else {
		metadataCh <- trackPipelineMetadataResult{}
	}

	lyricsStarted := req.EmbedLyrics && now().Before(lyricsDeadline)
	if lyricsStarted {
		branchWG.Add(1)
		go func() {
			defer branchWG.Done()
			lyrics := trackPipelineLyricsCall(deps, lyricsCtx, req)
			lyrics.completedAt = now()
			lyrics.elapsed = trackPipelineElapsedBetween(lyricsStartedAt, lyrics.completedAt)
			lyricsCh <- lyrics
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
	duration, durationErr := trackPipelineProbeDurationCall(deps, branchCtx, audioOutcome.path)
	result.Timings.DurationProbeMS = trackPipelineElapsedMS(now, probeStarted)
	if durationErr != nil {
		warnings = append(warnings, fmt.Sprintf("duration probe failed: %v", durationErr))
		duration = 0
	}
	result.DurationSeconds = duration

	var lyricsOutcome trackPipelineLyricsResult
	if lyricsStarted {
		remaining := lyricsDeadline.Sub(now())
		if remaining > 0 {
			timer := trackPipelineNewTimer(deps, remaining)
			stopTimer := func() {
				if timer.Stop == nil || timer.Stop() || timer.C == nil {
					return
				}
				select {
				case <-timer.C:
				default:
				}
			}
			select {
			case candidate := <-lyricsCh:
				stopTimer()
				lyricsOutcome = candidate
				if !candidate.completedAt.IsZero() && !candidate.completedAt.Before(lyricsDeadline) {
					cancelLyrics()
					warnings = append(warnings, "lyrics deadline exceeded; finalized without lyrics")
					result.Timings.LyricsMS = candidate.elapsed
					lyricsOutcome = trackPipelineLyricsResult{}
				}
			case <-timer.C:
				cancelLyrics()
				warnings = append(warnings, "lyrics deadline exceeded; finalized without lyrics")
				result.Timings.LyricsMS = trackPipelineElapsedMS(now, lyricsStartedAt)
				lyricsOutcome = trackPipelineLyricsResult{}
			case <-ctx.Done():
				stopTimer()
				return trackPipelineFailure(&result, req, ctx.Err(), warnings)
			}
		} else {
			cancelLyrics()
			warnings = append(warnings, "lyrics deadline exceeded; finalized without lyrics")
			result.Timings.LyricsMS = trackPipelineElapsedMS(now, lyricsStartedAt)
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

	if itemID != "" {
		UpdateItemPhase(req.ItemID, PhaseFinalizing, "writing metadata, cover, and lyrics")
	}
	finalizeStarted := now()
	if err = trackPipelineFinalizeMediaCall(deps, branchCtx, audioOutcome.path, metadata, coverOutcome.path); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trackPipelineFailure(&result, req, ctxErr, warnings)
		}
		return trackPipelineFailure(&result, req, fmt.Errorf("finalize media: %w", err), warnings)
	}
	result.Timings.FinalizationMS = trackPipelineElapsedMS(now, finalizeStarted)

	finalPath := audioOutputPath(outputBasePath, audioOutcome.path, req.AudioFormat)
	if itemID != "" {
		UpdateItemPhase(req.ItemID, PhaseFinalizing, "publishing finalized media")
	}
	if err = trackPipelinePublishCall(deps, ctx, audioOutcome.path, finalPath); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return trackPipelineFailure(&result, req, ctxErr, warnings)
		}
		return trackPipelineFailure(&result, req, fmt.Errorf("publish finalized media: %w", err), warnings)
	}

	result.File = finalPath
	if info, statErr := os.Stat(finalPath); statErr == nil {
		result.Timings.TotalMS = trackPipelineElapsedMS(now, pipelineStarted)
		if req.ItemID != "" {
			for _, warning := range warnings {
				AddItemWarning(req.ItemID, warning)
			}
			CompleteDownloadItem(req.ItemID, finalPath, float64(info.Size())/float64(bytesPerMegabyte), result.Timings)
		}
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
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			CancelDownloadItem(req.ItemID)
		} else {
			FailDownloadItem(req.ItemID, err.Error())
		}
	}
	return *result, err
}

func trackPipelineCurrentDeps(s *SpotiDownloader) trackPipelineDeps {
	deps := defaultTrackPipelineDeps
	deps = overlayTrackPipelineDeps(deps, pipelineDeps)
	deps = overlayTrackPipelineDeps(deps, trackPipelineDepsForTest)
	deps = overlayTrackPipelineDeps(deps, trackPipelineDependencies)
	if s != nil {
		deps = overlayTrackPipelineDeps(deps, s.pipelineDeps)
		deps = overlayTrackPipelineDeps(deps, s.trackPipelineDeps)
		deps = overlayTrackPipelineDeps(deps, s.trackDeps)
	}
	return deps
}

func overlayTrackPipelineDeps(dst, src trackPipelineDeps) trackPipelineDeps {
	if src.resolveTarget != nil {
		dst.resolveTarget = src.resolveTarget
	}
	if src.resolveDownloadTarget != nil {
		dst.resolveDownloadTarget = src.resolveDownloadTarget
	}
	if src.resolve != nil {
		dst.resolveTarget = src.resolve
	}
	if src.downloadAudio != nil {
		dst.downloadAudio = src.downloadAudio
	}
	if src.audioDownload != nil {
		dst.downloadAudio = src.audioDownload
	}
	if src.download != nil {
		dst.downloadAudio = src.download
	}
	if src.convertAudio != nil {
		dst.convertAudio = src.convertAudio
	}
	if src.convert != nil {
		dst.convertAudio = src.convert
	}
	if src.convertDownloadedAudio != nil {
		dst.convertAudio = src.convertDownloadedAudio
	}
	if src.downloadCover != nil {
		dst.downloadCover = src.downloadCover
	}
	if src.cover != nil {
		dst.downloadCover = src.cover
	}
	if src.metadata != nil {
		dst.metadata = src.metadata
	}
	if src.fetchMetadata != nil {
		dst.metadata = src.fetchMetadata
	}
	if src.taggingMetadata != nil {
		dst.metadata = src.taggingMetadata
	}
	if src.fetchTaggingMetadata != nil {
		dst.metadata = src.fetchTaggingMetadata
	}
	if src.lyrics != nil {
		dst.lyrics = src.lyrics
	}
	if src.fetchLyrics != nil {
		dst.lyrics = src.fetchLyrics
	}
	if src.probeDuration != nil {
		dst.probeDuration = src.probeDuration
	}
	if src.duration != nil {
		dst.probeDuration = src.duration
	}
	if src.durationProbe != nil {
		dst.probeDuration = src.durationProbe
	}
	if src.finalizeMedia != nil {
		dst.finalizeMedia = src.finalizeMedia
	}
	if src.finalizer != nil {
		dst.finalizeMedia = src.finalizer
	}
	if src.finalize != nil {
		dst.finalizeMedia = src.finalize
	}
	if src.publish != nil {
		dst.publish = src.publish
	}
	if src.now != nil {
		dst.now = src.now
	}
	if src.clock != nil {
		dst.now = src.clock
	}
	if src.newTimer != nil {
		dst.newTimer = src.newTimer
	}
	if src.withTimeout != nil {
		dst.withTimeout = src.withTimeout
	}
	return dst
}

func trackPipelineNow(deps trackPipelineDeps) func() time.Time {
	switch value := deps.now.(type) {
	case func() time.Time:
		return value
	default:
		return time.Now
	}
}

func trackPipelineNewTimer(deps trackPipelineDeps, duration time.Duration) trackPipelineTimer {
	switch f := deps.newTimer.(type) {
	case func(time.Duration) <-chan time.Time:
		return trackPipelineTimer{C: f(duration)}
	case func(time.Duration) *time.Timer:
		timer := f(duration)
		return trackPipelineTimer{C: timer.C, Stop: timer.Stop}
	default:
		timer := time.NewTimer(duration)
		return trackPipelineTimer{C: timer.C, Stop: timer.Stop}
	}
}

func trackPipelineWithTimeout(deps trackPipelineDeps, parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if f, ok := deps.withTimeout.(func(context.Context, time.Duration) (context.Context, context.CancelFunc)); ok {
		return f(parent, timeout)
	}
	return context.WithTimeout(parent, timeout)
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

func mergeTrackPipelineMetadata(base Metadata, optional trackPipelineMetadataResult) Metadata {
	metadata := base
	if strings.TrimSpace(optional.metadata.Title) != "" {
		metadata.Title = optional.metadata.Title
	}
	if strings.TrimSpace(optional.metadata.Artist) != "" {
		metadata.Artist = optional.metadata.Artist
	}
	if strings.TrimSpace(optional.metadata.Album) != "" {
		metadata.Album = optional.metadata.Album
	}
	if strings.TrimSpace(optional.metadata.AlbumArtist) != "" {
		metadata.AlbumArtist = optional.metadata.AlbumArtist
	}
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
	if strings.TrimSpace(optional.metadata.Date) != "" {
		metadata.Date = optional.metadata.Date
	}
	if strings.TrimSpace(optional.metadata.ISRC) != "" {
		metadata.ISRC = optional.metadata.ISRC
	}
	if strings.TrimSpace(optional.metadata.Genre) != "" {
		metadata.Genre = optional.metadata.Genre
	}
	return metadata
}

func trackPipelineResolveTarget(ctx context.Context, s *SpotiDownloader, req TrackPipelineRequest) (string, string, error) {
	resp, err := s.getDownloadLinkContextWithRetry(ctx, req.TrackID)
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

func trackPipelineDownloadAudio(ctx context.Context, s *SpotiDownloader, url, base, ext, itemID string) (string, error) {
	return s.downloadFileContextWithItemIDRetry(ctx, url, base, ext, itemID)
}

func trackPipelineConvertAudio(ctx context.Context, path, base, format string) (string, error) {
	return convertDownloadedAudioContext(ctx, path, base, format)
}

func trackPipelineDownloadCover(ctx context.Context, s *SpotiDownloader, url, dir string, maxQuality bool) (string, error) {
	return s.downloadCoverImageContextToDir(ctx, url, dir, maxQuality, "")
}

func trackPipelineMetadata(ctx context.Context, cache *MetadataCache, req TaggingMetadataRequest) (taggingMetadataResult, []string) {
	result, warnings := fetchTaggingMetadata(ctx, cache, req)
	return result, warnings
}

func trackPipelineMetadataWithOutcomes(ctx context.Context, cache *MetadataCache, req TaggingMetadataRequest) (taggingMetadataResult, []string, map[string]CacheOutcome) {
	return fetchTaggingMetadataWithOutcomes(ctx, cache, req)
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

func trackPipelineProbeDuration(ctx context.Context, path string) (float64, error) {
	return GetAudioDurationContext(ctx, path)
}

func trackPipelineFinalizeMedia(ctx context.Context, path string, metadata Metadata, cover string) error {
	return FinalizeMedia(ctx, path, metadata, cover)
}

func trackPipelineResolveTargetCall(deps trackPipelineDeps, ctx context.Context, s *SpotiDownloader, req TrackPipelineRequest) (string, string, error) {
	fn := deps.resolveTarget
	if fn == nil {
		fn = deps.resolveDownloadTarget
	}
	switch f := fn.(type) {
	case func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, string, error):
		return f(ctx, s, req)
	case func(context.Context, TrackPipelineRequest) (string, string, error):
		return f(ctx, req)
	case func(context.Context, *SpotiDownloader, TrackPipelineRequest) (string, error):
		url, err := f(ctx, s, req)
		return url, preferredAudioExtension(req.AudioFormat), err
	case func(context.Context, TrackPipelineRequest) (string, error):
		url, err := f(ctx, req)
		return url, preferredAudioExtension(req.AudioFormat), err
	case func(context.Context, *SpotiDownloader, string) (string, error):
		url, err := f(ctx, s, req.TrackID)
		return url, preferredAudioExtension(req.AudioFormat), err
	case func(context.Context, string) (string, error):
		url, err := f(ctx, req.TrackID)
		return url, preferredAudioExtension(req.AudioFormat), err
	case func(context.Context, *SpotiDownloader, string) (string, string, error):
		return f(ctx, s, req.TrackID)
	case func(context.Context, string) (string, string, error):
		return f(ctx, req.TrackID)
	case func(TrackPipelineRequest) (string, string, error):
		return f(req)
	default:
		return "", "", errors.New("track pipeline source resolver is unavailable")
	}
}

func trackPipelineDownloadAudioCall(deps trackPipelineDeps, ctx context.Context, s *SpotiDownloader, url, base, ext, itemID string) (string, error) {
	switch f := deps.downloadAudio.(type) {
	case func(context.Context, *SpotiDownloader, string, string, string, string) error:
		return base, f(ctx, s, url, base, ext, itemID)
	case func(context.Context, string, string, string, string) error:
		return base, f(ctx, url, base, ext, itemID)
	case func(context.Context, *SpotiDownloader, string, string, string) error:
		return base, f(ctx, s, url, base, ext)
	case func(context.Context, string, string, string) error:
		return base, f(ctx, url, base, ext)
	case func(context.Context, *SpotiDownloader, string, string, string, string) (string, error):
		return f(ctx, s, url, base, ext, itemID)
	case func(context.Context, string, string, string, string) (string, error):
		return f(ctx, url, base, ext, itemID)
	case func(context.Context, *SpotiDownloader, string, string, string) (string, error):
		return f(ctx, s, url, base, ext)
	case func(context.Context, string, string, string) (string, error):
		return f(ctx, url, base, ext)
	case func(context.Context, *SpotiDownloader, string, string) error:
		return base, f(ctx, s, url, base)
	case func(context.Context, string, string) error:
		return base, f(ctx, url, base)
	case func(context.Context, string) (string, error):
		return f(ctx, base)
	default:
		return "", errors.New("track pipeline audio downloader is unavailable")
	}
}

func trackPipelineConvertAudioCall(deps trackPipelineDeps, ctx context.Context, path, base, format string) (string, error) {
	fn := deps.convertAudio
	if fn == nil {
		fn = deps.convert
	}
	switch f := fn.(type) {
	case func(context.Context, string, string, string) (string, error):
		return f(ctx, path, base, format)
	case func(string, string, string) (string, error):
		return f(path, base, format)
	case func(context.Context, string, string) (string, error):
		return f(ctx, path, format)
	case func(context.Context, string) (string, error):
		return f(ctx, path)
	default:
		return "", errors.New("track pipeline audio converter is unavailable")
	}
}

func trackPipelineDownloadCoverCall(deps trackPipelineDeps, ctx context.Context, s *SpotiDownloader, url, dir string, maxQuality bool) (string, error) {
	fn := deps.downloadCover
	if fn == nil {
		fn = deps.cover
	}
	switch f := fn.(type) {
	case func(context.Context, *SpotiDownloader, string, string, bool, string) (string, error):
		return f(ctx, s, url, dir, maxQuality, "")
	case func(context.Context, *SpotiDownloader, string, string, bool) (string, error):
		return f(ctx, s, url, dir, maxQuality)
	case func(context.Context, string, string, bool) (string, error):
		return f(ctx, url, dir, maxQuality)
	case func(context.Context, string, string) (string, error):
		return f(ctx, url, dir)
	case func(context.Context, string) (string, error):
		return f(ctx, url)
	default:
		return "", errors.New("track pipeline cover downloader is unavailable")
	}
}

func trackPipelineMetadataCall(deps trackPipelineDeps, ctx context.Context, s *SpotiDownloader, req TrackPipelineRequest) trackPipelineMetadataResult {
	fn := deps.metadata
	if fn == nil {
		fn = deps.fetchMetadata
	}
	if fn == nil {
		fn = deps.taggingMetadata
	}
	tagReq := TaggingMetadataRequest{
		TrackID: req.TrackID, TrackName: req.BaseMetadata.Title, ArtistName: req.BaseMetadata.Artist,
		AlbumName: req.BaseMetadata.Album, ReleaseDate: req.BaseMetadata.Date,
		UseSingleGenre: req.UseSingleGenre, EmbedGenre: req.EmbedGenre,
	}
	switch f := fn.(type) {
	case func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string, map[string]CacheOutcome):
		tagging, warnings, outcomes := f(ctx, s.metadataCache, tagReq)
		return trackPipelineMetadataResult{tagging: tagging, warnings: warnings, cacheOutcomes: outcomes}
	case func(context.Context, *MetadataCache, TaggingMetadataRequest) (taggingMetadataResult, []string):
		tagging, warnings := f(ctx, s.metadataCache, tagReq)
		return trackPipelineMetadataResult{tagging: tagging, warnings: warnings}
	case func(context.Context, TaggingMetadataRequest) (taggingMetadataResult, []string):
		tagging, warnings := f(ctx, tagReq)
		return trackPipelineMetadataResult{tagging: tagging, warnings: warnings}
	case func(context.Context, TrackPipelineRequest) (taggingMetadataResult, []string):
		tagging, warnings := f(ctx, req)
		return trackPipelineMetadataResult{tagging: tagging, warnings: warnings}
	case func(context.Context, *SpotiDownloader, TrackPipelineRequest) (taggingMetadataResult, []string):
		tagging, warnings := f(ctx, s, req)
		return trackPipelineMetadataResult{tagging: tagging, warnings: warnings}
	case func(context.Context, *SpotiDownloader, TrackPipelineRequest) (Metadata, error):
		metadata, err := f(ctx, s, req)
		return trackPipelineMetadataResult{metadata: metadata, err: err}
	case func(context.Context, *SpotiDownloader, TrackPipelineRequest) (Metadata, []string, map[string]CacheOutcome):
		metadata, warnings, outcomes := f(ctx, s, req)
		return trackPipelineMetadataResult{metadata: metadata, warnings: warnings, cacheOutcomes: outcomes}
	case func(context.Context, TrackPipelineRequest) (Metadata, []string, map[string]CacheOutcome):
		metadata, warnings, outcomes := f(ctx, req)
		return trackPipelineMetadataResult{metadata: metadata, warnings: warnings, cacheOutcomes: outcomes}
	case func(context.Context, TrackPipelineRequest) (Metadata, error):
		metadata, err := f(ctx, req)
		return trackPipelineMetadataResult{metadata: metadata, err: err}
	case func(context.Context, TrackPipelineRequest) (taggingMetadataResult, []string, map[string]CacheOutcome):
		tagging, warnings, outcomes := f(ctx, req)
		return trackPipelineMetadataResult{tagging: tagging, warnings: warnings, cacheOutcomes: outcomes}
	default:
		return trackPipelineMetadataResult{err: errors.New("track pipeline metadata loader is unavailable")}
	}
}

func trackPipelineLyricsCall(deps trackPipelineDeps, ctx context.Context, req TrackPipelineRequest) trackPipelineLyricsResult {
	fn := deps.lyrics
	if fn == nil {
		fn = deps.fetchLyrics
	}
	if fn == nil {
		return trackPipelineLyricsResult{err: errors.New("track pipeline lyrics loader is unavailable")}
	}
	switch f := fn.(type) {
	case func(context.Context, TrackPipelineRequest, int) (string, error):
		return trackPipelineLyricsResultFrom(f(ctx, req, req.ExpectedDurationMS))
	case func(context.Context, TrackPipelineRequest) (string, error):
		return trackPipelineLyricsResultFrom(f(ctx, req))
	case func(context.Context, string, string, string, int) (string, error):
		return trackPipelineLyricsResultFrom(f(ctx, req.TrackID, req.BaseMetadata.Title, req.BaseMetadata.Artist, req.ExpectedDurationMS/1000))
	case func(context.Context, string, string, string) (string, error):
		return trackPipelineLyricsResultFrom(f(ctx, req.TrackID, req.BaseMetadata.Title, req.BaseMetadata.Artist))
	default:
		return trackPipelineLyricsResult{err: errors.New("track pipeline lyrics loader has unsupported signature")}
	}
}

func trackPipelineLyricsResultFrom(lyrics string, err error) trackPipelineLyricsResult {
	return trackPipelineLyricsResult{lyrics: lyrics, err: err}
}

func trackPipelineProbeDurationCall(deps trackPipelineDeps, ctx context.Context, path string) (float64, error) {
	fn := deps.probeDuration
	if fn == nil {
		fn = deps.duration
	}
	switch f := fn.(type) {
	case func(context.Context, string) (float64, error):
		return f(ctx, path)
	case func(string) (float64, error):
		return f(path)
	default:
		return 0, errors.New("track pipeline duration probe is unavailable")
	}
}

func trackPipelineFinalizeMediaCall(deps trackPipelineDeps, ctx context.Context, path string, metadata Metadata, cover string) error {
	fn := deps.finalizeMedia
	if fn == nil {
		fn = deps.finalizer
	}
	switch f := fn.(type) {
	case func(context.Context, string, Metadata, string) error:
		return f(ctx, path, metadata, cover)
	case func(string, Metadata, string) error:
		return f(path, metadata, cover)
	case func(context.Context, string, Metadata) error:
		return f(ctx, path, metadata)
	default:
		return errors.New("track pipeline finalizer is unavailable")
	}
}

func trackPipelinePublishCall(deps trackPipelineDeps, ctx context.Context, src, dst string) error {
	if fn := deps.publish; fn != nil {
		switch f := fn.(type) {
		case func(context.Context, string, string) error:
			return f(ctx, src, dst)
		case func(string, string) error:
			return f(src, dst)
		}
	}
	return publishTrackPipelineFile(ctx, src, dst)
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
