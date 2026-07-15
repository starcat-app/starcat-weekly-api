package model

// RepoSearchResult 是本地控制台选择置顶项目所需的最小仓库信息。
type RepoSearchResult struct {
	GhRepoID    int64    `json:"gh_repo_id"`
	FullName    string   `json:"full_name"`
	Owner       string   `json:"owner"`
	Repo        string   `json:"repo"`
	OwnerAvatar string   `json:"owner_avatar,omitempty"`
	Description string   `json:"description,omitempty"`
	Stars       int      `json:"stars"`
	SourceTypes []string `json:"source_types"`
}

type PinnedRepo struct {
	RepoSearchResult
	Position int    `json:"position"`
	PinnedAt string `json:"pinned_at"`
}
