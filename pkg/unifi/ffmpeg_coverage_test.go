package unifi

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/require"
)

// protects: inputRTPMap — fails if a browser codec is announced to ffmpeg with
// the wrong payload type or rtpmap, so ffmpeg would drop or misdecode the
// microphone RTP. Opus is the normal WebRTC case; PCMU/PCMA use static payload
// types; other codecs fall back to their own payload type and clock rate.
func TestInputRTPMap(t *testing.T) {
	for name, tc := range map[string]struct {
		codec  *core.Codec
		pt     uint8
		rtpmap string
	}{
		"opus":            {codec: &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2, PayloadType: 111}, pt: 111, rtpmap: "opus/48000/2"},
		"opusDynamicPT":   {codec: &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2, PayloadType: 109}, pt: 111, rtpmap: "opus/48000/2"},
		"pcmu":            {codec: &core.Codec{Name: core.CodecPCMU, ClockRate: 8000}, pt: 0, rtpmap: "PCMU/8000"},
		"pcma":            {codec: &core.Codec{Name: core.CodecPCMA, ClockRate: 8000, PayloadType: 8}, pt: 8, rtpmap: "PCMA/8000"},
		"unknownMono":     {codec: &core.Codec{Name: core.CodecG722, ClockRate: 8000, PayloadType: 9}, pt: 9, rtpmap: "g722/8000"},
		"unknownStereo":   {codec: &core.Codec{Name: "L16", ClockRate: 16000, Channels: 2, PayloadType: 96}, pt: 96, rtpmap: "l16/16000/2"},
		"unknownDefaults": {codec: &core.Codec{}, pt: 111, rtpmap: "opus/48000"},
	} {
		t.Run(name, func(t *testing.T) {
			pt, rtpmap := inputRTPMap(tc.codec)
			require.Equal(t, tc.pt, pt)
			require.Equal(t, tc.rtpmap, rtpmap)

			sdp := buildInputSDP(tc.codec, 45000)
			require.Contains(t, sdp, "m=audio 45000 RTP/AVP "+strconv.Itoa(int(tc.pt))+"\r\n")
			require.Contains(t, sdp, "a=rtpmap:"+strconv.Itoa(int(tc.pt))+" "+tc.rtpmap+"\r\n")
		})
	}
}

// protects: udpRTPWriter.Write and dialRTPPort — fails if microphone RTP written
// by the talkback client does not arrive, byte for byte, at the local UDP port
// ffmpeg was told to listen on.
func TestUDPRTPWriterDeliversToLocalPort(t *testing.T) {
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer listener.Close()

	port := listener.LocalAddr().(*net.UDPAddr).Port
	writer, err := dialRTPPort(port)
	require.NoError(t, err)

	payload := []byte{0x80, 0x6f, 0x00, 0x01, 0xaa, 0xbb}
	require.NoError(t, writer.SetWriteDeadline(time.Now().Add(time.Second)))
	n, err := writer.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)

	buf := make([]byte, 64)
	require.NoError(t, listener.SetReadDeadline(time.Now().Add(5*time.Second)))
	n, _, err = listener.ReadFromUDP(buf)
	require.NoError(t, err)
	require.Equal(t, payload, buf[:n])

	require.NoError(t, writer.Close())
	_, err = writer.Write(payload)
	require.Error(t, err, "a closed writer must not keep its socket")
}

// protects: reserveRTPPort — fails if the reserved port is not a free local UDP
// port that ffmpeg can bind afterwards.
func TestReserveRTPPortIsBindable(t *testing.T) {
	port, err := reserveRTPPort()
	require.NoError(t, err)
	require.NotZero(t, port)

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

// protects: logFFmpegStderr — fails if ffmpeg error lines are not logged, or are
// logged with a registered talkback session URL in clear text (ffmpeg prints its
// output URL when it cannot open it).
func TestLogFFmpegStderrRedactsSecrets(t *testing.T) {
	logs := captureLog(t)
	sessionURL := "rtp://127.0.0.1:6600/talkback?token=stderr-session-token"
	registerSecretURL(sessionURL)

	logFFmpegStderr(strings.NewReader("stderr-test first line\n" + sessionURL + ": stderr-test Connection refused\n"))

	out := logs.String()
	require.Contains(t, out, `"line":"stderr-test first line","message":"[unifi] ffmpeg"`)
	require.Contains(t, out, `"line":"***: stderr-test Connection refused","message":"[unifi] ffmpeg"`)
	require.NotContains(t, out, "stderr-session-token")
}

// protects: the stderr wiring in startFFmpegRTP — fails if a command that offers
// StderrPipe (like shell.Command) no longer has its stderr forwarded to the log.
func TestStartFFmpegRTPForwardsStderr(t *testing.T) {
	logs := captureLog(t)
	cmd := newScriptedCommand("")
	cmd.stderr = "Unknown encoder 'libopus'\n"
	overrideFFmpeg(t, func(command string) ffmpegCommand {
		cmd.command = command
		return scriptedStderrCommand{cmd}
	}, func() (int, error) { return 45000, nil }, func(int) (rtpWriter, error) { return &fakeRTPWriter{}, nil })

	output, err := startFFmpegRTP(&core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2}, &TalkbackSession{
		URL:          "rtp://127.0.0.1:6500",
		Codec:        "opus",
		SamplingRate: 24000,
	})
	require.NoError(t, err)
	defer output.Close()

	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "Unknown encoder")
	}, 5*time.Second, 10*time.Millisecond)
}

// protects: error cleanup in startFFmpegRTP — fails if a failure at any step
// leaves ffmpeg running, leaves its stdin open, or is swallowed.
func TestStartFFmpegRTPFailureCleanup(t *testing.T) {
	session := &TalkbackSession{URL: "rtp://127.0.0.1:6500", Codec: "opus", SamplingRate: 24000}
	codec := &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2}
	boom := errors.New("boom")

	for name, tc := range map[string]struct {
		setup       func(cmd *scriptedCommand)
		reserveErr  error
		dialErr     error
		wantCmd     bool
		wantClosed  bool
		wantStdinCl bool
	}{
		"reservePort": {reserveErr: boom},
		"stdinPipe":   {setup: func(c *scriptedCommand) { c.stdinErr = boom }, wantCmd: true},
		"start":       {setup: func(c *scriptedCommand) { c.startErr = boom }, wantCmd: true, wantStdinCl: true},
		"writeSDP":    {setup: func(c *scriptedCommand) { c.writeErr = boom }, wantCmd: true, wantClosed: true, wantStdinCl: true},
		"dialRTP":     {dialErr: boom, wantCmd: true, wantClosed: true, wantStdinCl: true},
	} {
		t.Run(name, func(t *testing.T) {
			var cmd *scriptedCommand
			overrideFFmpeg(t, func(command string) ffmpegCommand {
				cmd = newScriptedCommand(command)
				if tc.setup != nil {
					tc.setup(cmd)
				}
				return cmd
			}, func() (int, error) {
				return 45000, tc.reserveErr
			}, func(int) (rtpWriter, error) {
				if tc.dialErr != nil {
					return nil, tc.dialErr
				}
				return &fakeRTPWriter{}, nil
			})

			output, err := startFFmpegRTP(codec, session)

			require.Nil(t, output)
			require.ErrorIs(t, err, boom)
			if !tc.wantCmd {
				require.Nil(t, cmd)
				return
			}
			require.NotNil(t, cmd)
			require.Equal(t, tc.wantClosed, cmd.closed.Load(), "ffmpeg closed")
			require.Equal(t, tc.wantStdinCl, cmd.stdinCl.Load(), "stdin closed")
		})
	}
}

// protects: ffmpegAudioCodec input normalisation — fails if the codec name the
// Protect API returns is no longer matched case- and whitespace-insensitively.
func TestBuildFFmpegCommandCodecNameNormalised(t *testing.T) {
	for _, codec := range []string{"OPUS", " opus ", "Opus"} {
		command, err := buildFFmpegCommand("rtp://10.0.0.2:4444", codec, 24000)
		require.NoError(t, err, codec)
		require.Contains(t, command, "-c:a libopus")
	}
}

// protects: ffmpeg argument order for every supported Protect codec — fails if
// the SDP input stops being read from stdin before the encoder options, or if
// the Protect session URL is no longer the final (output) argument. ffmpeg
// treats options after the last URL as trailing garbage.
func TestBuildFFmpegCommandArgumentOrder(t *testing.T) {
	for _, codec := range []string{"opus", "aac", "vorbis"} {
		t.Run(codec, func(t *testing.T) {
			const output = "rtp://10.0.0.2:4444/talkback?token=x"
			command, err := buildFFmpegCommand(output, codec, 16000)
			require.NoError(t, err)

			args := strings.Split(command, " ")
			require.Equal(t, "ffmpeg", args[0])
			require.Equal(t, output, args[len(args)-1])

			input := strings.Index(command, "-f sdp -i pipe:0")
			encoder := strings.Index(command, "-c:a ")
			format := strings.LastIndex(command, " -f ")
			require.NotEqual(t, -1, input)
			require.Less(t, input, encoder)
			require.Less(t, encoder, format)
			require.Contains(t, command, "-protocol_whitelist file,pipe,udp,rtp")
		})
	}
}
