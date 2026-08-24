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
)

type TrackMetadataUpdateRequest struct {
	FilePath             string `json:"file_path"`
	TrackID              string `json:"track_id,omitempty"`
	TrackName            string `json:"track_name,omitempty"`
	ArtistName           string `json:"artist_name,omitempty"`
	AlbumName            string `json:"album_name,omitempty"`
	AlbumArtist          string `json:"album_artist,omitempty"`
	ReleaseDate          string `json:"release_date,omitempty"`
	CoverURL             string `json:"cover_url,omitempty"`
	TrackNumber          int    `json:"track_number,omitempty"`
	TotalTracks          int    `json:"total_tracks,omitempty"`
	DiscNumber           int    `json:"disc_number,omitempty"`
	TotalDiscs           int    `json:"total_discs,omitempty"`
	Copyright            string `json:"copyright,omitempty"`
	Publisher            string `json:"publisher,omitempty"`
	ISRC                 string `json:"isrc,omitempty"`
	Genre                string `json:"genre,omitempty"`
	Duration             int    `json:"duration,omitempty"`
	EmbedLyrics          bool   `json:"embed_lyrics,omitempty"`
	EmbedMaxQualityCover bool   `json:"embed_max_quality_cover,omitempty"`
	UseSingleGenre       bool   `json:"use_single_genre,omitempty"`
	EmbedGenre           bool   `json:"embed_genre,omitempty"`
}

type TrackMetadataUpdateResult struct {
	Success                 bool    `json:"success"`
	Message                 string  `json:"message"`
	FilePath                string  `json:"file_path,omitempty"`
	Error                   string  `json:"error,omitempty"`
	TrackID                 string  `json:"track_id,omitempty"`
	ExpectedDurationSeconds float64 `json:"expected_duration_seconds,omitempty"`
	ActualDurationSeconds   float64 `json:"actual_duration_seconds,omitempty"`
	DurationDeltaSeconds    float64 `json:"duration_delta_seconds,omitempty"`
	Suspicious              bool    `json:"suspicious,omitempty"`
	ValidationWarning       string  `json:"validation_warning,omitempty"`
}

type SuspiciousRedownloadRequest struct {
	QueueID             string                     `json:"queue_id,omitempty"`
	JobID               string                     `json:"job_id,omitempty"`
	OriginalFilePath    string                     `json:"original_file_path"`
	ReplacementFilePath string                     `json:"replacement_file_path,omitempty"`
	CollectionDir       string                     `json:"collection_dir,omitempty"`
	AudioFormat         string                     `json:"audio_format,omitempty"`
	Metadata            TrackMetadataUpdateRequest `json:"metadata"`
}

type SuspiciousRedownloadResult struct {
	Success                 bool    `json:"success"`
	Status                  string  `json:"status"`
	TrackID                 string  `json:"track_id,omitempty"`
	OriginalFilePath        string  `json:"original_file_path,omitempty"`
	MovedOriginalPath       string  `json:"moved_original_path,omitempty"`
	ReplacementPath         string  `json:"replacement_path,omitempty"`
	Error                   string  `json:"error,omitempty"`
	ExpectedDurationSeconds float64 `json:"expected_duration_seconds,omitempty"`
	ActualDurationSeconds   float64 `json:"actual_duration_seconds,omitempty"`
	DurationDeltaSeconds    float64 `json:"duration_delta_seconds,omitempty"`
	Suspicious              bool    `json:"suspicious,omitempty"`
	ValidationWarning       string  `json:"validation_warning,omitempty"`
	Stage                   string  `json:"stage,omitempty"`
	Diagnostics             string  `json:"diagnostics,omitempty"`
}

func BuildTrackMetadataPayload(req TrackMetadataUpdateRequest) Metadata {
	metadata := Metadata{
		Title:       strings.TrimSpace(req.TrackName),
		Artist:      strings.TrimSpace(req.ArtistName),
		Album:       strings.TrimSpace(req.AlbumName),
		AlbumArtist: strings.TrimSpace(req.AlbumArtist),
		Date:        strings.TrimSpace(req.ReleaseDate),
		TrackNumber: req.TrackNumber,
		TotalTracks: req.TotalTracks,
		DiscNumber:  req.DiscNumber,
		TotalDiscs:  req.TotalDiscs,
		Copyright:   strings.TrimSpace(req.Copyright),
		Publisher:   strings.TrimSpace(req.Publisher),
		Description: "https://github.com/afkarxyz/SpotiDownloader",
		ISRC:        strings.TrimSpace(req.ISRC),
		Genre:       strings.TrimSpace(req.Genre),
	}

	if trackID := strings.TrimSpace(req.TrackID); trackID != "" {
		metadata.URL = fmt.Sprintf("https://open.spotify.com/track/%s", trackID)
	}

	return metadata
}

func BuildYtDlpSearchQuery(artistName, trackName string) string {
	return fmt.Sprintf("ytsearch1:%s - %s audio", strings.TrimSpace(artistName), strings.TrimSpace(trackName))
}

func normalizeYouTubeFallbackMetadata(req TrackMetadataUpdateRequest) TrackMetadataUpdateRequest {
	if req.TrackNumber <= 0 || req.TrackNumber > 99 {
		req.TrackNumber = 1
	}
	if req.DiscNumber <= 0 || req.DiscNumber > 99 {
		req.DiscNumber = 1
	}
	return req
}

func sameFilesystemPath(left, right string) bool {
	if sameFilesystemEntry(left, right) {
		return true
	}
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftInfo, leftErr := os.Stat(leftAbs)
	rightInfo, rightErr := os.Stat(rightAbs)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}

func sameFilesystemEntry(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftAbs = filepath.Clean(leftAbs)
	rightAbs = filepath.Clean(rightAbs)
	return leftAbs == rightAbs || (runtime.GOOS == "windows" && strings.EqualFold(leftAbs, rightAbs))
}

func UpdateTrackMetadata(req TrackMetadataUpdateRequest) TrackMetadataUpdateResult {
	result := TrackMetadataUpdateResult{
		FilePath: req.FilePath,
		TrackID:  req.TrackID,
	}

	if strings.TrimSpace(req.FilePath) == "" {
		result.Error = "file path is required"
		return result
	}

	if info, err := os.Stat(req.FilePath); err != nil {
		result.Error = fmt.Sprintf("failed to stat file: %v", err)
		return result
	} else if info.IsDir() {
		result.Error = "file path points to a directory"
		return result
	}

	metadata := BuildTrackMetadataPayload(req)
	if req.EmbedGenre || metadata.Date == "" {
		tagging, warnings := fetchTaggingMetadata(context.Background(), CurrentMetadataCache(), TaggingMetadataRequest{
			TrackID: req.TrackID, TrackName: req.TrackName, ArtistName: req.ArtistName, AlbumName: req.AlbumName,
			ReleaseDate: req.ReleaseDate, UseSingleGenre: req.UseSingleGenre, EmbedGenre: req.EmbedGenre,
		})
		for _, warning := range warnings {
			fmt.Printf("Warning: %s\n", warning)
		}
		if metadata.Date == "" {
			metadata.Date = strings.TrimSpace(tagging.ReleaseDate)
		}
		if metadata.ISRC == "" {
			metadata.ISRC = strings.TrimSpace(tagging.ISRC)
		}
		if metadata.Genre == "" {
			metadata.Genre = strings.TrimSpace(tagging.Genre)
		}
	}

	if req.EmbedLyrics && strings.TrimSpace(req.TrackID) != "" {
		client := NewLyricsClient()
		resp, _, err := client.FetchLyricsAllSources(req.TrackID, req.TrackName, req.ArtistName, req.Duration)
		if err == nil && resp != nil && len(resp.Lines) > 0 {
			metadata.Lyrics = client.ConvertToLRC(resp, req.TrackName, req.ArtistName)
		}
	}

	var coverPath string
	if strings.TrimSpace(req.CoverURL) != "" {
		downloader := NewSpotiDownloader("")
		var coverErr error
		coverPath, coverErr = downloader.downloadCoverImage(req.CoverURL, filepath.Dir(req.FilePath), req.EmbedMaxQualityCover)
		downloader.CloseIdleConnections()
		if coverErr != nil {
			fmt.Printf("Warning: Failed to download cover image for metadata update: %v\n", coverErr)
			coverPath = ""
		}
	}
	if coverPath != "" {
		defer os.Remove(coverPath)
	}

	if err := EmbedMetadata(req.FilePath, metadata, coverPath); err != nil {
		result.Error = fmt.Sprintf("failed to embed metadata: %v", err)
		return result
	}

	if err := FinalizeTaggedAudioFile(req.FilePath); err != nil {
		result.Error = fmt.Sprintf("failed to finalize tagged file: %v", err)
		return result
	}

	populateDurationValidation(&result, req.Duration)
	result.Success = true
	result.Message = "Metadata updated successfully"
	return result
}

func UpdateTracksMetadata(reqs []TrackMetadataUpdateRequest) []TrackMetadataUpdateResult {
	results := make([]TrackMetadataUpdateResult, len(reqs))
	for i, req := range reqs {
		results[i] = UpdateTrackMetadata(req)
	}
	return results
}

type suspiciousRedownloadDeps struct {
	makeOperationDir func(string, string) (string, error)
	download         func(context.Context, YtDlpDownloadRequest) YtDlpDownloadResult
	prepareMetadata  func(context.Context, TrackMetadataUpdateRequest, string) (Metadata, string, error)
	finalize         func(context.Context, string, Metadata, string) error
	probeDuration    func(context.Context, string) (float64, error)
	moveOriginal     func(string, string) (string, error)
	publish          func(context.Context, string, string) error
	restoreOriginal  func(string, string) error
}

var defaultSuspiciousRedownloadDeps = suspiciousRedownloadDeps{
	makeOperationDir: os.MkdirTemp,
	download:         DownloadWithYtDlpContext,
	prepareMetadata:  prepareYouTubeRedownloadMetadata,
	finalize:         FinalizeMedia,
	probeDuration:    GetAudioDurationContext,
	moveOriginal:     MoveSuspiciousOriginal,
	publish:          publishTrackPipelineFile,
	restoreOriginal:  MoveFileWithFallback,
}

var youtubePublicationMu sync.Mutex

func prepareYouTubeRedownloadMetadata(ctx context.Context, req TrackMetadataUpdateRequest, tempDir string) (Metadata, string, error) {
	metadata := BuildTrackMetadataPayload(req)
	if req.EmbedGenre || metadata.Date == "" {
		tagging, warnings := fetchTaggingMetadata(ctx, CurrentMetadataCache(), TaggingMetadataRequest{
			TrackID: req.TrackID, TrackName: req.TrackName, ArtistName: req.ArtistName, AlbumName: req.AlbumName,
			ReleaseDate: req.ReleaseDate, UseSingleGenre: req.UseSingleGenre, EmbedGenre: req.EmbedGenre,
		})
		if err := ctx.Err(); err != nil {
			return Metadata{}, "", err
		}
		for _, warning := range warnings {
			fmt.Printf("Warning: %s\n", warning)
		}
		if metadata.Date == "" {
			metadata.Date = strings.TrimSpace(tagging.ReleaseDate)
		}
		if metadata.ISRC == "" {
			metadata.ISRC = strings.TrimSpace(tagging.ISRC)
		}
		if metadata.Genre == "" {
			metadata.Genre = strings.TrimSpace(tagging.Genre)
		}
	}

	if req.EmbedLyrics && strings.TrimSpace(req.TrackID) != "" {
		client := NewLyricsClient()
		resp, _, err := client.FetchLyricsAllSourcesContext(ctx, req.TrackID, req.TrackName, req.ArtistName, req.Duration/1000)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Metadata{}, "", ctxErr
		}
		if err == nil && resp != nil && len(resp.Lines) > 0 {
			metadata.Lyrics = client.ConvertToLRC(resp, req.TrackName, req.ArtistName)
		}
	}

	coverPath := ""
	if strings.TrimSpace(req.CoverURL) != "" {
		downloader := NewSpotiDownloader("")
		var err error
		coverPath, err = downloader.downloadCoverImageContextToDir(ctx, req.CoverURL, tempDir, req.EmbedMaxQualityCover, "")
		downloader.CloseIdleConnections()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Metadata{}, "", ctxErr
		}
		if err != nil {
			fmt.Printf("Warning: Failed to download cover image for metadata update: %v\n", err)
			coverPath = ""
		}
	}
	return metadata, coverPath, nil
}

func RedownloadSuspiciousTrackFromYouTubeContext(ctx context.Context, req SuspiciousRedownloadRequest) SuspiciousRedownloadResult {
	return runSuspiciousRedownloadWithDeps(ctx, req, defaultSuspiciousRedownloadDeps)
}

func RedownloadSuspiciousTrackFromYouTube(req SuspiciousRedownloadRequest) SuspiciousRedownloadResult {
	return RedownloadSuspiciousTrackFromYouTubeContext(context.Background(), req)
}

func runSuspiciousRedownloadWithDeps(ctx context.Context, req SuspiciousRedownloadRequest, deps suspiciousRedownloadDeps) SuspiciousRedownloadResult {
	if ctx == nil {
		ctx = context.Background()
	}
	result := SuspiciousRedownloadResult{TrackID: req.Metadata.TrackID, OriginalFilePath: req.OriginalFilePath}
	fail := func(stage string, err error) SuspiciousRedownloadResult {
		if err == nil {
			err = errors.New("YouTube redownload failed")
		}
		result.Success = false
		result.Stage = stage
		result.Error = err.Error()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Status = "cancelled"
		} else {
			result.Status = "failed"
		}
		return result
	}
	if err := ctx.Err(); err != nil {
		return fail("preparation", err)
	}

	originalPath := strings.TrimSpace(req.OriginalFilePath)
	replacementPath := strings.TrimSpace(req.ReplacementFilePath)
	if originalPath == "" && replacementPath == "" {
		return fail("preparation", errors.New("original or replacement file path is required"))
	}
	if replacementPath == "" {
		replacementPath = originalPath
	}
	result.ReplacementPath = replacementPath

	collectionDir := strings.TrimSpace(req.CollectionDir)
	if collectionDir == "" && replacementPath != "" {
		collectionDir = filepath.Dir(replacementPath)
	} else if collectionDir == "" {
		collectionDir = filepath.Dir(originalPath)
	}
	suspiciousDir := filepath.Join(collectionDir, SuspiciousTracksDirName)

	paths, err := CurrentRuntimePaths()
	if err != nil {
		return fail("preparation", err)
	}
	if strings.TrimSpace(paths.RunTempDir) == "" {
		return fail("preparation", errors.New("runtime run temp directory is not configured"))
	}
	if err := os.MkdirAll(paths.RunTempDir, 0o755); err != nil {
		return fail("preparation", fmt.Errorf("create runtime temp directory: %w", err))
	}
	makeOperationDir := deps.makeOperationDir
	if makeOperationDir == nil {
		makeOperationDir = os.MkdirTemp
	}
	operationDir, err := makeOperationDir(paths.RunTempDir, sanitizeTrackPipelineID(req.JobID)+"-")
	if err != nil {
		return fail("preparation", fmt.Errorf("create YouTube operation directory: %w", err))
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	defer func() {
		cancelWork()
		_ = os.RemoveAll(operationDir)
	}()
	if pathResolvesInsideDirectory(replacementPath, operationDir) {
		return fail("preparation", errors.New("replacement destination is inside the YouTube operation directory"))
	}
	youtubeDir := filepath.Join(operationDir, "youtube")
	if err := os.MkdirAll(youtubeDir, 0o755); err != nil {
		return fail("preparation", fmt.Errorf("create YouTube job temp directory: %w", err))
	}

	outputBase := filepath.Join(youtubeDir, "replacement")
	downloadResult := deps.download(workCtx, YtDlpDownloadRequest{
		Query:       BuildYtDlpSearchQuery(req.Metadata.ArtistName, req.Metadata.TrackName),
		OutputBase:  outputBase,
		AudioFormat: req.AudioFormat,
	})
	result.Stage = string(downloadResult.Stage)
	result.Diagnostics = downloadResult.Diagnostics
	if !downloadResult.Success {
		if ctxErr := workCtx.Err(); ctxErr != nil {
			return fail(result.Stage, ctxErr)
		}
		downloadErr := errors.New(downloadResult.Error)
		if downloadResult.Error == "" {
			downloadErr = errors.New("yt-dlp failed")
		}
		return fail(result.Stage, downloadErr)
	}
	stagedMedia, err := resolveYouTubeStagedMedia(operationDir, outputBase, downloadResult.File)
	if err != nil {
		return fail(result.Stage, err)
	}

	metadataReq := normalizeYouTubeFallbackMetadata(req.Metadata)
	metadataReq.FilePath = stagedMedia
	metadata, coverPath, err := deps.prepareMetadata(workCtx, metadataReq, youtubeDir)
	if err != nil {
		return fail("metadata", err)
	}
	if err := workCtx.Err(); err != nil {
		return fail("metadata", err)
	}
	if err := deps.finalize(workCtx, stagedMedia, metadata, coverPath); err != nil {
		return fail("finalization", err)
	}
	if err := workCtx.Err(); err != nil {
		return fail("finalization", err)
	}

	actualSeconds, err := deps.probeDuration(workCtx, stagedMedia)
	expectedSeconds := float64(req.Metadata.Duration) / 1000
	result.ExpectedDurationSeconds = expectedSeconds
	if err != nil {
		result.ValidationWarning = fmt.Sprintf("failed to probe audio duration: %v", err)
		return fail("validation", err)
	}
	result.ActualDurationSeconds = actualSeconds
	result.DurationDeltaSeconds = actualSeconds - expectedSeconds
	result.Suspicious = IsSuspiciousDuration(expectedSeconds, actualSeconds)
	if result.Suspicious {
		result.Status = "still_suspicious"
		result.Stage = "validation"
		result.Error = "redownloaded audio is still suspicious"
		return result
	}
	if err := workCtx.Err(); err != nil {
		return fail("validation", err)
	}

	youtubePublicationMu.Lock()
	defer youtubePublicationMu.Unlock()
	if err := workCtx.Err(); err != nil {
		return fail("publication", err)
	}
	destinationInfo, destinationStatErr := os.Stat(replacementPath)
	destinationExisted := destinationStatErr == nil
	if destinationStatErr != nil && !os.IsNotExist(destinationStatErr) {
		return fail("publication", fmt.Errorf("stat replacement destination: %w", destinationStatErr))
	}
	if destinationExisted && destinationInfo.IsDir() {
		return fail("publication", errors.New("replacement destination is a directory"))
	}
	originalIsDestination := sameFilesystemPath(originalPath, replacementPath)
	originalIsDestinationEntry := sameFilesystemEntry(originalPath, replacementPath)
	originalNeedsBackup := originalPath != "" && !IsPathInsideDir(originalPath, suspiciousDir)
	movedOriginal := ""
	if originalNeedsBackup {
		movedOriginal, err = deps.moveOriginal(originalPath, suspiciousDir)
		if err != nil {
			return fail("publication", fmt.Errorf("move suspicious original: %w", err))
		}
	} else if originalPath != "" {
		movedOriginal = originalPath
	}

	destinationRollbackPath := ""
	destinationNeedsRollbackFile := destinationExisted && (!originalIsDestination || !originalNeedsBackup || !originalIsDestinationEntry)
	if destinationNeedsRollbackFile {
		destinationRollbackPath, err = moveToUniqueYouTubeRollback(replacementPath)
		if err != nil {
			if originalNeedsBackup && movedOriginal != "" {
				if restoreErr := deps.restoreOriginal(movedOriginal, originalPath); restoreErr != nil {
					result.MovedOriginalPath = movedOriginal
					return fail("rollback", errors.Join(err, fmt.Errorf("restore original: %w", restoreErr)))
				}
			}
			return fail("publication", err)
		}
	}

	rollback := func(cause error, publishAttempted bool) SuspiciousRedownloadResult {
		errs := []error{cause}
		rollbackFailed := false
		if destinationRollbackPath != "" {
			if restoreErr := deps.restoreOriginal(destinationRollbackPath, replacementPath); restoreErr != nil {
				errs = append(errs, fmt.Errorf("restore replacement destination from %s: %w", destinationRollbackPath, restoreErr))
				rollbackFailed = true
			}
		} else if publishAttempted && !destinationExisted {
			if removeErr := os.Remove(replacementPath); removeErr != nil && !os.IsNotExist(removeErr) {
				errs = append(errs, fmt.Errorf("remove unpublished replacement: %w", removeErr))
				rollbackFailed = true
			}
		}
		if originalNeedsBackup && movedOriginal != "" {
			if restoreErr := deps.restoreOriginal(movedOriginal, originalPath); restoreErr != nil {
				errs = append(errs, fmt.Errorf("restore original from %s: %w", movedOriginal, restoreErr))
				result.MovedOriginalPath = movedOriginal
				rollbackFailed = true
			}
		}
		if rollbackFailed {
			return fail("rollback", errors.Join(errs...))
		}
		return fail("publication", cause)
	}

	if err := workCtx.Err(); err != nil {
		return rollback(err, false)
	}
	if err = deps.publish(workCtx, stagedMedia, replacementPath); err != nil {
		if ctxErr := workCtx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = errors.Join(err, ctxErr)
		}
		return rollback(err, true)
	}
	if err := workCtx.Err(); err != nil {
		return rollback(err, true)
	}
	if destinationRollbackPath != "" {
		if err := os.Remove(destinationRollbackPath); err != nil {
			return rollback(fmt.Errorf("remove replacement rollback backup: %w", err), true)
		}
	}

	result.Success = true
	result.Status = "replaced"
	result.MovedOriginalPath = movedOriginal
	return result
}

func moveToUniqueYouTubeRollback(path string) (string, error) {
	rollbackFile, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".youtube-rollback-*")
	if err != nil {
		return "", fmt.Errorf("reserve replacement rollback path: %w", err)
	}
	rollbackPath := rollbackFile.Name()
	if err := rollbackFile.Close(); err != nil {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("close replacement rollback reservation: %w", err)
	}
	if err := os.Remove(rollbackPath); err != nil {
		return "", fmt.Errorf("release replacement rollback reservation: %w", err)
	}
	if err := MoveFileWithFallback(path, rollbackPath); err != nil {
		_ = os.Remove(rollbackPath)
		return "", fmt.Errorf("preserve replacement destination: %w", err)
	}
	return rollbackPath, nil
}

func pathResolvesInsideDirectory(path, dir string) bool {
	if pathInsideDirectory(path, dir) {
		return true
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
		if parentErr != nil {
			return false
		}
		resolvedPath = filepath.Join(resolvedParent, filepath.Base(path))
	}
	return pathInsideDirectory(resolvedPath, resolvedDir)
}

func pathInsideDirectory(path, dir string) bool {
	pathAbs, pathErr := filepath.Abs(path)
	dirAbs, dirErr := filepath.Abs(dir)
	if pathErr != nil || dirErr != nil {
		return false
	}
	pathAbs = filepath.Clean(pathAbs)
	dirAbs = filepath.Clean(dirAbs)
	if runtime.GOOS == "windows" {
		pathAbs = strings.ToLower(pathAbs)
		dirAbs = strings.ToLower(dirAbs)
	}
	rel, err := filepath.Rel(dirAbs, pathAbs)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func resolveYouTubeStagedMedia(operationDir, outputBase, reportedPath string) (string, error) {
	reportedPath = filepath.Clean(strings.TrimSpace(reportedPath))
	if strings.TrimSpace(reportedPath) == "" {
		return "", errors.New("yt-dlp returned an empty media path")
	}
	if !sameFilesystemPath(strings.TrimSuffix(reportedPath, filepath.Ext(reportedPath)), outputBase) {
		return "", fmt.Errorf("yt-dlp media is outside the expected job output: %s", reportedPath)
	}
	info, err := os.Lstat(reportedPath)
	if err != nil {
		return "", fmt.Errorf("stat yt-dlp media: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("yt-dlp media is a symlink: %s", reportedPath)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("yt-dlp media is not a non-empty regular file: %s", reportedPath)
	}
	realOperationDir, err := filepath.EvalSymlinks(operationDir)
	if err != nil {
		return "", fmt.Errorf("resolve YouTube operation directory: %w", err)
	}
	realMediaPath, err := filepath.EvalSymlinks(reportedPath)
	if err != nil {
		return "", fmt.Errorf("resolve yt-dlp media: %w", err)
	}
	if !pathInsideDirectory(realMediaPath, realOperationDir) {
		return "", fmt.Errorf("yt-dlp media resolves outside the operation directory: %s", reportedPath)
	}
	return reportedPath, nil
}

func RedownloadSuspiciousTracksFromYouTube(reqs []SuspiciousRedownloadRequest) []SuspiciousRedownloadResult {
	results := make([]SuspiciousRedownloadResult, len(reqs))
	for i, req := range reqs {
		results[i] = RedownloadSuspiciousTrackFromYouTube(req)
	}
	return results
}

func populateDurationValidation(result *TrackMetadataUpdateResult, expectedDurationMs int) {
	if result == nil || expectedDurationMs <= 0 || strings.TrimSpace(result.FilePath) == "" {
		return
	}

	expectedSeconds := float64(expectedDurationMs) / 1000.0
	actualSeconds, err := GetAudioDuration(result.FilePath)
	if err != nil {
		result.ExpectedDurationSeconds = expectedSeconds
		result.ValidationWarning = fmt.Sprintf("failed to probe audio duration: %v", err)
		return
	}

	result.ExpectedDurationSeconds = expectedSeconds
	result.ActualDurationSeconds = actualSeconds
	result.DurationDeltaSeconds = actualSeconds - expectedSeconds
	result.Suspicious = IsSuspiciousDuration(expectedSeconds, actualSeconds)
}
