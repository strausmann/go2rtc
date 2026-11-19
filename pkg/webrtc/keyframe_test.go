package webrtc

import (
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
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
