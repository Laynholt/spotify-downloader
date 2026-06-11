package backend

import "testing"

func TestSelectAppUpdateAssetForPlatform(t *testing.T) {
	assets := []GitHubReleaseAsset{
		{Name: "SpotiDownloader.exe", BrowserDownloadURL: "windows"},
		{Name: "SpotiDownloader-Intel.dmg", BrowserDownloadURL: "mac-intel"},
		{Name: "SpotiDownloader.dmg", BrowserDownloadURL: "mac-arm"},
		{Name: "SpotiDownloader.AppImage", BrowserDownloadURL: "linux"},
	}

	tests := []struct {
		goos string
		arch string
		want string
	}{
		{"windows", "amd64", "windows"},
		{"linux", "amd64", "linux"},
		{"darwin", "amd64", "mac-intel"},
		{"darwin", "arm64", "mac-arm"},
	}

	for _, tt := range tests {
		got, ok := SelectAppUpdateAsset(tt.goos, tt.arch, assets)
		if !ok || got.BrowserDownloadURL != tt.want {
			t.Fatalf("SelectAppUpdateAsset(%s,%s) = %#v ok=%v, want %s", tt.goos, tt.arch, got, ok, tt.want)
		}
	}
}

func TestIsAppVersionNewer(t *testing.T) {
	if !IsAppVersionNewer("v7.2.0", "7.1.9") {
		t.Fatalf("expected v7.2.0 to be newer than 7.1.9")
	}
	if IsAppVersionNewer("7.10.0", "7.10.0") {
		t.Fatalf("same version must not be newer")
	}
	if IsAppVersionNewer("7.2.0", "7.10.0") {
		t.Fatalf("lexicographic comparison regression: 7.2.0 must not be newer than 7.10.0")
	}
}
