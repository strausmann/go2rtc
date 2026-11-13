# Doorbridge fork

This fork of [AlexxIT/go2rtc](https://github.com/AlexxIT/go2rtc) exists to carry changes that are not
yet available in an upstream release: the UniFi Protect talkback source from upstream pull request
[AlexxIT/go2rtc#2340](https://github.com/AlexxIT/go2rtc/pull/2340) (Opus talkback to UniFi Protect
cameras and doorbells), another upstream pull request, and a few additions of its own (see
[Fork additions](#fork-additions)).

## Carried upstream pull requests

| Upstream PR | Purpose |
|---|---|
| [AlexxIT/go2rtc#2340](https://github.com/AlexxIT/go2rtc/pull/2340) | UniFi Protect Opus talkback source |
| [AlexxIT/go2rtc#2486](https://github.com/AlexxIT/go2rtc/pull/2486) | Read RTCP feedback from WebRTC senders so pion's NACK responder can retransmit lost packets |

## Fork additions

Changes that exist only in this fork.

| Addition | Purpose |
|---|---|
| NACK limiter ([fork PR #4](https://github.com/strausmann/go2rtc/pull/4)) | Bounds NACK retransmission requests per WebRTC sender |
| Keyframe on viewer request ([fork issue #5](https://github.com/strausmann/go2rtc/issues/5)) | Gets a fresh keyframe from a UniFi Protect camera when a WebRTC viewer asks for one |

### Keyframe on viewer request

**Purpose.** UniFi Protect ignores PLI and FIR and emits a keyframe only every GOP (about 5 s). A
viewer that joins or loses a frame therefore waits for the next GOP. Protect does emit a keyframe
when a new RTSP session sets up a video track, so go2rtc triggers that on demand.

**Configuration.** Opt-in per source, off by default:

```yaml
streams:
  doorbell: rtspx://protect-host:7441/camera-token#keyframe=resetup
```

Any other value logs a warning and leaves the feature off; the main stream is unaffected. Only
`rtsp://`, `rtsps://` and `rtspx://` sources support it; other producers ignore the request. Sources
with a WebSocket transport (`#transport=ws...`) are refused as well: that dial has no timeout.

**Behaviour.** The WebRTC sender watches RTCP feedback for PLI and FIR. A request for a video track
is forwarded to the producer's current connection, which opens a short helper session to the same
URL: OPTIONS, DESCRIBE, SETUP of the first video media, TEARDOWN. There is no PLAY, no redirect
following, and the helper always uses TCP-interleaved transport, even with `#transport=udp`.

- At most one helper session per second per source connection, shared by all viewers; requests
  inside the window are dropped, not queued.
- Never two helper sessions at the same time on one connection. After the source reconnected, the
  new connection starts with fresh limits. Stopping the old connection aborts its helper session in
  every phase, including a pending TCP connect or TLS handshake, so the two overlap at most for the
  moment the aborted helper needs to return.
- The helper session has the same dial timeout as the main source. Its RTSP exchange after the dial
  (OPTIONS to SETUP) is limited to 2 s.
- Failures never affect the main stream. They are logged at debug level only, as stage and error
  class, without URL, host or token.

The same change also bounds the TLS handshake by the dial timeout, which hardens the main
connection too, and keeps the RTCP read loop alive across a single unparsable packet (it ends on
read end or after 64 consecutive errors).

**Limits.**

- This relies on undocumented Protect behaviour, measured on a doorbell: an extra SETUP makes the
  shared encoder emit a keyframe within roughly 0.03 to 0.2 s. Because the encoder is shared, the
  keyframe reaches all sessions and presumably the recording too. A firmware update may make the
  trick ineffective; that is harmless, viewers then wait for the next GOP as before.
- A viewer that keeps sending PLI raises the keyframe rate of the shared encoder to at most one per
  second (instead of one per GOP), including the Protect recording and all other viewers.
- Tracks renegotiated after the first connect get no keyframe forwarding (pre-existing behaviour
  of the RTCP reader, which attaches once per connection).

**Rollback.** Remove `#keyframe=resetup` from the source URL.

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

## Pull request tests

`doorbridge-test.yml` runs the WebRTC and RTSP tests (`pkg/webrtc`, `internal/webrtc`, `pkg/rtsp`,
`internal/rtsp`), the TLS dial tests (`pkg/tcp`) and the keyframe forwarding tests (`-run Keyframe`
in `internal/streams`) on pull requests against `doorbridge` and on pushes to it: on `amd64` with the
race detector, and on `386` for the 32-bit platforms upstream builds.

The package selections skip pre-existing failures so that the gate only turns red for real defects:
the `pkg/core` tests, two tests in `internal/streams`, and `TestURLParse` in `pkg/rtsp`. That test
recurses endlessly in `urlParse` on Go up to 1.26, which is what `go.mod` selects in CI, until the
10 min test timeout; the image is built with a newer Go and is not affected.

The workflow builds no images, pushes nothing and uses no Go cache.

## Exit condition

This fork is temporary. Switch consumers to the upstream image and archive this fork only when the
talkback source from PR #2340 (or an equivalent) is part of an upstream go2rtc release **and** the
fork additions are either upstream as well or no longer needed.

## Security of the upstream check

The check merges and tests upstream code that has not been vetted yet. It therefore runs in a job
with no token permissions and no Go cache, and only writes plain data files (exit code, commit list,
conflicts, failures). A separate job holding `issues: write` downloads those files, validates the
exit code, and passes the contents to `gh` as issue bodies only. It checks out and executes nothing. Release builds run without any build cache for the same reason: the GitHub Actions cache is writable from any job on the same ref, so a published image must not depend on it.
