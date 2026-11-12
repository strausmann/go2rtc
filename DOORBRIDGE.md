# Doorbridge fork

This fork of [AlexxIT/go2rtc](https://github.com/AlexxIT/go2rtc) exists for one reason: it carries
the UniFi Protect talkback source from upstream pull request
[AlexxIT/go2rtc#2340](https://github.com/AlexxIT/go2rtc/pull/2340) (Opus talkback to UniFi Protect
cameras and doorbells) before that change is available in an upstream release.

## Branch model

| Branch | Purpose |
|---|---|
| `master` | Pure mirror of `upstream/master`. Never carries fork commits. Updated only by force-pushing upstream's tip. |
| `doorbridge` | Default branch. `master` plus the PR #2340 merge and the helper scripts in `scripts/doorbridge/`. All images are built from here. |

`doorbridge` is the default branch because GitHub runs scheduled workflows only from the default
branch. Upstream's own `build.yml` is disabled in this fork so that nothing is built from `master`.

## Staying current with upstream

A scheduled workflow (`upstream-check.yml`, daily 03:30 UTC) only **checks**: it merges
`upstream/master` into `doorbridge` in the runner, runs the test gate, and opens (or comments on) an
issue:

| Result | Issue |
|---|---|
| Upstream has new commits that merge cleanly with a green gate | "Upstream has new commits – sync ready" |
| Merge conflict | "Upstream sync blocked: merge conflict" |
| New test failures compared with unmodified upstream | "Upstream sync blocked: new test failures" |
| Already up to date | none; open issues of these titles are closed |

The workflow never pushes and holds no push credentials. Pulling upstream in happens **on request**,
locally:

1. In a clone with a remote named `upstream`, run `scripts/doorbridge/sync.sh doorbridge`.
2. On exit code 0 (merged, gate green): `git push --force origin upstream/master:master`, then
   `git push origin doorbridge`.
3. The image follows with the next 04:00 UTC run, or immediately via
   `gh workflow run release.yml`.

Exit codes of `sync.sh`: `0` merged and green, `10` already up to date, `20` merge conflict,
`30` new test failures, `2` usage or environment error. It never pushes and rolls back on any
non-zero exit.

## Release mechanics

`release.yml` builds `linux/amd64` and `linux/arm64` from `docker/Dockerfile` and pushes

- `ghcr.io/strausmann/go2rtc:doorbridge` (moving), and
- `ghcr.io/strausmann/go2rtc:doorbridge-<sha7>` (immutable, `<sha7>` is the short commit of `doorbridge`),

with provenance (`mode=max`) and an SBOM attached.

A push or merge builds nothing. Images are built only by the daily 04:00 UTC schedule or by hand
(`gh workflow run release.yml`, optional `-f dry_run=true`). If the image for the current commit
already exists, the run ends silently. The job refuses to run on any ref other than `doorbridge`.

Consumers should pin the `doorbridge-<sha7>` tag.

## Exit condition

This fork is temporary. Once the talkback source from PR #2340 (or an equivalent) is part of an
upstream go2rtc release, switch consumers to the upstream image and archive this fork.

## Security of the upstream check

The check merges and tests upstream code that has not been vetted yet. It therefore runs in a job
with no token permissions and no Go cache, and only writes plain data files (exit code, commit list,
conflicts, failures). A separate job holding `issues: write` downloads those files, validates the
exit code, and passes the contents to `gh` as issue bodies only. It checks out and executes nothing. Release builds run without any build cache for the same reason: the GitHub Actions cache is writable from any job on the same ref, so a published image must not depend on it.
