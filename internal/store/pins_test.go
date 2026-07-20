package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

func TestReplaceWeeklyPinsControlsAllSortsWithoutBypassingFilters(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "pins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 7, 16, 7, 0, 0, 0, time.UTC)
	seedPinnedRepo(t, s, 1, "weekly-one", model.SourceWeekly, 10, base)
	seedPinnedRepo(t, s, 2, "ai-two", model.SourceAIIntelligence, 20, base.Add(time.Minute))
	seedPinnedRepo(t, s, 3, "weekly-three", model.SourceWeekly, 30, base.Add(2*time.Minute))

	pins, err := s.ReplaceWeeklyPins([]int64{2, 1}, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins[0].GhRepoID != 2 || pins[0].Position != 1 || pins[1].GhRepoID != 1 {
		t.Fatalf("pins=%#v", pins)
	}
	for _, sortKey := range []string{"stars", "updated_at", "created_at", "name", "latest_event_at"} {
		items, _, err := s.QueryRepos(model.RepoQuery{Page: 1, PageSize: 30, Sort: sortKey, Order: "desc"})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 || items[0].GhRepoID != 2 || items[0].PinPosition == nil || *items[0].PinPosition != 1 || items[1].GhRepoID != 1 || items[2].GhRepoID != 3 {
			t.Fatalf("sort=%s items=%#v", sortKey, items)
		}
	}

	weekly, total, err := s.QueryRepos(model.RepoQuery{Page: 1, PageSize: 30, Source: []string{model.SourceWeekly}, Sort: "stars", Order: "desc"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || weekly[0].GhRepoID != 1 || weekly[1].GhRepoID != 3 {
		t.Fatalf("weekly=%#v", weekly)
	}

	if _, err := s.ReplaceWeeklyPins([]int64{1, 1}, base); err == nil {
		t.Fatal("duplicate pin must fail")
	}
	unchanged, err := s.GetWeeklyPins()
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged) != 2 || unchanged[0].GhRepoID != 2 || unchanged[1].GhRepoID != 1 {
		t.Fatalf("invalid replacement was not atomic: %#v", unchanged)
	}

	cleared, err := s.ReplaceWeeklyPins([]int64{}, base)
	if err != nil || len(cleared) != 0 {
		t.Fatalf("cleared=%#v err=%v", cleared, err)
	}
}

func seedPinnedRepo(t *testing.T, s *SQLiteStore, id int64, name, sourceCode string, stars int, occurredAt time.Time) {
	t.Helper()
	if err := s.UpsertGitHubRepo(model.GitHubRepo{
		GhRepoID: id, Owner: "acme", Name: name, FullName: "acme/" + name,
		Description: "agent " + name, Stars: stars, FirstEventAt: occurredAt,
		LatestEventAt: occurredAt, IsAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSourceEvent(id, model.SourceEventInput{
		SourceCode: sourceCode, ExternalKey: sourceCode + ":" + name, OccurredAt: occurredAt,
	}); err != nil {
		t.Fatal(err)
	}
}
