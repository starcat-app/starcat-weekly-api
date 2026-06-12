package model

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	SourceWeekly    = "weekly"
	SourceZread     = "zread"
	SourceDiscovery = "discovery"

	UncategorizedLanguageKey   = "__uncategorized__"
	UncategorizedLanguageLabel = "Uncategorized"
)

// GitHubRepo is the canonical repo row shared by weekly, zread, and Show HN.
//
// R-04 uses GitHub's immutable numeric repo id as the only identity source.
// owner/name/full_name are mutable display attributes and may change after a
// rename or transfer.
type GitHubRepo struct {
	GhRepoID      int64
	Owner         string
	Name          string
	FullName      string
	Description   string
	Homepage      string
	Language      string
	Stars         int
	Forks         int
	Watchers      int
	Subscribers   int
	OpenIssues    int
	OwnerAvatar   string
	DefaultBranch string
	LicenseSpdx   string
	Topics        []string
	PushedAt      string
	UpdatedAt     string
	CreatedAt     string
	IsArchived    bool
	IsFork        bool
	IsPrivate     bool
	SourceTypes   []string
	FirstEventAt  time.Time
	LatestEventAt time.Time
	EnrichedAt    *time.Time
	RecordUpdated time.Time
	IsAvailable   bool
}

// ToRepoCard converts the canonical repo row to the shared Starcat card DTO.
func (r GitHubRepo) ToRepoCard() StarcatRepoCardDTO {
	htmlURL := "https://github.com/" + r.Owner + "/" + r.Name
	return StarcatRepoCardDTO{
		GhRepoID:      r.GhRepoID,
		FullName:      r.FullName,
		Owner:         r.Owner,
		Repo:          r.Name,
		OwnerAvatar:   strPtrOrNil(r.OwnerAvatar),
		Description:   strPtrOrNil(r.Description),
		Language:      strPtrOrNil(r.Language),
		Stars:         r.Stars,
		Forks:         r.Forks,
		Watchers:      r.Watchers,
		Subscribers:   r.Subscribers,
		Topics:        r.Topics,
		Homepage:      strPtrOrNil(r.Homepage),
		LicenseSpdx:   strPtrOrNil(r.LicenseSpdx),
		IsArchived:    r.IsArchived,
		IsFork:        r.IsFork,
		IsPrivate:     r.IsPrivate,
		DefaultBranch: strPtrOrNil(r.DefaultBranch),
		OpenIssues:    r.OpenIssues,
		PushedAt:      strPtrOrNil(r.PushedAt),
		UpdatedAt:     strPtrOrNil(r.UpdatedAt),
		CreatedAt:     strPtrOrNil(r.CreatedAt),
		HTMLURL:       &htmlURL,
	}
}

type WeeklySnapshot struct {
	IssueNumber    int    `json:"issue_number"`
	IssueURL       string `json:"issue_url"`
	Recommendation string `json:"recommendation,omitempty"`
}

type ZreadSnapshot struct {
	WeekStart     string `json:"week_start"`
	WeekEnd       string `json:"week_end,omitempty"`
	WeekLabel     string `json:"week_label,omitempty"`
	RankInWeek    int    `json:"rank_in_week"`
	DescriptionZh string `json:"description_zh,omitempty"`
}

type DiscoverySnapshot struct {
	HNID        int64  `json:"hn_id"`
	Title       string `json:"title"`
	Score       int    `json:"score"`
	Comments    int    `json:"comments"`
	PublishedAt string `json:"published_at"`
}

// RepoFeedItem is the flattened wire DTO returned by GET /api/v1/repos.
//
// The embedded StarcatRepoCardDTO intentionally serializes as a flat JSON
// object. The client decodes the same object as StarcatRepoCardDTO + feed
// fields, keeping common repo metadata separate from weekly-feed semantics.
type RepoFeedItem struct {
	StarcatRepoCardDTO
	Name          string             `json:"name"`
	IsAvailable   bool               `json:"is_available"`
	SourceTypes   []string           `json:"source_types"`
	FirstEventAt  string             `json:"first_event_at"`
	LatestEventAt string             `json:"latest_event_at"`
	Weekly        *WeeklySnapshot    `json:"weekly"`
	Zread         *ZreadSnapshot     `json:"zread"`
	Discovery     *DiscoverySnapshot `json:"discovery"`
}

type SourceEvent struct {
	ID         string                 `json:"id"`
	Source     string                 `json:"source"`
	OccurredAt string                 `json:"occurred_at"`
	URL        string                 `json:"url,omitempty"`
	Weekly     *WeeklyEventPayload    `json:"weekly,omitempty"`
	Zread      *ZreadEventPayload     `json:"zread,omitempty"`
	Discovery  *DiscoveryEventPayload `json:"discovery,omitempty"`
}

type WeeklyEventPayload struct {
	IssueNumber    int    `json:"issue_number"`
	Recommendation string `json:"recommendation,omitempty"`
}

type ZreadEventPayload struct {
	WeekStart     string `json:"week_start"`
	WeekEnd       string `json:"week_end,omitempty"`
	RankInWeek    int    `json:"rank_in_week"`
	DescriptionZh string `json:"description_zh,omitempty"`
}

type DiscoveryEventPayload struct {
	HNID     int64  `json:"hn_id"`
	Title    string `json:"title"`
	Score    int    `json:"score"`
	Comments int    `json:"comments"`
}

type RepoDetail struct {
	Repo   RepoFeedItem  `json:"repo"`
	Events []SourceEvent `json:"events"`
}

type RepoQuery struct {
	Page     int
	PageSize int
	Source   []string
	Language string
	Sort     string
	Order    string
}

type LanguageAggregate struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

func NewRepoFeedItem(repo GitHubRepo, weekly *WeeklySnapshot, zread *ZreadSnapshot, discovery *DiscoverySnapshot) RepoFeedItem {
	return RepoFeedItem{
		StarcatRepoCardDTO: repo.ToRepoCard(),
		Name:               repo.Name,
		IsAvailable:        repo.IsAvailable,
		SourceTypes:        repo.SourceTypes,
		FirstEventAt:       repo.FirstEventAt.UTC().Format(time.RFC3339),
		LatestEventAt:      repo.LatestEventAt.UTC().Format(time.RFC3339),
		Weekly:             weekly,
		Zread:              zread,
		Discovery:          discovery,
	}
}

func EncodeStringArray(values []string) string {
	if values == nil {
		values = []string{}
	}
	data, _ := json.Marshal(values)
	return string(data)
}

func DecodeStringArray(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	return values
}
