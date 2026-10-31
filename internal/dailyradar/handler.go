// HTTP 读取由不可变目录快照驱动；必须先校验游标版本，再处理条件缓存。
package dailyradar

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Handler 仅依赖日报仓库，读取鉴权由 server 的原有中间件在外层执行。
type Handler struct {
	store              Repository
	worker             *Worker
	coverHosts         []string
	sourceRepositories []string
}

func NewHandler(store Repository, worker *Worker, coverHosts, sourceRepositories []string) *Handler {
	return &Handler{store: store, worker: worker, coverHosts: coverHosts, sourceRepositories: sourceRepositories}
}

type query struct {
	Category string `json:"category"`
	Period   string `json:"period"`
	Language string `json:"language"`
	Fallback bool   `json:"fallback"`
	From     string `json:"from"`
	To       string `json:"to"`
	Month    string `json:"month"`
	Limit    int    `json:"limit"`
}

type cursor struct {
	Revision string `json:"revision"`
	Query    string `json:"query"`
	Offset   int    `json:"offset"`
}

// ListResponse 为双方共享的列表 data 契约。
type ListResponse struct {
	CatalogRevision   string    `json:"catalog_revision"`
	RequestedLanguage string    `json:"requested_language"`
	Items             []Summary `json:"items"`
	Total             int       `json:"total"`
	NextCursor        *string   `json:"next_cursor"`
	FeaturedID        *string   `json:"featured_id"`
}

type DateCount struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}
type DatesResponse struct {
	CatalogRevision     string      `json:"catalog_revision"`
	Dates               []DateCount `json:"dates"`
	LatestAvailableDate *string     `json:"latest_available_date"`
}
type CatalogResponse struct {
	CatalogRevision     string     `json:"catalog_revision"`
	Categories          []Category `json:"categories"`
	AvailableLanguages  []string   `json:"available_languages"`
	LatestAvailableDate *string    `json:"latest_available_date"`
}

func parseQuery(values url.Values) (query, error) {
	// 客户端语言策略是“中文取 zh-Hans，其它语言取 en”；默认禁止 fallback，
	// 避任一语言缺口被悄悄展示成另一种语言。
	q := query{Category: values.Get("category"), Period: values.Get("period"), Language: values.Get("language"),
		Fallback: false, From: values.Get("date_from"), To: values.Get("date_to"), Month: values.Get("month"), Limit: 20}
	if q.Period == "" {
		q.Period = "daily"
	}
	if q.Language == "" {
		q.Language = "en"
	}
	if q.Language != "en" && q.Language != "zh-Hans" {
		return q, errors.New("invalid language")
	}
	if q.Period != "daily" && q.Period != "weekly" && q.Period != "monthly" {
		return q, errors.New("invalid period")
	}
	if q.Category != "" {
		if _, ok := FindCategory(q.Category); !ok {
			return q, errors.New("invalid category")
		}
	}
	if v := values.Get("fallback"); v != "" {
		if v != "true" && v != "false" {
			return q, errors.New("invalid fallback")
		}
		q.Fallback = v == "true"
	}
	for _, date := range []string{q.From, q.To} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return q, errors.New("invalid date")
			}
		}
	}
	if q.From != "" && q.To != "" && q.From > q.To {
		return q, errors.New("invalid date range")
	}
	if q.Month != "" {
		if _, err := time.Parse("2006-01", q.Month); err != nil {
			return q, errors.New("invalid month")
		}
	}
	if v := values.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			return q, errors.New("limit must be 1–50")
		}
		q.Limit = n
	}
	return q, nil
}

// selectItems 完成语言去重后再过滤及计数，避免 fallback 造成重复报告。
func selectItems(snapshot Snapshot, q query) []Summary {
	groups := make(map[string][]Summary)
	for _, s := range snapshot.Items {
		groups[s.ID] = append(groups[s.ID], s)
	}
	items := []Summary{}
	for _, variants := range groups {
		rank := func(language string) int {
			if language == q.Language {
				return 0
			}
			if language == "en" {
				return 1
			}
			return 2
		}
		sort.Slice(variants, func(i, j int) bool { return rank(variants[i].ContentLanguage) < rank(variants[j].ContentLanguage) })
		s := variants[0]
		if !q.Fallback && s.ContentLanguage != q.Language {
			continue
		}
		if s.Period != q.Period || (q.Category != "" && s.Category != q.Category) ||
			(q.From != "" && s.EditionDate < q.From) || (q.To != "" && s.EditionDate > q.To) ||
			(q.Month != "" && !strings.HasPrefix(s.EditionDate, q.Month+"-")) {
			continue
		}
		s.AvailableLanguages = []string{}
		for _, v := range variants {
			s.AvailableLanguages = append(s.AvailableLanguages, v.ContentLanguage)
		}
		sort.Strings(s.AvailableLanguages)
		items = append(items, s)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].EditionDate != items[j].EditionDate {
			return items[i].EditionDate > items[j].EditionDate
		}
		if items[i].Category != items[j].Category {
			return items[i].Category < items[j].Category
		}
		return items[i].ID < items[j].ID
	})
	return items
}

func (h *Handler) snapshot(w http.ResponseWriter, r *http.Request) (Snapshot, query, bool) {
	q, err := parseQuery(r.URL.Query())
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return Snapshot{}, q, false
	}
	s, err := h.store.RadarSnapshot()
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "failed to read catalog")
		return s, q, false
	}
	if expected := r.URL.Query().Get("catalog_revision"); expected != "" && expected != s.Revision {
		writeError(w, 409, "CATALOG_CHANGED", "reload catalog")
		return s, q, false
	}
	return s, q, true
}

func (h *Handler) Catalog(w http.ResponseWriter, r *http.Request) {
	s, q, ok := h.snapshot(w, r)
	if !ok {
		return
	}
	items := selectItems(s, q)
	out := CatalogResponse{CatalogRevision: s.Revision, Categories: append([]Category{}, Categories...), AvailableLanguages: []string{}}
	languages := make(map[string]bool)
	for _, item := range s.Items {
		languages[item.ContentLanguage] = true
	}
	for lang := range languages {
		out.AvailableLanguages = append(out.AvailableLanguages, lang)
	}
	sort.Strings(out.AvailableLanguages)
	if len(items) > 0 {
		out.LatestAvailableDate = &items[0].EditionDate
	}
	writeCached(w, r, out, 600)
}

func (h *Handler) Dates(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("cursor") != "" {
		writeError(w, 400, "INVALID_REQUEST", "dates does not accept cursor")
		return
	}
	s, q, ok := h.snapshot(w, r)
	if !ok {
		return
	}
	out := DatesResponse{CatalogRevision: s.Revision, Dates: []DateCount{}}
	for _, item := range selectItems(s, q) {
		if len(out.Dates) == 0 || out.Dates[len(out.Dates)-1].Date != item.EditionDate {
			out.Dates = append(out.Dates, DateCount{Date: item.EditionDate})
		}
		out.Dates[len(out.Dates)-1].Count++
	}
	if len(out.Dates) > 0 {
		out.LatestAvailableDate = &out.Dates[0].Date
	}
	writeCached(w, r, out, 600)
}

func (h *Handler) Reports(w http.ResponseWriter, r *http.Request) {
	s, q, ok := h.snapshot(w, r)
	if !ok {
		return
	}
	all := selectItems(s, q)
	offset := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var c cursor
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(b, &c) != nil {
			writeError(w, 400, "INVALID_CURSOR", "invalid cursor")
			return
		}
		if c.Revision != s.Revision {
			writeError(w, 409, "CATALOG_CHANGED", "reload catalog")
			return
		}
		if c.Query != HashJSON(q) || c.Offset < 1 || c.Offset > len(all) {
			writeError(w, 400, "INVALID_CURSOR", "cursor query mismatch")
			return
		}
		offset = c.Offset
	}
	end := min(len(all), offset+q.Limit)
	out := ListResponse{CatalogRevision: s.Revision, RequestedLanguage: q.Language, Items: all[offset:end], Total: len(all)}
	if offset == 0 && len(out.Items) > 0 {
		out.FeaturedID = &out.Items[0].ID
	}
	if end < len(all) {
		b, _ := json.Marshal(cursor{Revision: s.Revision, Query: HashJSON(q), Offset: end})
		token := base64.RawURLEncoding.EncodeToString(b)
		out.NextCursor = &token
	}
	writeCached(w, r, out, 600)
}

func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	q, err := parseQuery(r.URL.Query())
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	d, err := h.store.RadarDetail(r.PathValue("id"), r.URL.Query().Get("revision"), q.Language, q.Fallback)
	if err != nil {
		for _, known := range []error{ErrReport, ErrRevision, ErrLanguage} {
			if errors.Is(err, known) {
				writeError(w, 404, known.Error(), "report unavailable")
				return
			}
		}
		writeError(w, 500, "INTERNAL_ERROR", "failed to read report")
		return
	}
	// 可用语言是目录属性；正文修订本身保持不变。
	ttl := 600
	if r.URL.Query().Get("revision") != "" {
		ttl = 86400
	}
	writeCached(w, r, d, ttl)
}

// Import 拒绝超大/未知字段/尾随 JSON；202 前已将批次持久化。
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request ImportRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid JSON body")
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		writeError(w, 400, "INVALID_REQUEST", "trailing JSON body")
		return
	}
	if err := ValidateImport(&request, h.coverHosts, h.sourceRepositories); err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	batch, err := h.store.EnqueueRadar(request)
	if errors.Is(err, ErrIdempotency) {
		writeError(w, 409, ErrIdempotency.Error(), "idempotency key already used")
		return
	}
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "enqueue failed")
		return
	}
	h.worker.Wake()
	writeSuccess(w, 202, batch)
}

func (h *Handler) Batch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	batch, err := h.store.RadarBatch(r.PathValue("id"))
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "batch lookup failed")
		return
	}
	if batch == nil {
		writeError(w, 404, "BATCH_NOT_FOUND", "batch not found")
		return
	}
	writeSuccess(w, 200, batch)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "error": map[string]string{"code": code, "message": message}})
}
func writeSuccess(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "data": data})
}

// writeCached 的身份含完整 URL，所以内容相同的不同语言/回退请求不会共享 ETag。
func writeCached(w http.ResponseWriter, r *http.Request, data any, ttl int) {
	b, _ := json.Marshal(struct {
		Schema int `json:"schema_version"`
		Data   any `json:"data"`
	}{1, data})
	etag := `"` + HashJSON(struct {
		URI  string
		Body string
	}{r.URL.RequestURI(), string(b)}) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(ttl))
	w.Header().Set("Vary", "Authorization")
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			w.WriteHeader(304)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(200)
	_, _ = w.Write(b)
}
