package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RuntimePaths contains the directories owned by one portable application run.
type RuntimePaths struct {
	ExecutableDir     string
	RootDir           string
	BinDir            string
	CacheDir          string
	DenoCacheDir      string
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

const runtimeStaleArtifactAge = 24 * time.Hour

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
		DenoCacheDir:      filepath.Join(root, "cache", "deno"),
		TempDir:           filepath.Join(root, "temp"),
		RunTempDir:        filepath.Join(root, "temp", runID),
		MetadataCacheFile: filepath.Join(root, "cache", "metadata-cache.json"),
	}, nil
}

// Prepare creates and verifies the portable runtime directories.
func (p *RuntimePaths) Prepare() error {
	for _, dir := range []string{p.RootDir, p.BinDir, p.CacheDir, p.DenoCacheDir, p.RunTempDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return p.portableWriteError(err)
		}
	}
	if err := p.cleanupStaleArtifacts(time.Now()); err != nil {
		return p.portableWriteError(err)
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

func (p *RuntimePaths) cleanupStaleArtifacts(now time.Time) error {
	entries, err := os.ReadDir(p.TempDir)
	if err != nil {
		return err
	}
	currentRun := filepath.Clean(p.RunTempDir)
	for _, entry := range entries {
		entryPath := filepath.Join(p.TempDir, entry.Name())
		if filepath.Clean(entryPath) == currentRun {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if now.Sub(info.ModTime()) < runtimeStaleArtifactAge {
			continue
		}
		if info.IsDir() || strings.HasPrefix(entry.Name(), "spotidownloader-update-") {
			if err := os.RemoveAll(entryPath); err != nil {
				return err
			}
		}
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

// CreateRunTemp creates a temporary file in the active run's portable temp
// directory. It never falls back to the process or system temp directory.
func CreateRunTemp(pattern string) (*os.File, error) {
	paths, err := CurrentRuntimePaths()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.RunTempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create runtime run temp directory: %w", err)
	}
	return os.CreateTemp(paths.RunTempDir, pattern)
}

// CreateUpdateTempPath returns a path below the runtime temp directory for an
// updater script or mount directory. The caller owns creation of the file or
// directory at that path.
func CreateUpdateTempPath(name string) (string, error) {
	paths, err := CurrentRuntimePaths()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		return "", errors.New("runtime update temp name is empty")
	}
	path := filepath.Join(paths.TempDir, name)
	rel, err := filepath.Rel(paths.TempDir, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("runtime update temp path escapes portable temp directory: %s", name)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("failed to create runtime update temp directory: %w", err)
	}
	return path, nil
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
