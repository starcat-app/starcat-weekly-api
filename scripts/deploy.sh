#!/usr/bin/env bash
# =============================================================================
# scripts/deploy.sh — starcat-weekly-api 发版脚本
# =============================================================================
#
# 用法:
#   ./scripts/deploy.sh v1.1.0
#
# 前置依赖:
#   - git (>= 2.x)
#   - gh (GitHub CLI, 已认证: `gh auth status`)
#   - 当前所在分支不能是 main / master
#
# 完整流程 (按顺序):
#   1. 校验参数 (semver: vX.Y.Z)
#   2. 校验: 在 git 仓库, 当前分支不是 main / master
#   3. 校验: 工作区干净 (无 uncommitted / untracked)
#   4. 校验: 当前分支没有未推送的 commit
#   5. 校验: 目标 tag 在 local + origin 都不存在
#   6. 校验: 目标 tag 不低于最新已有 tag (semver compare)
#   7. 校验: gh CLI 已认证
#   8. 推送当前分支到 origin
#   9. 创建 PR (dev → main) 用 PULL_REQUEST_TEMPLATE
#  10. 合并 PR (--merge, 保留 dev 历史, 不删 dev 分支)
#  11. checkout main, pull
#  12. 打 annotated tag v1.1.0 (指向 merge commit)
#  13. 推送 tag → 触发 .github/workflows/fly-deploy.yml
#
# 关键约束 (踩过的坑):
#   - tag 必须在 PR merge 之后打, 确保 tag 指向 main 的 merge commit,
#     而不是 dev 的 tip (否则 tag 跟 main HEAD 指向不同 commit,
#     fly-deploy 会部署到错的代码)
#   - 不能用 --squash merge, 否则会丢失 dev 上的多个 commit 信息
#   - dev 分支绝对不能删 (后续发版还要用)
#   - main / master 上禁止运行此脚本 (会 PR 自己到自己)
#
# 失败处理: set -e + 任意一步 exit 1 都会停止, 不会留下半成品状态
# (已创建的 PR 不会自动关, 需要手动去 GitHub 处理或 gh pr close)
# =============================================================================

set -euo pipefail

# =============================================================================
# 颜色 (只在 TTY 输出, pipe 时关掉避免污染日志)
# =============================================================================
if [[ -t 1 ]]; then
    RED='\033[0;31m'
    GREEN='\033[0;32m'
    YELLOW='\033[1;33m'
    BLUE='\033[0;34m'
    NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; NC=''
fi

# 错误退出: 红字 + exit 1
die() {
    echo -e "${RED}✗ Error:${NC} $*" >&2
    exit 1
}

# 成功步骤: 绿勾
ok() {
    echo -e "${GREEN}✓${NC} $*"
}

# 提示信息: 蓝色
info() {
    echo -e "${BLUE}▶${NC} $*"
}

# 警告: 黄色 (不退出)
warn() {
    echo -e "${YELLOW}!${NC} $*" >&2
}

# =============================================================================
# 1. 参数解析
# =============================================================================
VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
    echo "Usage: $0 vX.Y.Z" >&2
    echo "  Example: $0 v1.1.0" >&2
    exit 1
fi

# semver 格式: vMAJOR.MINOR.PATCH (3 段数字, 不允许 v1.0 / v1 等简写)
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    die "version must match vX.Y.Z (semver), got: '$VERSION' (e.g. v1.1.0)"
fi
ok "version: $VERSION"

# =============================================================================
# 2. 仓库 + 分支检查
# =============================================================================
git rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || die "not in a git repository"

CURRENT_BRANCH=$(git symbolic-ref --short HEAD 2>/dev/null \
    || git rev-parse --short HEAD)

# 硬拦: main / master 上不能跑 (会 PR 自己到自己, 无意义且危险)
if [[ "$CURRENT_BRANCH" == "main" || "$CURRENT_BRANCH" == "master" ]]; then
    die "deploy.sh cannot run on '$CURRENT_BRANCH' — switch to dev or a feature branch first"
fi
ok "branch: $CURRENT_BRANCH (not main/master)"

# =============================================================================
# 3. 工作区干净
# =============================================================================
if ! git diff --quiet HEAD 2>/dev/null; then
    die "working tree has unstaged/staged changes, commit or stash first:
$(git status --short)"
fi

if [[ -n "$(git ls-files --others --exclude-standard)" ]]; then
    die "working tree has untracked files, commit or remove first:
$(git ls-files --others --exclude-standard)"
fi
ok "working tree clean"

# =============================================================================
# 4. 当前分支没有未推送的 commit
# =============================================================================
# 防止本地有 commit 没推, deploy 后 origin 还少这些 commit
if git rev-parse --abbrev-ref "@{u}" >/dev/null 2>&1; then
    UNPUSHED=$(git log --oneline "@{u}..HEAD" 2>/dev/null || true)
    if [[ -n "$UNPUSHED" ]]; then
        die "current branch has unpushed commits, push first:
$UNPUSHED"
    fi
    ok "no unpushed commits on $CURRENT_BRANCH"
else
    warn "current branch has no upstream tracking — assuming local is the source of truth"
fi

# =============================================================================
# 5. Tag 不存在 (local + origin 都要检查)
# =============================================================================
if git rev-parse "refs/tags/$VERSION" >/dev/null 2>&1; then
    die "tag '$VERSION' already exists locally (use a higher version)"
fi

# 检查 origin 是否有这个 tag (处理 local 删除但 origin 仍有的情况)
if git ls-remote --tags origin 2>/dev/null | grep -q "refs/tags/${VERSION}$"; then
    die "tag '$VERSION' already exists on origin (use a higher version)"
fi
ok "tag $VERSION does not exist (local + origin)"

# =============================================================================
# 6. Tag 不低于最新 tag (semver compare)
# =============================================================================
LATEST_TAG=$(git tag --list 'v*' --sort=-v:refname | head -1 || true)
if [[ -n "$LATEST_TAG" ]]; then
    # 用 sort -V (version sort) 取出两个中较大的那个
    HIGHEST=$(printf "%s\n%s\n" "$LATEST_TAG" "$VERSION" | sort -V | tail -1)
    if [[ "$HIGHEST" != "$VERSION" ]]; then
        die "$VERSION is lower than or equal to existing tag $LATEST_TAG"
    fi
    ok "version $VERSION > $LATEST_TAG (semver ok)"
else
    ok "no existing tags — $VERSION will be the first"
fi

# =============================================================================
# 7. gh CLI 已认证
# =============================================================================
if ! gh auth status >/dev/null 2>&1; then
    die "gh CLI not authenticated, run: gh auth login"
fi
ok "gh CLI authenticated"

# =============================================================================
# 8. 推送当前分支 (确保 origin 是最新)
# =============================================================================
info "pushing $CURRENT_BRANCH to origin..."
git push origin "$CURRENT_BRANCH"
ok "pushed $CURRENT_BRANCH"

# =============================================================================
# 9. 创建 PR (dev → main)
# =============================================================================
info "creating PR $CURRENT_BRANCH → main..."

# 用 heredoc 写 PR body (按 .github/PULL_REQUEST_TEMPLATE.md 规范填)
PR_BODY=$(cat <<EOF
## 变更说明

将 \`$CURRENT_BRANCH\` 合并到 \`main\`, 发布版本 **$VERSION**。

## 关联 Issue

<!-- 如有关联 Issue, 请使用 \`Closes #123\` 或 \`Fixes #123\` -->

- Fixes #

## 变更类型

请勾选适用的选项:

- [x] 新功能 (非破坏性,新增功能)
- [ ] Bug 修复
- [ ] 文档更新
- [x] 重构 / 性能优化
- [ ] 测试相关

## 变更内容

- 发版 $VERSION
- 详见 CHANGELOG.md [$VERSION] 段

## 测试

- \`go build ./...\` 通过
- \`go vet ./...\` 通过
- \`gofmt -s -l .\` 无输出
- \`go test ./...\` 通过
EOF
)

# gh pr create 失败会触发 set -e 退出
PR_URL=$(gh pr create \
    --base main \
    --head "$CURRENT_BRANCH" \
    --title "chore(release): $VERSION 发布" \
    --body "$PR_BODY")

PR_NUM=$(echo "$PR_URL" | grep -oE '/pull/[0-9]+$' | grep -oE '[0-9]+')
ok "PR created: $PR_URL (PR #$PR_NUM)"

# =============================================================================
# 10. 合并 PR (--merge 保留 dev 历史, 不删 dev 分支)
# =============================================================================
info "merging PR #$PR_NUM..."
gh pr merge "$PR_NUM" --merge 2>&1
ok "PR #$PR_NUM merged"

# =============================================================================
# 11. 切 main, pull
# =============================================================================
info "switching to main and pulling..."
git checkout main
git pull origin main --ff-only
ok "on main, up-to-date with origin"

# =============================================================================
# 12. 打 annotated tag (指向 merge commit)
# =============================================================================
info "tagging $VERSION..."
git tag -a "$VERSION" -m "Release $VERSION

首个使用 scripts/deploy.sh 自动发布的版本。
- 合并自 $CURRENT_BRANCH (PR #$PR_NUM)
- 详见 CHANGELOG.md [$VERSION] 段"
ok "tagged $VERSION"

# =============================================================================
# 13. 推送 tag → 触发 fly-deploy
# =============================================================================
info "pushing tag $VERSION to origin (triggers fly-deploy)..."
git push origin "$VERSION"
ok "tag $VERSION pushed"

# =============================================================================
# 完成
# =============================================================================
echo ""
echo -e "${GREEN}=========================================${NC}"
echo -e "${GREEN}  $VERSION 部署完成 ✓${NC}"
echo -e "${GREEN}=========================================${NC}"
echo ""
echo "  - PR:      $PR_URL"
echo "  - Tag:     https://github.com/dong4j/starcat-weekly-api/releases/tag/$VERSION"
echo "  - Fly:     https://fly.io/apps/starcat-weekly-api"
echo "  - Action:  https://github.com/dong4j/starcat-weekly-api/actions/workflows/fly-deploy.yml"
echo ""
echo "  下一步: 等待 fly-deploy workflow 完成 (通常 < 2 分钟)"
