package backend

import (
	"errors"
	"net/http"
	"testing"
)

func TestStatusErrorTreatsUnauthorizedStatusesAsSessionTokenErrors(t *testing.T) {
	err := statusError(http.StatusUnauthorized, []byte(`{"message":"unauthorized"}`))
	if !isSessionTokenError(err) {
		t.Fatalf("expected sessionTokenError for 401, got %T", err)
	}
}

func TestStatusErrorTreatsRequestInvalidBodyAsSessionTokenError(t *testing.T) {
	err := statusError(http.StatusBadRequest, []byte(`{"success":false,"message":"ERR_REQUEST_INVALID","statusCode":400}`))
	if !isSessionTokenError(err) {
		t.Fatalf("expected sessionTokenError for expired-token 400 body, got %T", err)
	}
}

func TestStatusErrorKeepsRegularBadRequestsAsNormalErrors(t *testing.T) {
	err := statusError(http.StatusBadRequest, []byte(`{"success":false,"message":"TRACK_NOT_FOUND","statusCode":400}`))
	if isSessionTokenError(err) {
		t.Fatalf("did not expect sessionTokenError for regular 400 response")
	}

	var tokenErr *sessionTokenError
	if errors.As(err, &tokenErr) {
		t.Fatalf("did not expect wrapped sessionTokenError")
	}
}
