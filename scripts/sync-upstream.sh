#!/usr/bin/env bash
#
# sync-upstream.sh —— 安全地把上游 Ponphil/LitePan 的更新合并进当前分支。
#
# 设计原则：
#   1. 只读优先：默认先 fetch + 报告规模，不自动合并
#   2. 破坏性动作前留退路：合并前必打 pre-sync-<日期> tag
#   3. 不 push、不改上游远程
#   4. 生成物单独处理：internal/api/web/ 的本地脏改动可直接丢弃（构建会重生成）
#
# 用法：
#   scripts/sync-upstream.sh                 # 交互式：报告 → 确认 → 合并 → 测试
#   scripts/sync-upstream.sh --check         # 只看上游更新了什么，不做任何改动
#   scripts/sync-upstream.sh --yes           # 跳过确认提示
#   scripts/sync-upstream.sh --no-test       # 合并后不跑 make test
#   scripts/sync-upstream.sh --upstream NAME # 上游 remote 名（默认 upstream）
#
set -euo pipefail

UPSTREAM="upstream"
ASSUME_YES=0
DO_TEST=1
CHECK_ONLY=0
GEN_DIR="internal/api/web"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --check)     CHECK_ONLY=1; shift ;;
    --yes|-y)    ASSUME_YES=1; shift ;;
    --no-test)   DO_TEST=0; shift ;;
    --upstream)  UPSTREAM="${2:?--upstream 需要一个 remote 名}"; shift 2 ;;
    -h|--help)   awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"; exit 0 ;;
    *)           echo "未知参数：$1（--help 查看用法）" >&2; exit 2 ;;
  esac
done

cd "$(git rev-parse --show-toplevel)"

say()  { printf '\n\033[1m%s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m%s\033[0m\n' "$*" >&2; exit 1; }
confirm() {
  [[ $ASSUME_YES -eq 1 ]] && return 0
  if [[ ! -t 0 ]]; then
    warn "非交互环境（stdin 不是终端），需显式加 --yes 才会执行。"
    return 1
  fi
  local reply
  read -r -p "$1 [y/N] " reply
  [[ "$reply" == [yY] ]]
}

# ── 0. 前置检查 ───────────────────────────────────────────────────────────────
command -v git >/dev/null || die "找不到 git"
git remote get-url "$UPSTREAM" >/dev/null 2>&1 \
  || die "没有名为 '$UPSTREAM' 的 remote。先跑：git remote add $UPSTREAM https://github.com/Ponphil/LitePan.git"

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
[[ "$BRANCH" == "HEAD" ]] && die "当前处于 detached HEAD，先切回分支再同步"

say "当前分支：$BRANCH    上游：$UPSTREAM"

# ── 1. 工作区必须干净（生成物例外）────────────────────────────────────────────
if [[ -n "$(git status --porcelain)" ]]; then
  DIRTY_OTHER="$(git status --porcelain | grep -v " $GEN_DIR/" || true)"
  if [[ -z "$DIRTY_OTHER" ]]; then
    warn "工作区只有 $GEN_DIR/ 下的生成物改动（构建残留）："
    git status --porcelain | sed 's/^/    /'
    if confirm "这些是打包产物，合并前直接丢弃（构建时会重新生成）？"; then
      git checkout -- "$GEN_DIR"
      # 构建还会留下未跟踪的新 hash 文件，checkout 清不掉，必须一并删除
      git clean -fdq -- "$GEN_DIR"
      echo "已丢弃生成物改动。"
    else
      die "已取消。请手动处理后重试。"
    fi
  else
    warn "工作区有未提交的改动，先提交或 stash 再同步："
    echo "$DIRTY_OTHER" | sed 's/^/    /'
    die "中止。"
  fi
fi

# ── 2. 拉取上游（只读）────────────────────────────────────────────────────────
say "拉取上游 $UPSTREAM ..."
git fetch "$UPSTREAM" --tags --prune

MERGE_BASE="$(git merge-base HEAD "$UPSTREAM/main" 2>/dev/null || true)"
[[ -z "$MERGE_BASE" ]] && die "无法找到与 $UPSTREAM/main 的共同祖先。"

BEHIND="$(git rev-list --count "HEAD..$UPSTREAM/main")"
AHEAD="$(git rev-list --count "$UPSTREAM/main..HEAD")"

say "上游新增 $BEHIND 个提交；本分支自有二开提交 $AHEAD 个。"
[[ "$BEHIND" -eq 0 ]] && { echo "已是最新，无需合并。"; exit 0; }

say "上游新增的提交："
git log --oneline --no-decorate "HEAD..$UPSTREAM/main" | sed 's/^/    /'

say "上游改动的文件规模："
git diff --stat "HEAD...$UPSTREAM/main" | tail -n 20 | sed 's/^/    /'

# 双方都改动过的文件（合并时最可能撞车，仅供心理预期）
# 两侧都用三点 diff（相对共同祖先），否则会把上游改动反向算进本分支。
OVERLAP="$(comm -12 \
  <(git diff --name-only "HEAD...$UPSTREAM/main" | sort -u) \
  <(git diff --name-only "$UPSTREAM/main...HEAD" | sort -u) || true)"
if [[ -n "$OVERLAP" ]]; then
  say "⚠ 双方都改动过的文件（最可能冲突）："
  echo "$OVERLAP" | sed 's/^/    /'
fi

if [[ $CHECK_ONLY -eq 1 ]]; then
  say "--check 模式：到此为止，未做任何合并。"
  exit 0
fi

# 合并间隔提醒（攒太久冲突成本非线性上升）
if [[ "$BEHIND" -gt 100 ]]; then
  warn "上游积压 $BEHIND 个提交，建议以后缩短同步间隔（1–2 周一次）。"
fi

# ── 3. 打退路 tag，然后合并 ───────────────────────────────────────────────────
SAFE_TAG="pre-sync-$(date +%Y%m%d-%H%M%S)"
git tag "$SAFE_TAG"
echo "已打退路 tag：$SAFE_TAG（出问题回退：git reset --hard $SAFE_TAG）"

confirm "开始合并 $UPSTREAM/main 到 $BRANCH ？" || die "已取消（tag $SAFE_TAG 保留，可 git tag -d 删除）"

if ! git merge --no-ff --no-edit "$UPSTREAM/main"; then
  say "合并产生冲突，需要手工处理："
  git diff --name-only --diff-filter=U | sed 's/^/    /'
  cat <<'EOF'

  处理规则（详见 docs/upstream-sync.md）：
    · internal/api/web/**  → 生成物，取任意一侧后重新构建
                             git checkout --ours -- internal/api/web && (cd web && npm run build)
    · 上游重构了结构       → 以上游为新基线，把自己的改动重贴到新结构上
    · 同文件不同行         → 两边都留
  解完：
    git add -A && git commit --no-edit
EOF
  die "合并未完成。要整体放弃：git merge --abort && git reset --hard $SAFE_TAG"
fi

say "合并完成。"

# ── 4. 清理生成物 + 验证 ──────────────────────────────────────────────────────
# 合并可能带进上游版本的产物；本地构建时会重新生成，这里保持工作区干净。
if [[ -n "$(git status --porcelain -- "$GEN_DIR" 2>/dev/null || true)" ]]; then
  warn "合并后 $GEN_DIR/ 有改动，已还原为已提交状态（下次 npm run build 会重新生成）。"
  git checkout -- "$GEN_DIR" 2>/dev/null || true
  git clean -fdq -- "$GEN_DIR" 2>/dev/null || true
fi

if [[ $DO_TEST -eq 1 ]]; then
  say "跑测试（make test）..."
  if make test; then
    echo "测试通过。"
  else
    warn "测试失败。可回退：git reset --hard $SAFE_TAG"
    die "同步后测试未通过，请修复后再 push。"
  fi
fi

# ── 5. 收尾提醒 ───────────────────────────────────────────────────────────────
POST_TAG="sync-$(date +%Y%m%d)"
git tag -f "$POST_TAG" >/dev/null
cat <<EOF

下一步（脚本不代你执行）：
    git push origin $BRANCH --tags          # 备份到自己的 fork
    git tag -f $POST_TAG && git push origin $POST_TAG

记得在 docs/upstream-sync.md 台账里补一行：
    同步日期 / 上游版本 / 上游提交数($BEHIND) / 冲突文件 / 取舍 / 验证结果

⚠ 永远不要点 GitHub 网页上的 "Sync fork → Discard commits"，那会丢掉本地未 push 的二开提交。
EOF
