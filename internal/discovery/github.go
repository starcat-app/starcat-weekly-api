package discovery

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/dong4j/starcat-weekly-api/internal/github"
	"github.com/dong4j/starcat-weekly-api/internal/model"
)

// GitHubClient 拉取 Discovery 分类所需的 repo metadata 与 README 摘要。
//
// 改造后委托给 github.Client（统一 Token 池 + 速率限制），
// 本层只负责模型映射 + README 净化。
type GitHubClient struct {
	client *github.Client
}

// NewGitHubClient 创建 Discovery GitHub 客户端。
// pool 和 limiter 保留参数以兼容调用方，但不再直接使用——全部由 github.Client 管理。
func NewGitHubClient(client *github.Client) *GitHubClient {
	return &GitHubClient{client: client}
}

// Fetch 返回完整 metadata；README 404 视为"无 README"而不是仓库不可用。
func (c *GitHubClient) Fetch(ctx context.Context, owner, repo string) (model.DiscoveryRepo, error) {
	resp, err := c.client.GetRepo(ctx, owner, repo)
	if err != nil {
		if errors.Is(err, github.ErrRepoNotFound) {
			return model.DiscoveryRepo{}, &github.HTTPError{StatusCode: 404, Message: "repo not found"}
		}
		return model.DiscoveryRepo{}, err
	}

	// README：404 不视为错误
	readme := ""
	if content, err := c.client.GetReadme(ctx, owner, repo); err == nil {
		readme = sanitizeREADME(content, 2000)
	}

	result := model.DiscoveryRepo{
		Owner: owner, Repo: repo, GhRepoID: resp.ID,
		Description:   stringValue(resp.Description),
		Homepage:      stringValue(resp.Homepage),
		Language:      stringValue(resp.Language),
		Stars:         resp.Stars,
		Forks:         resp.Forks,
		Watchers:      resp.Watchers,
		Subscribers:   resp.Subscribers,
		OpenIssues:    resp.OpenIssues,
		DefaultBranch: resp.DefaultBranch,
		Topics:        resp.Topics,
		PushedAt:      resp.PushedAt,
		UpdatedAt:     resp.UpdatedAt,
		CreatedAt:     resp.CreatedAt,
		IsArchived:    resp.Archived,
		IsFork:        resp.Fork,
		IsPrivate:     resp.Private,
		READMEExcerpt: readme,
	}
	if resp.OwnerAvatar != nil {
		result.OwnerAvatar = *resp.OwnerAvatar
	}
	if resp.LicenseSpdx != nil {
		result.LicenseSpdx = *resp.LicenseSpdx
	}
	return result, nil
}

var (
	markdownImagePattern = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	htmlTagPattern       = regexp.MustCompile(`<[^>]+>`)
	markdownLinkPattern  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
)

func sanitizeREADME(raw string, maxRunes int) string {
	cleaned := markdownImagePattern.ReplaceAllString(raw, "")
	cleaned = markdownLinkPattern.ReplaceAllString(cleaned, "$1")
	cleaned = htmlTagPattern.ReplaceAllString(cleaned, " ")
	lines := strings.Split(cleaned, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if trimmed == "" || strings.Contains(lower, "shields.io") || lower == "table of contents" || lower == "toc" {
			continue
		}
		kept = append(kept, trimmed)
	}
	cleaned = strings.Join(kept, "\n")
	if utf8.RuneCountInString(cleaned) <= maxRunes {
		return cleaned
	}
	runes := []rune(cleaned)
	return string(runes[:maxRunes])
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
