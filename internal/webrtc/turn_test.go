package webrtc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	pion "github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"
)

const testSecret = "s3cret-value-for-tests"

var testStun = []string{"stun:stun.cloudflare.com:3478", "stun:stun.l.google.com:19302"}

type iceResponse struct {
	IceServers []struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	} `json:"ice_servers"`
}

func setICEConfig(t *testing.T, cfg iceConfig) {
	t.Helper()
	old := currentICE
	currentICE = cfg
	t.Cleanup(func() { currentICE = old })
}

func callICE(t *testing.T, method string) (*httptest.ResponseRecorder, iceResponse) {
	t.Helper()
	w := httptest.NewRecorder()
	iceHandler(w, httptest.NewRequest(method, "/api/webrtc/ice", nil))
	var res iceResponse
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	}
	return w, res
}

func TestTurnCredentialKnownVector(t *testing.T) {
	user, cred := turnCredential("s3cret", "doorbridge", time.Unix(1700003600, 0))
	require.Equal(t, "1700003600:doorbridge", user)
	require.Equal(t, "WidxQdrlyT1WUf0p568o95rV5sA=", cred)
}

func TestICEHandlerWithoutTurn(t *testing.T) {
	setICEConfig(t, iceConfig{Servers: []iceServer{{URLs: testStun}}})

	w, res := callICE(t, http.MethodGet)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, res.IceServers, 1)
	require.Equal(t, testStun, res.IceServers[0].URLs)
	require.Empty(t, res.IceServers[0].Username)
	require.Empty(t, res.IceServers[0].Credential)
}

func TestICEHandlerEmptyURLsNeverEmitted(t *testing.T) {
	// turn enabled but incomplete (no urls): feature off, no entry with empty urls
	setICEConfig(t, iceConfig{
		Servers: []iceServer{{URLs: testStun}, {URLs: nil}},
		Turn:    turnConfig{Secret: testSecret},
	})

	_, res := callICE(t, http.MethodGet)
	require.Len(t, res.IceServers, 1)
	for _, s := range res.IceServers {
		require.NotEmpty(t, s.URLs)
	}

	// urls without secret: also off
	setICEConfig(t, iceConfig{
		Servers: []iceServer{{URLs: testStun}},
		Turn:    turnConfig{URLs: []string{"turn:turn.example.org:3478"}},
	})
	_, res = callICE(t, http.MethodGet)
	require.Len(t, res.IceServers, 1)
}

func TestICEHandlerWithTurn(t *testing.T) {
	turnURLs := []string{
		"turn:turn.example.org:3478?transport=udp",
		"turn:turn.example.org:3478?transport=tcp",
		"turns:turn.example.org:443?transport=tcp",
	}
	setICEConfig(t, iceConfig{
		Servers: []iceServer{{URLs: testStun}},
		Turn:    turnConfig{URLs: turnURLs, Secret: testSecret, TTL: 90 * time.Minute},
	})

	now := time.Unix(1700000000, 0)
	oldClock := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = oldClock })

	w, res := callICE(t, http.MethodGet)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, res.IceServers, 2)
	require.Equal(t, testStun, res.IceServers[0].URLs)

	turn := res.IceServers[1]
	require.Equal(t, turnURLs, turn.URLs)

	expiryStr, user, ok := strings.Cut(turn.Username, ":")
	require.True(t, ok)
	require.Equal(t, "doorbridge", user)
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	require.NoError(t, err)
	want := now.Add(90 * time.Minute).Unix()
	require.InDelta(t, want, expiry, 5)

	_, wantCred := turnCredential(testSecret, "doorbridge", time.Unix(expiry, 0))
	require.Equal(t, wantCred, turn.Credential)
}

func TestICEHandlerDefaultTTL(t *testing.T) {
	setICEConfig(t, iceConfig{
		Turn: turnConfig{URLs: []string{"turn:turn.example.org:3478"}, Secret: testSecret},
	})
	now := time.Unix(1700000000, 0)
	oldClock := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = oldClock })

	_, res := callICE(t, http.MethodGet)
	require.Len(t, res.IceServers, 1)
	expiryStr, _, _ := strings.Cut(res.IceServers[0].Username, ":")
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	require.NoError(t, err)
	require.Equal(t, now.Add(time.Hour).Unix(), expiry)
}

func TestICEHandlerNeverLeaksSecret(t *testing.T) {
	setICEConfig(t, iceConfig{
		Servers: []iceServer{{URLs: testStun}},
		Turn:    turnConfig{URLs: []string{"turn:turn.example.org:3478"}, Secret: testSecret, TTL: time.Hour},
	})

	w, _ := callICE(t, http.MethodGet)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotContains(t, w.Body.String(), testSecret)
	for k, v := range w.Header() {
		require.NotContains(t, strings.Join(v, ","), testSecret, k)
	}
}

func TestICEHandlerMethod(t *testing.T) {
	setICEConfig(t, iceConfig{Servers: []iceServer{{URLs: testStun}}})

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		w, _ := callICE(t, m)
		require.Equal(t, http.StatusMethodNotAllowed, w.Code, m)
		require.Equal(t, "GET", w.Header().Get("Allow"), m)
	}
}

func TestICEHandlerNoStore(t *testing.T) {
	setICEConfig(t, iceConfig{Servers: []iceServer{{URLs: testStun}}})

	w, _ := callICE(t, http.MethodGet)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestICEHandlerStaticTurnEntryNotServed(t *testing.T) {
	const staticPass = "static-turn-pass"
	in := []pion.ICEServer{
		{URLs: []string{"stun:stun.example.org:3478"}},
		{URLs: []string{"turn:turn.example.org:3478"}, Username: "static-user", Credential: staticPass},
		{URLs: []string{"stun:stun2.example.org:3478", "turns:turn.example.org:443"}, Username: "u", Credential: staticPass},
	}
	setICEConfig(t, iceConfig{Servers: browserICEServers(in)})

	w, res := callICE(t, http.MethodGet)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, res.IceServers, 2)
	require.Equal(t, []string{"stun:stun.example.org:3478"}, res.IceServers[0].URLs)
	require.Equal(t, []string{"stun:stun2.example.org:3478"}, res.IceServers[1].URLs)
	body := w.Body.String()
	for _, leak := range []string{staticPass, "static-user", "turn:", "turns:", "username", "credential"} {
		require.NotContains(t, body, leak)
	}
}

func TestBrowserICEServersDropsEmpty(t *testing.T) {
	require.Empty(t, browserICEServers([]pion.ICEServer{{URLs: []string{"turn:turn.example.org:3478"}}}))
}

func TestICEHandlerTTLClamped(t *testing.T) {
	setICEConfig(t, iceConfig{
		Turn: turnConfig{URLs: []string{"turn:turn.example.org:3478"}, Secret: testSecret, TTL: 99999 * time.Hour},
	})
	now := time.Unix(1700000000, 0)
	oldClock := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = oldClock })

	_, res := callICE(t, http.MethodGet)
	require.Len(t, res.IceServers, 1)
	expiryStr, _, _ := strings.Cut(res.IceServers[0].Username, ":")
	expiry, err := strconv.ParseInt(expiryStr, 10, 64)
	require.NoError(t, err)
	require.InDelta(t, now.Add(24*time.Hour).Unix(), expiry, 5)
}

// configureICE is what Init calls: config in, handler output out.
func TestConfigureICEWiring(t *testing.T) {
	old := currentICE
	t.Cleanup(func() { currentICE = old })

	configureICE(
		turnConfig{URLs: []string{"turn:turn.example.org:3478"}, Secret: testSecret},
		[]pion.ICEServer{
			{URLs: []string{"stun:stun.example.org:3478"}},
			{URLs: []string{"turn:static.example.org:3478"}, Username: "static-user", Credential: "static-pass"},
		},
	)

	w, res := callICE(t, http.MethodGet)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, res.IceServers, 2)
	require.Equal(t, []string{"stun:stun.example.org:3478"}, res.IceServers[0].URLs)
	require.Equal(t, []string{"turn:turn.example.org:3478"}, res.IceServers[1].URLs)
	require.NotEmpty(t, res.IceServers[1].Credential)
	require.NotContains(t, w.Body.String(), "static")
	require.NotContains(t, w.Body.String(), testSecret)
}
