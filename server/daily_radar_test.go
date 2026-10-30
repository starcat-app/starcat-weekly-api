// 日报原生装配测试覆盖生产读取 / 管理 Key 隔离，不启动真实来源抓取。
package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

func TestDailyRadarAuthAndRouteWiring(t *testing.T) {
	p := filepath.Join(t.TempDir(), "weekly.db")
	seed, err := store.NewSQLiteStore(p)
	if err != nil {
		t.Fatal(err)
	}
	// 已有持久期号会使 Scheduler 跳过启动 Collector；无 repo / ingest 任务。
	if err := seed.UpsertIssue(&model.WeeklyIssue{Number: 1, PublishedAt: time.Now(), SourceURL: "https://example.com"}); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	service, err := New(Options{DBPath: p, APIKeys: []string{"reader"}, AdminAPIKeys: []string{"admin"}, GithubTokens: []string{"unused"},
		DiscoveryCron: "invalid", HelloGitHubCron: "invalid", HelloGitHubReconcile: "invalid", ZreadTrendingCron: "invalid",
		SkipListenLogEndpoints: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	for _, tc := range []struct {
		method, path, key string
		status            int
	}{
		{"GET", "/api/v1/daily-radar/catalog", "", 401},
		{"GET", "/api/v1/daily-radar/catalog", "reader", 200},
		{"GET", "/api/v1/daily-radar/reports", "reader", 200},
		{"GET", "/internal/daily-radar/imports/missing", "reader", 401},
		{"GET", "/internal/daily-radar/imports/missing", "admin", 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.key != "" {
			r.Header.Set("Authorization", "Bearer "+tc.key)
		}
		w := httptest.NewRecorder()
		service.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s got %d: %s", tc.path, w.Code, w.Body.String())
		}
	}
}
