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

func TestInit(t *testing.T) {
	// unique per run, because creds secrets are process-global (go test -count)
	apiKey := "configured-api-key-" + strconv.Itoa(int(core.NewID())) + "-end"
	configured := "unifi:https://192.168.1.1?camera_id=camera1&api_key=" + apiKey

	// a stream from the config exists before the module starts,
	// streams.New only accepts known schemes, so register a placeholder first
	streams.HandleFunc("unifi", func(string) (core.Producer, error) { return nil, nil })
	_, err := streams.New("doorbell_"+apiKey, configured)
	require.NoError(t, err)
	require.Contains(t, creds.SecretString(configured), apiKey, "precondition")

	Init()

	require.Contains(t, creds.SecretString(configured), "api_key=***")
	require.NotContains(t, creds.SecretString(configured), apiKey)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"featureFlags":{"hasSpeaker":true}}`))
	}))
	defer server.Close()

	prod, err := streams.GetProducer("unifi:" + server.URL + "?camera_id=camera1&api_key=test-api-key")
	require.NoError(t, err)
	defer prod.Stop()

	client, ok := prod.(*unifi.Client)
	require.True(t, ok, "producer is %T", prod)
	require.NotNil(t, client.Backchannel)
	require.Equal(t, core.DirectionSendonly, client.GetMedias()[0].Direction)
}
