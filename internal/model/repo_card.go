package model

/*
StarcatRepoCardDTO Schema Rules:
1. Core Fields (Top Level): Only native GitHub repo metadata (from /repos/{o}/{r}).
2. Extension Segments (Nested): Scene-specific data (e.g., weekly, trending).
3. Red Line: NEVER put scene-specific fields (like editorComment) at the top level.
*/

// StarcatRepoCardDTO is the unified repo card data transfer object.
type StarcatRepoCardDTO struct {
	GhRepoID      int64            `json:"gh_repo_id"`
	FullName      string           `json:"full_name"`
	Owner         string           `json:"owner"`
	Repo          string           `json:"repo"`
	OwnerAvatar   *string          `json:"owner_avatar"`
	Description   *string          `json:"description"`
	Language      *string          `json:"language"`
	Stars         int              `json:"stars"`
	Forks         int              `json:"forks"`
	Watchers      int              `json:"watchers"`
	Subscribers   int              `json:"subscribers"`
	Topics        []string         `json:"topics"`
	Homepage      *string          `json:"homepage"`
	LicenseSpdx   *string          `json:"license_spdx"`
	IsArchived    bool             `json:"is_archived"`
	IsFork        bool             `json:"is_fork"`
	IsPrivate     bool             `json:"is_private"`
	DefaultBranch *string          `json:"default_branch"`
	OpenIssues    int              `json:"open_issues"`
	PushedAt      *string          `json:"pushed_at"`
	UpdatedAt     *string          `json:"updated_at"`
	CreatedAt     *string          `json:"created_at"`
	HtmlURL       *string          `json:"html_url"`
	Weekly        *WeeklyExtension `json:"weekly,omitempty"`
}

// WeeklyExtension contains scene-specific data for Weekly.
type WeeklyExtension struct {
	FirstIssue int    `json:"first_issue"`
	IssueURL   string `json:"issue_url"`
}
