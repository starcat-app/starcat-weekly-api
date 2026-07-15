package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHelloGitHubAdminSyncReturnsAccepted(t *testing.T) {
	called := false
	handler := NewHelloGitHubHandler(func() { called = true })
	recorder := httptest.NewRecorder()
	handler.HandleAdminSync(recorder, httptest.NewRequest(http.MethodPost, "/internal/sync/hellogithub", nil))
	if recorder.Code != http.StatusAccepted || !called {
		t.Fatalf("status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
}
