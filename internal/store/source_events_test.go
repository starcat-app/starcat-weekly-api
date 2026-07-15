package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestGenericSourceEventsDeduplicateRepoAndExposeLatestEntryPerSource(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "source-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Date(2026, 7, 16, 1, 0, 0, 0, time.UTC)
	if err := s.UpsertGitHubRepo(model.GitHubRepo{
		GhRepoID: 99, Owner: "acme", Name: "agent", FullName: "acme/agent",
		Language: "Go", FirstEventAt: now, LatestEventAt: now, IsAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}

	events := []model.SourceEventInput{
		{SourceCode: model.SourceHelloGitHub, ExternalKey: "featured:1", OccurredAt: now.Add(-time.Hour), SourceURL: "https://hellogithub.com/repository/acme-agent", Title: "Agent", Summary: "old"},
		{SourceCode: model.SourceHelloGitHub, ExternalKey: "volume:123:acme/agent", OccurredAt: now, SourceURL: "https://hellogithub.com/periodical/volume/123", Title: "Agent", Summary: "latest", Payload: map[string]any{"volume": 123}},
		{SourceCode: model.SourceAIIntelligence, ExternalKey: "news:acme/agent", OccurredAt: now.Add(-30 * time.Minute), SourceURL: "https://example.com/news", Title: "AI Agent"},
	}
	for _, event := range events {
		if err := s.UpsertSourceEvent(99, event); err != nil {
			t.Fatal(err)
		}
	}

	items, total, err := s.QueryRepos(model.RepoQuery{Page: 1, PageSize: 30})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("total=%d items=%d", total, len(items))
	}
	if got := items[0].SourceTypes; len(got) != 2 || got[0] != model.SourceHelloGitHub || got[1] != model.SourceAIIntelligence {
		t.Fatalf("source_types=%v", got)
	}
	if got := items[0].SourceEntries; len(got) != 2 || got[0].Summary != "latest" || got[1].SourceCode != model.SourceAIIntelligence {
		t.Fatalf("source_entries=%#v", got)
	}

	filtered, filteredTotal, err := s.QueryRepos(model.RepoQuery{Page: 1, PageSize: 30, Source: []string{model.SourceAIIntelligence}})
	if err != nil {
		t.Fatal(err)
	}
	if filteredTotal != 1 || len(filtered) != 1 || filtered[0].GhRepoID != 99 {
		t.Fatalf("filtered total=%d items=%#v", filteredTotal, filtered)
	}

	detail, err := s.GetRepoDetail(99)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || len(detail.Events) != 3 || detail.Events[0].SourceCode != model.SourceHelloGitHub {
		t.Fatalf("detail=%#v", detail)
	}

	sources, err := s.GetSourceCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 || sources[0].Code != model.SourceHelloGitHub || sources[0].Count != 1 || sources[1].Code != model.SourceAIIntelligence {
		t.Fatalf("sources=%#v", sources)
	}
}

func TestUpsertSourceEventRejectsUnknownSource(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "unknown-source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertSourceEvent(1, model.SourceEventInput{SourceCode: "free-form", ExternalKey: "x"}); err == nil {
		t.Fatal("unknown source must be rejected before database write")
	}
}
