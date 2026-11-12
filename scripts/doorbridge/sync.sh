#!/usr/bin/env bash
# sync.sh <target-branch>
# Merge upstream (remote "upstream", branch $UPSTREAM_BRANCH, default master) into
# <target-branch> and check that the merge introduces no new test failures.
# Never pushes. The caller pushes only on exit 0.
#
# Exit codes:
#   0   merged, gate green, ready to push
#   10  already up to date (no new upstream commit)
#   20  merge conflict, merge aborted, conflicting files in conflicts.txt
#   30  new test failures, merge rolled back, list in new-failures.txt
#   2   usage or environment error
#
# Output files go to $SYNC_OUT_DIR (default: current directory).
#
# Gate: failures are compared on test-name level. The base list comes from
# unmodified upstream/<branch>, the head list from the merge result. Only failures
# that exist in head and not in base count, so packages that already fail upstream
# do not hide new failures inside them.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET="${1:-}"
UPSTREAM_BRANCH="${UPSTREAM_BRANCH:-master}"
OUT_DIR="${SYNC_OUT_DIR:-$PWD}"
UP="upstream/$UPSTREAM_BRANCH"

if [ -z "$TARGET" ]; then
  echo "usage: sync.sh <target-branch>" >&2
  exit 2
fi
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"
rm -f "$OUT_DIR/conflicts.txt" "$OUT_DIR/new-failures.txt"

if [ -n "$(git status --porcelain)" ]; then
  echo "sync: working tree is not clean" >&2
  exit 2
fi

if ! git fetch --quiet upstream "+refs/heads/$UPSTREAM_BRANCH:refs/remotes/$UP"; then
  echo "sync: git fetch from remote 'upstream' failed" >&2
  exit 2
fi
if ! git checkout --quiet "$TARGET"; then
  echo "sync: cannot check out target branch '$TARGET'" >&2
  exit 2
fi

# is-ancestor: 0 = yes, 1 = no, anything else = real error.
rc=0
git merge-base --is-ancestor "$UP" HEAD || rc=$?
case "$rc" in
  0) echo "sync: $TARGET already contains $UP"; exit 10 ;;
  1) ;;
  *) echo "sync: git merge-base failed (exit $rc)" >&2; exit 2 ;;
esac

ORIG="$(git rev-parse HEAD)"
WORK="$(mktemp -d)"
BASE_TREE="$WORK/base"
MERGED=0

# Only exit 0 may keep the merge. Every other exit code (including an unexpected
# `set -e` death after the merge) rolls back to the pre-merge commit.
cleanup() {
  local rc=$?
  trap - EXIT
  if [ "$MERGED" = 1 ] && [ "$rc" -ne 0 ]; then
    git reset --hard --quiet "$ORIG" || echo "sync: ROLLBACK FAILED, reset to $ORIG manually" >&2
  fi
  git worktree remove --force "$BASE_TREE" >/dev/null 2>&1 || true
  rm -rf "$WORK"
  exit "$rc"
}
trap cleanup EXIT

# Merge. A failing merge is either a content conflict (MERGE_HEAD exists or there are
# unmerged files) or something else (unrelated histories, no merge base in a shallow
# clone, untracked files in the way). Only the first is exit 20.
mrc=0
git merge --no-ff --quiet -m "Merge $UP into $TARGET" "$UP" >"$WORK/merge.log" 2>&1 || mrc=$?
if [ "$mrc" -ne 0 ]; then
  unmerged="$(git diff --name-only --diff-filter=U)"
  if [ -e "$(git rev-parse --git-path MERGE_HEAD)" ] || [ -n "$unmerged" ]; then
    printf '%s\n' "$unmerged" >"$OUT_DIR/conflicts.txt"
    git merge --abort
    echo "sync: merge conflict, see $OUT_DIR/conflicts.txt" >&2
    exit 20
  fi
  echo "sync: merge failed without conflict (exit $mrc):" >&2
  cat "$WORK/merge.log" >&2
  echo "sync: a shallow clone has no merge base; fetch full history (fetch-depth 0)" >&2
  exit 2
fi
MERGED=1

# run_tests <dir> <out>: go test exits 1 on any test failure; that is data here.
# An empty result would look like "no failures", so it is an error.
run_tests() {
  local dir="$1" out="$2" trc=0
  (cd "$dir" && go test -json ./...) >"$WORK/raw.json" 2>"$WORK/go-stderr.txt" || trc=$?
  if [ ! -s "$WORK/raw.json" ]; then
    echo "sync: go test produced no output (exit $trc)" >&2
    cat "$WORK/go-stderr.txt" >&2
    return 1
  fi
  bash "$HERE/failed-tests.sh" <"$WORK/raw.json" >"$out"
  # Non-zero exit with no parsed failure means a failure that never reached the
  # JSON (load error, toolchain problem). That must not count as green.
  if [ "$trc" -ne 0 ] && [ ! -s "$out" ]; then
    echo "sync: go test exited $trc but no failure was parsed from its output" >&2
    cat "$WORK/go-stderr.txt" >&2
    return 1
  fi
}

git worktree add --quiet --detach "$BASE_TREE" "$UP"
if ! run_tests "$BASE_TREE" "$WORK/base.txt" || ! run_tests "$PWD" "$WORK/head.txt"; then
  exit 2
fi

nrc=0
bash "$HERE/new-failures.sh" "$WORK/base.txt" "$WORK/head.txt" >"$OUT_DIR/new-failures.txt" || nrc=$?
case "$nrc" in
  0) rm -f "$OUT_DIR/new-failures.txt"; echo "sync: merged $UP into $TARGET, gate green"; exit 0 ;;
  1) echo "sync: new test failures, see $OUT_DIR/new-failures.txt" >&2; exit 30 ;;
  *) echo "sync: new-failures.sh failed (exit $nrc)" >&2; exit 2 ;;
esac
