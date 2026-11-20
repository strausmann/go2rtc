package unifi

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/require"
)

func TestDialInvalidSource(t *testing.T) {
	_, err := Dial("unifi:https://192.168.1.1?api_key=test-api-key")
	require.EqualError(t, err, "unifi: camera_id and api_key required")

	_, err = Dial("unifi:%zz")
	require.Error(t, err)
}

func TestDialCameraResponse(t *testing.T) {
	source := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"featureFlags":{}}`))
	})
	client, err := Dial(source)
	require.NoError(t, err)
	require.Empty(t, client.GetMedias(), "camera without speaker has no backchannel")

	source = newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	_, err = Dial(source)
	require.ErrorContains(t, err, "invalid character")
}

func TestGetTrack(t *testing.T) {
	p := newProtect(t)
	client, err := Dial(p.source())
	require.NoError(t, err)

	_, err = client.GetTrack(client.Medias[0], client.Medias[0].Codecs[0])
	require.ErrorIs(t, err, core.ErrCantGetTrack)
}

func TestTalkbackSessionHTTPError(t *testing.T) {
	p := newProtect(t)
	p.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			http.Error(w, "", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"featureFlags":{"hasSpeaker":true}}`))
	})
	_, bc, track, done := dial(t, p)

	writeRTP(track)
	require.EqualError(t, recv(t, done), "unifi: POST "+cameraPath+"/talkback-session: 500 Internal Server Error")
	require.Nil(t, bc.session)
}

func TestTalkbackSessionUnreachable(t *testing.T) {
	p := newProtect(t)
	_, bc, track, done := dial(t, p)

	p.Close()
	writeRTP(track)
	require.ErrorContains(t, recv(t, done), "connection refused")
	require.Nil(t, bc.session)
}

func TestStopAbortsHangingSession(t *testing.T) {
	hang := make(chan struct{})
	p := newProtect(t)
	p.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			<-hang
			return
		}
		_, _ = w.Write([]byte(`{"featureFlags":{"hasSpeaker":true}}`))
	})
	defer close(hang)

	client, _, track, done := dial(t, p)
	writeRTP(track)
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, client.Stop())
	require.ErrorContains(t, recv(t, done), "context canceled")
}

func TestStopBeforeStart(t *testing.T) {
	p := newProtect(t)
	client, err := Dial(p.source())
	require.NoError(t, err)

	require.NoError(t, client.Stop())
	require.NoError(t, client.Start())
	require.NoError(t, client.Stop(), "second Stop")
}

func TestBackchannelErrors(t *testing.T) {
	errBackchannel := errors.New("backchannel error")

	for _, test := range []struct {
		name    string
		prod    func(bc *backchannel) (core.Producer, error)
		err     string
		stopped bool
	}{
		{"factory", func(*backchannel) (core.Producer, error) { return nil, errBackchannel }, "backchannel error", false},
		{"not consumer", func(bc *backchannel) (core.Producer, error) { return producerOnly{bc}, nil }, "unifi: backchannel is not a consumer", true},
		{"add track", func(bc *backchannel) (core.Producer, error) { return &failingTrack{bc}, nil }, "add track error", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := newProtect(t)
			p.session = Session{URL: "rtp://127.0.0.1:6500", Codec: "opus", SamplingRate: 24000}
			client, bc, track, done := dial(t, p)
			client.Backchannel = func(*Session, *core.Codec) (core.Producer, error) { return test.prod(bc) }

			writeRTP(track)
			require.EqualError(t, recv(t, done), test.err)
			select {
			case <-bc.stopped:
				require.True(t, test.stopped)
			default:
				require.False(t, test.stopped)
			}
		})
	}
}

func TestBackchannelEndsTalkback(t *testing.T) {
	p := newProtect(t)
	p.session = Session{URL: "rtp://127.0.0.1:6500", Codec: "opus", SamplingRate: 24000}
	_, bc, track, done := dial(t, p)

	writeRTP(track)
	recv(t, bc.started)
	close(bc.stopped) // ffmpeg exits

	require.NoError(t, recv(t, done))
}

func TestStopDuringBackchannelStart(t *testing.T) {
	p := newProtect(t)
	p.session = Session{URL: "rtp://127.0.0.1:6500", Codec: "opus", SamplingRate: 24000}
	client, bc, track, done := dial(t, p)

	factory := client.Backchannel
	client.Backchannel = func(session *Session, codec *core.Codec) (core.Producer, error) {
		_ = client.Stop() // the viewer leaves while the session is opened
		return factory(session, codec)
	}

	writeRTP(track)
	require.NoError(t, recv(t, done))
	recv(t, bc.stopped)
}

type producerOnly struct{ bc *backchannel }

func (p producerOnly) GetMedias() []*core.Media                                  { return nil }
func (p producerOnly) GetTrack(*core.Media, *core.Codec) (*core.Receiver, error) { return nil, nil }
func (p producerOnly) Start() error                                              { return nil }
func (p producerOnly) Stop() error                                               { return p.bc.Stop() }

type failingTrack struct{ *backchannel }

func (f *failingTrack) AddTrack(*core.Media, *core.Codec, *core.Receiver) error {
	return errors.New("add track error")
}
