package github

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/dong4j/starcat-weekly-api/internal/tokenpool"
)

func TestGetRepoRetriesNextTokenOnRateLimit(t *testing.T) {
	pool := tokenpool.New([]string{"github_pat_token_one_123456", "github_pat_token_two_123456"})
	var authHeaders []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		if len(authHeaders) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "rate limit exceeded"})
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4000")
		_ = json.NewEncoder(w).Encode(repoResponse("acme", "agent"))
	}))
	defer server.Close()

	client := NewClient(pool, nil)
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(server.Client())

	repo, err := client.GetRepo(t.Context(), "acme", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if repo.FullName != "acme/agent" {
		t.Fatalf("unexpected repo: %#v", repo)
	}
	if len(authHeaders) != 2 {
		t.Fatalf("want 2 GitHub calls, got %d", len(authHeaders))
	}
	if authHeaders[0] == authHeaders[1] {
		t.Fatalf("want retry with another token, got same header %q", authHeaders[0])
	}
}

func TestGetReadmeRetriesNextTokenOnRateLimit(t *testing.T) {
	pool := tokenpool.New([]string{"github_pat_token_one_123456", "github_pat_token_two_123456"})
	var authHeaders []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		if len(authHeaders) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": "rate limit exceeded"})
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4000")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  base64.StdEncoding.EncodeToString([]byte("hello")),
			"encoding": "base64",
		})
	}))
	defer server.Close()

	client := NewClient(pool, nil)
	client.SetBaseURL(server.URL)
	client.SetHTTPClient(server.Client())

	readme, err := client.GetReadme(t.Context(), "acme", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if readme != "hello" {
		t.Fatalf("unexpected readme: %q", readme)
	}
	if len(authHeaders) != 2 {
		t.Fatalf("want 2 GitHub calls, got %d", len(authHeaders))
	}
	if authHeaders[0] == authHeaders[1] {
		t.Fatalf("want retry with another token, got same header %q", authHeaders[0])
	}
}

func repoResponse(owner, repo string) map[string]interface{} {
	return map[string]interface{}{
		"id":                42,
		"name":              repo,
		"full_name":         owner + "/" + repo,
		"description":       "AI agent",
		"homepage":          "https://example.com",
		"language":          "Go",
		"stargazers_count":  10,
		"forks_count":       2,
		"watchers_count":    10,
		"subscribers_count": 3,
		"open_issues_count": 1,
		"topics":            []string{"ai", "agent"},
		"archived":          false,
		"fork":              false,
		"private":           false,
		"default_branch":    "main",
		"pushed_at":         "2026-06-11T00:00:00Z",
		"updated_at":        "2026-06-11T00:00:00Z",
		"created_at":        "2026-01-01T00:00:00Z",
		"license":           map[string]string{"spdx_id": "MIT"},
		"owner":             map[string]string{"login": owner, "avatar_url": "https://avatars.example/acme"},
	}
}
