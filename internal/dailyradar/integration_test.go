// 日报离线集成测试覆盖真实 SQLite、HTTP、worker；不启动现有 Collector 或访问公网。
package dailyradar_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	radar "github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
	"github.com/starcat-app/starcat-weekly-api/internal/github"
	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

type fakeGitHub struct {
	calls int
	err   error
}

func (f *fakeGitHub) GetRepo(context.Context, string, string) (*github.RepoResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	avatar := "https://avatars.example/owner"
	return &github.RepoResponse{ID: 123, Owner: "owner", Name: "repo", FullName: "owner/repo", HTMLURL: "https://github.com/owner/repo", Topics: []string{}, OwnerAvatar: &avatar}, nil
}
func candidate(date, category, lang string) radar.Candidate {
	return radar.Candidate{EditionDate: date, Category: category, Language: lang, Title: "AI CLI", Summary: "Summary",
		Markdown:     "# AI CLI\n\n[Project](https://github.com/owner/repo)\n[Issue](https://github.com/owner/repo/issues/1)\n",
		SourceCommit: strings.Repeat("a", 40), SourcePath: "digests/" + date + "/" + category + ".md",
		GeneratedAt: "2026-10-28T01:00:00Z", Cover: radar.Cover{Kind: "category", Fallback: "orbit", Alt: "AI CLI"}}
}
func setup(t *testing.T) (*store.SQLiteStore, *radar.Worker, *http.ServeMux) {
	t.Helper()
	s, err := store.NewSQLiteStore(t.TempDir() + "/weekly.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	w := radar.NewWorker(s, &fakeGitHub{})
	h := radar.NewHandler(s, w, []string{"cdn.example.com"}, []string{"starcat-app/radar"})
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/daily-radar/catalog", h.Catalog)
	m.HandleFunc("GET /api/v1/daily-radar/dates", h.Dates)
	m.HandleFunc("GET /api/v1/daily-radar/reports", h.Reports)
	m.HandleFunc("GET /api/v1/daily-radar/reports/{id}", h.Detail)
	m.HandleFunc("POST /internal/daily-radar/imports", h.Import)
	m.HandleFunc("GET /internal/daily-radar/imports/{id}", h.Batch)
	return s, w, m
}
func enqueue(t *testing.T, s *store.SQLiteStore, w *radar.Worker, seq int64, kind string, items ...radar.Candidate) *radar.Batch {
	t.Helper()
	r := radar.ImportRequest{IdempotencyKey: fmt.Sprintf("%s-%d-%s", kind, seq, items[0].SourcePath), Source: "agents-radar", SourceKind: kind, SourceSequence: seq, Reports: items}
	if kind == "starcat" {
		r.SourceRepository = "starcat-app/radar"
	}
	if err := radar.ValidateImport(&r, nil, []string{"starcat-app/radar"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.EnqueueRadar(r)
	if err != nil {
		t.Fatal(err)
	}
	for range items {
		j, err := s.ClaimRadarJob(time.Now())
		if err != nil || j == nil {
			t.Fatalf("claim: %v", err)
		}
		if err := w.Process(context.Background(), *j); err != nil {
			t.Fatal(err)
		}
	}
	return b
}
func get(t *testing.T, h http.Handler, path, etag string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("If-None-Match", etag)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, r)
	return res
}
func data[T any](t *testing.T, r *httptest.ResponseRecorder) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func TestPublicationPaginationCacheAndWeeklyIsolation(t *testing.T) {
	s, w, m := setup(t)
	enqueue(t, s, w, 1, "upstream", candidate("2026-10-28", "ai-cli", "en"), candidate("2026-10-28", "ai-cli", "zh-Hans"),
		candidate("2026-10-27", "ai-cli", "en"))
	_, total, err := s.QueryRepos(model.RepoQuery{Page: 1, PageSize: 50})
	if err != nil || total != 0 {
		t.Fatalf("daily import leaked into Weekly: total=%d err=%v", total, err)
	}
	// 默认不回退语言；这里显式测试旧客户端仍可通过 fallback=true 获取另一语言。
	r := get(t, m, "/api/v1/daily-radar/reports?language=zh-Hans&fallback=true&limit=1", "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	list := data[radar.ListResponse](t, r)
	if list.Total != 2 || len(list.Items) != 1 || len(list.Items[0].AvailableLanguages) != 2 || list.NextCursor == nil || list.FeaturedID == nil {
		t.Fatalf("%+v", list)
	}
	badges := list.Items[0].RelatedProjectBadges
	if len(badges) != 1 || badges[0].GhRepoID != 123 || badges[0].FullName != "owner/repo" || badges[0].OwnerAvatar == nil || *badges[0].OwnerAvatar != "https://avatars.example/owner" {
		t.Fatalf("summary badges: %+v", badges)
	}
	etag := r.Header().Get("ETag")
	if get(t, m, "/api/v1/daily-radar/reports?language=zh-Hans&fallback=true&limit=1", etag).Code != 304 {
		t.Fatal("missing conditional response")
	}
	nextPath := "/api/v1/daily-radar/reports?language=zh-Hans&fallback=true&limit=1&cursor=" + *list.NextCursor
	next := data[radar.ListResponse](t, get(t, m, nextPath, ""))
	if len(next.Items) != 1 || next.FeaturedID != nil || next.Items[0].EditionDate != "2026-10-27" {
		t.Fatalf("%+v", next)
	}
	strict := data[radar.ListResponse](t, get(t, m, "/api/v1/daily-radar/reports?language=zh-Hans&fallback=false", ""))
	if strict.Total != 1 {
		t.Fatal("strict language leaked fallback")
	}
	dates := data[radar.DatesResponse](t, get(t, m, "/api/v1/daily-radar/dates?language=zh-Hans&fallback=false", ""))
	if len(dates.Dates) != 1 || dates.Dates[0].Count != 1 {
		t.Fatalf("%+v", dates)
	}
	// 侧边栏计数徽标：语言去重后按分类统计；strict 语言下 zh 只见双语那份，
	// en 见两份（10-28 与 10-27），父级总数覆盖全部期次。
	catalog := data[radar.CatalogResponse](t, get(t, m, "/api/v1/daily-radar/catalog?language=zh-Hans&fallback=false", ""))
	if catalog.CategoryCounts["ai-cli"] != 1 || catalog.TotalCount != 1 {
		t.Fatalf("%+v", catalog)
	}
	enCatalog := data[radar.CatalogResponse](t, get(t, m, "/api/v1/daily-radar/catalog?language=en&fallback=false", ""))
	if enCatalog.CategoryCounts["ai-cli"] != 2 || enCatalog.TotalCount != 2 {
		t.Fatalf("%+v", enCatalog)
	}
	enqueue(t, s, w, 2, "upstream", candidate("2026-10-29", "ai-cli", "en"))
	if r := get(t, m, nextPath, etag); r.Code != 409 || !strings.Contains(r.Body.String(), "CATALOG_CHANGED") {
		t.Fatal("stale cursor accepted", r.Body.String())
	}
	if r := get(t, m, "/api/v1/daily-radar/dates?catalog_revision="+list.CatalogRevision, etag); r.Code != 409 {
		t.Fatal("stale date identity accepted")
	}
	if r := get(t, m, "/api/v1/daily-radar/reports?cursor=bad", etag); r.Code != 400 {
		t.Fatal("invalid cursor accepted")
	}
}

func TestRevisionPinnedSequenceAndSourcePreference(t *testing.T) {
	s, w, _ := setup(t)
	c := candidate("2026-10-28", "ai-cli", "en")
	enqueue(t, s, w, 10, "upstream", c)
	old, err := s.RadarDetail(radar.ReportID(c), "", "en", true)
	if err != nil {
		t.Fatal(err)
	}
	c.Markdown += "\nRevision two"
	enqueue(t, s, w, 11, "upstream", c)
	current, _ := s.RadarDetail(radar.ReportID(c), "", "en", true)
	pinned, _ := s.RadarDetail(radar.ReportID(c), old.Revision, "en", false)
	if current.Revision == old.Revision || pinned.Markdown == current.Markdown {
		t.Fatal("immutable revision overwritten")
	}
	enqueue(t, s, w, 9, "upstream", candidate("2026-10-28", "ai-cli", "en"))
	latest, _ := s.RadarDetail(radar.ReportID(c), "", "en", true)
	if latest.Revision != current.Revision {
		t.Fatal("late old sequence overwrote current")
	}
	enqueue(t, s, w, 1, "starcat", c)
	d, _ := s.RadarDetail(radar.ReportID(c), "", "en", true)
	if !strings.Contains(d.Source.URL, "starcat-app/radar/blob/") {
		t.Fatal(d.Source)
	}
	c.Markdown += "upstream latest"
	enqueue(t, s, w, 12, "upstream", c)
	latest, _ = s.RadarDetail(radar.ReportID(c), "", "en", true)
	if latest.Revision != d.Revision {
		t.Fatal("upstream overwrote self generated")
	}
	if _, err := s.RadarDetail(radar.ReportID(c), "", "zh-Hans", false); !errors.Is(err, radar.ErrLanguage) {
		t.Fatal(err)
	}
	if _, err := s.RadarDetail(radar.ReportID(c), strings.Repeat("b", 64), "en", true); !errors.Is(err, radar.ErrRevision) {
		t.Fatal(err)
	}
}

func TestIdempotencyRecoveryAndSequenceConflict(t *testing.T) {
	s, w, m := setup(t)
	c := candidate("2026-10-28", "ai-cli", "en")
	request := radar.ImportRequest{Source: "agents-radar", SourceKind: "upstream", SourceSequence: 1, IdempotencyKey: "same", Reports: []radar.Candidate{c}}
	if err := radar.ValidateImport(&request, nil, nil); err != nil {
		t.Fatal(err)
	}
	first, err := s.EnqueueRadar(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnqueueRadar(request)
	if err != nil || first.ID != second.ID {
		t.Fatal("duplicate batch")
	}
	request.Reports[0].Title = "Different"
	if _, err := s.EnqueueRadar(request); !errors.Is(err, radar.ErrIdempotency) {
		t.Fatal(err)
	}
	job, _ := s.ClaimRadarJob(time.Now())
	if job == nil {
		t.Fatal("no job")
	}
	if err := s.RecoverRadarJobs(); err != nil {
		t.Fatal(err)
	}
	job, _ = s.ClaimRadarJob(time.Now())
	if job == nil || job.Attempts != 2 {
		t.Fatal("crash recovery lost job")
	}
	if err := w.Process(context.Background(), *job); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "other"
	if _, err := s.EnqueueRadar(request); err != nil {
		t.Fatal(err)
	}
	job, _ = s.ClaimRadarJob(time.Now())
	if err := w.Process(context.Background(), *job); !errors.Is(err, radar.ErrSequence) {
		t.Fatal("same sequence changed payload accepted", err)
	}
	req := httptest.NewRequest("POST", "/internal/daily-radar/imports", strings.NewReader(`{"unknown":true}`))
	res := httptest.NewRecorder()
	m.ServeHTTP(res, req)
	if res.Code != 400 || res.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(res)
	}
	if b, _ := s.RadarBatch(first.ID); b.Published != 1 {
		t.Fatal(b)
	}
}

func TestValidationAndRepositoryExtraction(t *testing.T) {
	r := radar.ImportRequest{Source: "agents-radar", SourceKind: "upstream", SourceSequence: 1, IdempotencyKey: "k", Reports: []radar.Candidate{candidate("2026-10-28", "ai-cli", "en")}}
	u := "https://127.0.0.1/cover.png"
	r.Reports[0].Cover.CardURL = &u
	if radar.ValidateImport(&r, nil, nil) == nil {
		t.Fatal("arbitrary cover URL accepted")
	}
	r.Reports[0].Cover.CardURL = nil
	r.Reports[0].SourcePath = "digests/2026-10-28/../../secret.md"
	if radar.ValidateImport(&r, nil, nil) == nil {
		t.Fatal("path traversal accepted")
	}
	markdown := "![badge](https://github.com/badge/image)\n`https://github.com/code/fake`\n" +
		"[Project](https://github.com/owner/repo/issues/2)\n[dup](https://github.com/OWNER/REPO)\n" +
		"[user](https://github.com/owner)\n[tree](https://github.com/o/r/blob/main/x)\n" +
		"[nav](https://github.com/topics/ai)\n[evil](https://github.com.evil/o/r)\n" +
		"[upstream generator](https://github.com/duanyytop/agents-radar)\n" +
		"[starcat generator](https://github.com/starcat-app/agents-radar)"
	links := radar.ExtractRepositories(markdown)
	if len(links) != 1 || links[0].URL != "https://github.com/owner/repo/issues/2" {
		t.Fatalf("%+v", links)
	}
}

func TestReopenKeepsPublishedDataAndQueuedBatch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "weekly.db")
	s, err := store.NewSQLiteStore(p)
	if err != nil {
		t.Fatal(err)
	}
	w := radar.NewWorker(s, &fakeGitHub{})
	c := candidate("2026-10-28", "ai-cli", "en")
	enqueue(t, s, w, 1, "upstream", c)
	c2 := candidate("2026-10-27", "ai-cli", "en")
	request := radar.ImportRequest{Source: "agents-radar", SourceKind: "upstream", SourceSequence: 1, IdempotencyKey: "resume", Reports: []radar.Candidate{c2}}
	if err := radar.ValidateImport(&request, nil, nil); err != nil {
		t.Fatal(err)
	}
	b, err := s.EnqueueRadar(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.NewSQLiteStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot, err := s.RadarSnapshot()
	if err != nil || len(snapshot.Items) != 1 {
		t.Fatal(snapshot, err)
	}
	reloaded, err := s.RadarBatch(b.ID)
	if err != nil || reloaded.Total != 1 {
		t.Fatal(reloaded, err)
	}
	j, err := s.ClaimRadarJob(time.Now())
	if err != nil || j == nil {
		t.Fatal("lost queued job", err)
	}
	if err := radar.NewWorker(s, &fakeGitHub{}).Process(context.Background(), *j); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.RadarSnapshot()
	if err != nil || len(snapshot.Items) != 2 {
		t.Fatal(snapshot, err)
	}
}
