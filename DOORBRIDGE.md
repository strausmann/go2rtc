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
| Short-lived TURN credentials for browsers | `GET /api/webrtc/ice` hands browsers ICE servers with time-limited TURN credentials, so a coturn shared secret never reaches the page |

### TURN credentials for the browser

**Purpose.** Viewers behind a restrictive network need a TURN relay. A static username and password
in `webrtc.ice_servers` would be visible to every page visitor. With coturn's `use-auth-secret`,
go2rtc derives credentials per request instead: username `<unix expiry>:doorbridge`, credential
`base64(HMAC-SHA1(secret, username))`.

**Configuration.** Opt-in, off by default (no `urls` or no `secret` means off):

```yaml
webrtc:
  turn:
    urls:
      - turn:turn.example.org:3478?transport=udp
      - turn:turn.example.org:3478?transport=tcp
      - turns:turn.example.org:443?transport=tcp
    secret: ${TURN_SECRET}   # static-auth-secret of coturn
    ttl: 1h                  # default 1h

api:
  allow_paths: [/api, /api/webrtc, /api/webrtc/ice]
```

> **Note.** `api.HandleFunc` silently does not register routes that are not in `api.allow_paths`.
> When that list is set, `/api/webrtc/ice` must be added, otherwise the route returns 404 and the
> web pages fall back to STUN only.

**Behaviour.** `GET /api/webrtc/ice` returns `{"ice_servers": [...]}`: the configured
`webrtc.ice_servers` first, then one TURN entry with fresh credentials. Other methods get `405`;
the response is `Cache-Control: no-store`; the secret is neither logged nor part of the response.
`www/webrtc.html`, `www/webrtc-sync.html` and `www/video-rtc.js` fetch the route before they create
the `RTCPeerConnection` and fall back to the built-in STUN servers on any error or after 2 s.

**Limits.**

- Anyone who can reach the route gets credentials that are valid for `ttl`. Protect the API with
  `api.username`/`password` or a reverse proxy, as for the rest of the API.
- The browser-side fetch uses the same origin as the page (`video-rtc.js`: the host of the
  WebSocket). A cross-origin embed without CORS silently falls back to STUN.
- `ttl` is capped at 24 h; larger values are clamped with a warning in the log (without the secret).
- Put the secret only as `${TURN_SECRET}` in the config and never add `/api/config` to
  `api.allow_paths`: without `allow_paths`, `GET /api/config` returns the raw config file.
- The route has no authentication of its own. Whoever reaches go2rtc directly gets TURN credentials.
  Expose `api.listen` only inside the proxy network (for example `api.listen: "172.18.0.5:1984"`
  on a private Docker network, not `:1984` on all interfaces) and do not set `api.origin`.
- All viewers share the user part `doorbridge`; abuse at the TURN server cannot be attributed to a
  person, only to a credential's expiry window.
- `video-rtc.js` and the two HTML pages replace their own `iceServers` only when the response
  contains an entry with credentials (TURN). Otherwise a host page's `pcConfig.iceServers` is kept.
- Only `stun:`/`stuns:` URLs from `webrtc.ice_servers` are served to browsers. A static `turn:`
  entry there is not forwarded (its credentials stay server-side); use `webrtc.turn` for browsers.
- The repository has no JavaScript test framework; the fetch and fallback code is covered by the Go
  tests for the route only.

**Rollback.** Remove the `webrtc.turn` section.

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
read end or after 64 consecutive errors; the latter is logged at debug level with the MID only).

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
hangs on the CI runner's Go version until the 10 min test timeout; this is tracked separately.

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
