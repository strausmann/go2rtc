package unifi

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/creds"
	"github.com/AlexxIT/go2rtc/pkg/mp4"
	"github.com/AlexxIT/go2rtc/pkg/unifi"
	"github.com/pion/rtp"
)

func Init() {
	var cfg struct {
		FFmpeg struct {
			Bin string `yaml:"bin"`
		} `yaml:"ffmpeg"`
	}

	cfg.FFmpeg.Bin = "ffmpeg"

	app.LoadConfig(&cfg)

	log := app.GetLogger("unifi")

	streams.HandleFunc("unifi", func(source string) (core.Producer, error) {
		client, err := unifi.Dial(source)
		if err != nil {
			return nil, err
		}

		client.Backchannel = func(session *unifi.Session, codec *core.Codec) (core.Producer, error) {
			log.Debug().Str("codec", session.Codec).Int("rate", session.SamplingRate).Msg("[unifi] talkback session")
			return newBackchannel(cfg.FFmpeg.Bin, session, codec)
		}

		return client, nil
	})

	// hide api_key of configured sources before they are dialed
	for _, sources := range streams.GetAllSources() {
		for _, source := range sources {
			if s, ok := strings.CutPrefix(source, "unifi:"); ok {
				if u, err := url.Parse(s); err == nil {
					creds.AddSecret(u.Query().Get("api_key"))
				}
			}
		}
	}
}

// newBackchannel - FFmpeg via exec module, so `exec.allow_paths` applies and FFmpeg stops with the producer.
// G.711 goes to FFmpeg stdin as is, Opus as fragmented MP4.
func newBackchannel(bin string, session *unifi.Session, codec *core.Codec) (core.Producer, error) {
	cmd, err := backchannelCommand(bin, session, codec)
	if err != nil {
		return nil, err
	}

	prod, err := streams.GetProducer(cmd)
	if err != nil || codec.Name != core.CodecOpus {
		return prod, err
	}

	return &mp4Backchannel{Producer: prod}, nil
}

func backchannelCommand(bin string, session *unifi.Session, codec *core.Codec) (string, error) {
	output, err := talkbackURL(session.URL)
	if err != nil {
		return "", err
	}

	var encoder, format string
	switch strings.ToLower(session.Codec) {
	case "opus":
		encoder, format = "libopus -application:a lowdelay", "rtp"
	case "aac":
		encoder, format = "aac", "adts"
	case "vorbis":
		encoder, format = "libvorbis", "ogg"
	default:
		return "", errors.New("unifi: unsupported talkback codec: " + session.Codec)
	}

	var input, audio string
	switch codec.Name {
	case core.CodecPCMA:
		input, audio = "-f alaw -ar 8000 -ac 1 -i -", "alaw/8000"
	case core.CodecPCMU:
		input, audio = "-f mulaw -ar 8000 -ac 1 -i -", "mulaw/8000"
	case core.CodecOpus:
		input, audio = "-f mp4 -i -", "opus/48000/2"
	default:
		return "", errors.New("unifi: unsupported microphone codec: " + codec.Name)
	}

	rate := strconv.Itoa(session.SamplingRate)

	return "exec:" + bin + " -hide_banner -v error -fflags nobuffer " + input +
		" -c:a " + encoder + " -ar:a " + rate + " -ac:a 1 -f " + format + " " + output +
		"#backchannel=1#audio=" + audio, nil
}

// talkbackURL - the URL becomes part of the FFmpeg command line, query params there are protocol options,
// so the URL is rebuilt from scheme, host and port only
func talkbackURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "rtp" && u.Scheme != "udp" && u.Scheme != "tcp") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", errors.New("unifi: unsupported talkback url")
	}

	host := u.Hostname()
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 || host == "" || strings.ContainsAny(host, " \"'#{}?&") {
		return "", errors.New("unifi: unsupported talkback url")
	}

	return u.Scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// mp4Backchannel - muxes microphone Opus to MP4, the exec producer writes it to FFmpeg stdin
type mp4Backchannel struct {
	core.Producer
	sender *core.Sender
}

func (b *mp4Backchannel) AddTrack(media *core.Media, codec *core.Codec, track *core.Receiver) error {
	muxer := &mp4.Muxer{}
	muxer.AddTrack(track.Codec)

	init, err := muxer.GetInit()
	if err != nil {
		return err
	}

	// the exec producer writes the payload of each packet to FFmpeg stdin
	stdin := core.NewReceiver(media, track.Codec)
	cons, ok := b.Producer.(core.Consumer)
	if !ok {
		return errors.New("unifi: backchannel is not a consumer")
	}
	if err = cons.AddTrack(media, codec, stdin); err != nil {
		return err
	}
	stdin.WriteRTP(&rtp.Packet{Payload: init})

	b.sender = core.NewSender(media, track.Codec)
	b.sender.Handler = func(packet *rtp.Packet) {
		stdin.WriteRTP(&rtp.Packet{Payload: muxer.GetPayload(0, packet)})
	}
	b.sender.HandleRTP(track)
	return nil
}

func (b *mp4Backchannel) Stop() error {
	if b.sender != nil {
		b.sender.Close()
	}
	return b.Producer.Stop()
}
