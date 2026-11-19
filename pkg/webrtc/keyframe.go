package webrtc

import (
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// OnKeyframeRequest sets the handler for PLI/FIR from the remote side.
// The handler is called from the RTCP read loop and must not block.
func (c *Conn) OnKeyframeRequest(handler func()) {
	c.keyframe.Store(&handler)
}

// handleKeyframeRequest calls the handler if the RTCP read from a video sender
// holds PLI or FIR. The packets come from the attributes, because the NACK
// limiter may have rewritten the raw bytes.
func (c *Conn) handleKeyframeRequest(sender *webrtc.RTPSender, b []byte, attr interceptor.Attributes) {
	handler := c.keyframe.Load()
	if handler == nil {
		return
	}
	if track := sender.Track(); track == nil || track.Kind() != webrtc.RTPCodecTypeVideo {
		return
	}

	var pkts []rtcp.Packet
	var err error
	if attr != nil {
		pkts, err = attr.GetRTCPPackets(b)
	} else {
		pkts, err = rtcp.Unmarshal(b)
	}
	if err != nil {
		return // ignore a broken packet and wait for the next one
	}

	for _, pkt := range pkts {
		switch pkt.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			(*handler)()
			return
		}
	}
}
