// Package dailyradar 管理完整日报，刻意不复用周刊来源事件和仓库收录队列。
// 发布物是不可变修订；客户端固定修订阅读，后台导入不会改写正在阅读的内容。
package dailyradar

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

// Cover 支持 CDN 与客户端内置备用图；URL 为空不表示服务已生成专属图片。
type Cover struct {
	Kind     string  `json:"kind"`
	CardURL  *string `json:"card_url"`
	HeroURL  *string `json:"hero_url"`
	Fallback string  `json:"fallback"`
	Alt      string  `json:"alt"`
}

// Summary 是列表唯一计数单位，不包含正文和完整关联仓库。
type Summary struct {
	ID                  string   `json:"id"`
	Revision            string   `json:"revision"`
	EditionDate         string   `json:"edition_date"`
	Category            string   `json:"category"`
	Period              string   `json:"period"`
	Title               string   `json:"title"`
	Summary             string   `json:"summary"`
	ReadingMinutes      int      `json:"reading_minutes"`
	Cover               Cover    `json:"cover"`
	RelatedProjectCount int      `json:"related_project_count"`
	ContentLanguage     string   `json:"content_language"`
	AvailableLanguages  []string `json:"available_languages"`
}

// Source 保存固定 Git 对象来源，而非会随 master 改动的链接。
type Source struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Commit string `json:"commit"`
	Path   string `json:"path"`
}

// RelatedProject 是经 GitHub 核验的卡片快照与原链接证据，不含用户 Star 状态。
type RelatedProject struct {
	Repo          model.StarcatRepoCardDTO `json:"repo"`
	EvidenceURL   string                   `json:"evidence_url"`
	SectionAnchor *string                  `json:"section_anchor"`
}

// Detail 的正文、封面、关联项目由同一 revision 固定。
type Detail struct {
	Summary
	Source                Source           `json:"source"`
	Markdown              string           `json:"markdown"`
	RelatedProjects       []RelatedProject `json:"related_projects"`
	RelatedProjectsStatus string           `json:"related_projects_status"`
	GeneratedAt           string           `json:"generated_at"`
	UpdatedAt             string           `json:"updated_at"`
}

// Candidate 是管理端允许提交的原始文稿；不接受上传者注入已核验仓库卡片。
type Candidate struct {
	EditionDate  string `json:"edition_date"`
	Category     string `json:"category"`
	Language     string `json:"language"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	Markdown     string `json:"markdown"`
	SourceCommit string `json:"source_commit"`
	SourcePath   string `json:"source_path"`
	GeneratedAt  string `json:"generated_at"`
	Cover        Cover  `json:"cover"`
}

// ImportRequest 使用来源序列而非网络到达顺序决定版本新旧。
type ImportRequest struct {
	IdempotencyKey   string      `json:"idempotency_key"`
	Source           string      `json:"source"`
	SourceRepository string      `json:"source_repository"`
	SourceKind       string      `json:"source_kind"`
	SourceSequence   int64       `json:"source_sequence"`
	Reports          []Candidate `json:"reports"`
}

// BatchItem 的错误是可展示的脱敏代码，不保存 Token 或原始 HTTP 响应。
type BatchItem struct {
	ID       int64   `json:"id"`
	Status   string  `json:"status"`
	ReportID string  `json:"report_id"`
	Revision *string `json:"revision"`
	Error    *string `json:"error"`
}

// Batch 是可恢复导入的进度快照。
type Batch struct {
	ID        string      `json:"id"`
	Status    string      `json:"status"`
	Total     int         `json:"total"`
	Published int         `json:"published"`
	Failed    int         `json:"failed"`
	Items     []BatchItem `json:"items"`
}

// Job 是 worker 已领取的一项持久任务。
type Job struct {
	ID       int64
	Attempts int
	Request  ImportRequest
}

// Snapshot 在一个 SQLite 读事务内读取目录版本与当前语言指针。
type Snapshot struct {
	Revision string
	Items    []Summary
}

// Repository 只暴露日报路径，不能调用会增加周刊计数的 CompleteIngestItem。
type Repository interface {
	EnqueueRadar(ImportRequest) (*Batch, error)
	RadarBatch(string) (*Batch, error)
	RecoverRadarJobs() error
	ClaimRadarJob(time.Time) (*Job, error)
	FailRadarJob(int64, string, time.Time, bool) error
	PublishRadar(Job, Detail) error
	RadarSnapshot() (Snapshot, error)
	RadarDetail(string, string, string, bool) (*Detail, error)
}

var (
	ErrIdempotency = errors.New("IDEMPOTENCY_CONFLICT")
	ErrSequence    = errors.New("SOURCE_SEQUENCE_CONFLICT")
	ErrReport      = errors.New("REPORT_NOT_FOUND")
	ErrRevision    = errors.New("REVISION_NOT_FOUND")
	ErrLanguage    = errors.New("LANGUAGE_UNAVAILABLE")
)

// Category 是稳定上游类型；名称仅作来源展示，不充当客户端国际化 key。
type Category struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Period string `json:"period"`
}

var Categories = []Category{
	{"ai-cli", "AI CLI", "daily"}, {"ai-agents", "AI Agents", "daily"},
	{"ai-infra", "AI Infrastructure", "daily"}, {"ai-web", "AI Official", "daily"},
	{"ai-trending", "AI Open Source", "daily"}, {"ai-hn", "Hacker News", "daily"},
	{"ai-ph", "Product Hunt", "daily"}, {"ai-arxiv", "ArXiv", "daily"},
	{"ai-hf", "Hugging Face", "weekly"}, {"ai-community", "Community", "daily"},
	{"ai-weekly", "AI Weekly Archive", "weekly"}, {"ai-monthly", "AI Monthly Archive", "monthly"},
}

// FindCategory 确保未知文件不会凭名称猜测进入公开目录。
func FindCategory(id string) (Category, bool) {
	for _, item := range Categories {
		if item.ID == id {
			return item, true
		}
	}
	return Category{}, false
}

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateImport 在入队前验证数据边界；不抓取发布请求中的任何 URL。
func ValidateImport(request *ImportRequest, coverHosts, sourceRepositories []string) error {
	if request.Source != "agents-radar" || (request.SourceKind != "upstream" && request.SourceKind != "starcat") {
		return errors.New("unsupported source")
	}
	if request.SourceKind == "upstream" {
		if request.SourceRepository == "" {
			request.SourceRepository = "duanyytop/agents-radar"
		}
		if request.SourceRepository != "duanyytop/agents-radar" {
			return errors.New("unsupported upstream repository")
		}
	} else {
		parts := strings.Split(request.SourceRepository, "/")
		if len(parts) != 2 || !repoPart.MatchString(parts[0]) || !repoPart.MatchString(parts[1]) || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." || !hostAllowed(request.SourceRepository, sourceRepositories) {
			return errors.New("source_repository is not on the allowlist")
		}
	}
	if len(request.IdempotencyKey) < 1 || len(request.IdempotencyKey) > 200 || request.SourceSequence < 1 {
		return errors.New("idempotency_key and positive source_sequence required")
	}
	if len(request.Reports) < 1 || len(request.Reports) > 50 {
		return errors.New("reports must contain 1–50 items")
	}
	seen := make(map[string]bool)
	for i := range request.Reports {
		c := &request.Reports[i]
		if c.Language == "zh" {
			c.Language = "zh-Hans"
		}
		if c.Language != "zh-Hans" && c.Language != "en" {
			return errors.New("unsupported content language")
		}
		if _, err := time.Parse("2006-01-02", c.EditionDate); err != nil {
			return errors.New("invalid edition_date")
		}
		if _, ok := FindCategory(c.Category); !ok {
			return errors.New("unknown category")
		}
		if strings.TrimSpace(c.Title) == "" || len(c.Title) > 1000 || len(c.Summary) > 12000 || strings.TrimSpace(c.Markdown) == "" || len(c.Markdown) > 1<<20 {
			return errors.New("invalid title, summary or markdown size")
		}
		if !commitPattern.MatchString(c.SourceCommit) || !strings.HasPrefix(c.SourcePath, "digests/"+c.EditionDate+"/") ||
			path.Clean(c.SourcePath) != c.SourcePath || !strings.HasSuffix(c.SourcePath, ".md") {
			return errors.New("invalid fixed Git source")
		}
		generated, err := time.Parse(time.RFC3339, c.GeneratedAt)
		if err != nil {
			return errors.New("invalid generated_at")
		}
		c.GeneratedAt = generated.UTC().Format(time.RFC3339)
		if c.Cover.Kind != "category" && c.Cover.Kind != "generated" {
			return errors.New("invalid cover kind")
		}
		if c.Cover.Fallback != "orbit" && c.Cover.Fallback != "studio" {
			return errors.New("invalid cover fallback")
		}
		for _, raw := range []*string{c.Cover.CardURL, c.Cover.HeroURL} {
			if raw == nil {
				continue
			}
			u, err := url.Parse(*raw)
			if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !hostAllowed(u.Hostname(), coverHosts) {
				return errors.New("cover URL is not on the HTTPS allowlist")
			}
		}
		key := c.EditionDate + ":" + c.Category + ":" + c.Language
		if seen[key] {
			return errors.New("duplicate logical report language in batch")
		}
		seen[key] = true
	}
	return nil
}

func hostAllowed(host string, allowed []string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(host, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

// HashJSON 用稳定字段顺序的结构体产生不可变内容标识。
func HashJSON(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ReportID 在语言间共享，同来源的历史稿与自生成稿也不重复占列表。
func ReportID(c Candidate) string {
	return fmt.Sprintf("agents-radar:%s:%s", c.EditionDate, c.Category)
}
