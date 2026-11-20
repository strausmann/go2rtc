package unifi

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a goroutine-safe log sink, because the package logger is
// written from sender and ffmpeg goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) since(offset int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()[offset:]
}

func (b *syncBuffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// testLogs receives the package log at trace level for the whole test binary.
// The logger is installed once in TestMain and never swapped afterwards:
// goroutines of earlier tests (ffmpeg waiters, senders) may still log, and
// replacing the package-level logger under them would be a data race.
var testLogs = &syncBuffer{}

func TestMain(m *testing.M) {
	SetLogger(zerolog.New(testLogs).Level(zerolog.TraceLevel))
	os.Exit(m.Run())
}

// logView is the part of the log written after captureLog was called. It may
// contain lines from goroutines of earlier tests, so assertions use values that
// are unique to the test.
type logView struct {
	offset int
}

func (v logView) String() string {
	return testLogs.since(v.offset)
}

func captureLog(t *testing.T) logView {
	t.Helper()
	return logView{offset: testLogs.len()}
}

// protectStub is a configurable fake of the UniFi Protect integration API.
type protectStub struct {
	*httptest.Server

	mu       sync.Mutex
	requests []string

	posts atomic.Int64
}

func (s *protectStub) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func newProtectStub(t *testing.T, camera, session http.HandlerFunc) *protectStub {
	t.Helper()

	stub := &protectStub{}
	stub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		stub.requests = append(stub.requests, r.Method+" "+r.URL.Path)
		stub.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/proxy/protect/integration/v1/cameras/camera1":
			camera(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/proxy/protect/integration/v1/cameras/camera1/talkback-session":
			stub.posts.Add(1)
			session(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.Close)

	return stub
}

func cameraWithSpeaker(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte(`{"featureFlags":{"hasSpeaker":true}}`))
}

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func talkbackURL(server *httptest.Server, apiKey string) string {
	return SchemeTalkback + ":" + server.URL + "?camera_id=camera1&api_key=" + apiKey
}

// attachMicrophone adds a browser-side audio receiver with the given codec
// (an index into the advertised codecs) and starts the client in background.
func attachMicrophone(t *testing.T, client *Client, codecIndex int) (*core.Receiver, chan error) {
	t.Helper()

	require.Len(t, client.Medias, 1)
	media := client.Medias[0]
	codec := media.Codecs[codecIndex]
	track := core.NewReceiver(&core.Media{
		Kind:      core.KindAudio,
		Direction: core.DirectionRecvonly,
	}, codec)
	require.NoError(t, client.AddTrack(media, codec, track))

	done := make(chan error, 1)
	go func() {
		done <- client.Start()
	}()

	return track, done
}

func writeTestRTP(track *core.Receiver, seq uint16) {
	track.WriteRTP(&rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    track.Codec.PayloadType,
			SequenceNumber: seq,
			Timestamp:      uint32(seq) * 960,
		},
		Payload: []byte{0x01, 0x02, 0x03},
	})
}

// waitDone returns the Start() result; it fails the test instead of hanging
// when Start never returns.
func waitDone(t *testing.T, done chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Start() did not return")
		return nil
	}
}

// scriptedCommand is an ffmpegCommand fake whose failure points are set per test.
type scriptedCommand struct {
	command string
	stdin   bytes.Buffer

	stdinErr error
	startErr error
	writeErr error
	waitErr  error
	exitNow  bool
	stderr   string

	done    chan struct{}
	once    sync.Once
	stdinCl atomic.Bool
	closed  atomic.Bool
}

func newScriptedCommand(command string) *scriptedCommand {
	return &scriptedCommand{command: command, done: make(chan struct{})}
}

func (c *scriptedCommand) StdinPipe() (io.WriteCloser, error) {
	if c.stdinErr != nil {
		return nil, c.stdinErr
	}
	return &scriptedStdin{cmd: c}, nil
}

func (c *scriptedCommand) Start() error {
	if c.startErr != nil {
		return c.startErr
	}
	if c.exitNow {
		c.once.Do(func() { close(c.done) })
	}
	return nil
}

func (c *scriptedCommand) Wait() error {
	<-c.done
	return c.waitErr
}

func (c *scriptedCommand) Close() error {
	c.closed.Store(true)
	c.once.Do(func() { close(c.done) })
	return nil
}

type scriptedStdin struct {
	cmd *scriptedCommand
}

func (s *scriptedStdin) Write(b []byte) (int, error) {
	if s.cmd.writeErr != nil {
		return 0, s.cmd.writeErr
	}
	return s.cmd.stdin.Write(b)
}

func (s *scriptedStdin) Close() error {
	s.cmd.stdinCl.Store(true)
	return nil
}

// scriptedStderrCommand additionally exposes StderrPipe like shell.Command.
type scriptedStderrCommand struct {
	*scriptedCommand
}

func (c scriptedStderrCommand) StderrPipe() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewBufferString(c.stderr)), nil
}

// erroringRTPWriter fails every write, like a closed or unreachable socket.
type erroringRTPWriter struct {
	closed atomic.Bool
}

func (w *erroringRTPWriter) Write([]byte) (int, error) {
	return 0, errors.New("write: connection refused")
}

func (w *erroringRTPWriter) Close() error {
	w.closed.Store(true)
	return nil
}

func (w *erroringRTPWriter) SetWriteDeadline(time.Time) error {
	return nil
}

// overrideFFmpeg swaps the ffmpeg/RTP hooks for the test and restores them.
func overrideFFmpeg(t *testing.T, cmd func(string) ffmpegCommand, reserve func() (int, error), dial func(int) (rtpWriter, error)) {
	t.Helper()

	oldCommand, oldReserve, oldDial := newFFmpegCommand, reserveRTPPort, dialRTPPort
	if cmd != nil {
		newFFmpegCommand = cmd
	}
	if reserve != nil {
		reserveRTPPort = reserve
	}
	if dial != nil {
		dialRTPPort = dial
	}
	t.Cleanup(func() {
		newFFmpegCommand, reserveRTPPort, dialRTPPort = oldCommand, oldReserve, oldDial
	})
}
