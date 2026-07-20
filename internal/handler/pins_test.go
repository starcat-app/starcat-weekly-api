package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

func TestPinsHandlerSearchReplaceListAndInvalidate(t *testing.T) {
	repository, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "pins-handler.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	now := time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)
	if err := repository.UpsertGitHubRepo(model.GitHubRepo{
		GhRepoID: 42, Owner: "acme", Name: "agent", FullName: "acme/agent",
		Description: "AI agent", Stars: 100, FirstEventAt: now, LatestEventAt: now, IsAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpsertSourceEvent(42, model.SourceEventInput{
		SourceCode: model.SourceAIIntelligence, ExternalKey: "news:42", OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	cache := &handlerInvalidator{}
	handler := NewPinsHandler(repository, cache)
	handler.now = func() time.Time { return now }

	search := httptest.NewRecorder()
	handler.HandleSearch(search, httptest.NewRequest(http.MethodGet, "/internal/repos/search?q=agent&limit=10", nil))
	if search.Code != http.StatusOK || !bytes.Contains(search.Body.Bytes(), []byte(`"gh_repo_id":42`)) {
		t.Fatalf("status=%d body=%s", search.Code, search.Body.String())
	}

	replace := httptest.NewRecorder()
	handler.HandleReplace(replace, httptest.NewRequest(http.MethodPost, "/internal/pins", bytes.NewBufferString(`{"gh_repo_ids":[42]}`)))
	if replace.Code != http.StatusOK || cache.count != 1 {
		t.Fatalf("status=%d invalidations=%d body=%s", replace.Code, cache.count, replace.Body.String())
	}

	list := httptest.NewRecorder()
	handler.HandleList(list, httptest.NewRequest(http.MethodGet, "/internal/pins", nil))
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"position":1`)) {
		t.Fatalf("status=%d body=%s", list.Code, list.Body.String())
	}

	invalid := httptest.NewRecorder()
	handler.HandleReplace(invalid, httptest.NewRequest(http.MethodPost, "/internal/pins", bytes.NewBufferString(`{"gh_repo_ids":[42,42]}`)))
	if invalid.Code != http.StatusBadRequest || cache.count != 1 {
		t.Fatalf("status=%d invalidations=%d body=%s", invalid.Code, cache.count, invalid.Body.String())
	}
}

type handlerInvalidator struct{ count int }

func (c *handlerInvalidator) Invalidate() { c.count++ }
