// 固定 Git 快照测试覆盖未提交文件隔离和历史旧类型，不执行任何上游代码。
package dailyradar_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	radar "github.com/starcat-app/starcat-weekly-api/internal/dailyradar"
)

func TestHistoryReadsCommitAndReportsLegacyFiles(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_AUTHOR_DATE=2026-02-23T01:00:00Z", "GIT_COMMITTER_DATE=2026-02-23T01:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, "digests", "2026-02-23", name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("ai-cli.md", "# CLI\n\nOriginal content")
	write("codex.md", "# Codex\n\nChild report")
	write("ai-agents.md", "# Agents\n\nChinese aggregate")
	write("openclaw-en.md", "# OpenClaw\n\nLegacy English body")
	write("unknown.md", "# Unknown\n\nUnrecognized historical report")
	git("add", ".")
	git("commit", "-qm", "fixture")
	sha := git("rev-parse", "HEAD")
	write("ai-cli.md", "# MODIFIED\n\nUncommitted change")
	scan, err := radar.ScanHistory(context.Background(), dir, sha)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Commit != sha || len(scan.Reports) != 3 || len(scan.Skipped) != 2 {
		t.Fatalf("%+v", scan)
	}
	for _, report := range scan.Reports {
		if strings.Contains(report.Markdown, "MODIFIED") {
			t.Fatal("read uncommitted file")
		}
		if report.GeneratedAt != "2026-02-23T01:00:00Z" {
			t.Fatal(report.GeneratedAt)
		}
	}
	if _, err := radar.ScanHistory(context.Background(), dir, "--help"); err == nil {
		t.Fatal("accepted flag ref")
	}
}
