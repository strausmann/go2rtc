package unifi

import (
	"strings"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/unifi"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

func TestBackchannelCommand(t *testing.T) {
	session := &unifi.Session{URL: "rtp://192.168.1.1:7004", Codec: "opus", SamplingRate: 24000}

	cmd, err := backchannelCommand("/usr/bin/ffmpeg", session, &core.Codec{Name: core.CodecPCMA})
	require.NoError(t, err)
	require.Equal(t, "exec:/usr/bin/ffmpeg -hide_banner -v error -fflags nobuffer -f alaw -ar 8000 -ac 1 -i -"+
		" -c:a libopus -application:a lowdelay -ar:a 24000 -ac:a 1 -f rtp rtp://192.168.1.1:7004"+
		"#backchannel=1#audio=alaw/8000", cmd)

	cmd, err = backchannelCommand("ffmpeg", session, &core.Codec{Name: core.CodecPCMU})
	require.NoError(t, err)
	require.Contains(t, cmd, " -f mulaw -ar 8000 ")
	require.Contains(t, cmd, "#audio=mulaw/8000")

	session = &unifi.Session{URL: "udp://192.168.1.1:7004", Codec: "aac", SamplingRate: 16000}
	cmd, err = backchannelCommand("ffmpeg", session, &core.Codec{Name: core.CodecPCMA})
	require.NoError(t, err)
	require.Contains(t, cmd, " -c:a aac -ar:a 16000 -ac:a 1 -f adts udp://192.168.1.1:7004#")

	session = &unifi.Session{URL: "tcp://192.168.1.1:7004", Codec: "vorbis", SamplingRate: 16000}
	cmd, err = backchannelCommand("ffmpeg", session, &core.Codec{Name: core.CodecPCMA})
	require.NoError(t, err)
	require.Contains(t, cmd, " -c:a libvorbis -ar:a 16000 -ac:a 1 -f ogg tcp://192.168.1.1:7004#")
}

func TestTalkbackURL(t *testing.T) {
	for _, s := range []string{"rtp://192.168.1.1:7004", "rtp://192.168.1.1:7004/", "rtp://[fd00::1]:7004"} {
		u, err := talkbackURL(s)
		require.NoError(t, err, s)
		require.Equal(t, strings.TrimSuffix(s, "/"), u)
	}
}

func TestBackchannelCommandOpus(t *testing.T) {
	session := &unifi.Session{URL: "rtp://192.168.1.1:7004", Codec: "opus", SamplingRate: 24000}
	codec := &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2, PayloadType: 111}

	cmd, err := backchannelCommand("ffmpeg", session, codec)
	require.NoError(t, err)
	require.Equal(t, "exec:ffmpeg -hide_banner -v error -fflags nobuffer -f mp4 -i -"+
		" -c:a libopus -application:a lowdelay -ar:a 24000 -ac:a 1 -f rtp rtp://192.168.1.1:7004"+
		"#backchannel=1#audio=opus/48000/2", cmd)
}

func TestBackchannelCommandErrors(t *testing.T) {
	pcma := &core.Codec{Name: core.CodecPCMA}

	for _, test := range []struct {
		session unifi.Session
		codec   *core.Codec
		err     string
	}{
		{unifi.Session{URL: "rtp://192.168.1.1:7004", Codec: "flac"}, pcma, "unifi: unsupported talkback codec: flac"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004", Codec: "opus"}, &core.Codec{Name: core.CodecG722}, "unifi: unsupported microphone codec: G722"},
		{unifi.Session{URL: "/tmp/file.ogg", Codec: "vorbis"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "file:///tmp/file.aac", Codec: "aac"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "tcp://0.0.0.0:7004?listen=1", Codec: "aac"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "udp://192.168.1.1:7004?localaddr=192.168.1.2", Codec: "vorbis"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004 -y /tmp/file", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004#backchannel=0", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004?listen=1", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004?localport=7004&pkt_size=64", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004?", Codec: "opus"}, pcma, ""},
		{unifi.Session{URL: "rtp://user:pass@192.168.1.1:7004", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004/stream", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:7004#", Codec: "opus"}, pcma, ""},
		{unifi.Session{URL: "rtp://192.168.1.1", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:abc", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://192.168.1.1:70000", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp://:7004", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
		{unifi.Session{URL: "rtp:/tmp/file", Codec: "opus"}, pcma, "unifi: unsupported talkback url"},
	} {
		cmd, err := backchannelCommand("ffmpeg", &test.session, test.codec)
		if test.err == "" {
			// rebuilt from scheme, host and port, nothing else reaches FFmpeg
			require.NoError(t, err, test.session.URL)
			require.Contains(t, cmd, " -f rtp rtp://192.168.1.1:7004#backchannel=1#", test.session.URL)
			continue
		}
		require.EqualError(t, err, test.err, test.session.URL)
	}
}

// process - stands in for the exec backchannel, collects what goes to FFmpeg stdin
type process struct {
	core.Connection
	stdin   chan []byte
	stopped bool
}

func (p *process) AddTrack(media *core.Media, _ *core.Codec, track *core.Receiver) error {
	sender := core.NewSender(media, track.Codec)
	sender.Handler = func(packet *rtp.Packet) { p.stdin <- packet.Payload }
	sender.HandleRTP(track)
	return nil
}

func (p *process) Start() error { return nil }
func (p *process) Stop() error  { p.stopped = true; return nil }

func TestMP4Backchannel(t *testing.T) {
	proc := &process{stdin: make(chan []byte, 10)}
	b := &mp4Backchannel{Producer: proc}

	codec := &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2, PayloadType: 111}
	media := &core.Media{Kind: core.KindAudio, Direction: core.DirectionSendonly, Codecs: []*core.Codec{codec}}
	track := core.NewReceiver(media, codec)
	require.NoError(t, b.AddTrack(media, codec, track))

	track.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, Timestamp: 960}, Payload: []byte{1, 2, 3}})

	read := func() []byte {
		select {
		case data := <-proc.stdin:
			return data
		case <-time.After(time.Second):
			t.Fatal("no data for FFmpeg stdin")
			return nil
		}
	}

	init := read()
	require.Equal(t, "ftyp", string(init[4:8]))
	require.Contains(t, string(init), "Opus")

	fragment := read()
	require.Equal(t, "moof", string(fragment[4:8]))
	require.Equal(t, []byte{1, 2, 3}, fragment[len(fragment)-3:], "Opus payload in mdat")

	require.NoError(t, b.Stop())
	require.True(t, proc.stopped, "FFmpeg process stopped")
}

func TestNewBackchannel(t *testing.T) {
	var source string
	streams.HandleFunc("exec", func(s string) (core.Producer, error) {
		source = s
		return &process{}, nil
	})

	session := &unifi.Session{URL: "rtp://192.168.1.1:7004", Codec: "opus", SamplingRate: 24000}

	prod, err := newBackchannel("ffmpeg", session, &core.Codec{Name: core.CodecPCMA})
	require.NoError(t, err)
	require.IsType(t, &process{}, prod, "G.711 goes to FFmpeg stdin as is")
	require.Contains(t, source, " -f alaw ")

	prod, err = newBackchannel("ffmpeg", session, &core.Codec{Name: core.CodecOpus, ClockRate: 48000, Channels: 2})
	require.NoError(t, err)
	require.IsType(t, &mp4Backchannel{}, prod, "Opus goes to FFmpeg stdin as MP4")
	require.Contains(t, source, " -f mp4 ")
}
