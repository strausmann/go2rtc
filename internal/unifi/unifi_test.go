package unifi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/creds"
	"github.com/AlexxIT/go2rtc/pkg/unifi"
	"github.com/stretchr/testify/require"
)

// protects: scheme registration via streams.HandleFunc — fails if, after Init(),
// the unifi-talkback scheme is not a known source or does not resolve to the
// talkback client with its backchannel media.
func TestInitRegistersTalkbackScheme(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/proxy/protect/integration/v1/cameras/camera1" {
			_, _ = w.Write([]byte(`{"featureFlags":{"hasSpeaker":true}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	source := unifi.SchemeTalkback + ":" + server.URL + "?camera_id=camera1&api_key=init-scheme-key"
	Init()

	require.True(t, streams.HasProducer(source))
	require.Contains(t, streams.SupportedSchemes(), unifi.SchemeTalkback)

	prod, err := streams.GetProducer(source)
	require.NoError(t, err)
	defer prod.Stop()

	client, ok := prod.(*unifi.Client)
	require.True(t, ok, "producer is %T", prod)
	require.Len(t, client.GetMedias(), 1)
	require.Equal(t, core.DirectionSendonly, client.GetMedias()[0].Direction)
}

// protects: secret registration of configured sources in Init() — fails if the
// api_key of a talkback source that already exists in the config (and is shown
// by /api/streams before it is ever dialled) is not masked after Init().
func TestInitMasksAPIKeysOfConfiguredSources(t *testing.T) {
	// unique per run, because creds secrets are process-global (go test -count)
	apiKey := "configured-source-api-key-" + strconv.FormatUint(uint64(core.NewID()), 10) + "-end"
	source := unifi.SchemeTalkback + ":https://192.168.1.1?camera_id=camera1&api_key=" + apiKey

	// a stream from the config exists before the unifi module initialises;
	// streams.New only accepts known schemes, so register a placeholder first
	streams.HandleFunc(unifi.SchemeTalkback, func(string) (core.Producer, error) { return nil, nil })
	_, err := streams.New("doorbell_talkback_"+apiKey, source)
	require.NoError(t, err)
	require.Contains(t, creds.SecretString(source), apiKey, "precondition: not masked yet")

	Init()

	require.NotContains(t, creds.SecretString(source), apiKey)
	require.Contains(t, creds.SecretString(source), "api_key=***")
}
