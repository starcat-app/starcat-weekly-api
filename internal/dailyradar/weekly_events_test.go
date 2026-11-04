// 每周 AI 大事契约测试只访问临时 SQLite，不调用生产日报或 GitHub
package dailyradar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	radar "github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
)

// weeklyRequest 固定双语同提交，避免测试把单语言入队误当成整期发布
func weeklyRequest() radar.ImportRequest {
	return radar.ImportRequest{
		IdempotencyKey: "weekly-1", Source: "ai-weekly-events",
		SourceRepository: "starcat-app/ai-weekly-events", SourceKind: "editorial", SourceSequence: 1,
		Reports: []radar.Candidate{
			candidate("2026-11-02", "ai-weekly-events", "zh-Hans"),
			candidate("2026-11-02", "ai-weekly-events", "en"),
		},
	}
}

func TestWeeklyEventsSourceBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*radar.ImportRequest)
	}{
		{"single language", func(r *radar.ImportRequest) { r.Reports = r.Reports[:1] }},
		{"different commit", func(r *radar.ImportRequest) { r.Reports[1].SourceCommit = strings.Repeat("b", 40) }},
		{"other repository", func(r *radar.ImportRequest) { r.SourceRepository = "starcat-app/radar" }},
		{"other category", func(r *radar.ImportRequest) { r.Reports[0].Category = "ai-cli" }},
		{"upstream injection", func(r *radar.ImportRequest) {
			r.Source, r.SourceKind, r.SourceRepository = "agents-radar", "upstream", ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := weeklyRequest()
			tc.edit(&r)
			if err := radar.ValidateImport(&r, nil, []string{"starcat-app/ai-weekly-events", "starcat-app/radar"}); err == nil {
				t.Fatal("invalid editorial import accepted")
			}
		})
	}
	r := weeklyRequest()
	if err := radar.ValidateImport(&r, nil, nil); err == nil {
		t.Fatal("editorial repository bypassed allowlist")
	}
}

func TestWeeklyEventsPublicationAndRevision(t *testing.T) {
	s, w, mux := setup(t)
	publish := func(r radar.ImportRequest) *radar.Batch {
		t.Helper()
		if err := radar.ValidateImport(&r, nil, []string{"starcat-app/ai-weekly-events"}); err != nil {
			t.Fatal(err)
		}
		b, err := s.EnqueueRadar(r)
		if err != nil {
			t.Fatal(err)
		}
		again, err := s.EnqueueRadar(r)
		if err != nil || again.ID != b.ID {
			t.Fatal("retry was not idempotent", err)
		}
		for range r.Reports {
			j, err := s.ClaimRadarJob(time.Now())
			if err != nil || j == nil {
				t.Fatal("missing job", err)
			}
			if err := w.Process(context.Background(), *j); err != nil {
				t.Fatal(err)
			}
		}
		return b
	}
	r := weeklyRequest()
	publish(r)
	id := radar.ReportID(r.Reports[0])
	old, err := s.RadarDetail(id, "", "zh-Hans", false)
	if err != nil || old.Period != "weekly" || old.Source.Name != "每周 AI 大事" ||
		!strings.Contains(old.Source.URL, "starcat-app/ai-weekly-events/blob/") {
		t.Fatal(old, err)
	}
	page := data[radar.ListResponse](t, get(t, mux,
		"/api/v1/daily-radar/reports?category=ai-weekly-events&period=weekly&language=en", ""))
	if page.Total != 1 || len(page.Items[0].AvailableLanguages) != 2 {
		t.Fatal(page)
	}
	r.IdempotencyKey, r.SourceSequence = "weekly-2", 2
	for i := range r.Reports {
		r.Reports[i].Markdown += "\nRevised"
		r.Reports[i].SourceCommit = strings.Repeat("b", 40)
	}
	publish(r)
	current, err := s.RadarDetail(id, "", "zh-Hans", false)
	pinned, pinErr := s.RadarDetail(id, old.Revision, "zh-Hans", false)
	if err != nil || pinErr != nil || current.Revision == old.Revision || pinned.Markdown != old.Markdown {
		t.Fatal("revision pinning failed", err, pinErr)
	}
	r = weeklyRequest()
	r.IdempotencyKey = "late-weekly-1"
	publish(r)
	latest, err := s.RadarDetail(id, "", "zh-Hans", false)
	if err != nil || latest.Revision != current.Revision {
		t.Fatal("late revision overwrote current", err)
	}
}

// HTTP 入口与发布工具使用相同 JSON 契约，覆盖允许清单、202 批次和双语回读
func TestWeeklyEventsHTTPImport(t *testing.T) {
	s, w, _ := setup(t)
	h := radar.NewHandler(s, w, nil, []string{"starcat-app/ai-weekly-events"})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/daily-radar/imports", h.Import)
	mux.HandleFunc("GET /internal/daily-radar/imports/{id}", h.Batch)
	mux.HandleFunc("GET /api/v1/daily-radar/reports/{id}", h.Detail)
	request := weeklyRequest()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest("POST", "/internal/daily-radar/imports", strings.NewReader(string(body))))
	if res.Code != http.StatusAccepted {
		t.Fatal(res.Code, res.Body.String())
	}
	batch := data[radar.Batch](t, res)
	for range request.Reports {
		job, err := s.ClaimRadarJob(time.Now())
		if err != nil || job == nil {
			t.Fatal("claim failed", err)
		}
		if err := w.Process(context.Background(), *job); err != nil {
			t.Fatal(err)
		}
	}
	done := data[radar.Batch](t, get(t, mux, "/internal/daily-radar/imports/"+batch.ID, ""))
	if done.Status != "complete" || done.Published != 2 || done.Failed != 0 {
		t.Fatal(done)
	}
	for _, c := range request.Reports {
		detail := data[radar.Detail](t, get(t, mux,
			"/api/v1/daily-radar/reports/"+radar.ReportID(c)+"?language="+c.Language+"&fallback=false", ""))
		if detail.Markdown != c.Markdown || detail.Source.Commit != c.SourceCommit || detail.ContentLanguage != c.Language {
			t.Fatal(detail)
		}
	}
}
