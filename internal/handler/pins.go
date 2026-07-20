package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

type pinsRepository interface {
	SearchWeeklyRepos(query string, limit int) ([]model.RepoSearchResult, error)
	GetWeeklyPins() ([]model.PinnedRepo, error)
	ReplaceWeeklyPins(repoIDs []int64, now time.Time) ([]model.PinnedRepo, error)
}

type PinsHandler struct {
	repository pinsRepository
	cache      interface{ Invalidate() }
	now        func() time.Time
}

func NewPinsHandler(repository pinsRepository, cache interface{ Invalidate() }) *PinsHandler {
	return &PinsHandler{repository: repository, cache: cache, now: time.Now}
}

func (h *PinsHandler) HandleSearch(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid limit", nil)
			return
		}
		limit = parsed
	}
	items, err := h.repository.SearchWeeklyRepos(r.URL.Query().Get("q"), limit)
	if err != nil {
		log.Printf("[handler] search weekly repos: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to search repos", nil)
		return
	}
	writeJSONWithMeta(w, items, &model.Meta{Total: len(items)})
}

func (h *PinsHandler) HandleList(w http.ResponseWriter, _ *http.Request) {
	items, err := h.repository.GetWeeklyPins()
	if err != nil {
		log.Printf("[handler] get weekly pins: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query pins", nil)
		return
	}
	writeJSONWithMeta(w, items, &model.Meta{Total: len(items)})
}

func (h *PinsHandler) HandleReplace(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request struct {
		GhRepoIDs []int64 `json:"gh_repo_ids"`
	}
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body", map[string]string{"error": err.Error()})
		return
	}
	items, err := h.repository.ReplaceWeeklyPins(request.GhRepoIDs, h.now().UTC())
	if err != nil {
		var validationError *store.PinValidationError
		if errors.As(err, &validationError) {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", validationError.Error(), nil)
			return
		}
		log.Printf("[handler] replace weekly pins: %v", err)
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to replace pins", nil)
		return
	}
	h.cache.Invalidate()
	writeJSONWithMeta(w, items, &model.Meta{Total: len(items)})
}
