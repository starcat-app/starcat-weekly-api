// 历史导入器只读 Git 对象库，不执行上游代码，也不读取未提交的工作区文稿。
package dailyradar

import (
	"context"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
	"time"
)

// HistoryScan 明确记录不支持 / 被聚合稿覆盖的旧文件，不静默声称已全量导入。
type HistoryScan struct {
	Commit  string            `json:"commit"`
	Reports []Candidate       `json:"reports"`
	Skipped map[string]string `json:"skipped"`
}

// ScanHistory 将 refs 解析成固定 commit，然后枚举该 commit 的树和 blobs。
func ScanHistory(ctx context.Context, checkout, ref string) (HistoryScan, error) {
	out := HistoryScan{Reports: []Candidate{}, Skipped: make(map[string]string)}
	run := func(args ...string) ([]byte, error) {
		b, err := exec.CommandContext(ctx, "git", append([]string{"-C", checkout}, args...)...).Output()
		if err != nil {
			return nil, fmt.Errorf("read fixed Git object: %w", err)
		}
		return b, nil
	}
	if strings.HasPrefix(ref, "-") || strings.TrimSpace(ref) == "" {
		return out, fmt.Errorf("commit is required")
	}
	raw, err := run("rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return out, err
	}
	out.Commit = strings.TrimSpace(string(raw))
	raw, err = run("ls-tree", "-r", "--name-only", out.Commit, "--", "digests")
	if err != nil {
		return out, err
	}
	paths := strings.Split(strings.TrimSpace(string(raw)), "\n")
	present := make(map[string]bool)
	for _, p := range paths {
		present[p] = true
	}
	for _, p := range paths {
		parts := strings.Split(p, "/")
		if len(parts) != 3 || !strings.HasSuffix(p, ".md") {
			continue
		}
		date := parts[1]
		if _, err := time.Parse("2006-01-02", date); err != nil {
			out.Skipped[p] = "unrecognized date"
			continue
		}
		name := strings.TrimSuffix(parts[2], ".md")
		language := "zh-Hans"
		suffix := ""
		if strings.HasSuffix(name, "-en") {
			language = "en"
			name = strings.TrimSuffix(name, "-en")
			suffix = "-en"
		}
		category := name
		if _, ok := FindCategory(category); !ok {
			// 旧 OpenClaw 可独立映射；其他工具已经被 ai-cli 聚合时不再重复占目录。
			if name == "openclaw" && !present[path.Join("digests", date, "ai-agents"+suffix+".md")] {
				category = "ai-agents"
			} else {
				reason := "unknown legacy report; retained upstream, not published"
				if present[path.Join("digests", date, "ai-cli"+suffix+".md")] {
					reason = "legacy child/index covered by aggregate; not duplicated"
				}
				out.Skipped[p] = reason
				continue
			}
		}
		blob, err := run("show", out.Commit+":"+p)
		if err != nil {
			return out, err
		}
		markdown := string(blob)
		if len(blob) > 1<<20 {
			out.Skipped[p] = "markdown exceeds 1 MiB import limit"
			continue
		}
		title, summary := "", ""
		for _, line := range strings.Split(markdown, "\n") {
			line = strings.TrimSpace(line)
			if title == "" && strings.HasPrefix(line, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				continue
			}
			if summary == "" && len([]rune(line)) > 15 && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "|") && !strings.HasPrefix(line, "<") {
				r := []rune(line)
				if len(r) > 240 {
					r = r[:240]
				}
				summary = string(r)
			}
		}
		if title == "" {
			out.Skipped[p] = "missing document title"
			continue
		}
		// 历史稿通常没有机器可读的精确生成时刻；使用该文件在固定 ref 中的
		// 最后提交时间作来源时间，不能把整个仓库的最新 commit 时间套给全部历史。
		stamp, err := run("log", "-1", "--format=%cI", out.Commit, "--", p)
		if err != nil {
			return out, err
		}
		generated, err := time.Parse(time.RFC3339, strings.TrimSpace(string(stamp)))
		if err != nil {
			return out, err
		}
		fallback := "orbit"
		if category == "ai-trending" || category == "ai-community" {
			fallback = "studio"
		}
		out.Reports = append(out.Reports, Candidate{EditionDate: date, Category: category, Language: language,
			Title: title, Summary: summary, Markdown: markdown, SourceCommit: out.Commit, SourcePath: p,
			GeneratedAt: generated.UTC().Format(time.RFC3339), Cover: Cover{Kind: "category", Fallback: fallback, Alt: title}})
	}
	sort.Slice(out.Reports, func(i, j int) bool { return out.Reports[i].SourcePath < out.Reports[j].SourcePath })
	return out, nil
}
