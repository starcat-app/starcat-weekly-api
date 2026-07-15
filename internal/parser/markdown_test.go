// Package parser 单元测试
package parser

import (
	"os"
	"testing"
)

// TestParseIssue399 用固定周刊片段验证解析器。
// fixture 随 package 提交，避免测试依赖开发机上另一个仓库的相对路径。
func TestParseIssue399(t *testing.T) {
	content, err := os.ReadFile("testdata/issue-399.md")
	if err != nil {
		t.Fatalf("无法读取测试样本: %v", err)
	}

	projects, err := ParseMarkdown(content, 399)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	t.Logf("从 issue-399.md 中提取到 %d 个 GitHub 项目", len(projects))

	// 预期至少 10 个（issue-399 包含工具、AI 相关、资源三个板块）
	if len(projects) < 10 {
		t.Errorf("预期至少 10 个项目，实际得到 %d", len(projects))
	}

	// 验证不应包含 ruanyf/weekly 自身
	for _, p := range projects {
		if p.RepoOwner == "ruanyf" {
			t.Errorf("不应包含 ruanyf 仓库: %s/%s", p.RepoOwner, p.RepoName)
		}
	}

	// 验证提取了预期的具体项目
	expectedRepos := []string{
		"sky22333/skyadb",
		"extrastu/readneo",
		"wzh4869/AppPorts",
		"palemoky/fight-the-landlord",
		"hczs/fuckssh",
	}

	found := make(map[string]bool)
	for _, p := range projects {
		found[p.RepoOwner+"/"+p.RepoName] = true
	}

	for _, expected := range expectedRepos {
		if !found[expected] {
			t.Errorf("未找到预期项目: %s", expected)
		}
	}
}

// TestParseGitHubURL 验证 URL 解析和过滤逻辑
func TestParseGitHubURL(t *testing.T) {
	tests := []struct {
		url       string
		wantOwner string
		wantRepo  string
		wantValid bool
	}{
		{"https://github.com/user/repo", "user", "repo", true},
		{"https://github.com/user/repo.git", "user", "repo", true},
		{"https://github.com/user/repo/", "user", "repo", true},
		{"https://github.com/user/repo/issues", "user", "repo", true},
		{"http://github.com/user/repo", "user", "repo", true},
		// 过滤项
		{"https://github.com/ruanyf/weekly", "", "", false},
		{"https://github.com/ruanyf/weekly/issues/10101", "", "", false},
		{"https://github.com/topics/go", "", "", false},
		{"https://github.com/sponsors/user", "", "", false},
		{"https://github.com/orgs/golang", "", "", false},
		// 非法格式
		{"https://github.com/user", "", "", false},
		{"not-a-url", "", "", false},
		{"https://gitlab.com/user/repo", "", "", false},
		// 子路径合法（GitHub URL 中 /user/repo/issues 等同 repo 页面）
		{"https://github.com/user/repo/sub/path", "user", "repo", true},
		// XSS 防护
		{"https://github.com/user/<script>", "", "", false},
	}

	for _, tc := range tests {
		owner, repo, ok := parseGitHubURL(tc.url)
		if ok != tc.wantValid {
			t.Errorf("parseGitHubURL(%q): want valid=%v, got valid=%v", tc.url, tc.wantValid, ok)
		}
		if ok && (owner != tc.wantOwner || repo != tc.wantRepo) {
			t.Errorf("parseGitHubURL(%q): want %s/%s, got %s/%s", tc.url, tc.wantOwner, tc.wantRepo, owner, repo)
		}
	}
}

// TestIsValidRepoPart 验证仓库名合法性
func TestIsValidRepoPart(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"hello", true},
		{"hello-world", true},
		{"hello_world", true},
		{"Hello.World", true},
		{"a", true},
		{"", false},
		{"<script>", false},
		{"hello world", false},
		{"..", false},
		{".hidden", false},
		{"hidden.", false},
	}

	for _, tc := range tests {
		got := isValidRepoPart(tc.input)
		if got != tc.valid {
			t.Errorf("isValidRepoPart(%q) = %v, want %v", tc.input, got, tc.valid)
		}
	}
}
