package main

import (
	"path/filepath"
	"testing"

	"github.com/afkarxyz/SpotiDownloader/backend"
)

// Production break caught: settings are written into the portable helper bin
// directory instead of remaining in the legacy per-user data directory.
func TestGetConfigPathStaysInUserDataDir(t *testing.T) {
	if _, err := backend.InitRuntimePaths(); err != nil {
		t.Fatal(err)
	}

	app := &App{}
	got, err := app.GetConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	userDataDir, err := backend.GetUserDataDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(userDataDir, "config.json")
	if got != want {
		t.Fatalf("config path = %q, want legacy user-data path %q", got, want)
	}
}
