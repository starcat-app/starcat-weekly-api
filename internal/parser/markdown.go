// Package parser 从周刊 Markdown 中提取 GitHub 项目链接
package parser

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/starcat-app/starcat-weekly-api/internal/model"
)

// githubURLRe 匹配 GitHub 仓库 URL: https://github.com/owner/repo
var githubURLRe = regexp.MustCompile(`^https?://github\.com/([\w.-]+)/([\w.-]+)(?:[/?#].*)?$`)

// filterOwners 需要过滤的 owner（非项目，而是平台/组织页）
var filterOwners = map[string]bool{
	"ruanyf":        true, // 周刊自身仓库
	"topics":        true, // github.com/topics/*
	"sponsors":      true, // github.com/sponsors/*
	"orgs":          true, // github.com/orgs/*
	"features":      true, // github.com/features/*
	"marketplace":   true,
	"explore":       true,
	"notifications": true,
	"settings":      true,
}

// ParseFile 解析单个 Markdown 文件，提取 GitHub 项目列表
func ParseFile(path string, issueNumber int) ([]model.Project, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", path, err)
	}
	return ParseMarkdown(content, issueNumber)
}

// ParseMarkdown 从 Markdown 文本中提取 GitHub 项目
func ParseMarkdown(source []byte, issueNumber int) ([]model.Project, error) {
	md := goldmark.New()
	doc := md.Parser().Parse(text.NewReader(source))

	var projects []model.Project
	seen := make(map[string]bool) // 同一 issue 内去重

	// 遍历 AST 节点
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		link, ok := n.(*ast.Link)
		if !ok {
			return ast.WalkContinue, nil
		}

		url := string(link.Destination)
		owner, repo, ok := parseGitHubURL(url)
		if !ok {
			return ast.WalkContinue, nil
		}

		key := owner + "/" + repo
		if seen[key] {
			return ast.WalkContinue, nil
		}
		seen[key] = true

		// description 由 enricher 从 GitHub API 填入，此处保持空白
		projects = append(projects, model.Project{
			RepoOwner:        owner,
			RepoName:         repo,
			URL:              fmt.Sprintf("https://github.com/%s/%s", owner, repo),
			Description:      "",
			FirstIssueNumber: issueNumber,
			IssueURL:         fmt.Sprintf("https://github.com/ruanyf/weekly/blob/master/docs/issue-%d.md", issueNumber),
			IsAvailable:      true,
		})

		return ast.WalkContinue, nil
	})

	return projects, nil
}

// parseGitHubURL 验证并解析 GitHub URL，提取 owner/repo
func parseGitHubURL(url string) (owner, repo string, ok bool) {
	matches := githubURLRe.FindStringSubmatch(url)
	if len(matches) != 3 {
		return "", "", false
	}
	owner = matches[1]
	repo = matches[2]

	// 过滤非项目的 GitHub 路径
	if filterOwners[strings.ToLower(owner)] {
		return "", "", false
	}

	// 剥离 .git 后缀
	repo = strings.TrimSuffix(repo, ".git")

	// 验证 owner/repo 合法字符（防 XSS / 异常输入）
	if !isValidRepoPart(owner) || !isValidRepoPart(repo) {
		return "", "", false
	}

	return owner, repo, true
}

// isValidRepoPart 验证 GitHub 用户名或仓库名的字符合法性
func isValidRepoPart(s string) bool {
	if len(s) == 0 || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	// 不能以 . 开头或结尾，不能连续 ..
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	return true
}
