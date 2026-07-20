// Package enricher maps owner/name inputs to the canonical github_repos table.
package enricher

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/github"
	"github.com/starcat-app/starcat-weekly-api/internal/model"
	"github.com/starcat-app/starcat-weekly-api/internal/store"
)

type Enricher struct {
	store  store.Store
	client *github.Client
	now    func() time.Time
}

func NewEnricher(s store.Store, client *github.Client) *Enricher {
	return &Enricher{store: s, client: client, now: time.Now}
}

// EnsureGitHubRepo returns a canonical repo row with a stable gh_repo_id.
//
// Enrichment is deliberately separated from source event attachment: callers
// must still write weekly/zread/discovery events even when this method hits the
// 30-minute debounce path. Otherwise a second source could be silently lost.
func (e *Enricher) EnsureGitHubRepo(ctx context.Context, owner, name string, force bool) (model.GitHubRepo, error) {
	if !force {
		existing, err := e.store.GetGitHubRepoByOwnerName(owner, name)
		if err != nil {
			return model.GitHubRepo{}, err
		}
		if existing != nil && existing.EnrichedAt != nil && e.now().UTC().Sub(*existing.EnrichedAt) < 30*time.Minute {
			return *existing, nil
		}
	}

	resp, err := e.client.GetRepo(ctx, owner, name)
	if err != nil {
		if errors.Is(err, github.ErrRepoNotFound) {
			if markErr := e.store.MarkGitHubRepoUnavailable(owner, name, err.Error(), e.now().UTC()); markErr != nil {
				log.Printf("[enricher] mark unavailable %s/%s: %v", owner, name, markErr)
			}
		}
		return model.GitHubRepo{}, err
	}

	now := e.now().UTC()
	canonicalOwner, canonicalName, canonicalFullName := canonicalNames(owner, name, resp.Owner, resp.Name, resp.FullName)
	repo := model.GitHubRepo{
		GhRepoID:      resp.ID,
		Owner:         canonicalOwner,
		Name:          canonicalName,
		FullName:      canonicalFullName,
		Description:   stringValue(resp.Description),
		Homepage:      stringValue(resp.Homepage),
		Language:      stringValue(resp.Language),
		Stars:         resp.Stars,
		Forks:         resp.Forks,
		Watchers:      resp.Watchers,
		Subscribers:   resp.Subscribers,
		OpenIssues:    resp.OpenIssues,
		OwnerAvatar:   stringValue(resp.OwnerAvatar),
		DefaultBranch: resp.DefaultBranch,
		LicenseSpdx:   stringValue(resp.LicenseSpdx),
		Topics:        resp.Topics,
		PushedAt:      resp.PushedAt,
		UpdatedAt:     resp.UpdatedAt,
		CreatedAt:     resp.CreatedAt,
		IsArchived:    resp.Archived,
		IsFork:        resp.Fork,
		IsPrivate:     resp.Private,
		FirstEventAt:  now,
		LatestEventAt: now,
		EnrichedAt:    &now,
		IsAvailable:   true,
	}
	if err := e.store.UpsertGitHubRepo(repo); err != nil {
		return model.GitHubRepo{}, err
	}
	return repo, nil
}

// Legacy methods are intentionally no-ops after R-04. Pipelines now call
// EnsureGitHubRepo inline before attaching each source event.
func (e *Enricher) EnrichAll()      {}
func (e *Enricher) EnrichBatch()    {}
func (e *Enricher) EnrichAllZread() {}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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
