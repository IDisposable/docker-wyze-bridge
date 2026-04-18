package go2rtcmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestManager_NewManager(t *testing.T) {
	m := NewManager("/usr/local/bin/go2rtc", "/config/go2rtc.yaml", zerolog.Nop())

	if m.binaryPath != "/usr/local/bin/go2rtc" {
		t.Errorf("binaryPath = %q", m.binaryPath)
	}
	if m.configPath != "/config/go2rtc.yaml" {
		t.Errorf("configPath = %q", m.configPath)
	}
	if m.APIURL() != "http://localhost:1984" {
		t.Errorf("APIURL = %q", m.APIURL())
	}
}

func TestManager_IsHealthy_NoServer(t *testing.T) {
	m := NewManager("", "", zerolog.Nop())
	m.apiURL = "http://localhost:19998" // unlikely to be listening

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if m.IsHealthy(ctx) {
		t.Error("should not be healthy with no server")
	}
}

func TestManager_StartMissingBinary(t *testing.T) {
	m := NewManager("/nonexistent/binary", "/tmp/test.yaml", zerolog.Nop())

	err := m.Start(context.Background())
	if err == nil {
		t.Error("expected error starting missing binary")
	}
}

func TestEmitLogLine(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantLevel string // zerolog JSON "level" value; "" = no output expected
		wantMsg   string // expected logged message (prefix stripped for parsed lines)
	}{
		{name: "empty", line: "", wantLevel: ""},
		{name: "whitespace", line: "   ", wantLevel: ""},
		{name: "zerolog DBG demoted to trace",
			line:      "21:38:15.743 DBG [streams] start producer url=wyze://x",
			wantLevel: "trace", wantMsg: "[streams] start producer url=wyze://x"},
		{name: "zerolog TRC stays trace",
			line:      "21:38:15.743 TRC some trace",
			wantLevel: "trace", wantMsg: "some trace"},
		{name: "zerolog INF demoted to debug",
			line:      "21:36:44.606 INF go2rtc platform=linux/amd64",
			wantLevel: "debug", wantMsg: "go2rtc platform=linux/amd64"},
		{name: "zerolog WRN stays warn",
			line:      "21:36:44.606 WRN something odd",
			wantLevel: "warn", wantMsg: "something odd"},
		{name: "zerolog ERR stays error",
			line:      "21:38:51.720 ERR streams: wyze: connect failed",
			wantLevel: "error", wantMsg: "streams: wyze: connect failed"},
		{name: "zerolog FTL demoted to error (keep bridge alive)",
			line:      "21:38:51.720 FTL fatal thing",
			wantLevel: "error", wantMsg: "fatal thing"},
		{name: "unprefixed [OOO] reset noise suppressed",
			line:      "[OOO] ch=0x05 #3643 frameType=0x00 pktTotal=107 expected pkt 0, got 102 - reset",
			wantLevel: ""},
		{name: "unknown level token → trace",
			line:      "12:00:00.000 ??? weird line",
			wantLevel: "trace", wantMsg: "12:00:00.000 ??? weird line"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			m := NewManager("", "", zerolog.New(&buf).Level(zerolog.TraceLevel))
			m.emitLogLine(tc.line)

			out := strings.TrimSpace(buf.String())
			if tc.wantLevel == "" {
				if out != "" {
					t.Errorf("expected no log output, got %q", out)
				}
				return
			}
			var entry map[string]interface{}
			if err := json.Unmarshal([]byte(out), &entry); err != nil {
				t.Fatalf("unmarshal log %q: %v", out, err)
			}
			if got, _ := entry["level"].(string); got != tc.wantLevel {
				t.Errorf("level = %q, want %q", got, tc.wantLevel)
			}
			if got, _ := entry["message"].(string); got != tc.wantMsg {
				t.Errorf("message = %q, want %q", got, tc.wantMsg)
			}
		})
	}
}
