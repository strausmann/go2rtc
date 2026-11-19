package streams

import (
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/require"
)

func TestRecursion(t *testing.T) {
	// create stream with some source
	stream1, err := New("from_yaml", "does_not_matter")
	require.NoError(t, err)
	require.Len(t, streams, 1)

	// ask another unnamed stream that links go2rtc
	query, err := url.ParseQuery("src=rtsp://localhost:8554/from_yaml?video")
	require.NoError(t, err)
	stream2, err := GetOrPatch(query)
	require.NoError(t, err)

	// check stream is same
	require.Equal(t, stream1, stream2)
	// check stream urls is same
	require.Equal(t, stream1.producers[0].url, stream2.producers[0].url)
	require.Len(t, streams, 2)
}

func TestTempate(t *testing.T) {
	HandleFunc("rtsp", func(url string) (core.Producer, error) { return nil, nil }) // bypass HasProducer

	// config from yaml
	stream1, err := New("camera.from_hass", "ffmpeg:{input}#video=copy")
	require.NoError(t, err)
	// request from hass
	stream2, err := Patch("camera.from_hass", "rtsp://example.com")
	require.NoError(t, err)

	require.Equal(t, stream1, stream2)
	require.Equal(t, "ffmpeg:rtsp://example.com#video=copy", stream1.producers[0].url)
}

type keyframeProducer struct {
	requests atomic.Int32
	stop     chan struct{}
}

func (p *keyframeProducer) RequestKeyframe() { p.requests.Add(1) }
func (p *keyframeProducer) GetMedias() []*core.Media {
	return []*core.Media{{
		Kind: core.KindVideo, Direction: core.DirectionRecvonly, Codecs: []*core.Codec{{Name: core.CodecH264}},
	}}
}
func (p *keyframeProducer) GetTrack(media *core.Media, codec *core.Codec) (*core.Receiver, error) {
	return core.NewReceiver(media, codec), nil
}
func (p *keyframeProducer) Start() error { <-p.stop; return nil }
func (p *keyframeProducer) Stop() error  { close(p.stop); return nil }

type keyframeConsumer struct {
	request func()
}

func (c *keyframeConsumer) OnKeyframeRequest(f func()) { c.request = f }
func (c *keyframeConsumer) GetMedias() []*core.Media {
	return []*core.Media{{
		Kind: core.KindVideo, Direction: core.DirectionSendonly, Codecs: []*core.Codec{{Name: core.CodecH264}},
	}}
}
func (c *keyframeConsumer) AddTrack(*core.Media, *core.Codec, *core.Receiver) error { return nil }
func (c *keyframeConsumer) Stop() error                                             { return nil }

// the request comes from an RTCP read loop and must not wait for the stream lock
func TestRequestKeyframeLocked(t *testing.T) {
	prod := &keyframeProducer{stop: make(chan struct{})}
	stream := new(Stream)
	stream.producers = []*Producer{{conn: prod, state: stateExternal, url: "external"}}

	stream.mu.Lock() // ex. stopProducers waits for network I/O
	done := make(chan struct{})
	go func() {
		stream.RequestKeyframe()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("RequestKeyframe waits for the stream lock")
	}
	stream.mu.Unlock()
	<-done

	require.Equal(t, int32(0), prod.requests.Load())
}

// a keyframe request from the consumer (ex. WebRTC PLI) reaches the producer
func TestRequestKeyframe(t *testing.T) {
	prod := &keyframeProducer{stop: make(chan struct{})}
	stream := new(Stream)
	stream.producers = []*Producer{{conn: prod, state: stateExternal, url: "external"}}

	cons := new(keyframeConsumer)
	require.NoError(t, stream.AddConsumer(cons))
	require.NotNil(t, cons.request)
	t.Cleanup(func() { stream.RemoveConsumer(cons) })

	cons.request()
	require.Equal(t, int32(1), prod.requests.Load())
}
