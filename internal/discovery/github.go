package discovery

import (
	"context"
	"errors"
	"strings"

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

// Fetch returns GitHub metadata in the canonical R-04 repo model.
func (c *GitHubClient) Fetch(ctx context.Context, owner, repo string) (model.GitHubRepo, error) {
	resp, err := c.client.GetRepo(ctx, owner, repo)
	if err != nil {
		if errors.Is(err, github.ErrRepoNotFound) {
			return model.GitHubRepo{}, &github.HTTPError{StatusCode: 404, Message: "repo not found"}
		}
		return model.GitHubRepo{}, err
	}

	canonicalOwner, canonicalName, canonicalFullName := canonicalNames(owner, repo, resp.Owner, resp.Name, resp.FullName)
	result := model.GitHubRepo{
		Owner: canonicalOwner, Name: canonicalName, FullName: canonicalFullName, GhRepoID: resp.ID,
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
		IsAvailable:   true,
	}
	if resp.OwnerAvatar != nil {
		result.OwnerAvatar = *resp.OwnerAvatar
	}
	if resp.LicenseSpdx != nil {
		result.LicenseSpdx = *resp.LicenseSpdx
	}
	return result, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func canonicalNames(inputOwner, inputName, owner, name, fullName string) (string, string, string) {
	if owner == "" {
		owner = inputOwner
	}
	if name == "" {
		name = inputName
	}
	if fullName == "" {
		fullName = owner + "/" + name
	}
	return owner, name, fullName
}
