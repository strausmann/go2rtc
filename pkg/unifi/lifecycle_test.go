package unifi

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/creds"
	"github.com/stretchr/testify/require"
)

// protects: URL validation in DialTalkback — fails if a source without the
// scheme, camera_id or api_key is accepted and reaches the network.
func TestDialTalkbackRejectsInvalidSource(t *testing.T) {
	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{}`))

	for name, tc := range map[string]struct {
		url  string
		want string
	}{
		"wrongScheme":   {url: "rtsp://192.168.1.1/stream", want: "unifi: unsupported scheme"},
		"noCameraID":    {url: SchemeTalkback + ":" + stub.URL + "?api_key=k", want: "unifi: camera_id required"},
		"noAPIKey":      {url: SchemeTalkback + ":" + stub.URL + "?camera_id=camera1", want: "unifi: api_key required"},
		"unparsableURL": {url: SchemeTalkback + ":http://[::1", want: "missing ']'"},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := DialTalkback(tc.url)
			require.Nil(t, client)
			require.ErrorContains(t, err, tc.want)
		})
	}
	require.Empty(t, stub.Requests())
}

// protects: RegisterTalkbackSecrets scope — fails if foreign sources or
// unparsable talkback URLs panic or register unrelated values as secrets.
func TestRegisterTalkbackSecretsIgnoresForeignSources(t *testing.T) {
	const foreign = "rtsp://192.168.1.1/stream?api_key=not-a-talkback-key"
	require.NotPanics(t, func() {
		RegisterTalkbackSecrets(foreign)
		RegisterTalkbackSecrets(SchemeTalkback + ":http://[::1")
	})
	require.Contains(t, creds.SecretString(foreign), "not-a-talkback-key")
}

// protects: the producer contract of the talkback client — fails if it starts
// offering a receive track (it is a pure backchannel sink).
func TestTalkbackGetTrackIsUnsupported(t *testing.T) {
	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{}`))
	client, err := DialTalkback(talkbackURL(stub.Server, "get-track-key"))
	require.NoError(t, err)
	defer client.Stop()

	track, err := client.GetTrack(client.Medias[0], client.Medias[0].Codecs[0])
	require.Nil(t, track)
	require.ErrorIs(t, err, core.ErrCantGetTrack)
}

// protects: Stop before Start — fails if Start() blocks after an earlier Stop(),
// or if microphone RTP arriving after Stop() still opens a Protect session.
func TestTalkbackStopBeforeStart(t *testing.T) {
	var commands []*scriptedCommand
	overrideFFmpeg(t, func(command string) ffmpegCommand {
		cmd := newScriptedCommand(command)
		commands = append(commands, cmd)
		return cmd
	}, nil, nil)

	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{"url":"rtp://127.0.0.1:6500","codec":"opus","samplingRate":24000}`))
	client, err := DialTalkback(talkbackURL(stub.Server, "stop-first-key"))
	require.NoError(t, err)

	require.NoError(t, client.Stop())

	track, done := attachMicrophone(t, client, 0)
	writeTestRTP(track, 1)
	require.NoError(t, waitDone(t, done))

	_, err = client.ensureActive(track.Codec)
	require.ErrorIs(t, err, errStopped)
	require.Zero(t, stub.posts.Load())
	require.Empty(t, commands)
}

// protects: idempotent Stop — fails if a second Stop() panics (double close of
// done/ffmpeg) or closes ffmpeg/RTP resources more than once with an error.
func TestTalkbackDoubleStop(t *testing.T) {
	restore, ffmpeg := fakeFFmpeg(t)
	defer restore()

	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{"url":"rtp://127.0.0.1:6500","codec":"opus","samplingRate":24000}`))
	client, err := DialTalkback(talkbackURL(stub.Server, "double-stop-key"))
	require.NoError(t, err)

	track, done := attachMicrophone(t, client, 0)
	writeTestRTP(track, 1)
	require.Eventually(t, func() bool {
		return ffmpeg.writer.writeCount.Load() == 1
	}, 5*time.Second, 10*time.Millisecond)

	require.NotPanics(t, func() {
		require.NoError(t, client.Stop())
		require.NoError(t, client.Stop())
	})
	require.NoError(t, waitDone(t, done))
	require.True(t, ffmpeg.Commands()[0].closed.Load())
	require.True(t, ffmpeg.writer.closed.Load())
	require.Equal(t, int64(1), stub.posts.Load())
}

// protects: waitOutput — fails if an ffmpeg process that exits on its own (for
// example because the camera rejected the stream) is not reported through
// Start(), or leaves the RTP socket open.
func TestTalkbackFFmpegExitEndsSession(t *testing.T) {
	exitErr := errors.New("exit status 1")
	var cmd *scriptedCommand
	writer := &fakeRTPWriter{}
	overrideFFmpeg(t, func(command string) ffmpegCommand {
		cmd = newScriptedCommand(command)
		cmd.exitNow = true
		cmd.waitErr = exitErr
		return cmd
	}, func() (int, error) { return 45000, nil }, func(int) (rtpWriter, error) { return writer, nil })

	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{"url":"rtp://127.0.0.1:6500","codec":"opus","samplingRate":24000}`))
	client, err := DialTalkback(talkbackURL(stub.Server, "ffmpeg-exit-key"))
	require.NoError(t, err)
	defer client.Stop()

	track, done := attachMicrophone(t, client, 0)
	writeTestRTP(track, 1)

	require.ErrorIs(t, waitDone(t, done), exitErr)
	require.True(t, cmd.closed.Load())
	require.True(t, writer.closed.Load())
}

// protects: the RTP write error path in AddTrack's sender handler — fails if a
// failing socket towards ffmpeg is ignored instead of ending the session.
func TestTalkbackRTPWriteErrorEndsSession(t *testing.T) {
	writer := &erroringRTPWriter{}
	overrideFFmpeg(t, func(command string) ffmpegCommand {
		return newScriptedCommand(command)
	}, func() (int, error) { return 45000, nil }, func(int) (rtpWriter, error) { return writer, nil })

	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{"url":"rtp://127.0.0.1:6500","codec":"opus","samplingRate":24000}`))
	client, err := DialTalkback(talkbackURL(stub.Server, "write-error-key"))
	require.NoError(t, err)

	track, done := attachMicrophone(t, client, 0)
	writeTestRTP(track, 1)

	err = waitDone(t, done)
	require.ErrorContains(t, err, "connection refused")

	require.NoError(t, client.Stop())
	require.True(t, writer.closed.Load())
}

// protects: codec hand-over from the browser track to ffmpeg — fails if the
// codec negotiated with the browser (Opus, PCMU or PCMA) is not the one
// described in the SDP that ffmpeg reads.
func TestTalkbackBrowserCodecReachesFFmpegSDP(t *testing.T) {
	for i, want := range []struct {
		name string
		m    string
		a    string
	}{
		{name: core.CodecOpus, m: "m=audio 45000 RTP/AVP 111", a: "a=rtpmap:111 opus/48000/2"},
		{name: core.CodecPCMU, m: "m=audio 45000 RTP/AVP 0", a: "a=rtpmap:0 PCMU/8000"},
		{name: core.CodecPCMA, m: "m=audio 45000 RTP/AVP 8", a: "a=rtpmap:8 PCMA/8000"},
	} {
		t.Run(want.name, func(t *testing.T) {
			restore, ffmpeg := fakeFFmpeg(t)
			defer restore()

			stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{"url":"rtp://127.0.0.1:6500","codec":"opus","samplingRate":24000}`))
			client, err := DialTalkback(talkbackURL(stub.Server, "codec-"+strings.ToLower(want.name)+"-key"))
			require.NoError(t, err)

			track, done := attachMicrophone(t, client, i)
			writeTestRTP(track, 1)
			require.Eventually(t, func() bool {
				return ffmpeg.writer.writeCount.Load() == 1
			}, 5*time.Second, 10*time.Millisecond)

			sdp := ffmpeg.Commands()[0].stdin.String()
			require.Contains(t, sdp, want.m)
			require.Contains(t, sdp, want.a)

			require.NoError(t, client.Stop())
			require.NoError(t, waitDone(t, done))
		})
	}
}
