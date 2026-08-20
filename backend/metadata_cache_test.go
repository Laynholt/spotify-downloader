package backend

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

type doneSignalContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
}

func (c *doneSignalContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func openTestCache(t *testing.T) *MetadataCache {
	t.Helper()
	clock := newTestClock()
	cache, warnings := OpenMetadataCache(filepath.Join(t.TempDir(), "metadata-cache.json"), CacheOptions{
		MaxEntries: 5000,
		Now:        clock.Now,
		WriteDelay: 0,
		OnWarning: func(message string) {
			t.Errorf("unexpected cache warning: %s", message)
		},
	})
	if len(warnings) != 0 {
		t.Fatalf("open warnings = %v", warnings)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}

func TestCachedLookupTTLBoundaries(t *testing.T) {
	// Production break caught: a lookup treats an entry at its exact expiry as a hit.
	clock := newTestClock()
	path := filepath.Join(t.TempDir(), "metadata-cache.json")
	cache, warnings := OpenMetadataCache(path, CacheOptions{MaxEntries: 5000, Now: clock.Now, WriteDelay: 0})
	if len(warnings) != 0 {
		t.Fatalf("open warnings = %v", warnings)
	}
	defer cache.Close()

	loads := 0
	load := func(context.Context) (string, error) {
		loads++
		return "USRC17607839", nil
	}
	cacheable := func(value string) bool { return value != "" }

	value, outcome, err := CachedLookup(context.Background(), cache, "isrc:track", time.Hour, cacheable, load)
	if err != nil || value != "USRC17607839" || outcome != CacheMiss {
		t.Fatalf("first lookup = value %q outcome %q err %v", value, outcome, err)
	}
	value, outcome, err = CachedLookup(context.Background(), cache, "isrc:track", time.Hour, cacheable, load)
	if err != nil || value != "USRC17607839" || outcome != CacheHit {
		t.Fatalf("pre-expiry lookup = value %q outcome %q err %v", value, outcome, err)
	}
	clock.Add(time.Hour)
	value, outcome, err = CachedLookup(context.Background(), cache, "isrc:track", time.Hour, cacheable, load)
	if err != nil || value != "USRC17607839" || outcome != CacheMiss {
		t.Fatalf("exact-expiry lookup = value %q outcome %q err %v", value, outcome, err)
	}
	clock.Add(time.Nanosecond)
	value, outcome, err = CachedLookup(context.Background(), cache, "isrc:track", time.Hour, cacheable, load)
	if err != nil || value != "USRC17607839" || outcome != CacheHit {
		t.Fatalf("post-refresh lookup = value %q outcome %q err %v", value, outcome, err)
	}
	if loads != 2 {
		t.Fatalf("loader calls = %d, want 2", loads)
	}
}

func TestCachedLookupSingleflight(t *testing.T) {
	cache := openTestCache(t)
	var calls atomic.Int32
	outcomes := make(chan CacheOutcome, 12)
	loader := func(context.Context) (string, error) {
		calls.Add(1)
		time.Sleep(25 * time.Millisecond)
		return "USRC17607839", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, outcome, err := CachedLookup(context.Background(), cache, "isrc:track", 7*24*time.Hour, func(v string) bool { return v != "" }, loader)
			outcomes <- outcome
			if err != nil || value == "" {
				t.Errorf("value=%q err=%v", value, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("loader calls = %d", calls.Load())
	}
	close(outcomes)
	misses, shared := 0, 0
	for outcome := range outcomes {
		switch outcome {
		case CacheMiss:
			misses++
		case CacheShared:
			shared++
		}
	}
	if misses != 1 || shared != 11 {
		t.Fatalf("outcomes = miss:%d shared:%d, want miss:1 shared:11", misses, shared)
	}
}

func TestCachedLookupSingleflightAttributionFollowsRegisteredLoader(t *testing.T) {
	// Production break caught: miss/shared attribution must follow the loader
	// that singleflight actually registered, not a separate bookkeeping race.
	cache := openTestCache(t)
	started := make(chan struct{})
	release := make(chan struct{})
	loader := func(context.Context) (string, error) {
		close(started)
		<-release
		return "USRC17607839", nil
	}
	type lookupResult struct {
		value   string
		outcome CacheOutcome
		err     error
	}
	ownerResult := make(chan lookupResult, 1)
	go func() {
		value, outcome, err := CachedLookup(context.Background(), cache, "registered-loader", time.Hour, func(v string) bool { return v != "" }, loader)
		ownerResult <- lookupResult{value, outcome, err}
	}()
	<-started
	waiterContext := &doneSignalContext{Context: context.Background(), entered: make(chan struct{})}
	waiterResult := make(chan lookupResult, 1)
	go func() {
		value, outcome, err := CachedLookup(waiterContext, cache, "registered-loader", time.Hour, func(v string) bool { return v != "" }, loader)
		waiterResult <- lookupResult{value, outcome, err}
	}()
	<-waiterContext.entered
	close(release)
	owner := <-ownerResult
	waiter := <-waiterResult
	if owner.err != nil || owner.value != "USRC17607839" || owner.outcome != CacheMiss {
		t.Fatalf("owner = value %q outcome %q err %v", owner.value, owner.outcome, owner.err)
	}
	if waiter.err != nil || waiter.value != "USRC17607839" || waiter.outcome != CacheShared {
		t.Fatalf("waiter = value %q outcome %q err %v", waiter.value, waiter.outcome, waiter.err)
	}
}

func TestCachedLookupWaiterHonorsCancellation(t *testing.T) {
	cache := openTestCache(t)
	started := make(chan struct{})
	release := make(chan struct{})
	loader := func(context.Context) (string, error) {
		close(started)
		<-release
		return "USRC17607839", nil
	}
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := CachedLookup(context.Background(), cache, "isrc:track", time.Hour, func(v string) bool { return v != "" }, loader)
		firstDone <- err
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan struct {
		value   string
		outcome CacheOutcome
		err     error
	}, 1)
	go func() {
		value, outcome, err := CachedLookup(ctx, cache, "isrc:track", time.Hour, func(v string) bool { return v != "" }, loader)
		waiter <- struct {
			value   string
			outcome CacheOutcome
			err     error
		}{value, outcome, err}
	}()
	time.Sleep(time.Millisecond)
	cancel()
	result := <-waiter
	if !errors.Is(result.err, context.Canceled) || result.value != "" || result.outcome != CacheShared {
		t.Fatalf("cancelled waiter = value %q outcome %q err %v", result.value, result.outcome, result.err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("loader caller error = %v", err)
	}
}

func TestCachedLookupOwnerCancellationReportsMiss(t *testing.T) {
	// Production break caught: a caller that owns the singleflight load is
	// reported as shared when its own context is cancelled.
	cache := openTestCache(t)
	started := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan struct {
		value   string
		outcome CacheOutcome
		err     error
	}, 1)
	go func() {
		value, outcome, err := CachedLookup(ctx, cache, "owner-cancel", time.Hour, func(v string) bool { return v != "" }, func(context.Context) (string, error) {
			close(started)
			<-release
			return "USRC17607839", nil
		})
		result <- struct {
			value   string
			outcome CacheOutcome
			err     error
		}{value, outcome, err}
	}()
	<-started
	cancel()
	got := <-result
	if !errors.Is(got.err, context.Canceled) || got.value != "" || got.outcome != CacheMiss {
		t.Fatalf("cancelled owner = value %q outcome %q err %v", got.value, got.outcome, got.err)
	}
	close(release)
}

func TestCachedLookupDoesNotStoreEmptyValuesOrErrors(t *testing.T) {
	cache := openTestCache(t)
	var emptyCalls atomic.Int32
	emptyLoader := func(context.Context) (string, error) {
		emptyCalls.Add(1)
		return "", nil
	}
	for i := 0; i < 2; i++ {
		value, _, err := CachedLookup(context.Background(), cache, "empty", time.Hour, func(v string) bool { return v != "" }, emptyLoader)
		if err != nil || value != "" {
			t.Fatalf("empty lookup %d = value %q err %v", i, value, err)
		}
	}
	if emptyCalls.Load() != 2 {
		t.Fatalf("empty loader calls = %d, want 2", emptyCalls.Load())
	}

	wantErr := errors.New("upstream failed")
	var errorCalls atomic.Int32
	errorLoader := func(context.Context) (string, error) {
		errorCalls.Add(1)
		return "", wantErr
	}
	for i := 0; i < 2; i++ {
		value, _, err := CachedLookup(context.Background(), cache, "error", time.Hour, func(v string) bool { return v != "" }, errorLoader)
		if !errors.Is(err, wantErr) || value != "" {
			t.Fatalf("error lookup %d = value %q err %v", i, value, err)
		}
	}
	if errorCalls.Load() != 2 {
		t.Fatalf("error loader calls = %d, want 2", errorCalls.Load())
	}
}

func TestMetadataCacheEvictsOldestEntriesAtCapacity(t *testing.T) {
	clock := newTestClock()
	path := filepath.Join(t.TempDir(), "metadata-cache.json")
	cache, warnings := OpenMetadataCache(path, CacheOptions{MaxEntries: 5000, Now: clock.Now, WriteDelay: 0})
	if len(warnings) != 0 {
		t.Fatalf("open warnings = %v", warnings)
	}
	for i := 0; i < 5001; i++ {
		key := "key-" + string(rune(i))
		value, _, err := CachedLookup(context.Background(), cache, key, time.Hour, func(v string) bool { return v != "" }, func(context.Context) (string, error) {
			return key, nil
		})
		if err != nil || value != key {
			t.Fatalf("insert %d = value %q err %v", i, value, err)
		}
		clock.Add(time.Nanosecond)
	}
	if err := cache.Close(); err != nil {
		t.Fatalf("close cache: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	var file cacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatalf("decode cache: %v", err)
	}
	if len(file.Entries) != 5000 {
		t.Fatalf("entries = %d, want 5000", len(file.Entries))
	}
	if _, ok := file.Entries["key-"+string(rune(0))]; ok {
		t.Fatal("oldest entry was not evicted")
	}
}

func TestMetadataCacheCorruptionIsBackedUp(t *testing.T) {
	clock := newTestClock()
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata-cache.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0600); err != nil {
		t.Fatalf("write corrupt cache: %v", err)
	}
	cache, warnings := OpenMetadataCache(path, CacheOptions{MaxEntries: 5000, Now: clock.Now, WriteDelay: 0})
	if cache == nil {
		t.Fatal("cache is nil")
	}
	defer cache.Close()
	if len(warnings) == 0 {
		t.Fatal("corrupt cache did not report a warning")
	}
	entries, err := filepath.Glob(path + ".corrupt-*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("corrupt backups = %d, want 1", len(entries))
	}
}

func TestMetadataCacheInterruptedWritePreservesPrimary(t *testing.T) {
	clock := newTestClock()
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata-cache.json")
	valid := []byte(`{"version":1,"entries":{}}`)
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatalf("write primary: %v", err)
	}
	var warnings []string
	cache, openWarnings := OpenMetadataCache(path, CacheOptions{
		MaxEntries: 5000,
		Now:        clock.Now,
		WriteDelay: 0,
		OnWarning:  func(message string) { warnings = append(warnings, message) },
		ReplaceFile: func(tempPath, targetPath string) error {
			if err := os.WriteFile(tempPath, []byte(`{"version":1,"entries":{"partial":{"value":"bad"}}}`), 0600); err != nil {
				return err
			}
			return errors.New("simulated interrupted replace")
		},
	})
	if len(openWarnings) != 0 {
		t.Fatalf("open warnings = %v", openWarnings)
	}
	_, _, err := CachedLookup(context.Background(), cache, "new", time.Hour, func(v string) bool { return v != "" }, func(context.Context) (string, error) {
		return "value", nil
	})
	if err != nil {
		t.Fatalf("cache lookup: %v", err)
	}
	if err := cache.Close(); err == nil {
		t.Fatal("close succeeded despite replace failure")
	}
	if len(warnings) == 0 {
		t.Fatal("replace failure did not report a warning")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	if string(got) != string(valid) {
		t.Fatalf("primary changed after interrupted write: %s", got)
	}
}

func TestMetadataCachePersistenceFailureStaysInMemory(t *testing.T) {
	clock := newTestClock()
	path := filepath.Join(t.TempDir(), "metadata-cache.json")
	var warnings []string
	cache, openWarnings := OpenMetadataCache(path, CacheOptions{
		MaxEntries:  5000,
		Now:         clock.Now,
		WriteDelay:  0,
		OnWarning:   func(message string) { warnings = append(warnings, message) },
		ReplaceFile: func(_, _ string) error { return errors.New("write unavailable") },
	})
	if len(openWarnings) != 0 {
		t.Fatalf("open warnings = %v", openWarnings)
	}
	value, _, err := CachedLookup(context.Background(), cache, "key", time.Hour, func(v string) bool { return v != "" }, func(context.Context) (string, error) {
		return "value", nil
	})
	if err != nil || value != "value" {
		t.Fatalf("lookup = value %q err %v", value, err)
	}
	if _, _, err := CachedLookup(context.Background(), cache, "key", time.Hour, func(v string) bool { return v != "" }, func(context.Context) (string, error) {
		return "unexpected", nil
	}); err != nil {
		t.Fatalf("in-memory lookup: %v", err)
	}
	if err := cache.Close(); err == nil {
		t.Fatal("close succeeded despite persistence failure")
	}
	if len(warnings) == 0 {
		t.Fatal("persistence failure did not report a warning")
	}
}

func TestMetadataCacheRegistryReusesInstalledStore(t *testing.T) {
	cache := openTestCache(t)
	InstallMetadataCache(cache)
	t.Cleanup(func() { InstallMetadataCache(nil) })
	if got := CurrentMetadataCache(); got != cache {
		t.Fatalf("current cache pointer %p, want %p", got, cache)
	}
	if got := CurrentMetadataCache(); got != cache {
		t.Fatalf("current cache pointer changed to %p", got)
	}
}

func TestMetadataCacheReadFailureWarnsAndStartsEmpty(t *testing.T) {
	clock := newTestClock()
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata-cache.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("make unreadable primary: %v", err)
	}
	var warnings []string
	cache, openWarnings := OpenMetadataCache(path, CacheOptions{MaxEntries: 5000, Now: clock.Now, OnWarning: func(message string) {
		warnings = append(warnings, message)
	}})
	if len(openWarnings) == 0 || len(warnings) == 0 {
		t.Fatalf("read failure warnings: open=%v callback=%v", openWarnings, warnings)
	}
	if cache == nil {
		t.Fatal("cache is nil after read failure")
	}
	_ = cache.Close()
}
