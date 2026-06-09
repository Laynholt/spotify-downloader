package backend

import (
	"os"
	"testing"
)

func TestExtractSessionTokenFromResponse(t *testing.T) {
	token := "eyJhbGciOiJIUzI1NiJ9.payload.signature"
	got, err := extractSessionTokenFromResponse([]byte(`{"token":"` + token + `"}`))
	if err != nil {
		t.Fatalf("extractSessionTokenFromResponse returned error: %v", err)
	}
	if got != token {
		t.Fatalf("expected %q, got %q", token, got)
	}
}

func TestExtractSessionTokenRejectsMissingToken(t *testing.T) {
	if _, err := extractSessionTokenFromResponse([]byte(`{"ok":true}`)); err == nil {
		t.Fatal("expected error for response without token")
	}
}

func TestIsLikelySessionToken(t *testing.T) {
	if !isLikelySessionToken("eyJhbGciOiJIUzI1NiJ9.payload.signature") {
		t.Fatal("expected JWT-like token to be accepted")
	}
	if isLikelySessionToken("not-a-token") {
		t.Fatal("expected plain text to be rejected")
	}
}

func TestFetchSessionTokenLive(t *testing.T) {
	if os.Getenv("SPOTIDOWNLOADER_LIVE_TOKEN_TEST") != "1" {
		t.Skip("set SPOTIDOWNLOADER_LIVE_TOKEN_TEST=1 to run live browser token fetch")
	}

	token, err := FetchSessionTokenWithParams(20, 0)
	if err != nil {
		t.Fatalf("FetchSessionTokenWithParams returned error: %v", err)
	}
	if !isLikelySessionToken(token) {
		t.Fatalf("expected JWT-like token, got %q", token)
	}
}
