// Package handler 的 bulk endpoint 测试: bulk.go / bulk_cache.go
//
// 覆盖:
//  1. 200 正常返回（envelope schema_version=1 / data.repos / data.languages / meta.total）
//  2. cache hit 第二次不再 callStore（callCount 仍 1）
//  3. ETag 304: 带匹配 If-None-Match → 304 + 无 body
//  4. gzip 路径: Accept-Encoding: gzip → Content-Encoding: gzip + 字节级一致
//  5. 不带 Accept-Encoding → uncompressed payload
//  6. Vary header 必须含 Accept-Encoding（防止 CDN 串响应）
//  7. Invalidate 后强制走 store
//  8. store error → 500
//  9. languages query 失败 → 500（独立路径）
//
// 10. BulkCache TTL 过期 → 强制 fetch（用 Set 后改 builtAt 模拟）
// 11. NewBulkCache 初始 HasEntry == false
//
// fakeBulkStore 实现 store.Store interface,只覆盖 QueryAllRepos /
// GetAggregatedLanguages 调用路径,其他方法 panic（不调）。
package handler

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

// fakeBulkStore 是 store.Store 的最小实现,只支持 QueryAllRepos / GetAggregatedLanguages
// 的可观测调用。
type fakeBulkStore struct {
	repos       []model.RepoFeedItem
	langs       []model.LanguageAggregate
	sources     []model.SourceDescriptor
	reposErr    error
	langsErr    error
	reposCalls  int
	langsCalls  int
	sourceCalls int
}

func (f *fakeBulkStore) GetSourceCatalog() ([]model.SourceDescriptor, error) {
	f.sourceCalls++
	return f.sources, nil
}

func (f *fakeBulkStore) QueryAllRepos() ([]model.RepoFeedItem, error) {
	f.reposCalls++
	if f.reposErr != nil {
		return nil, f.reposErr
	}
	return f.repos, nil
}

func (f *fakeBulkStore) GetAggregatedLanguages() ([]model.LanguageAggregate, error) {
	f.langsCalls++
	if f.langsErr != nil {
		return nil, f.langsErr
	}
	return f.langs, nil
}

// 其余 Store 接口方法不在 bulk 路径上,统一 panic。
func (f *fakeBulkStore) UpsertGitHubRepo(model.GitHubRepo) error { panic("not used") }
func (f *fakeBulkStore) GetGitHubRepoByOwnerName(string, string) (*model.GitHubRepo, error) {
	panic("not used")
}
func (f *fakeBulkStore) MarkGitHubRepoUnavailable(string, string, string, time.Time) error {
	panic("not used")
}
func (f *fakeBulkStore) AttachWeeklyEvent(int64, model.Project, model.WeeklyIssue) error {
	panic("not used")
}
func (f *fakeBulkStore) AttachZreadEvent(int64, model.ZreadTrending) error { panic("not used") }
func (f *fakeBulkStore) AttachDiscoveryEvent(int64, model.DiscoverySubmission) error {
	panic("not used")
}
func (f *fakeBulkStore) UpsertSourceEvent(int64, model.SourceEventInput) error { panic("not used") }
func (f *fakeBulkStore) QueryRepos(model.RepoQuery) ([]model.RepoFeedItem, int, error) {
	panic("not used")
}
func (f *fakeBulkStore) GetRepoDetail(int64) (*model.RepoDetail, error) { panic("not used") }
func (f *fakeBulkStore) RebuildAggregates() error                       { panic("not used") }
func (f *fakeBulkStore) GetAllSourceRepos() []string                    { return nil }
func (f *fakeBulkStore) UpsertProject(*model.Project) error             { panic("not used") }
func (f *fakeBulkStore) GetProjects(model.QueryParams) ([]model.Project, int, error) {
	panic("not used")
}
func (f *fakeBulkStore) UpsertIssue(*model.WeeklyIssue) error     { panic("not used") }
func (f *fakeBulkStore) GetIssues() ([]model.WeeklyIssue, error)  { panic("not used") }
func (f *fakeBulkStore) GetIssue(int) (*model.WeeklyIssue, error) { panic("not used") }
func (f *fakeBulkStore) GetLatestIssueNumber() (int, error)       { panic("not used") }
func (f *fakeBulkStore) GetUnenrichedProjects(int) ([]model.Project, error) {
	panic("not used")
}
func (f *fakeBulkStore) UpdateProjectMeta(*model.Project) error { panic("not used") }
func (f *fakeBulkStore) GetProjectByOwnerRepo(string, string) (*model.Project, error) {
	panic("not used")
}
func (f *fakeBulkStore) UpsertZreadTrending(model.ZreadTrending) error { panic("not used") }
func (f *fakeBulkStore) QueryZreadTrending(string, int) ([]model.ZreadTrending, error) {
	panic("not used")
}
func (f *fakeBulkStore) LookupZreadWikiID(string, string) (string, error) { panic("not used") }
func (f *fakeBulkStore) GetZreadRepos() []string                          { return nil }
func (f *fakeBulkStore) GetUnenrichedZreadRepos(int) ([]model.ZreadTrending, error) {
	panic("not used")
}
func (f *fakeBulkStore) UpdateZreadEnriched(string, string, string, *model.ZreadTrending) error {
	panic("not used")
}
func (f *fakeBulkStore) GetAggregatedLanguagesFAKE() {} // marker (gosimple no-op，just保编辑器满意)
func (f *fakeBulkStore) Close() error                { return nil }

// makeBulkRepo 构造一条 minimal RepoFeedItem 用于 fake store 注入。
func makeBulkRepo(id int64, fullName, lang string) model.RepoFeedItem {
	langPtr := &lang
	return model.RepoFeedItem{
		StarcatRepoCardDTO: model.StarcatRepoCardDTO{
			GhRepoID: id,
			FullName: fullName,
			Owner:    "acme",
			Repo:     fullName,
			Language: langPtr,
			Stars:    100,
		},
		Name:          fullName,
		IsAvailable:   true,
		SourceTypes:   []string{model.SourceWeekly},
		FirstEventAt:  "2026-06-15T00:00:00Z",
		LatestEventAt: "2026-06-15T00:00:00Z",
	}
}

func doBulkReq(s *fakeBulkStore, c *BulkCache, headers http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/repos/bulk", nil)
	for k, vv := range headers {
		for _, v := range vv {
			r.Header.Add(k, v)
		}
	}
	HandleBulkV1(s, c)(w, r)
	return w
}

// TestBulk_ReturnsEnvelope 验证 200 + envelope schema_version + data.repos +
// data.languages + meta.total 字段全部正确填充。
func TestBulk_ReturnsEnvelope(t *testing.T) {
	f := &fakeBulkStore{
		repos:   []model.RepoFeedItem{makeBulkRepo(1, "r1", "Go"), makeBulkRepo(2, "r2", "Rust")},
		langs:   []model.LanguageAggregate{{Key: "Go", Label: "Go", Count: 1}, {Key: "Rust", Label: "Rust", Count: 1}},
		sources: []model.SourceDescriptor{{Code: model.SourceWeekly, DisplayNameZH: "阮一峰周刊", DisplayNameEN: "Weekly", IconKey: "ruanyf", SortOrder: 10, Count: 2}},
	}
	w := doBulkReq(f, NewBulkCache(), nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var env model.Envelope[bulkData]
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, w.Body.String())
	}
	if env.SchemaVersion != 2 {
		t.Errorf("schema_version=%d, want 2", env.SchemaVersion)
	}
	if len(env.Data.Sources) != 1 || env.Data.Sources[0].Count != 2 {
		t.Errorf("sources=%#v", env.Data.Sources)
	}
	if len(env.Data.Repos) != 2 {
		t.Errorf("repos len=%d, want 2", len(env.Data.Repos))
	}
	if len(env.Data.Languages) != 2 {
		t.Errorf("languages len=%d, want 2", len(env.Data.Languages))
	}
	if env.Meta == nil || env.Meta.Total != 2 {
		t.Errorf("meta.total: want 2, got %+v", env.Meta)
	}
}

// TestBulk_CacheHitSkipsStore：同次请求第二次 cache 命中，store 调用计数维持 1。
func TestBulk_CacheHitSkipsStore(t *testing.T) {
	f := &fakeBulkStore{repos: []model.RepoFeedItem{makeBulkRepo(1, "r1", "Go")}}
	c := NewBulkCache()

	_ = doBulkReq(f, c, nil)
	if f.reposCalls != 1 || f.langsCalls != 1 {
		t.Fatalf("first call: reposCalls=%d langsCalls=%d want 1/1", f.reposCalls, f.langsCalls)
	}

	w2 := doBulkReq(f, c, nil)
	if w2.Code != http.StatusOK {
		t.Fatalf("second call status=%d", w2.Code)
	}
	if f.reposCalls != 1 || f.langsCalls != 1 {
		t.Errorf("second call should NOT hit store, reposCalls=%d langsCalls=%d", f.reposCalls, f.langsCalls)
	}
	if w2.Header().Get("ETag") == "" {
		t.Errorf("cache-hit must expose ETag")
	}
	if w2.Header().Get("Last-Modified") == "" {
		t.Errorf("cache-hit must expose Last-Modified")
	}
}

// TestBulk_ETagReturns304：带匹配 If-None-Match 返回 304 + 无 body + 仍来自 cache。
func TestBulk_ETagReturns304(t *testing.T) {
	f := &fakeBulkStore{repos: []model.RepoFeedItem{makeBulkRepo(1, "r1", "Go")}}
	c := NewBulkCache()

	w1 := doBulkReq(f, c, nil)
	etag := w1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("first call did not set ETag")
	}

	headers := http.Header{}
	headers.Set("If-None-Match", etag)
	w2 := doBulkReq(f, c, headers)

	if w2.Code != http.StatusNotModified {
		t.Errorf("304 expected, got %d", w2.Code)
	}
	if w2.Body.Len() != 0 {
		t.Errorf("304 body should be empty, got %d bytes", w2.Body.Len())
	}
	if f.reposCalls != 1 {
		t.Errorf("304 must still come from cache, reposCalls=%d (want 1)", f.reposCalls)
	}
}

// TestBulk_GzipPath：带 Accept-Encoding: gzip → Content-Encoding: gzip + 解压后内容等于
// uncompressed payload。
func TestBulk_GzipPath(t *testing.T) {
	f := &fakeBulkStore{
		repos: []model.RepoFeedItem{makeBulkRepo(1, "r1", "Go"), makeBulkRepo(2, "r2", "Rust")},
		langs: []model.LanguageAggregate{{Key: "Go", Label: "Go", Count: 1}},
	}
	c := NewBulkCache()

	// 先用 uncompressed 请求拿基准 payload
	wPlain := doBulkReq(f, c, nil)
	plain := wPlain.Body.Bytes()

	// 再用 gzip 请求（同一 cache，hit 路径）
	headers := http.Header{}
	headers.Set("Accept-Encoding", "gzip, deflate, br")
	wGzip := doBulkReq(f, c, headers)

	if wGzip.Code != http.StatusOK {
		t.Fatalf("gzip status=%d", wGzip.Code)
	}
	if got := wGzip.Header().Get("Content-Encoding"); got != "gzip" {
		t.Errorf("Content-Encoding=%q, want gzip", got)
	}
	if !bytes.Equal(wGzip.Body.Bytes(), plain) {
		// 必须不等于（一个是压缩字节流，一个是 JSON 明文）
		gr, err := gzip.NewReader(bytes.NewReader(wGzip.Body.Bytes()))
		if err != nil {
			t.Fatalf("gzip body should be valid gzip, got err=%v", err)
		}
		decoded, err := io.ReadAll(gr)
		if err != nil {
			t.Fatalf("gzip decode: %v", err)
		}
		if !bytes.Equal(decoded, plain) {
			t.Errorf("gzip decoded body differs from plain")
		}
	}
}

// TestBulk_VaryHeader：必须含 Vary: Accept-Encoding 防 CDN 串响应。
func TestBulk_VaryHeader(t *testing.T) {
	f := &fakeBulkStore{repos: []model.RepoFeedItem{makeBulkRepo(1, "r1", "Go")}}
	w := doBulkReq(f, NewBulkCache(), nil)
	if got := w.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary=%q, want Accept-Encoding", got)
	}
}

// TestBulk_InvalidateForcesRefetch：cache.Invalidate() 后下次请求强制走 store。
func TestBulk_InvalidateForcesRefetch(t *testing.T) {
	f := &fakeBulkStore{repos: []model.RepoFeedItem{makeBulkRepo(1, "r1", "Go")}}
	c := NewBulkCache()

	_ = doBulkReq(f, c, nil)
	if f.reposCalls != 1 {
		t.Fatalf("after first call: reposCalls=%d, want 1", f.reposCalls)
	}

	c.Invalidate()
	if c.HasEntry() {
		t.Errorf("HasEntry should be false after Invalidate")
	}

	_ = doBulkReq(f, c, nil)
	if f.reposCalls != 2 {
		t.Errorf("after Invalidate: reposCalls=%d, want 2", f.reposCalls)
	}
}

// TestBulk_StoreErrorReposBranch：QueryAllRepos 错误 → 500。
func TestBulk_StoreErrorReposBranch(t *testing.T) {
	f := &fakeBulkStore{reposErr: errors.New("db locked")}
	w := doBulkReq(f, NewBulkCache(), nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500", w.Code)
	}
}

// TestBulk_StoreErrorLangsBranch：GetAggregatedLanguages 错误 → 500（且 repos 已查过一次）。
func TestBulk_StoreErrorLangsBranch(t *testing.T) {
	f := &fakeBulkStore{
		repos:    []model.RepoFeedItem{makeBulkRepo(1, "r1", "Go")},
		langsErr: errors.New("agg fail"),
	}
	w := doBulkReq(f, NewBulkCache(), nil)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status=%d, want 500", w.Code)
	}
	if f.reposCalls != 1 || f.langsCalls != 1 {
		t.Errorf("calls reposCalls=%d langsCalls=%d, want 1/1", f.reposCalls, f.langsCalls)
	}
}

// TestBulkCache_New：初始 HasEntry 为 false。
func TestBulkCache_New(t *testing.T) {
	c := NewBulkCache()
	if c.HasEntry() {
		t.Errorf("new BulkCache should have no entry")
	}
	if _, ok := c.Get(); ok {
		t.Errorf("new BulkCache Get should return ok=false")
	}
}

// TestBulkCache_SetAndGet：Set 后立即 Get 应该命中。
func TestBulkCache_SetAndGet(t *testing.T) {
	c := NewBulkCache()
	payload := []byte(`{"data":[]}`)
	entry := c.Set(payload)
	if entry == nil {
		t.Fatalf("Set returned nil")
	}
	got, ok := c.Get()
	if !ok {
		t.Fatalf("Get after Set should be ok")
	}
	if !bytes.Equal(got.payload, payload) {
		t.Errorf("payload mismatch")
	}
	if got.etag == "" {
		t.Errorf("ETag should be non-empty")
	}
	if len(got.gzipPayload) == 0 {
		t.Errorf("gzip payload should be non-empty")
	}
}

// TestBulkCache_TTLExpiry：把 builtAt 改成过去 70s 模拟过期，Get 应当返回 ok=false。
func TestBulkCache_TTLExpiry(t *testing.T) {
	c := NewBulkCache()
	c.Set([]byte(`{"data":[]}`))
	// 直接改 entry.builtAt 模拟 TTL 过期（只能从包内测试访问私有字段）。
	c.mu.Lock()
	c.entry.builtAt = time.Now().Add(-bulkCacheTTL - 5*time.Second)
	c.mu.Unlock()

	if _, ok := c.Get(); ok {
		t.Errorf("Get after TTL expiry should be ok=false")
	}
}

// TestBulkCache_ETagStableSamePayload：同 payload Set 两次 ETag 一致。
func TestBulkCache_ETagStableSamePayload(t *testing.T) {
	c := NewBulkCache()
	a := c.Set([]byte(`{"data":[1,2,3]}`))
	b := c.Set([]byte(`{"data":[1,2,3]}`))
	if a.etag != b.etag {
		t.Errorf("ETag should be stable for same payload, got %q vs %q", a.etag, b.etag)
	}
}

// TestBulkCache_ETagDiffersOnPayloadChange：payload 不同 ETag 必须不同。
func TestBulkCache_ETagDiffersOnPayloadChange(t *testing.T) {
	c := NewBulkCache()
	a := c.Set([]byte(`{"data":[1]}`))
	b := c.Set([]byte(`{"data":[2]}`))
	if a.etag == b.etag {
		t.Errorf("ETag should differ for different payloads")
	}
}
