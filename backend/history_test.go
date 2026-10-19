package backend

import (
	"encoding/json"
	"fmt"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func useTempHistoryDB(t *testing.T) {
	t.Helper()
	CloseHistoryDB()
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(CloseHistoryDB)
}

func TestFetchHistoryReplacesRepeatedURLAndStaysBounded(t *testing.T) {
	useTempHistoryDB(t)
	for i := 0; i < maxFetchHistory+5; i++ {
		if err := AddFetchHistoryItem(FetchHistoryItem{URL: fmt.Sprintf("https://x/%d", i), Type: "album"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddFetchHistoryItem(FetchHistoryItem{URL: "https://x/204", Type: "album", Name: "again"}, ""); err != nil {
		t.Fatal(err)
	}
	items, err := GetFetchHistoryItems("")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != maxFetchHistory {
		t.Fatalf("got %d items, want %d", len(items), maxFetchHistory)
	}
	seen := 0
	for _, item := range items {
		if item.URL == "https://x/204" {
			seen++
			if item.Name != "again" {
				t.Fatalf("repeated fetch kept stale entry: %+v", item)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("repeated URL stored %d times", seen)
	}
	if err := DeleteFetchHistoryItem(items[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	if after, _ := GetFetchHistoryItems(""); len(after) != maxFetchHistory-1 {
		t.Fatalf("delete by returned ID failed: %d items", len(after))
	}
}

func TestOpenHistoryDBMigratesLegacyFetchKeys(t *testing.T) {
	useTempHistoryDB(t)
	db, err := openHistoryDB()
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(fetchHistoryBucket))
		if err != nil {
			return err
		}
		for i, name := range []string{"old", "new"} {
			buf, _ := json.Marshal(FetchHistoryItem{URL: "u", Type: "playlist", Name: name, Timestamp: int64(i)})
			if err := b.Put([]byte(fmt.Sprintf("%d-%d", i, i)), buf); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	CloseHistoryDB()

	items, err := GetFetchHistoryItems("")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "new" || items[0].ID != "playlist|u" {
		t.Fatalf("legacy history not migrated: %+v", items)
	}
}
