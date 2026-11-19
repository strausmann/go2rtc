package webrtc

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// TestKeyframeRequest - browser asks for a keyframe with PLI and FIR
func TestKeyframeRequest(t *testing.T) {
	api, err := NewAPI()
	require.Nil(t, err)

	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	require.Nil(t, err)

	conn := NewConn(pc)
	conn.Mode = core.ModePassiveConsumer
	defer conn.Close()

	requests := make(chan struct{}, 10)
	conn.OnKeyframeRequest(func() { requests <- struct{}{} })

	browser, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	require.Nil(t, err)
	defer browser.Close()

	ssrc := make(chan webrtc.SSRC, 1)
	browser.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		ssrc <- remote.SSRC()
		for {
			if _, _, err := remote.ReadRTP(); err != nil {
				return
			}
		}
	})

	_, err = browser.AddTransceiverFromKind(
		webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
	)
	require.Nil(t, err)

	require.Nil(t, conn.SetOffer(createOffer(t, browser)))

	media := conn.getMedia("0", core.DirectionSendonly)
	require.NotNil(t, media)
	codec := getCodec(media, core.CodecH264)
	require.NotNil(t, codec)

	camera := core.NewReceiver(
		&core.Media{Kind: core.KindVideo, Direction: core.DirectionRecvonly},
		&core.Codec{Name: core.CodecH264, ClockRate: 90000, PayloadType: 96},
	)
	require.Nil(t, conn.AddTrack(media, codec, camera))

	answer, err := conn.GetCompleteAnswer(nil, nil)
	require.Nil(t, err)
	require.Nil(t, browser.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}))

	// the browser receives the track after the first packet
	var mediaSSRC webrtc.SSRC
	timeout := time.After(10 * time.Second)
	for seq := uint16(0); mediaSSRC == 0; seq++ {
		select {
		case mediaSSRC = <-ssrc:
		case <-timeout:
			t.Fatal("media timeout")
		case <-time.After(20 * time.Millisecond):
			camera.WriteRTP(&rtp.Packet{
				Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, SSRC: 1234, Marker: true},
				Payload: []byte{0x65, 0x00},
			})
		}
	}

	// RTCP is not retransmitted, so send until the handler is called
	for _, pkt := range []rtcp.Packet{
		&rtcp.PictureLossIndication{MediaSSRC: uint32(mediaSSRC)},
		&rtcp.FullIntraRequest{FIR: []rtcp.FIREntry{{SSRC: uint32(mediaSSRC)}}},
	} {
		for n := 0; ; n++ {
			require.Less(t, n, 100, "no keyframe request for %T", pkt)
			require.Nil(t, browser.WriteRTCP([]rtcp.Packet{pkt}))

			select {
			case <-requests:
			case <-time.After(50 * time.Millisecond):
				continue
			}
			break
		}
	}
}

// rtcpReads returns a reader that replays errs (nil = PLI packet) and counts
// the remaining reads
func rtcpReads(t *testing.T, errs ...error) (func([]byte) (int, interceptor.Attributes, error), *[]error) {
	pli, err := rtcp.Marshal([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 1}})
	require.Nil(t, err)

	read := func(b []byte) (int, interceptor.Attributes, error) {
		err := errs[0]
		errs = errs[1:]
		if err != nil {
			return 0, nil, err
		}
		return copy(b, pli), nil, nil
	}
	return read, &errs
}

// TestReadRTCPSkipsBrokenPackets - a failed read must not end the loop,
// a later PLI still reaches the handler
func TestReadRTCPSkipsBrokenPackets(t *testing.T) {
	read, left := rtcpReads(t, errors.New("broken packet"), io.ErrShortBuffer, nil, io.EOF)

	var calls int
	c := &Conn{}
	c.OnKeyframeRequest(func() { calls++ })
	c.readRTCP(read)
	require.Equal(t, 1, calls)
	require.Empty(t, *left)
}

// TestReadRTCPWithoutHandler - the NACK responder needs the reads, so the loop
// consumes packets until the reader is closed, also without a handler
func TestReadRTCPWithoutHandler(t *testing.T) {
	read, left := rtcpReads(t, nil, nil, errors.New("broken packet"), nil, io.ErrClosedPipe)

	c := &Conn{}
	c.readRTCP(read)
	require.Empty(t, *left)
}

// TestReadRTCPLateHandler - a handler set after the loop started gets later PLI
func TestReadRTCPLateHandler(t *testing.T) {
	pli, err := rtcp.Marshal([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: 1}})
	require.Nil(t, err)

	packets := make(chan []byte)
	read := func(b []byte) (int, interceptor.Attributes, error) {
		p, ok := <-packets
		if !ok {
			return 0, nil, io.EOF
		}
		return copy(b, p), nil, nil
	}

	c := &Conn{}
	done := make(chan struct{})
	go func() {
		c.readRTCP(read)
		close(done)
	}()

	packets <- pli // the loop is running, no handler yet

	// the first PLI may still meet the new handler, so count instead of expecting one
	var calls atomic.Int32
	c.OnKeyframeRequest(func() { calls.Add(1) })
	packets <- pli
	close(packets)
	<-done

	require.Positive(t, calls.Load(), "late handler got no keyframe request")
}

// TestReadRTCPStopsAfterTooManyErrors - a reader that only fails ends the loop
func TestReadRTCPStopsAfterTooManyErrors(t *testing.T) {
	var reads int
	read := func([]byte) (int, interceptor.Attributes, error) {
		reads++
		return 0, nil, errors.New("broken packet")
	}
	(&Conn{}).readRTCP(read)
	require.Equal(t, maxRTCPReadErrors, reads)
}
