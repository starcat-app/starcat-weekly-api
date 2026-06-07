// Package fetcher 通过 git 获取 ruanyf/weekly 的 MD 文件
package fetcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// IssueFile 代表一个周刊 MD 文件
type IssueFile struct {
	Number int    // 期号
	Path   string // 本地文件路径
}

// CloneOrPull 克隆或更新 ruanyf/weekly 仓库，返回所有 MD 文件列表
func CloneOrPull(repoDir string) ([]IssueFile, error) {
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		// 首次克隆
		cmd := exec.Command("git", "clone", "--depth", "1",
			"https://github.com/ruanyf/weekly.git", repoDir)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("git clone: %w", err)
		}
	} else {
		// 增量更新
		cmd := exec.Command("git", "-C", repoDir, "pull", "--ff-only")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// pull 失败不阻塞（可能是网络问题），用已有数据继续
			fmt.Fprintf(os.Stderr, "[fetcher] git pull failed (using cached data): %v\n", err)
		}
	}

	return listIssueFiles(repoDir)
}

// listIssueFiles 扫描 docs/issue-*.md 文件，按期号排序
func listIssueFiles(repoDir string) ([]IssueFile, error) {
	docsDir := filepath.Join(repoDir, "docs")
	pattern := filepath.Join(docsDir, "issue-*.md")

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}

	// 从文件名提取期号
	re := regexp.MustCompile(`issue-(\d+)\.md$`)
	var issues []IssueFile
	for _, m := range matches {
		parts := re.FindStringSubmatch(m)
		if len(parts) != 2 {
			continue
		}
		num, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}
		issues = append(issues, IssueFile{Number: num, Path: m})
	}

	sort.Slice(issues, func(i, j int) bool {
		return issues[i].Number < issues[j].Number
	})

	return issues, nil
}
