package backend

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type FFmpegStatus struct {
	Installed   bool   `json:"installed"`
	Source      string `json:"source"`
	FFmpegPath  string `json:"ffmpeg_path"`
	FFprobePath string `json:"ffprobe_path"`
	Error       string `json:"error"`
}

func GetFFmpegStatus() FFmpegStatus { return GetFFmpegStatusContext(context.Background()) }

// verifiedFFmpeg is the last pair that passed the version check, so every
// probe and conversion does not spawn two extra "-version" processes.
var verifiedFFmpeg atomic.Pointer[verifiedFFmpegPair]

type verifiedFFmpegPair struct {
	searchKey string
	status    FFmpegStatus
}

// ffmpegSearchKey changes whenever detection could find a different pair.
func ffmpegSearchKey() string {
	binDir := ""
	if paths, err := CurrentRuntimePaths(); err == nil {
		binDir = paths.BinDir
	}
	return binDir + "\x00" + os.Getenv("PATH")
}

// installedFFmpegStatus reuses the verified pair while it still exists where
// detection found it.
func installedFFmpegStatus(ctx context.Context) FFmpegStatus {
	if cached := verifiedFFmpeg.Load(); cached != nil && cached.searchKey == ffmpegSearchKey() {
		_, ffmpegErr := os.Stat(cached.status.FFmpegPath)
		_, ffprobeErr := os.Stat(cached.status.FFprobePath)
		if ffmpegErr == nil && ffprobeErr == nil {
			return cached.status
		}
	}
	return GetFFmpegStatusContext(ctx)
}

// A source is usable only when both binaries run; never mix installations.
func GetFFmpegStatusContext(ctx context.Context) FFmpegStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	searchKey := ffmpegSearchKey()
	status := detectFFmpeg(ctx)
	if status.Installed {
		verifiedFFmpeg.Store(&verifiedFFmpegPair{searchKey: searchKey, status: status})
	} else {
		verifiedFFmpeg.Store(nil)
	}
	return status
}

func detectFFmpeg(ctx context.Context) FFmpegStatus {
	var failures []string
	names := []string{"ffmpeg", "ffprobe"}
	if runtime.GOOS == "windows" {
		names = []string{"ffmpeg.exe", "ffprobe.exe"}
	}
	paths, err := CurrentRuntimePaths()
	if err == nil {
		status := FFmpegStatus{Source: "portable", FFmpegPath: filepath.Join(paths.BinDir, names[0]), FFprobePath: filepath.Join(paths.BinDir, names[1])}
		if err := checkFFmpegPair(ctx, status); err == nil {
			status.Installed = true
			return status
		} else {
			failures = append(failures, "Portable: "+err.Error())
		}
	} else {
		failures = append(failures, "Portable: "+err.Error())
	}
	if ctx.Err() != nil {
		return FFmpegStatus{Error: ctx.Err().Error()}
	}
	ffmpeg, err := exec.LookPath(names[0])
	if err != nil {
		failures = append(failures, "PATH: "+err.Error())
	} else {
		ffmpeg, err = filepath.Abs(ffmpeg)
		status := FFmpegStatus{Source: "path", FFmpegPath: ffmpeg, FFprobePath: filepath.Join(filepath.Dir(ffmpeg), names[1])}
		if err == nil {
			err = checkFFmpegPair(ctx, status)
		}
		if err == nil {
			status.Installed = true
			return status
		}
		failures = append(failures, fmt.Sprintf("PATH pair beside FFmpeg %s: %v", ffmpeg, err))
	}
	return FFmpegStatus{Error: strings.Join(failures, "; ")}
}

func checkFFmpegPair(ctx context.Context, status FFmpegStatus) error {
	for _, path := range []string{status.FFmpegPath, status.FFprobePath} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ValidateExecutable(path); err != nil {
			return err
		}
		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := newManagedCommand(checkCtx, path, "-version").Run()
		if checkCtx.Err() != nil {
			err = checkCtx.Err()
		}
		cancel()
		if err != nil {
			return fmt.Errorf("%s version check: %w", path, err)
		}
	}
	return nil
}

func ffmpegStatusError(ctx context.Context, status FFmpegStatus) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %s", ErrFFmpegUnavailable, status.Error)
}

var ErrFFmpegUnavailable = errors.New("FFmpeg/FFprobe unavailable")
