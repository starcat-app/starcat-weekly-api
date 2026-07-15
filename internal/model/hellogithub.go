package model

// HelloGitHubBackfillCursor 是历史回填控制批次的持久化游标。
// Controller 用于区分不含 ingest_items 的控制批次与按期创建的普通子批次。
type HelloGitHubBackfillCursor struct {
	Controller    bool   `json:"controller"`
	FromVolume    int    `json:"from_volume"`
	ToVolume      int    `json:"to_volume"`
	NextVolume    int    `json:"next_volume"`
	Attempts      int    `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

// HelloGitHubBackfillStart 是管理接口启动一次历史回填所需的固定边界。
type HelloGitHubBackfillStart struct {
	ID             string
	IdempotencyKey string
	FromVolume     int
	ToVolume       int
}
