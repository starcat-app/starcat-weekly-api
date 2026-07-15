package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHelloGitHubFetchFeaturedPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Fatalf("page=%q", got)
		}
		_, _ = w.Write([]byte(`{"success":true,"page":2,"data":[{"item_id":"item-1","full_name":"owner/repo","title":"标题","title_en":"Title","summary":"摘要","summary_en":"Summary","is_hot":true,"primary_lang":"Go","updated_at":"2026-06-29T08:10:22"}]}`))
	}))
	defer server.Close()

	client := NewHelloGitHubClient(server.Client())
	client.apiBase = server.URL
	client.webBase = "https://hellogithub.example"
	candidates, err := client.FetchFeaturedPage(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Owner != "owner" || candidates[0].Repo != "repo" {
		t.Fatalf("candidates=%+v", candidates)
	}
	if candidates[0].ExternalKey != "featured:item-1" || candidates[0].SourceURL != "https://hellogithub.example/repository/item-1" {
		t.Fatalf("candidate=%+v", candidates[0])
	}
	if candidates[0].Payload["primary_language"] != "Go" || candidates[0].Payload["is_hot"] != true {
		t.Fatalf("payload=%+v", candidates[0].Payload)
	}
}

func TestHelloGitHubFetchVolume(t *testing.T) {
	document := `{"props":{"pageProps":{"volume":{"success":true,"total":123,"current_num":123,"publish_at":"2026-06-29T08:10:22","data":[{"category_id":11,"category_name":"C 项目","items":[{"rid":"rid-1","name":"keyd","full_name":"rvaiya/keyd","description":"说明","description_en":"Description","publish_at":"2026-06-29T08:10:22"}]}]}}}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><script id="__NEXT_DATA__" type="application/json">` + document + `</script></html>`))
	}))
	defer server.Close()

	client := NewHelloGitHubClient(server.Client())
	client.webBase = server.URL
	volume, err := client.FetchVolume(context.Background(), 123)
	if err != nil {
		t.Fatal(err)
	}
	if volume.Number != 123 || volume.Latest != 123 || len(volume.Candidates) != 1 {
		t.Fatalf("volume=%+v", volume)
	}
	candidate := volume.Candidates[0]
	if candidate.ExternalKey != "volume:123:rvaiya/keyd" || candidate.Payload["category_name"] != "C 项目" {
		t.Fatalf("candidate=%+v", candidate)
	}
}

func TestHelloGitHubRejectsChangedStructures(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "featured missing data", body: `{"success":true,"page":1}`},
		{name: "invalid repository", body: `{"success":true,"page":1,"data":[{"item_id":"1","full_name":"invalid","updated_at":"2026-06-29T08:10:22"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(test.body)) }))
			defer server.Close()
			client := NewHelloGitHubClient(server.Client())
			client.apiBase = server.URL
			if _, err := client.FetchFeaturedPage(context.Background(), 1); err == nil {
				t.Fatal("expected structural error")
			}
		})
	}
}
