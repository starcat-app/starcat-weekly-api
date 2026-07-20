package handler

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

func TestReposHandlerListAndDetail(t *testing.T) {
	s := newReposHandlerStore(t)
	seedReposHandlerRepo(t, s)
	h := NewReposHandler(s)

	listRecorder := httptest.NewRecorder()
	h.HandleListV1(listRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/repos?page=1&page_size=20&source=discovery", nil))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}

	detailRecorder := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/repos/{gh_repo_id}", h.HandleDetailV1)
	mux.ServeHTTP(detailRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/repos/42", nil))
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
}

func TestReposHandlerLanguagesRejectsSourceFilter(t *testing.T) {
	h := NewReposHandler(newReposHandlerStore(t))
	recorder := httptest.NewRecorder()
	h.HandleLanguagesV1(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/repos/languages?source=weekly", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func newReposHandlerStore(t *testing.T) *store.SQLiteStore {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "handler.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedReposHandlerRepo(t *testing.T, s *store.SQLiteStore) {
	t.Helper()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	if err := s.UpsertGitHubRepo(model.GitHubRepo{
		GhRepoID: 42, Owner: "acme", Name: "agent", FullName: "acme/agent",
		Description: "AI agent", Language: "Go", Stars: 100,
		FirstEventAt: now, LatestEventAt: now, IsAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachDiscoveryEvent(42, model.DiscoverySubmission{
		HNID: 1, Owner: "acme", Repo: "agent", Title: "Show HN: Agent",
		HNURL: "https://news.ycombinator.com/item?id=1", SourceURL: "https://github.com/acme/agent",
		Score: 10, Comments: 2, PublishedAt: now, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}
