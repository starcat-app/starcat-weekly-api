package discovery

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dong4j/starcat-weekly-api/internal/model"
)

func TestLLMClassifierDecodesStrictResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("missing bearer token")
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"agent\",\"confidence\":0.91,\"reason\":\"提供工具调用与任务规划\"}"}}]}`))
	}))
	defer server.Close()

	classifier := NewLLMClassifier(server.URL, "secret", "test-model", server.Client())
	result, err := classifier.Classify(t.Context(), model.DiscoveryRepo{Description: "agent", Topics: []string{"ai"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Category != "agent" || result.Confidence != 0.91 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestLLMClassifierRejectsUnknownCategory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"content":"{\"category\":\"chatbot\",\"confidence\":0.9,\"reason\":\"x\"}"}}]}`))
	}))
	defer server.Close()
	classifier := NewLLMClassifier(server.URL, "secret", "test-model", server.Client())
	if _, err := classifier.Classify(t.Context(), model.DiscoveryRepo{}); err == nil {
		t.Fatal("expected invalid category error")
	}
}
