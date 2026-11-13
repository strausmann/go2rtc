package rtsp

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/rs/zerolog"
)

func TestParseKeyframe(t *testing.T) {
	tests := []struct {
		name     string
		rawQuery string
		wantMode string
		wantOK   bool
	}{
		{"no query", "", "", true},
		{"other parameter only", "timeout=5", "", true},
		{"resetup", "keyframe=resetup", "resetup", true},
		{"resetup among others", "timeout=5#keyframe=resetup#media=video", "resetup", true},
		{"unknown value", "keyframe=yes", "", false},
		{"empty value", "keyframe=", "", false},
		{"bare parameter", "keyframe", "", false},
		{"first value wins", "keyframe=resetup#keyframe=x", "resetup", true},
		{"first value wins, invalid first", "keyframe=x#keyframe=resetup", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query := streams.ParseQuery(tc.rawQuery)
			mode, ok := parseKeyframe(query)
			if mode != tc.wantMode || ok != tc.wantOK {
				t.Fatalf("parseKeyframe(%q) = (%q, %t), want (%q, %t)", tc.rawQuery, mode, ok, tc.wantMode, tc.wantOK)
			}
		})
	}
}

func TestKeyframeLoggerDebugOnly(t *testing.T) {
	cause := errors.New("rtsp: keyframe helper failed: describe: timeout")

	t.Run("info level stays silent", func(t *testing.T) {
		var buf bytes.Buffer
		l := zerolog.New(&buf).Level(zerolog.InfoLevel)
		keyframeLogger(l)(cause, 1500*time.Millisecond)
		if buf.Len() != 0 {
			t.Fatalf("expected no output at info level, got %q", buf.String())
		}
	})

	t.Run("debug level logs one line", func(t *testing.T) {
		var buf bytes.Buffer
		l := zerolog.New(&buf).Level(zerolog.DebugLevel)
		keyframeLogger(l)(cause, 1500*time.Millisecond)
		out := buf.String()
		if n := strings.Count(out, "\n"); n != 1 {
			t.Fatalf("expected exactly one line, got %d: %q", n, out)
		}
		for _, want := range []string{`"level":"debug"`, "describe: timeout", "1500"} {
			if !strings.Contains(out, want) {
				t.Errorf("output %q does not contain %q", out, want)
			}
		}
	})

	t.Run("success is logged without error", func(t *testing.T) {
		var buf bytes.Buffer
		l := zerolog.New(&buf).Level(zerolog.DebugLevel)
		keyframeLogger(l)(nil, 20*time.Millisecond)
		out := buf.String()
		if !strings.Contains(out, `"level":"debug"`) || strings.Contains(out, `"error"`) {
			t.Fatalf("unexpected output %q", out)
		}
	})
}
