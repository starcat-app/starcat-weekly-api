package spider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestZreadSpider_RunOnce 覆盖 3 个典型 case：
//  1. 正常 200 + 完整 JSON
//  2. 字段缺失（desc/topics 缺失）→ 仍能解析（容错）
//  3. code != 0 → 返回 error
//
// 走 httptest mock server 替换真实 zread URL,store 留 nil 不落库
// （与 trending-api/internal/spider/zread_test.go 风格一致）。
func TestZreadSpider_RunOnce(t *testing.T) {
	tests := []struct {
		name     string
		mockJSON string
		mockCode int
		wantErr  bool
	}{
		{
			name: "success",
			mockJSON: `{
				"code": 0,
				"msg": "success",
				"data": [
					{
						"title": "This Week",
						"time_span": {"start": "06/01", "end": "06/07"},
						"repos": [
							{
								"repo_id": "123",
								"owner": "test",
								"name": "repo",
								"description": "desc",
								"description_zh": "desc zh",
								"star_count": 100,
								"language": "Go"
							}
						]
					}
				]
			}`,
			mockCode: http.StatusOK,
			wantErr:  false,
		},
		{
			name: "missing optional fields",
			mockJSON: `{
				"code": 0,
				"msg": "success",
				"data": [
					{
						"title": "Last Week",
						"time_span": {"start": "05/25", "end": "05/31"},
						"repos": [
							{
								"repo_id": "456",
								"owner": "min",
								"name": "fields"
							}
						]
					}
				]
			}`,
			mockCode: http.StatusOK,
			wantErr:  false,
		},
		{
			name:     "api error code",
			mockJSON: `{"code": 1, "msg": "rate limited", "data": []}`,
			mockCode: http.StatusOK,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Sec-Fetch-Dest") != "empty" {
					t.Errorf("missing or wrong Sec-Fetch-Dest")
				}
				w.WriteHeader(tt.mockCode)
				w.Write([]byte(tt.mockJSON))
			}))
			defer server.Close()

			spider := &ZreadSpider{
				client: &http.Client{Timeout: 5 * time.Second},
				url:    server.URL,
				store:  nil, // 不落库
			}

			err := spider.RunOnce(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("RunOnce() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
