package unifi

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/creds"
	"github.com/stretchr/testify/require"
)

// protects: core.Producer and core.Consumer — fails to compile if either
// interface changes in a way the talkback client no longer satisfies. streams
// registers the client as a Producer and drives the backchannel through the
// Consumer side (internal/streams/producer.go: p.conn.(core.Consumer)).
var (
	_ core.Producer = (*Client)(nil)
	_ core.Consumer = (*Client)(nil)
)

// protects: the backchannel shape of the advertised media — fails if the
// talkback media stops being a sendonly audio media, because only sendonly
// producer media is routed to consumer microphones in
// internal/streams/add_consumer.go.
func TestTalkbackMediaIsBackchannelForStreams(t *testing.T) {
	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{}`))
	client, err := DialTalkback(talkbackURL(stub.Server, "media-shape-key"))
	require.NoError(t, err)
	defer client.Stop()

	browser := &core.Media{
		Kind:      core.KindAudio,
		Direction: core.DirectionRecvonly,
		Codecs:    []*core.Codec{{Name: core.CodecOpus, ClockRate: 48000, Channels: 2, PayloadType: 111}},
	}
	prodCodec, consCodec := client.Medias[0].MatchMedia(browser)
	require.NotNil(t, prodCodec)
	require.Equal(t, core.CodecOpus, prodCodec.Name)
	require.Equal(t, core.CodecOpus, consCodec.Name)
}

// protects: creds masking of Protect session data — fails if, after a real
// session was opened, the session URL or its token can be read from creds
// output in any of the encodings that appear in logs and API JSON (plain,
// JSON-escaped slashes, &), or from the captured log.
func TestTalkbackSessionURLMaskedEverywhere(t *testing.T) {
	logs := captureLog(t)
	restore, ffmpeg := fakeFFmpeg(t)
	defer restore()

	const token = "contract-session-token-7f3a"
	sessionURL := "rtp://127.0.0.1:6510/talkback?token=" + token + "&ssrc=1"
	body, err := json.Marshal(TalkbackSession{URL: sessionURL, Codec: "opus", SamplingRate: 24000})
	require.NoError(t, err)

	const apiKey = "contract-api-key-5c1e"
	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, string(body)))
	client, err := DialTalkback(talkbackURL(stub.Server, apiKey))
	require.NoError(t, err)

	// marshal before audio flows: Connection counters are written by the
	// sender goroutine without synchronisation
	clientJSON, err := json.Marshal(client)
	require.NoError(t, err)
	require.NotContains(t, string(clientJSON), apiKey)

	track, done := attachMicrophone(t, client, 0)
	writeTestRTP(track, 1)
	require.Eventually(t, func() bool {
		return ffmpeg.writer.writeCount.Load() == 1
	}, 5*time.Second, 10*time.Millisecond)

	for _, encoded := range []string{
		sessionURL,
		strings.ReplaceAll(sessionURL, "/", `\/`),
		strings.ReplaceAll(sessionURL, "&", `&`),
		strings.ReplaceAll(strings.ReplaceAll(sessionURL, "&", `&`), "/", `\/`),
	} {
		require.NotContains(t, creds.SecretString(`{"url":"`+encoded+`"}`), token, encoded)
	}
	require.NotContains(t, creds.SecretString(ffmpeg.Commands()[0].command), token)

	require.NoError(t, client.Stop())
	require.NoError(t, waitDone(t, done))

	out := logs.String()
	require.NotContains(t, out, token)
	require.NotContains(t, out, apiKey)
}

// protects: masking in internal/streams/producer.go (added by #2340) — fails if
// the stream JSON of a not yet dialled talkback producer shows the api_key.
func TestStreamsJSONMasksTalkbackAPIKey(t *testing.T) {
	streams.HandleFunc(SchemeTalkback, func(source string) (core.Producer, error) {
		return DialTalkback(source)
	})

	const apiKey = "streams-json-api-key-91b2"
	source := SchemeTalkback + ":https://192.168.1.1?camera_id=camera1&api_key=" + apiKey
	RegisterTalkbackSecrets(source)

	stream := streams.NewStream(source)
	b, err := json.Marshal(stream)
	require.NoError(t, err)
	require.NotContains(t, string(b), apiKey)
	require.Contains(t, string(b), "api_key=***")
}

// backchannelConsumer stands in for a WebRTC viewer with a microphone: a
// consumer that offers a recvonly audio media and hands out a Receiver for it.
type backchannelConsumer struct {
	core.Connection

	once  sync.Once
	track *core.Receiver
}

func newBackchannelConsumer(codecs ...*core.Codec) *backchannelConsumer {
	return &backchannelConsumer{Connection: core.Connection{
		ID:         core.NewID(),
		FormatName: "test-webrtc",
		Medias: []*core.Media{{
			Kind:      core.KindAudio,
			Direction: core.DirectionRecvonly,
			Codecs:    codecs,
		}},
	}}
}

func (c *backchannelConsumer) GetTrack(media *core.Media, codec *core.Codec) (*core.Receiver, error) {
	c.once.Do(func() {
		c.track = core.NewReceiver(media, codec)
		c.Receivers = append(c.Receivers, c.track)
	})
	return c.track, nil
}

func (c *backchannelConsumer) AddTrack(*core.Media, *core.Codec, *core.Receiver) error {
	return nil
}

func (c *backchannelConsumer) Start() error { return nil }

// fakeCameraSide records what the talkback client sends to "ffmpeg": the SDP on
// stdin and the RTP datagrams on the reserved UDP port. Only the ffmpeg process
// is faked; port reservation, the UDP socket and the RTP path are real.
type fakeCameraSide struct {
	mu       sync.Mutex
	commands []*scriptedCommand
	listener *net.UDPConn
	writer   rtpWriter
}

func installFakeCameraSide(t *testing.T) *fakeCameraSide {
	t.Helper()

	side := &fakeCameraSide{}
	realReserve, realDial := reserveRTPPort, dialRTPPort

	overrideFFmpeg(t, func(command string) ffmpegCommand {
		cmd := newScriptedCommand(command)
		side.mu.Lock()
		side.commands = append(side.commands, cmd)
		side.mu.Unlock()
		return cmd
	}, func() (int, error) {
		port, err := realReserve()
		if err != nil {
			return 0, err
		}
		// listen where ffmpeg would, before the client starts sending
		listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			return 0, err
		}
		side.mu.Lock()
		side.listener = listener
		side.mu.Unlock()
		t.Cleanup(func() { _ = listener.Close() })
		return port, nil
	}, func(port int) (rtpWriter, error) {
		writer, err := realDial(port)
		side.mu.Lock()
		side.writer = writer
		side.mu.Unlock()
		return writer, err
	})

	return side
}

func (s *fakeCameraSide) Commands() []*scriptedCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*scriptedCommand(nil), s.commands...)
}

func (s *fakeCameraSide) read(t *testing.T) []byte {
	t.Helper()
	var listener *net.UDPConn
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		listener = s.listener
		return listener != nil
	}, 5*time.Second, 10*time.Millisecond, "no RTP port was reserved")

	buf := make([]byte, 1500)
	require.NoError(t, listener.SetReadDeadline(time.Now().Add(5*time.Second)))
	n, _, err := listener.ReadFromUDP(buf)
	require.NoError(t, err)
	return buf[:n]
}

// protects: the end-to-end backchannel path through internal/streams —
// registration of the scheme, add_consumer.go routing a consumer microphone to
// the sendonly producer media, producer.go calling the client's AddTrack, the
// first RTP packet opening exactly one Protect session, and RTP reaching the
// local ffmpeg port. Removing the consumer must stop the client: ffmpeg closed,
// the RTP socket closed and no further Protect requests. Fails if any link in
// that chain changes.
func TestStreamsBackchannelEndToEnd(t *testing.T) {
	side := installFakeCameraSide(t)
	stub := newProtectStub(t, cameraWithSpeaker, respond(http.StatusOK, `{"url":"rtp://127.0.0.1:6520","codec":"opus","samplingRate":24000}`))

	streams.HandleFunc(SchemeTalkback, func(source string) (core.Producer, error) {
		return DialTalkback(source)
	})
	stream := streams.NewStream(talkbackURL(stub.Server, "e2e-api-key"))

	cons := newBackchannelConsumer(&core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2, PayloadType: 111})
	require.NoError(t, stream.AddConsumer(cons))
	require.NotNil(t, cons.track, "streams did not request the microphone track")
	require.Len(t, cons.track.Senders(), 1, "streams did not attach the talkback sender")

	require.Zero(t, stub.posts.Load(), "a session must not be opened before audio flows")

	writeTestRTP(cons.track, 1)
	packet := side.read(t)
	require.Equal(t, int64(1), stub.posts.Load())

	// the RTP socket is dialled after ffmpeg got its SDP; waiting for it under
	// the lock orders the stdin writes before the reads below
	require.Eventually(t, func() bool {
		side.mu.Lock()
		defer side.mu.Unlock()
		return side.writer != nil
	}, 5*time.Second, 10*time.Millisecond)
	commands := side.Commands()
	require.Len(t, commands, 1)
	sdp := commands[0].stdin.String()
	require.Contains(t, sdp, "a=rtpmap:111 opus/48000/2")
	side.mu.Lock()
	port := side.listener.LocalAddr().(*net.UDPAddr).Port
	side.mu.Unlock()
	require.Contains(t, sdp, "m=audio "+strconv.Itoa(port)+" RTP/AVP 111")
	require.Equal(t, []byte{0x01, 0x02, 0x03}, packet[len(packet)-3:])

	writeTestRTP(cons.track, 2)
	_ = side.read(t)
	require.Equal(t, int64(1), stub.posts.Load(), "one session per talkback, not per packet")

	stream.RemoveConsumer(cons)

	require.Eventually(t, commands[0].closed.Load, 5*time.Second, 10*time.Millisecond, "ffmpeg not stopped")
	side.mu.Lock()
	writer := side.writer
	side.mu.Unlock()
	_, err := writer.Write([]byte{0})
	require.Error(t, err, "RTP socket still open after the consumer left")

	require.Equal(t, []string{
		"GET /proxy/protect/integration/v1/cameras/camera1",
		"POST /proxy/protect/integration/v1/cameras/camera1/talkback-session",
	}, stub.Requests())
}

// protects: streams matching for a camera without speaker — fails if a viewer
// microphone is attached to a camera that has no speaker, or if that attempt
// opens a Protect session.
func TestStreamsBackchannelCameraWithoutSpeaker(t *testing.T) {
	side := installFakeCameraSide(t)
	stub := newProtectStub(t, respond(http.StatusOK, `{"featureFlags":{"hasSpeaker":false}}`), respond(http.StatusOK, `{}`))

	streams.HandleFunc(SchemeTalkback, func(source string) (core.Producer, error) {
		return DialTalkback(source)
	})
	stream := streams.NewStream(talkbackURL(stub.Server, "no-speaker-e2e-key"))

	cons := newBackchannelConsumer(&core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2, PayloadType: 111})
	require.Error(t, stream.AddConsumer(cons))
	require.Nil(t, cons.track)
	require.Zero(t, stub.posts.Load())
	require.Empty(t, side.Commands())
}
