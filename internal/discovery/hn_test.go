package discovery

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHNClientFetchUsesOfficialAPIAndExtractsURLAndTextLinks(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/showstories.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[101,102]`))
	})
	mux.HandleFunc("/item/101.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":101,"type":"story","title":"Show HN: One","url":"https://github.com/acme/one","score":12,"descendants":3,"time":1710000000}`))
	})
	mux.HandleFunc("/item/102.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"id":102,"type":"story","title":"Show HN: Two","text":"Try &lt;a href=\"https://github.com/acme/two\"&gt;repo&lt;/a&gt; and https://github.com/acme/two/issues/1","score":8,"descendants":1,"time":1710000010}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewHNClient(server.Client())
	client.baseURL = server.URL
	items, err := client.Fetch(t.Context(), 30, time.Unix(1710000100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 submissions, got %d: %#v", len(items), items)
	}
	if items[0].Owner != "acme" || items[0].Repo != "one" {
		t.Fatalf("unexpected first repo: %#v", items[0])
	}
	if items[1].Owner != "acme" || items[1].Repo != "two" {
		t.Fatalf("unexpected second repo: %#v", items[1])
	}
}

func TestExtractGitHubReposRejectsReservedPathsAndDeduplicates(t *testing.T) {
	items := extractGitHubRepos("https://github.com/topics/ai https://github.com/acme/repo. https://github.com/ACME/repo/issues")
	if len(items) != 1 || items[0].owner != "acme" || items[0].repo != "repo" {
		t.Fatalf("unexpected repos: %#v", items)
	}
}
