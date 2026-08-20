package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	metadataCacheVersion    = 1
	metadataCacheMaxEntries = 5000
)

// CacheOptions configures a metadata cache. The zero value uses the portable
// cache defaults, the wall clock, and the production atomic replacement
// function.
type CacheOptions struct {
	MaxEntries  int
	Now         func() time.Time
	WriteDelay  time.Duration
	ReplaceFile func(tempPath, targetPath string) error
	OnWarning   func(message string)
}

// CacheOutcome describes whether a value came from the cache or a loader.
type CacheOutcome string

const (
	CacheHit    CacheOutcome = "hit"
	CacheMiss   CacheOutcome = "miss"
	CacheShared CacheOutcome = "shared"
)

type cacheEntry struct {
	Value      json.RawMessage `json:"value"`
	ExpiresAt  time.Time       `json:"expires_at"`
	LastAccess time.Time       `json:"last_access"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

// MetadataCache stores successful metadata lookups in memory and persists
// them as a versioned JSON envelope. Persistence is best effort: an I/O
// failure leaves the in-memory cache available to callers.
type MetadataCache struct {
	mu         sync.Mutex
	path       string
	entries    map[string]cacheEntry
	maxEntries int
	now        func() time.Time

	group singleflight.Group

	replaceFile func(tempPath, targetPath string) error
	onWarning   func(message string)
	writeDelay  time.Duration

	dirty      chan struct{}
	stop       chan struct{}
	done       chan struct{}
	dirtyState bool

	closed    bool
	closeOnce sync.Once
	closeErr  error
	lastErr   error
}

type cacheFlightResult[T any] struct {
	value  T
	loaded bool
	owner  *cacheFlightCall
}

type cacheFlightCall struct {
	started atomic.Bool
}

var metadataCacheRegistry struct {
	sync.RWMutex
	cache *MetadataCache
}

// InstallMetadataCache installs the process-wide cache once and returns the
// active instance. Passing nil clears the registry (used by process teardown
// and tests). A second non-nil installation never replaces the first store.
func InstallMetadataCache(cache *MetadataCache) *MetadataCache {
	metadataCacheRegistry.Lock()
	defer metadataCacheRegistry.Unlock()
	if cache == nil {
		metadataCacheRegistry.cache = nil
		return nil
	}
	if metadataCacheRegistry.cache == nil {
		metadataCacheRegistry.cache = cache
	}
	return metadataCacheRegistry.cache
}

// CurrentMetadataCache returns the process-wide installed cache, if any.
// It never creates a new store.
func CurrentMetadataCache() *MetadataCache {
	metadataCacheRegistry.RLock()
	defer metadataCacheRegistry.RUnlock()
	return metadataCacheRegistry.cache
}

// OpenMetadataCache opens path and starts the coalescing persistence worker.
// Cache read and corruption-recovery failures are returned as warnings and do
// not prevent an in-memory cache from being used.
func OpenMetadataCache(path string, opts CacheOptions) (*MetadataCache, []string) {
	if opts.MaxEntries <= 0 {
		opts.MaxEntries = metadataCacheMaxEntries
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.WriteDelay < 0 {
		opts.WriteDelay = 0
	}
	if opts.ReplaceFile == nil {
		opts.ReplaceFile = atomicReplaceFile
	}

	warnings := make([]string, 0, 2)
	warn := func(message string) {
		warnings = append(warnings, message)
		if opts.OnWarning != nil {
			opts.OnWarning(message)
		}
	}

	cache := &MetadataCache{
		path:        path,
		entries:     make(map[string]cacheEntry),
		maxEntries:  opts.MaxEntries,
		now:         opts.Now,
		replaceFile: opts.ReplaceFile,
		onWarning:   opts.OnWarning,
		writeDelay:  opts.WriteDelay,
		dirty:       make(chan struct{}, 1),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}

	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			warn(fmt.Sprintf("metadata cache directory unavailable: %v", err))
		} else if data, err := os.ReadFile(path); err == nil {
			var file cacheFile
			if err := json.Unmarshal(data, &file); err != nil || file.Version != metadataCacheVersion || file.Entries == nil {
				if err == nil {
					err = fmt.Errorf("unsupported cache envelope version %d", file.Version)
				}
				warn(fmt.Sprintf("metadata cache is corrupt: %v", err))
				backupPath := path + ".corrupt-" + strconv.FormatInt(opts.Now().UnixNano(), 10)
				if renameErr := os.Rename(path, backupPath); renameErr != nil {
					warn(fmt.Sprintf("metadata cache corrupt backup failed: %v", renameErr))
				}
			} else {
				cache.entries = file.Entries
				cache.mu.Lock()
				cache.evictExpiredLocked(cache.now())
				cache.evictCapacityLocked()
				cache.mu.Unlock()
			}
		} else if !os.IsNotExist(err) {
			warn(fmt.Sprintf("metadata cache read failed: %v", err))
		}
	}

	go cache.persistenceLoop()
	return cache, warnings
}

// Close flushes pending cache state and stops the persistence worker. A
// persistence failure is returned to the caller but is otherwise warning-only
// for cache consumers.
func (c *MetadataCache) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		close(c.stop)
		<-c.done
		c.mu.Lock()
		c.closeErr = c.lastErr
		c.mu.Unlock()
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}

func (c *MetadataCache) persistenceLoop() {
	defer close(c.done)
	for {
		select {
		case <-c.dirty:
			if c.writeDelay > 0 {
				timer := time.NewTimer(c.writeDelay)
				select {
				case <-timer.C:
				case <-c.stop:
					timer.Stop()
				}
			}
			// Mutations that arrived while the delay elapsed are represented by
			// the same dirty snapshot; discard their duplicate wake-ups before
			// writing once.
			c.drainDirty()
			c.flushPending()
		case <-c.stop:
			c.flushPending()
			return
		}
	}
}

func (c *MetadataCache) drainDirty() {
	for {
		select {
		case <-c.dirty:
		default:
			return
		}
	}
}

func (c *MetadataCache) flushPending() {
	data, shouldWrite, err := c.snapshot()
	if err != nil {
		c.recordPersistenceError(err)
		return
	}
	if !shouldWrite {
		return
	}
	if err := c.writeSnapshot(data); err != nil {
		c.recordPersistenceError(err)
		return
	}
	c.mu.Lock()
	c.lastErr = nil
	c.mu.Unlock()
}

func (c *MetadataCache) snapshot() ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirtyPendingLocked() {
		return nil, false, nil
	}
	c.evictExpiredLocked(c.now())
	c.evictCapacityLocked()
	data, err := json.Marshal(cacheFile{Version: metadataCacheVersion, Entries: cloneEntries(c.entries)})
	if err != nil {
		return nil, true, fmt.Errorf("metadata cache encode failed: %w", err)
	}
	c.setDirtyLocked(false)
	return data, true, nil
}

func (c *MetadataCache) writeSnapshot(data []byte) error {
	if c.path == "" {
		return errors.New("metadata cache path is empty")
	}
	tempPath := c.path + ".tmp"
	f, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("metadata cache temp open failed: %w", err)
	}
	writeErr := error(nil)
	if _, err := f.Write(data); err != nil {
		writeErr = fmt.Errorf("metadata cache temp write failed: %w", err)
	} else if err := f.Sync(); err != nil {
		writeErr = fmt.Errorf("metadata cache temp sync failed: %w", err)
	} else if err := f.Close(); err != nil {
		writeErr = fmt.Errorf("metadata cache temp close failed: %w", err)
	}
	if writeErr != nil {
		_ = f.Close()
		_ = os.Remove(tempPath)
		return writeErr
	}
	if err := c.replaceFile(tempPath, c.path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("metadata cache atomic replace failed: %w", err)
	}
	return nil
}

func (c *MetadataCache) recordPersistenceError(err error) {
	c.mu.Lock()
	c.lastErr = err
	// Keep the snapshot pending so a later mutation or Close can retry, but do
	// not enqueue another signal here (the writer is already draining one).
	c.dirtyState = true
	c.mu.Unlock()
	c.warn(err.Error())
}

func (c *MetadataCache) warn(message string) {
	if c.onWarning != nil {
		c.onWarning(message)
	}
}

func (c *MetadataCache) dirtyPendingLocked() bool {
	return c.dirtyState
}

func (c *MetadataCache) setDirtyLocked(value bool) {
	c.dirtyState = value
}

func (c *MetadataCache) markDirtyLocked() {
	if c.closed {
		return
	}
	c.dirtyState = true
	select {
	case c.dirty <- struct{}{}:
	default:
	}
}

func cloneEntries(entries map[string]cacheEntry) map[string]cacheEntry {
	clone := make(map[string]cacheEntry, len(entries))
	for key, entry := range entries {
		clone[key] = cacheEntry{
			Value:      append(json.RawMessage(nil), entry.Value...),
			ExpiresAt:  entry.ExpiresAt,
			LastAccess: entry.LastAccess,
		}
	}
	return clone
}

func (c *MetadataCache) evictExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.ExpiresAt) {
			delete(c.entries, key)
			c.markDirtyLocked()
		}
	}
}

func (c *MetadataCache) evictCapacityLocked() {
	for len(c.entries) > c.maxEntries {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.LastAccess.Before(oldest) || (entry.LastAccess.Equal(oldest) && key < oldestKey) {
				oldestKey = key
				oldest = entry.LastAccess
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.entries, oldestKey)
		c.markDirtyLocked()
	}
}

func (c *MetadataCache) lookupRaw(key string, now time.Time) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if !now.Before(entry.ExpiresAt) {
		delete(c.entries, key)
		c.markDirtyLocked()
		return nil, false
	}
	entry.LastAccess = now
	c.entries[key] = entry
	c.markDirtyLocked()
	return append(json.RawMessage(nil), entry.Value...), true
}

func (c *MetadataCache) storeRaw(key string, value json.RawMessage, ttl time.Duration, now time.Time) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.evictExpiredLocked(now)
	c.entries[key] = cacheEntry{Value: append(json.RawMessage(nil), value...), ExpiresAt: now.Add(ttl), LastAccess: now}
	c.evictCapacityLocked()
	c.markDirtyLocked()
}

// CachedLookup returns a cached value or coalesces concurrent loads for key.
// A caller waiting on an existing load can stop waiting when its context is
// cancelled without cancelling the loader owned by the first caller.
func CachedLookup[T any](ctx context.Context, cache *MetadataCache, key string, ttl time.Duration, cacheable func(T) bool, load func(context.Context) (T, error)) (T, CacheOutcome, error) {
	var zero T
	if cache == nil {
		return zero, CacheMiss, errors.New("metadata cache is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return zero, CacheMiss, err
	}
	if raw, ok := cache.lookupRaw(key, cache.now()); ok {
		var value T
		if err := json.Unmarshal(raw, &value); err == nil {
			return value, CacheHit, nil
		}
		cache.mu.Lock()
		delete(cache.entries, key)
		cache.markDirtyLocked()
		cache.mu.Unlock()
	}

	flightCall := &cacheFlightCall{}
	resultCh := cache.group.DoChan(key, func() (any, error) {
		flightCall.started.Store(true)
		// A previous flight may have populated the key between the initial
		// lookup and this singleflight invocation.
		if raw, ok := cache.lookupRaw(key, cache.now()); ok {
			var value T
			if err := json.Unmarshal(raw, &value); err == nil {
				return cacheFlightResult[T]{value: value, owner: flightCall}, nil
			}
		}
		value, err := load(ctx)
		if err != nil {
			return cacheFlightResult[T]{value: value, loaded: true, owner: flightCall}, err
		}
		if cacheable != nil && cacheable(value) {
			if encoded, marshalErr := json.Marshal(value); marshalErr == nil {
				cache.storeRaw(key, encoded, ttl, cache.now())
			} else {
				cache.warn(fmt.Sprintf("metadata cache value encode failed: %v", marshalErr))
			}
		}
		return cacheFlightResult[T]{value: value, loaded: true, owner: flightCall}, nil
	})

	select {
	case <-ctx.Done():
		if flightCall.started.Load() {
			return zero, CacheMiss, ctx.Err()
		}
		return zero, CacheShared, ctx.Err()
	case result := <-resultCh:
		flightResult, ok := result.Val.(cacheFlightResult[T])
		if !ok {
			return zero, CacheShared, errors.New("metadata cache singleflight result type mismatch")
		}
		outcome := CacheShared
		if flightResult.owner == flightCall {
			outcome = CacheMiss
		}
		if !flightResult.loaded && result.Err == nil {
			outcome = CacheHit
		}
		return flightResult.value, outcome, result.Err
	}
}
