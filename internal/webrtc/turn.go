package webrtc

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 is mandated by the TURN REST API draft used by coturn
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	pion "github.com/pion/webrtc/v4"
)

const (
	turnUser       = "doorbridge"
	turnDefaultTTL = time.Hour
)

// timeNow is the clock used for credential expiry, swappable in tests.
var timeNow = time.Now

// iceServer is the JSON shape served to browsers (RTCIceServer).
type iceServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// turnConfig is the "webrtc.turn" section: coturn "use-auth-secret" (TURN REST API).
type turnConfig struct {
	URLs   []string      `yaml:"urls"`
	Secret string        `yaml:"secret"`
	TTL    time.Duration `yaml:"ttl"`
}

// iceConfig is what iceHandler serves. It is written once in Init, before the
// HTTP routes are registered, and only read afterwards.
type iceConfig struct {
	Servers []iceServer
	Turn    turnConfig
}

var currentICE iceConfig

// browserICEServers maps the configured ice_servers to what browsers may see:
// only stun:/stuns: URLs. Static turn: entries carry credentials from the config
// and must never be served; without them the browser rejects the whole
// RTCPeerConnection (InvalidAccessError). Entries that end up empty are dropped.
func browserICEServers(in []pion.ICEServer) []iceServer {
	var out []iceServer
	for _, ice := range in {
		var urls []string
		for _, u := range ice.URLs {
			if strings.HasPrefix(u, "stun:") || strings.HasPrefix(u, "stuns:") {
				urls = append(urls, u)
			}
		}
		if len(urls) > 0 {
			out = append(out, iceServer{URLs: urls})
		}
	}
	return out
}

// turnCredential returns time-limited TURN credentials: the username is
// "<unix expiry>:<user>", the credential is base64(HMAC-SHA1(secret, username)).
func turnCredential(secret, user string, expiry time.Time) (username, credential string) {
	username = strconv.FormatInt(expiry.Unix(), 10) + ":" + user
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	credential = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return username, credential
}

// iceHandler serves the ICE servers for browsers, including fresh TURN credentials
// when webrtc.turn is configured. The secret itself is never part of the response.
func iceHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cfg := currentICE
	servers := make([]iceServer, 0, len(cfg.Servers)+1)
	for _, s := range cfg.Servers {
		if len(s.URLs) > 0 {
			servers = append(servers, iceServer{URLs: s.URLs})
		}
	}

	if cfg.Turn.Secret != "" && len(cfg.Turn.URLs) > 0 {
		ttl := cfg.Turn.TTL
		if ttl <= 0 {
			ttl = turnDefaultTTL
		}
		user, cred := turnCredential(cfg.Turn.Secret, turnUser, timeNow().Add(ttl))
		servers = append(servers, iceServer{URLs: cfg.Turn.URLs, Username: user, Credential: cred})
	}

	w.Header().Set("Cache-Control", "no-store")
	api.ResponseJSON(w, map[string]any{"ice_servers": servers})
}
