package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestHelloGitHubAdminSyncReturnsAccepted(t *testing.T) {
	called := false
	handler := NewHelloGitHubHandler(func() { called = true }, nil, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/internal/sources/hellogithub/sync", nil)
	request.SetPathValue("source_code", model.SourceHelloGitHub)
	handler.HandleSourceSync(recorder, request)
	if recorder.Code != http.StatusAccepted || !called {
		t.Fatalf("status=%d called=%t body=%s", recorder.Code, called, recorder.Body.String())
	}
}

type helloGitHubBackfillStarterStub struct {
	from int
	to   int
}

func (s *helloGitHubBackfillStarterStub) Start(fromVolume, toVolume int, _ string) (model.IngestBatchAcceptance, error) {
	s.from, s.to = fromVolume, toVolume
	return model.IngestBatchAcceptance{BatchID: "batch-1", SourceCode: model.SourceHelloGitHub, Status: model.IngestBatchPending}, nil
}

func TestHelloGitHubAdminSyncStartsPersistentBackfill(t *testing.T) {
	starter := &helloGitHubBackfillStarterStub{}
	handler := NewHelloGitHubHandler(func() {}, nil, starter)
	request := httptest.NewRequest(http.MethodPost, "/internal/sources/hellogithub/sync",
		bytes.NewBufferString(`{"mode":"backfill","from_volume":2,"to_volume":5}`))
	request.SetPathValue("source_code", model.SourceHelloGitHub)
	recorder := httptest.NewRecorder()
	handler.HandleSourceSync(recorder, request)
	if recorder.Code != http.StatusAccepted || starter.from != 2 || starter.to != 5 {
		t.Fatalf("status=%d from=%d to=%d body=%s", recorder.Code, starter.from, starter.to, recorder.Body.String())
	}
}
