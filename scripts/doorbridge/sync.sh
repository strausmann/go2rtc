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

git fetch --quiet upstream "+refs/heads/$UPSTREAM_BRANCH:refs/remotes/$UP"
git checkout --quiet "$TARGET"

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
cleanup() {
  git worktree remove --force "$BASE_TREE" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

# Merge. A conflict makes git exit non-zero; handle it, do not abort the script.
mrc=0
git merge --no-ff --quiet -m "Merge $UP into $TARGET" "$UP" >"$WORK/merge.log" 2>&1 || mrc=$?
if [ "$mrc" -ne 0 ]; then
  git diff --name-only --diff-filter=U >"$OUT_DIR/conflicts.txt"
  git merge --abort
  echo "sync: merge conflict, see $OUT_DIR/conflicts.txt" >&2
  exit 20
fi

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
}

rollback() { git reset --hard --quiet "$ORIG"; }

git worktree add --quiet --detach "$BASE_TREE" "$UP"
if ! run_tests "$BASE_TREE" "$WORK/base.txt" || ! run_tests "$PWD" "$WORK/head.txt"; then
  rollback
  exit 2
fi

nrc=0
bash "$HERE/new-failures.sh" "$WORK/base.txt" "$WORK/head.txt" >"$OUT_DIR/new-failures.txt" || nrc=$?
case "$nrc" in
  0) rm -f "$OUT_DIR/new-failures.txt"; echo "sync: merged $UP into $TARGET, gate green"; exit 0 ;;
  1) rollback; echo "sync: new test failures, see $OUT_DIR/new-failures.txt" >&2; exit 30 ;;
  *) rollback; echo "sync: new-failures.sh failed (exit $nrc)" >&2; exit 2 ;;
esac
