package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/middleware"
	"github.com/dong4j/starcat-weekly-api/internal/model"
)

type fakeDiscoveryStore struct {
	items []model.DiscoveryItemDTO
	total int
	item  *model.DiscoveryItemDTO
}

func (f *fakeDiscoveryStore) QueryDiscovery(model.DiscoveryQuery) ([]model.DiscoveryItemDTO, int, error) {
	return f.items, f.total, nil
}

func (f *fakeDiscoveryStore) GetDiscoveryByOwnerRepo(string, string) (*model.DiscoveryItemDTO, error) {
	return f.item, nil
}

func TestDiscoveryListReturnsEnvelopeAndPagination(t *testing.T) {
	store := &fakeDiscoveryStore{items: []model.DiscoveryItemDTO{{
		Repo:      model.StarcatRepoCardDTO{GhRepoID: 42, FullName: "acme/agent", Owner: "acme", Repo: "agent"},
		Discovery: model.DiscoveryExtension{HNID: 1, Category: "agent"},
	}}, total: 31}
	h := NewDiscoveryHandler(store, func() {})
	h.now = func() time.Time { return time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC) }

	req := httptest.NewRequest(http.MethodGet, "/api/v1/discovery?category=agent&page=1&page_size=30", nil)
	recorder := httptest.NewRecorder()
	h.HandleListV1(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		SchemaVersion int                      `json:"schema_version"`
		Data          []model.DiscoveryItemDTO `json:"data"`
		Meta          model.Meta               `json:"meta"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != 1 || len(response.Data) != 1 || response.Meta.NextPage == nil || *response.Meta.NextPage != 2 {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestDiscoveryListRejectsInvalidCategory(t *testing.T) {
	h := NewDiscoveryHandler(&fakeDiscoveryStore{}, func() {})
	recorder := httptest.NewRecorder()
	h.HandleListV1(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/discovery?category=chatbot", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", recorder.Code)
	}
}

func TestDiscoveryDetailReturns404(t *testing.T) {
	h := NewDiscoveryHandler(&fakeDiscoveryStore{}, func() {})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/discovery/{owner}/{repo}", h.HandleDetailV1)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/discovery/acme/missing", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", recorder.Code)
	}
}

func TestDiscoveryAdminSyncRequiresIndependentAdminKey(t *testing.T) {
	called := make(chan struct{}, 1)
	h := NewDiscoveryHandler(&fakeDiscoveryStore{}, func() { called <- struct{}{} })
	mux := http.NewServeMux()
	adminAuth := middleware.NewBearerAuth([]string{"admin-secret"})
	mux.Handle("POST /internal/sync/discovery", adminAuth.Wrap(http.HandlerFunc(h.HandleAdminSync)))

	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/internal/sync/discovery", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", unauthorized.Code)
	}

	authorizedRequest := httptest.NewRequest(http.MethodPost, "/internal/sync/discovery", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer admin-secret")
	authorized := httptest.NewRecorder()
	mux.ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", authorized.Code)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("sync callback was not invoked")
	}
}
