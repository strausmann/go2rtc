#!/usr/bin/env bash
# Tests for the doorbridge sync scripts. Builds throw-away git repos in a temp dir.
# Usage: bash scripts/doorbridge/test/run.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPTS="$(cd "$HERE/.." && pwd)"
DATA="$HERE/testdata"

# Isolate from the caller's git/go configuration.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.invalid
export GOTOOLCHAIN=local GOFLAGS=-mod=mod

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0
ok()  { PASS=$((PASS + 1)); echo "ok   - $1"; }
bad() { FAIL=$((FAIL + 1)); echo "FAIL - $1"; [ $# -lt 2 ] || echo "       $2"; }

# expect_eq <name> <want> <got>
expect_eq() {
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "want: [$2] got: [$3]"; fi
}

# ---------------------------------------------------------------- failed-tests.sh
got="$(bash "$SCRIPTS/failed-tests.sh" < "$DATA/gotest.json")"
expect_eq "failed-tests: fixture (fail, build failed, cached)" "$(cat "$DATA/gotest.expected")" "$got"

got="$(printf '' | bash "$SCRIPTS/failed-tests.sh")"
expect_eq "failed-tests: empty input gives empty output" "" "$got"

# A package that fails without any failing test (e.g. TestMain exit) must not vanish.
got="$(printf '%s\n' '{"Action":"fail","Package":"p/x"}' | bash "$SCRIPTS/failed-tests.sh")"
expect_eq "failed-tests: bare package failure is reported" "p/x [package failed]" "$got"

# ---------------------------------------------------------------- new-failures.sh
printf 'p/a TestA\np/b TestB\n' > "$TMP/base.txt"
printf 'p/b TestB\np/a TestA\n' > "$TMP/head-reordered.txt"
rc=0; out="$(bash "$SCRIPTS/new-failures.sh" "$TMP/base.txt" "$TMP/head-reordered.txt")" || rc=$?
expect_eq "new-failures: same set, other order -> exit 0" "0" "$rc"
expect_eq "new-failures: same set, other order -> no output" "" "$out"

printf 'p/a TestA\np/b TestB\ngithub.com/AlexxIT/go2rtc/internal/streams TestNewThing\n' > "$TMP/head-extra.txt"
rc=0; out="$(bash "$SCRIPTS/new-failures.sh" "$TMP/base.txt" "$TMP/head-extra.txt")" || rc=$?
expect_eq "new-failures: additional failure -> exit 1" "1" "$rc"
expect_eq "new-failures: additional failure -> exactly that line" "github.com/AlexxIT/go2rtc/internal/streams TestNewThing" "$out"

printf 'p/a TestA\n' > "$TMP/head-fewer.txt"
rc=0; out="$(bash "$SCRIPTS/new-failures.sh" "$TMP/base.txt" "$TMP/head-fewer.txt")" || rc=$?
expect_eq "new-failures: one failure fewer -> exit 0" "0" "$rc"
expect_eq "new-failures: one failure fewer -> no output" "" "$out"

# A missing/unreadable input must not look like "no failures".
rc=0; bash "$SCRIPTS/new-failures.sh" "$TMP/base.txt" "$TMP/does-not-exist.txt" >/dev/null 2>&1 || rc=$?
expect_eq "new-failures: missing head file -> exit 2" "2" "$rc"
rc=0; bash "$SCRIPTS/new-failures.sh" "$TMP/does-not-exist.txt" "$TMP/head-extra.txt" >/dev/null 2>&1 || rc=$?
expect_eq "new-failures: missing base file -> exit 2" "2" "$rc"

# ---------------------------------------------------------------- sync.sh
# Repo layout per case:
#   $W/upstream.git  bare repo, branch master
#   $W/repo          clone with remote "upstream", branch "target" forked from master
# Module: one package with Value(), TestGreen (passes) and TestRed (always fails),
# so the package is red on upstream already.

commit_file() { # <dir> <file> <content> <message>
  mkdir -p "$(dirname "$1/$2")"
  printf '%s\n' "$3" > "$1/$2"
  git -C "$1" add -- "$2"
  git -C "$1" commit -q -m "$4"
}

new_case() { # <name>; sets W, SEED, REPO, OUT
  W="$TMP/$1"; SEED="$W/seed"; REPO="$W/repo"; OUT="$W/out"
  mkdir -p "$OUT"
  git init -q --bare -b master "$W/upstream.git"
  git init -q -b master "$SEED"
  git -C "$SEED" remote add origin "$W/upstream.git"
  printf 'module example.invalid/tiny\n\ngo 1.21\n' > "$SEED/go.mod"
  printf 'package tiny\n\nfunc Value() int { return 1 }\n' > "$SEED/value.go"
  cat > "$SEED/tiny_test.go" <<'GO'
package tiny

import "testing"

func TestGreen(t *testing.T) {}

func TestRed(t *testing.T) { t.Fatal("red on upstream already") }
GO
  printf 'hello\n' > "$SEED/greeting.txt"
  git -C "$SEED" add -A && git -C "$SEED" commit -q -m "initial"
  git -C "$SEED" push -q origin master
}

clone_repo() { # branches "target" from the current upstream master
  git clone -q "$W/upstream.git" "$REPO"
  git -C "$REPO" remote rename origin upstream
  git -C "$REPO" checkout -q -b target
}

run_sync() { # [target] [shim-dir]; prints exit code of sync.sh; files go to $OUT
  local rc=0
  (cd "$REPO" && PATH="${2:+$2:}$PATH" SYNC_OUT_DIR="$OUT" bash "$SCRIPTS/sync.sh" "${1:-target}" >"$OUT/log.txt" 2>&1) || rc=$?
  echo "$rc"
}

# (a) upstream moves on without overlap -> merged, gate green
new_case a; clone_repo
commit_file "$REPO" target.txt "target change" "target: own commit"
commit_file "$SEED" docs.txt "upstream change" "upstream: new file"
git -C "$SEED" push -q origin master
rc="$(run_sync)"
expect_eq "sync (a): no overlap -> exit 0" "0" "$rc"
parents="$(git -C "$REPO" rev-list --parents -n 1 HEAD | wc -w)"
expect_eq "sync (a): HEAD is a merge commit" "3" "$parents"
expect_eq "sync (a): upstream file is merged in" "upstream change" "$(cat "$REPO/docs.txt")"
expect_eq "sync (a): nothing pushed" "$(git -C "$SEED" rev-parse HEAD)" "$(git -C "$W/upstream.git" rev-parse master)"

# (b) nothing new upstream -> exit 10, nothing changed
new_case b; clone_repo
commit_file "$REPO" target.txt "target change" "target: own commit"
before="$(git -C "$REPO" rev-parse HEAD)"
rc="$(run_sync)"
expect_eq "sync (b): up to date -> exit 10" "10" "$rc"
expect_eq "sync (b): HEAD unchanged" "$before" "$(git -C "$REPO" rev-parse HEAD)"

# (c) same line changed on both sides, target forked from an OLDER upstream commit
new_case c
commit_file "$SEED" other.txt "unrelated" "upstream: second commit"
git -C "$SEED" push -q origin master
git clone -q "$W/upstream.git" "$REPO"
git -C "$REPO" remote rename origin upstream
git -C "$REPO" checkout -q -b target HEAD~1
commit_file "$REPO" greeting.txt "from target" "target: change greeting"
commit_file "$SEED" greeting.txt "from upstream" "upstream: change greeting"
git -C "$SEED" push -q origin master
before="$(git -C "$REPO" rev-parse HEAD)"
rc="$(run_sync)"
expect_eq "sync (c): conflict -> exit 20" "20" "$rc"
expect_eq "sync (c): conflicts.txt names the file" "greeting.txt" "$(cat "$OUT/conflicts.txt" 2>/dev/null || true)"
expect_eq "sync (c): working tree clean" "" "$(git -C "$REPO" status --porcelain)"
expect_eq "sync (c): HEAD unchanged" "$before" "$(git -C "$REPO" rev-parse HEAD)"
merging="no"; [ ! -e "$REPO/.git/MERGE_HEAD" ] || merging="yes"
expect_eq "sync (c): no merge in progress" "no" "$merging"

# (d) clean merge, but a target-only test breaks in the already red package
new_case d; clone_repo
cat > "$REPO/target_test.go" <<'GO'
package tiny

import "testing"

func TestTargetNeedsOne(t *testing.T) {
	if Value() != 1 {
		t.Fatalf("Value() = %d", Value())
	}
}
GO
git -C "$REPO" add -- target_test.go && git -C "$REPO" commit -q -m "target: add test"
printf 'package tiny\n\nfunc Value() int { return 2 }\n' > "$SEED/value.go"
git -C "$SEED" add -- value.go && git -C "$SEED" commit -q -m "upstream: change Value"
git -C "$SEED" push -q origin master
before="$(git -C "$REPO" rev-parse HEAD)"
rc="$(run_sync)"
expect_eq "sync (d): new test failure -> exit 30" "30" "$rc"
expect_eq "sync (d): new-failures.txt lists exactly the new test" "example.invalid/tiny TestTargetNeedsOne" "$(cat "$OUT/new-failures.txt" 2>/dev/null || true)"
expect_eq "sync (d): merge is rolled back" "$before" "$(git -C "$REPO" rev-parse HEAD)"

# (e) merge fails without a content conflict (unrelated histories) -> exit 2, nothing changed
new_case e; clone_repo
git -C "$REPO" checkout -q --orphan target-orphan
git -C "$REPO" rm -rq -f . && printf 'x\n' > "$REPO/own.txt"
git -C "$REPO" add -- own.txt && git -C "$REPO" commit -q -m "orphan"
git -C "$REPO" branch -q -m target-orphan target-e
commit_file "$SEED" docs.txt "upstream change" "upstream: new file"
git -C "$SEED" push -q origin master
before="$(git -C "$REPO" rev-parse HEAD)"
rc="$(run_sync target-e)"
expect_eq "sync (e): merge failure without conflict -> exit 2" "2" "$rc"
expect_eq "sync (e): working tree clean" "" "$(git -C "$REPO" status --porcelain)"
expect_eq "sync (e): HEAD unchanged" "$before" "$(git -C "$REPO" rev-parse HEAD)"
expect_eq "sync (e): git's reason is shown" "1" "$(grep -c 'unrelated histories' "$OUT/log.txt" || true)"

# (f) unexpected failure AFTER the merge (git worktree add fails) -> HEAD back to pre-merge
new_case f; clone_repo
commit_file "$REPO" target.txt "target change" "target: own commit"
commit_file "$SEED" docs.txt "upstream change" "upstream: new file"
git -C "$SEED" push -q origin master
REALGIT="$(command -v git)"
mkdir -p "$W/shim"
cat > "$W/shim/git" <<SH
#!/usr/bin/env bash
if [ "\${1:-}" = worktree ] && [ "\${2:-}" = add ]; then exit 99; fi
exec "$REALGIT" "\$@"
SH
chmod +x "$W/shim/git"
before="$(git -C "$REPO" rev-parse HEAD)"
rc="$(run_sync target "$W/shim")"
expect_eq "sync (f): failure after merge -> non-zero exit" "1" "$([ "$rc" != 0 ] && echo 1 || echo 0)"
expect_eq "sync (f): HEAD rolled back to pre-merge" "$before" "$(git -C "$REPO" rev-parse HEAD)"
expect_eq "sync (f): working tree clean" "" "$(git -C "$REPO" status --porcelain)"

# (g) missing target branch -> exit 2
new_case g; clone_repo
commit_file "$SEED" docs.txt "upstream change" "upstream: new file"
git -C "$SEED" push -q origin master
rc="$(run_sync no-such-branch)"
expect_eq "sync (g): missing target branch -> exit 2" "2" "$rc"

# (h) fetch fails (remote gone) -> exit 2
new_case h; clone_repo
git -C "$REPO" remote set-url upstream "$W/does-not-exist.git"
rc="$(run_sync)"
expect_eq "sync (h): fetch failure -> exit 2" "2" "$rc"

# (i) go test exits non-zero but its JSON holds no failure -> exit 2, merge rolled back
new_case i; clone_repo
commit_file "$REPO" target.txt "target change" "target: own commit"
commit_file "$SEED" docs.txt "upstream change" "upstream: new file"
git -C "$SEED" push -q origin master
REALGO="$(command -v go)"
mkdir -p "$W/shim"
cat > "$W/shim/go" <<SH
#!/usr/bin/env bash
if [ "\${1:-}" = test ]; then
  echo '{"Action":"pass","Package":"p/x"}'
  exit 1
fi
exec "$REALGO" "\$@"
SH
chmod +x "$W/shim/go"
before="$(git -C "$REPO" rev-parse HEAD)"
rc="$(run_sync target "$W/shim")"
expect_eq "sync (i): go test failed without parsed failure -> exit 2" "2" "$rc"
expect_eq "sync (i): HEAD rolled back" "$before" "$(git -C "$REPO" rev-parse HEAD)"

echo
echo "passed: $PASS  failed: $FAIL"
[ "$FAIL" -eq 0 ]
