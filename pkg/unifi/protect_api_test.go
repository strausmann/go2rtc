package unifi

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var protectErrorStatuses = []int{
	http.StatusUnauthorized,
	http.StatusNotFound,
	http.StatusInternalServerError,
}

// protects: requestJSON/getCamera status handling — fails if a non-2xx camera
// lookup is treated as success, if the status disappears from the error, or if
// the API key reaches the error text or the log.
func TestDialTalkbackCameraLookupHTTPError(t *testing.T) {
	for _, status := range protectErrorStatuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			logs := captureLog(t)
			apiKey := fmt.Sprintf("camera-error-key-%d", status)
			stub := newProtectStub(t, respond(status, `{"error":"nope"}`), respond(http.StatusOK, `{}`))

			client, err := DialTalkback(talkbackURL(stub.Server, apiKey))

			require.Nil(t, client)
			require.Error(t, err)
			require.Contains(t, err.Error(), fmt.Sprintf("%d %s", status, http.StatusText(status)))
			require.NotContains(t, err.Error(), apiKey)
			require.NotContains(t, logs.String(), apiKey)
			require.Zero(t, stub.posts.Load())
		})
	}
}

// protects: requestJSON/createTalkbackSession status handling — fails if a
// non-2xx session response starts ffmpeg, is not reported by Start(), loses the
// status, or leaks the API key into the error or the log.
func TestTalkbackSessionHTTPError(t *testing.T) {
	for _, status := range protectErrorStatuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			logs := captureLog(t)
			var commands []*scriptedCommand
			overrideFFmpeg(t, func(command string) ffmpegCommand {
				cmd := newScriptedCommand(command)
				commands = append(commands, cmd)
				return cmd
			}, nil, nil)

			apiKey := fmt.Sprintf("session-error-key-%d", status)
			stub := newProtectStub(t, cameraWithSpeaker, respond(status, `{"error":"nope"}`))

			client, err := DialTalkback(talkbackURL(stub.Server, apiKey))
			require.NoError(t, err)
			defer client.Stop()

			track, done := attachMicrophone(t, client, 0)
			writeTestRTP(track, 1)

			err = waitDone(t, done)
			require.Error(t, err)
			require.Contains(t, err.Error(), fmt.Sprintf("%d %s", status, http.StatusText(status)))
			require.Contains(t, err.Error(), "talkback-session")
			require.NotContains(t, err.Error(), apiKey)
			require.NotContains(t, logs.String(), apiKey)
			require.Equal(t, int64(1), stub.posts.Load())
			require.Empty(t, commands)
		})
	}
}

// protects: requestJSON transport error path — fails if an unreachable Protect
// host does not produce an error from DialTalkback.
func TestDialTalkbackProtectUnreachable(t *testing.T) {
	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{}`))
	rawURL := talkbackURL(stub.Server, "unreachable-key")
	stub.Close() // nothing listens on the address any more

	client, err := DialTalkback(rawURL)

	require.Nil(t, client)
	var transportErr *url.Error
	require.ErrorAs(t, err, &transportErr, "want the transport error, got %v", err)
	require.NotContains(t, err.Error(), "unreachable-key")
}

// protects: requestJSON transport error path for the session request — fails if
// a Protect host that disappears after the camera lookup leaves Start() blocked
// or starts ffmpeg.
func TestTalkbackSessionProtectUnreachable(t *testing.T) {
	var commands []*scriptedCommand
	overrideFFmpeg(t, func(command string) ffmpegCommand {
		cmd := newScriptedCommand(command)
		commands = append(commands, cmd)
		return cmd
	}, nil, nil)

	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{}`))
	client, err := DialTalkback(talkbackURL(stub.Server, "gone-key"))
	require.NoError(t, err)
	defer client.Stop()

	stub.Close()

	track, done := attachMicrophone(t, client, 0)
	writeTestRTP(track, 1)

	err = waitDone(t, done)
	// the transport error itself, not a later validation error of an empty session
	var transportErr *url.Error
	require.ErrorAs(t, err, &transportErr, "want the transport error, got %v", err)
	require.NotContains(t, err.Error(), "gone-key")
	require.Empty(t, commands)
}

// protects: request context wiring in requestJSON (http.NewRequestWithContext
// with the client context) — fails if Stop() cannot abort a session request
// that the Protect API never answers. The check is ordering-based, not a time
// threshold: Start() must return while the server handler is still blocked and
// observes the aborted request.
func TestTalkbackStopAbortsHangingSessionRequest(t *testing.T) {
	var commands []*scriptedCommand
	overrideFFmpeg(t, func(command string) ffmpegCommand {
		cmd := newScriptedCommand(command)
		commands = append(commands, cmd)
		return cmd
	}, nil, nil)

	entered := make(chan struct{})
	aborted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	stub := newProtectStub(t, cameraWithSpeaker, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(aborted)
		case <-release:
		}
	})

	client, err := DialTalkback(talkbackURL(stub.Server, "hang-key"))
	require.NoError(t, err)

	track, done := attachMicrophone(t, client, 0)
	writeTestRTP(track, 1)
	<-entered

	require.NoError(t, client.Stop())
	require.NoError(t, waitDone(t, done))

	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("session request was not aborted by Stop()")
	}
	require.Empty(t, commands)
}

// protects: JSON decoding in requestJSON for the camera lookup — fails if a
// malformed or wrongly shaped Protect response panics or is accepted.
func TestDialTalkbackMalformedCameraJSON(t *testing.T) {
	for name, body := range map[string]string{
		"truncated": `{"featureFlags":{"hasSpeaker":tr`,
		"array":     `[]`,
		"wrongType": `{"featureFlags":{"hasSpeaker":"yes"}}`,
		"empty":     ``,
	} {
		t.Run(name, func(t *testing.T) {
			stub := newProtectStub(t, respond(http.StatusOK, body), respond(http.StatusOK, `{}`))

			client, err := DialTalkback(talkbackURL(stub.Server, "bad-json-key"))

			require.Nil(t, client)
			require.Error(t, err)
		})
	}
}

// protects: defined behaviour for a camera object without featureFlags — fails
// if missing fields are treated as "has speaker" (which would advertise a
// backchannel the camera cannot play).
func TestDialTalkbackCameraWithoutFeatureFlags(t *testing.T) {
	stub := newProtectStub(t, respond(http.StatusOK, `{"id":"camera1"}`), respond(http.StatusOK, `{}`))

	client, err := DialTalkback(talkbackURL(stub.Server, "no-flags-key"))
	require.NoError(t, err)
	defer client.Stop()

	require.Empty(t, client.Medias)
}

// protects: session response validation in requestJSON/openTalkback — fails if a
// malformed or incomplete talkback-session answer panics, starts ffmpeg, or is
// not reported by Start().
func TestTalkbackSessionMalformedOrIncomplete(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"truncated":       {body: `{"url":"rtp://127.0.0.1:6500","codec":`},
		"array":           {body: `[]`},
		"null":            {body: `null`, want: "unifi: talkback samplingRate required"},
		"emptyObject":     {body: `{}`, want: "unifi: talkback samplingRate required"},
		"missingRate":     {body: `{"url":"rtp://127.0.0.1:6500","codec":"opus"}`, want: "unifi: talkback samplingRate required"},
		"missingCodec":    {body: `{"url":"rtp://127.0.0.1:6500","samplingRate":24000}`, want: "unifi: unsupported talkback codec: "},
		"rateWrongType":   {body: `{"url":"rtp://127.0.0.1:6500","codec":"opus","samplingRate":"24000"}`},
		"codecNotAString": {body: `{"url":"rtp://127.0.0.1:6500","codec":1,"samplingRate":24000}`},
	} {
		t.Run(name, func(t *testing.T) {
			var commands []*scriptedCommand
			overrideFFmpeg(t, func(command string) ffmpegCommand {
				cmd := newScriptedCommand(command)
				commands = append(commands, cmd)
				return cmd
			}, func() (int, error) { return 45000, nil }, func(int) (rtpWriter, error) { return &fakeRTPWriter{}, nil })

			stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, tc.body))
			client, err := DialTalkback(talkbackURL(stub.Server, "session-json-key"))
			require.NoError(t, err)
			defer client.Stop()

			track, done := attachMicrophone(t, client, 0)
			writeTestRTP(track, 1)

			err = waitDone(t, done)
			require.Error(t, err)
			if tc.want != "" {
				require.EqualError(t, err, tc.want)
			}
			require.Empty(t, commands)
		})
	}
}
