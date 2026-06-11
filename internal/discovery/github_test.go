package discovery

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dong4j/starcat-weekly-api/internal/github"
	"github.com/dong4j/starcat-weekly-api/internal/tokenpool"
)

func TestGitHubClientFetchesMetadataAndSanitizesReadme(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/agent", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Write([]byte(`{"id":42,"description":"AI agent","homepage":"https://example.com","language":"Go","stargazers_count":10,"forks_count":2,"watchers_count":10,"subscribers_count":3,"open_issues_count":1,"topics":["ai","agent"],"archived":false,"fork":false,"private":false,"default_branch":"main","pushed_at":"2026-06-11T00:00:00Z","updated_at":"2026-06-11T00:00:00Z","created_at":"2026-01-01T00:00:00Z","license":{"spdx_id":"MIT"},"owner":{"avatar_url":"https://avatars.example/acme"}}`))
	})
	mux.HandleFunc("/repos/acme/agent/readme", func(w http.ResponseWriter, _ *http.Request) {
		content := "![badge](https://img.shields.io/x)\n# Agent\n[Docs](https://example.com)\nUseful AI agent."
		fmt.Fprintf(w, `{"encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(content)))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ghClient := github.NewClient(tokenpool.New([]string{"ghp_test_token_1234567890"}), nil)
	ghClient.SetBaseURL(server.URL)
	ghClient.SetHTTPClient(server.Client())

	client := NewGitHubClient(ghClient)
	repo, err := client.Fetch(t.Context(), "acme", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if repo.GhRepoID != 42 || repo.LicenseSpdx != "MIT" || len(repo.Topics) != 2 {
		t.Fatalf("unexpected metadata: %#v", repo)
	}
	if repo.READMEExcerpt != "# Agent\nDocs\nUseful AI agent." {
		t.Fatalf("unexpected README excerpt: %q", repo.READMEExcerpt)
	}
}
