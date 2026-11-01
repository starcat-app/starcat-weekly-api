// daily-radar-import 只读固定 agents-radar Git checkout；默认盘点，显式 -apply 才提交管理 API。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run 不下载或运行上游代码；管理员凭据从环境变量读取，避免进入 shell 历史。
func run() error {
	checkout := flag.String("checkout", "", "local agents-radar Git checkout")
	commit := flag.String("commit", "", "fixed Git commit/ref")
	apply := flag.Bool("apply", false, "submit candidates (default dry-run)")
	endpoint := flag.String("endpoint", "", "weekly or aggregate HTTPS base URL")
	kind := flag.String("source-kind", "upstream", "upstream or starcat")
	repository := flag.String("source-repository", "", "source owner/repo")
	sequence := flag.Int64("source-sequence", 1, "monotonic source sequence; never reuse for different content")
	keyEnv := flag.String("admin-key-env", "DAILY_RADAR_ADMIN_API_KEY", "environment variable containing admin key")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	scan, err := dailyradar.ScanHistory(ctx, *checkout, *commit)
	if err != nil {
		return err
	}
	if !*apply {
		return json.NewEncoder(os.Stdout).Encode(struct {
			DryRun  bool              `json:"dry_run"`
			Commit  string            `json:"commit"`
			Count   int               `json:"count"`
			Skipped map[string]string `json:"skipped"`
		}{true, scan.Commit, len(scan.Reports), scan.Skipped})
	}
	base, err := url.Parse(*endpoint)
	if err != nil || base.Host == "" || base.User != nil || (base.Scheme != "https" && !(base.Scheme == "http" && (base.Hostname() == "localhost" || base.Hostname() == "127.0.0.1"))) {
		return fmt.Errorf("endpoint must be HTTPS (HTTP allowed only on loopback)")
	}
	key := strings.TrimSpace(os.Getenv(*keyEnv))
	if key == "" {
		return fmt.Errorf("admin key environment variable is empty")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for start := 0; start < len(scan.Reports); {
		// JSON 会转义正文；按序列化体积而非仅 Markdown 字节数分批，留出 envelope 预算。
		end := start
		size := 0
		for end < len(scan.Reports) && end-start < 50 {
			b, _ := json.Marshal(scan.Reports[end])
			if end > start && size+len(b) > 7<<20 {
				break
			}
			size += len(b)
			end++
		}
		part := scan.Reports[start:end]
		request := dailyradar.ImportRequest{Source: "agents-radar", SourceKind: *kind, SourceRepository: *repository, SourceSequence: *sequence, Reports: part}
		request.IdempotencyKey = "history-" + scan.Commit + "-" + dailyradar.HashJSON(struct {
			Kind, Repository string
			Sequence         int64
			Paths            []dailyradar.Candidate
		}{*kind, *repository, *sequence, part})[:20]
		body, err := json.Marshal(request)
		if err != nil {
			return err
		}
		u := *base
		u.Path = strings.TrimRight(u.Path, "/") + "/internal/daily-radar/imports"
		u.RawQuery = ""
		req, err := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-SC-Svc", "weekly")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("submit import: %w", err)
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode != 202 {
			return fmt.Errorf("import rejected (HTTP %d)", resp.StatusCode)
		}
		// 202 返回批次 ID；重跑同一固定快照复用幂等键，可恢复中断批次。
		fmt.Println(string(b))
		start = end
	}
	return nil
}
