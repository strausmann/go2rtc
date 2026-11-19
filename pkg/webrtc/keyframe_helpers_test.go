package webrtc

import (
	"strings"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

// helpers of keyframe_test.go, upstream has them in renegotiation_test.go

func createOffer(t *testing.T, pc *webrtc.PeerConnection) string {
	offer, err := pc.CreateOffer(nil)
	require.Nil(t, err)

	gathered := webrtc.GatheringCompletePromise(pc)
	require.Nil(t, pc.SetLocalDescription(offer))
	<-gathered

	return pc.LocalDescription().SDP
}

func getCodec(media *core.Media, name string) *core.Codec {
	for _, codec := range media.Codecs {
		if strings.EqualFold(codec.Name, name) {
			return codec
		}
	}
	return nil
}

func (c *Conn) getMedia(mid string, direction string) *core.Media {
	for _, media := range c.Medias {
		if media.ID == mid && media.Direction == direction {
			return media
		}
	}
	return nil
}
