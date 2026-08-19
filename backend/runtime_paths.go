package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RuntimePaths contains the directories owned by one portable application run.
type RuntimePaths struct {
	ExecutableDir     string
	RootDir           string
	BinDir            string
	CacheDir          string
	TempDir           string
	RunTempDir        string
	MetadataCacheFile string
}

var runtimePathsState struct {
	sync.RWMutex
	paths       RuntimePaths
	initialized bool
	initErr     error
}

var errRuntimePathsNotInitialized = errors.New("runtime paths are not initialized")

// ResolveRuntimePaths builds the portable runtime layout beside executablePath.
func ResolveRuntimePaths(executablePath, runID string) (RuntimePaths, error) {
	absExe, err := filepath.Abs(executablePath)
	if err != nil {
		return RuntimePaths{}, err
	}

	executableDir := filepath.Dir(absExe)
	root := filepath.Join(executableDir, "SpotiDownloader.runtime")
	return RuntimePaths{
		ExecutableDir:     executableDir,
		RootDir:           root,
		BinDir:            filepath.Join(root, "bin"),
		CacheDir:          filepath.Join(root, "cache"),
		TempDir:           filepath.Join(root, "temp"),
		RunTempDir:        filepath.Join(root, "temp", runID),
		MetadataCacheFile: filepath.Join(root, "cache", "metadata-cache.json"),
	}, nil
}

// Prepare creates and verifies the portable runtime directories.
func (p *RuntimePaths) Prepare() error {
	for _, dir := range []string{p.RootDir, p.BinDir, p.CacheDir, p.RunTempDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return p.portableWriteError(err)
		}
	}

	probePath := filepath.Join(p.RootDir, ".write-probe")
	probe, err := os.OpenFile(probePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return p.portableWriteError(err)
	}
	if err := probe.Close(); err != nil {
		return p.portableWriteError(err)
	}
	if err := os.Remove(probePath); err != nil {
		return p.portableWriteError(err)
	}
	return nil
}

func (p *RuntimePaths) portableWriteError(err error) error {
	return fmt.Errorf("portable runtime directory is not writable (%s): %w", p.RootDir, err)
}

// CleanupRun removes the temporary directory for this run.
func (p *RuntimePaths) CleanupRun() error {
	if p.RunTempDir == "" {
		return errors.New("runtime run directory is not configured")
	}
	return os.RemoveAll(p.RunTempDir)
}

// InitRuntimePaths resolves, prepares, and registers the paths for this process.
func InitRuntimePaths() (RuntimePaths, error) {
	executablePath, err := os.Executable()
	if err != nil {
		return registerRuntimePathsFailure(fmt.Errorf("failed to resolve executable path: %w", err))
	}

	runID := fmt.Sprintf("run-%d-%d", os.Getpid(), time.Now().UnixNano())
	paths, err := ResolveRuntimePaths(executablePath, runID)
	if err != nil {
		return registerRuntimePathsFailure(err)
	}
	if err := paths.Prepare(); err != nil {
		return registerRuntimePathsFailure(err)
	}

	runtimePathsState.Lock()
	runtimePathsState.paths = paths
	runtimePathsState.initialized = true
	runtimePathsState.initErr = nil
	runtimePathsState.Unlock()
	return paths, nil
}

func registerRuntimePathsFailure(err error) (RuntimePaths, error) {
	runtimePathsState.Lock()
	runtimePathsState.paths = RuntimePaths{}
	runtimePathsState.initialized = false
	runtimePathsState.initErr = err
	runtimePathsState.Unlock()
	return RuntimePaths{}, err
}

// CurrentRuntimePaths returns the process's initialized portable runtime paths.
func CurrentRuntimePaths() (RuntimePaths, error) {
	runtimePathsState.RLock()
	defer runtimePathsState.RUnlock()
	if !runtimePathsState.initialized {
		if runtimePathsState.initErr != nil {
			return RuntimePaths{}, fmt.Errorf("%w: %v", errRuntimePathsNotInitialized, runtimePathsState.initErr)
		}
		return RuntimePaths{}, errRuntimePathsNotInitialized
	}
	return runtimePathsState.paths, nil
}
