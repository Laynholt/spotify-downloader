package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

type HistoryItem struct {
	ID          string `json:"id"`
	SpotifyID   string `json:"spotify_id"`
	Title       string `json:"title"`
	Artists     string `json:"artists"`
	Album       string `json:"album"`
	DurationStr string `json:"duration_str"`
	CoverURL    string `json:"cover_url"`
	Quality     string `json:"quality"`
	Format      string `json:"format"`
	Path        string `json:"path"`
	Timestamp   int64  `json:"timestamp"`
}

var (
	historyDB   *bolt.DB
	historyDBMu sync.Mutex
)

const (
	historyBucket = "DownloadHistory"
	maxHistory    = 10000
)

func InitHistoryDB(appName string) error {
	_, err := openHistoryDB()
	return err
}

// openHistoryDB opens history.db once; bbolt locks the file, so a second
// concurrent open would fail instead of sharing the handle.
func openHistoryDB() (*bolt.DB, error) {
	historyDBMu.Lock()
	defer historyDBMu.Unlock()
	if historyDB != nil {
		return historyDB, nil
	}
	appDir, err := GetUserDataDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return nil, err
	}
	db, err := bolt.Open(filepath.Join(appDir, "history.db"), 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(historyBucket)); err != nil {
			return err
		}
		// Rewrites fetch history saved before it was keyed by type and URL.
		if b := tx.Bucket([]byte(fetchHistoryBucket)); b != nil && hasLegacyFetchKeys(b) {
			return compactFetchHistory(b)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	historyDB = db
	return db, nil
}

func updateHistory(fn func(tx *bolt.Tx) error) error {
	db, err := openHistoryDB()
	if err != nil {
		return err
	}
	return db.Update(fn)
}

func viewHistory(fn func(tx *bolt.Tx) error) error {
	db, err := openHistoryDB()
	if err != nil {
		return err
	}
	return db.View(fn)
}

func CloseHistoryDB() {
	historyDBMu.Lock()
	defer historyDBMu.Unlock()
	if historyDB != nil {
		historyDB.Close()
		historyDB = nil
	}
}

func AddHistoryItem(item HistoryItem, appName string) error {
	return updateHistory(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(historyBucket))
		if err != nil {
			return err
		}
		id, _ := b.NextSequence()

		item.ID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), id)
		item.Timestamp = time.Now().Unix()

		buf, err := json.Marshal(item)
		if err != nil {
			return err
		}

		if b.Stats().KeyN >= maxHistory {
			c := b.Cursor()
			count := 0
			for k, _ := c.First(); k != nil && count < maxHistory/20; k, _ = c.Next() {
				if err := b.Delete(k); err != nil {
					return err
				}
				count++
			}
		}

		return b.Put([]byte(item.ID), buf)
	})
}

func GetHistoryItems(appName string) ([]HistoryItem, error) {
	var items []HistoryItem
	err := viewHistory(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(historyBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var item HistoryItem
			if err := json.Unmarshal(v, &item); err == nil {
				items = append(items, item)
			}
			return nil
		})
	})

	sort.Slice(items, func(i, j int) bool {
		return items[i].Timestamp > items[j].Timestamp
	})

	return items, err
}

func ClearHistory(appName string) error {
	return updateHistory(func(tx *bolt.Tx) error {
		return tx.DeleteBucket([]byte(historyBucket))
	})
}

type FetchHistoryItem struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Info      string `json:"info"`
	Image     string `json:"image"`
	Data      string `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

const (
	fetchHistoryBucket = "FetchHistory"
	// Each item carries the whole fetched JSON (a playlist can be megabytes).
	maxFetchHistory = 200
)

// fetchHistoryKey makes a re-fetch of the same URL replace its old entry.
func fetchHistoryKey(item FetchHistoryItem) string {
	return item.Type + "|" + item.URL
}

func AddFetchHistoryItem(item FetchHistoryItem, appName string) error {
	return updateHistory(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(fetchHistoryBucket))
		if err != nil {
			return err
		}
		item.ID = fetchHistoryKey(item)
		item.Timestamp = time.Now().Unix()

		buf, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(item.ID), buf); err != nil {
			return err
		}
		if b.Stats().KeyN > maxFetchHistory {
			return compactFetchHistory(b)
		}
		return nil
	})
}

// compactFetchHistory keeps the newest entry per type and URL, stores it under
// its fetchHistoryKey and drops the oldest entries beyond maxFetchHistory.
func compactFetchHistory(b *bolt.Bucket) error {
	type entry struct {
		key  string
		item FetchHistoryItem
	}
	newest := map[string]entry{}
	var stale [][]byte
	err := b.ForEach(func(k, v []byte) error {
		var item FetchHistoryItem
		if err := json.Unmarshal(v, &item); err != nil {
			stale = append(stale, k)
			return nil
		}
		key := fetchHistoryKey(item)
		if old, ok := newest[key]; ok {
			if old.item.Timestamp > item.Timestamp {
				stale = append(stale, k)
				return nil
			}
			stale = append(stale, []byte(old.item.ID))
		}
		if key != string(k) {
			stale = append(stale, k)
		}
		item.ID = string(k)
		newest[key] = entry{key, item}
		return nil
	})
	if err != nil {
		return err
	}

	kept := make([]entry, 0, len(newest))
	for _, e := range newest {
		kept = append(kept, e)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].item.Timestamp > kept[j].item.Timestamp })
	if len(kept) > maxFetchHistory {
		for _, e := range kept[maxFetchHistory:] {
			stale = append(stale, []byte(e.item.ID))
		}
		kept = kept[:maxFetchHistory]
	}

	for _, k := range stale {
		if err := b.Delete(k); err != nil {
			return err
		}
	}
	for _, e := range kept {
		if e.item.ID == e.key {
			continue
		}
		e.item.ID = e.key
		buf, err := json.Marshal(e.item)
		if err != nil {
			return err
		}
		if err := b.Put([]byte(e.key), buf); err != nil {
			return err
		}
	}
	return nil
}

func hasLegacyFetchKeys(b *bolt.Bucket) bool {
	c := b.Cursor()
	for k, _ := c.First(); k != nil; k, _ = c.Next() {
		if !bytes.Contains(k, []byte("|")) {
			return true
		}
	}
	return false
}

func GetFetchHistoryItems(appName string) ([]FetchHistoryItem, error) {
	var items []FetchHistoryItem
	err := viewHistory(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(fetchHistoryBucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(_, v []byte) error {
			var item FetchHistoryItem
			if err := json.Unmarshal(v, &item); err == nil {
				items = append(items, item)
			}
			return nil
		})
	})

	sort.Slice(items, func(i, j int) bool {
		return items[i].Timestamp > items[j].Timestamp
	})

	return items, err
}

func ClearFetchHistory(appName string) error {
	return updateHistory(func(tx *bolt.Tx) error {
		return tx.DeleteBucket([]byte(fetchHistoryBucket))
	})
}

func ClearFetchHistoryByType(itemType string, appName string) error {
	return updateHistory(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(fetchHistoryBucket))
		if b == nil {
			return nil
		}

		var keysToDelete [][]byte
		err := b.ForEach(func(k, v []byte) error {
			var item FetchHistoryItem
			if err := json.Unmarshal(v, &item); err == nil && item.Type == itemType {
				keysToDelete = append(keysToDelete, k)
			}
			return nil
		})
		if err != nil {
			return err
		}

		for _, k := range keysToDelete {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func DeleteHistoryItem(id string, appName string) error {
	return updateHistory(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(historyBucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(id))
	})
}

func DeleteFetchHistoryItem(id string, appName string) error {
	return updateHistory(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(fetchHistoryBucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(id))
	})
}
