// 日报 worker 在 SQLite 事务外核验 GitHub 项目；本文稿发布与周刊 ingest 无共享写路径。
package dailyradar

import (
	"context"
	"errors"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/starcat-app/starcat-weekly-api/internal/github"
	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// RepoClient 复用服务已有 TokenPool 与 rate limiter；测试注入离线模拟。
type RepoClient interface {
	GetRepo(context.Context, string, string) (*github.RepoResponse, error)
}

type repoCacheEntry struct {
	repo    *github.RepoResponse
	expires time.Time
}

// Worker 是单消费者，避免同一来源序列并行完成造成到达顺序覆盖。
type Worker struct {
	store      Repository
	github     RepoClient
	wake       chan struct{}
	cache      map[string]repoCacheEntry
	jobTimeout time.Duration
}

func NewWorker(store Repository, client RepoClient) *Worker {
	return &Worker{store: store, github: client, wake: make(chan struct{}, 1), cache: make(map[string]repoCacheEntry), jobTimeout: 2 * time.Minute}
}

// Wake 仅作为加速信号；持久队列与定期扫描保证信号丢失也不会漏导入。
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run 启动恢复后持续消费；关闭时保留未完成任务，由下次进程恢复。
func (w *Worker) Run(ctx context.Context) {
	if err := w.store.RecoverRadarJobs(); err != nil {
		log.Printf("[daily-radar] recover queue failed")
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		for ctx.Err() == nil {
			job, err := w.store.ClaimRadarJob(time.Now())
			if err != nil {
				log.Printf("[daily-radar] claim failed")
				break
			}
			if job == nil {
				break
			}
			if err := w.Process(ctx, *job); err != nil {
				if ctx.Err() != nil {
					return
				}
				retry := job.Attempts < 3 && !errors.Is(err, ErrSequence)
				code := "PROCESSING_FAILED"
				if errors.Is(err, ErrSequence) {
					code = "SOURCE_SEQUENCE_CONFLICT"
				}
				if err := w.store.FailRadarJob(job.ID, code, time.Now().Add(time.Duration(job.Attempts)*time.Minute), retry); err != nil {
					log.Printf("[daily-radar] record failure failed")
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-ticker.C:
		}
	}
}

// Process 内容先核验再原子发布。暂时网络错误有限重试，最终允许正文携已核验部分发布。
func (w *Worker) Process(ctx context.Context, job Job) error {
	parent := ctx
	c := job.Request.Reports[0]
	ctx, cancel := context.WithTimeout(ctx, w.jobTimeout)
	defer cancel()
	sourceRepository := strings.ToLower(job.Request.SourceRepository)
	links := ExtractRepositories(c.Markdown)
	projects := make([]RelatedProject, 0)
	seen := make(map[int64]bool)
	partial := false
	for i, link := range links {
		if i >= 100 {
			partial = true
			break
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		if ctx.Err() != nil {
			if job.Attempts < 3 {
				return ctx.Err()
			}
			partial = true
			break
		}
		key := strings.ToLower(link.Owner + "/" + link.Repo)
		// 报告来源本身同样只是出处元数据；不能进入文章相关项目。
		if sourceRepository != "" && key == sourceRepository {
			continue
		}
		entry, ok := w.cache[key]
		var repo *github.RepoResponse
		var err error
		if ok && time.Now().Before(entry.expires) {
			repo = entry.repo
		} else {
			repo = w.publicRepoCache(link.Owner, link.Repo)
			if repo == nil {
				repo, err = w.github.GetRepo(ctx, link.Owner, link.Repo)
			}
			if err == nil {
				if len(w.cache) >= 4096 {
					clear(w.cache)
				}
				w.cache[key] = repoCacheEntry{repo: repo, expires: time.Now().Add(24 * time.Hour)}
			}
		}
		if errors.Is(err, github.ErrRepoNotFound) {
			continue
		}
		if err != nil {
			if parent.Err() != nil {
				return parent.Err()
			}
			if job.Attempts < 3 {
				return err
			}
			partial = true
			continue
		}
		if repo == nil || repo.Private || repo.ID <= 0 || seen[repo.ID] {
			continue
		}
		seen[repo.ID] = true
		projects = append(projects, RelatedProject{Repo: repoCard(repo), EvidenceURL: link.URL})
	}
	category, _ := FindCategory(c.Category)
	reading := (utf8.RuneCountInString(c.Markdown) + 999) / 1000
	repository := job.Request.SourceRepository
	if repository == "" {
		repository = "duanyytop/agents-radar"
	}
	detail := Detail{
		Summary: Summary{ID: ReportID(c), EditionDate: c.EditionDate, Category: c.Category,
			Period: category.Period, Title: c.Title, Summary: c.Summary, ReadingMinutes: max(1, reading),
			Cover: c.Cover, RelatedProjectCount: len(projects), ContentLanguage: c.Language,
			RelatedProjectBadges: RelatedProjectBadges(projects),
			AvailableLanguages:   []string{c.Language}},
		Source: Source{Name: "agents-radar", URL: "https://github.com/" + repository + "/blob/" + c.SourceCommit + "/" + c.SourcePath,
			Commit: c.SourceCommit, Path: c.SourcePath},
		Markdown: c.Markdown, RelatedProjects: projects, RelatedProjectsStatus: "complete",
		GeneratedAt: c.GeneratedAt,
	}
	if partial {
		detail.RelatedProjectsStatus = "partial"
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	// updated_at 是存储元数据，不参加 hash；正文/封面/项目/来源都进入身份。
	detail.Revision = HashJSON(detail)
	detail.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return w.store.PublishRadar(job, detail)
}

// publicRepoCache 只读复用现有公共元数据；不写入来源事件或重算周刊聚合。
// 过期 / 私有 / 明确不可用项不能用旧公共快照冒充一次成功核验。
func (w *Worker) publicRepoCache(owner, name string) *github.RepoResponse {
	cache, ok := w.store.(interface {
		GetGitHubRepoByOwnerName(string, string) (*model.GitHubRepo, error)
	})
	if !ok {
		return nil
	}
	r, err := cache.GetGitHubRepoByOwnerName(owner, name)
	if err != nil || r == nil || !r.IsAvailable || r.IsPrivate || r.EnrichedAt == nil || time.Since(*r.EnrichedAt) > 24*time.Hour {
		return nil
	}
	ptr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	return &github.RepoResponse{ID: r.GhRepoID, Owner: r.Owner, Name: r.Name, FullName: r.FullName, Description: ptr(r.Description),
		Homepage: ptr(r.Homepage), Language: ptr(r.Language), HTMLURL: "https://github.com/" + r.Owner + "/" + r.Name,
		Stars: r.Stars, Forks: r.Forks, Watchers: r.Watchers, Subscribers: r.Subscribers, OpenIssues: r.OpenIssues,
		Topics: r.Topics, LicenseSpdx: ptr(r.LicenseSpdx), OwnerAvatar: ptr(r.OwnerAvatar), Archived: r.IsArchived,
		Fork: r.IsFork, Private: r.IsPrivate, DefaultBranch: r.DefaultBranch, PushedAt: r.PushedAt, UpdatedAt: r.UpdatedAt, CreatedAt: r.CreatedAt}
}

func repoCard(r *github.RepoResponse) model.StarcatRepoCardDTO {
	ptr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	topics := r.Topics
	if topics == nil {
		topics = []string{}
	}
	return model.StarcatRepoCardDTO{GhRepoID: r.ID, FullName: r.FullName, Owner: r.Owner, Repo: r.Name,
		OwnerAvatar: r.OwnerAvatar, Description: r.Description, Language: r.Language, Stars: r.Stars,
		Forks: r.Forks, Watchers: r.Watchers, Subscribers: r.Subscribers, Topics: topics, Homepage: r.Homepage,
		LicenseSpdx: r.LicenseSpdx, IsArchived: r.Archived, IsFork: r.Fork, IsPrivate: r.Private,
		DefaultBranch: ptr(r.DefaultBranch), OpenIssues: r.OpenIssues, PushedAt: ptr(r.PushedAt),
		UpdatedAt: ptr(r.UpdatedAt), CreatedAt: ptr(r.CreatedAt), HTMLURL: ptr(r.HTMLURL)}
}

// RepoLink 保留 Issue / PR / Release 的原始证据，不修改正文跳转语义。
type RepoLink struct{ Owner, Repo, URL string }

var repoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ExtractRepositories 使用 Markdown AST 忽略代码块与图片，只处理实际链接。
func ExtractRepositories(markdown string) []RepoLink {
	source := []byte(markdown)
	root := goldmark.DefaultParser().Parse(text.NewReader(source))
	items := []RepoLink{}
	seen := make(map[string]bool)
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var raw string
		switch node := n.(type) {
		case *ast.Image:
			return ast.WalkSkipChildren, nil
		case *ast.Link:
			raw = string(node.Destination)
		case *ast.AutoLink:
			raw = string(node.URL(source))
		}
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "github.com") || u.User != nil || u.Port() != "" {
			return ast.WalkContinue, nil
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return ast.WalkContinue, nil
		}
		owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
		for _, reserved := range []string{"features", "topics", "collections", "marketplace", "settings", "sponsors", "orgs", "users", "login", "signup", "search", "about", "site", "enterprise", "pricing", "security", "readme"} {
			if strings.EqualFold(owner, reserved) {
				return ast.WalkContinue, nil
			}
		}
		if !repoPart.MatchString(owner) || !repoPart.MatchString(repo) || owner == "." || repo == "." || owner == ".." || repo == ".." {
			return ast.WalkContinue, nil
		}
		if owner == "." || repo == "." || owner == ".." || repo == ".." {
			return ast.WalkContinue, nil
		}
		if len(parts) > 2 {
			switch parts[2] {
			case "issues", "pull", "releases", "discussions":
			default:
				return ast.WalkContinue, nil
			}
		}
		key := strings.ToLower(owner + "/" + repo)
		// 生成器署名是出处元数据，不是文稿讨论的相关项目；历史页脚普遍
		// 携带该链接，也不能等 GitHub 核验 404 后才被动丢弃。
		if key == "duanyytop/agents-radar" || key == "starcat-app/agents-radar" {
			return ast.WalkContinue, nil
		}
		if !seen[key] {
			items = append(items, RepoLink{owner, repo, raw})
			seen[key] = true
		}
		return ast.WalkContinue, nil
	})
	return items
}
