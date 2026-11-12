package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJellyfinPlaylistPlaybackModes(t *testing.T) {
	f := newAPITestFixture(t)
	f.state.SetChannels([]string{"alpha", "offline"})
	f.state.SetChannelStatus([]Status{{Name: "alpha", Online: true}, {Name: "offline", Online: false}})
	f.state.SetPlaylistBaseURL("https://jellych.test")
	for _, tc := range []struct {
		query, path string
		status      int
	}{
		{"", "/live/alpha/index.m3u8", 200},
		{"?playback=remux", "/live/alpha/index.m3u8", 200},
		{"?playback=proxy", "/api/live-proxy/alpha/index.m3u8", 200},
		{"?playback=unknown", "", 400},
	} {
		t.Run(tc.query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			f.api.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/twitch.m3u"+tc.query, nil))
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status == 200 && (!strings.Contains(rec.Body.String(), "https://jellych.test"+tc.path) || strings.Contains(rec.Body.String(), "/offline/")) {
				t.Fatalf("unexpected playlist %s", rec.Body.String())
			}
		})
	}
}

func TestJellyfinProxyWebhookDoesNotStartRemux(t *testing.T) {
	for _, mode := range []string{"", "remux", "proxy", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			f := newAPITestFixture(t)
			f.state.SetChannels([]string{"alpha"})
			f.state.SetJellyfinWebhookSecret("secret")
			starts := 0
			f.api.streams.Start = func(string) error { starts++; return nil }
			send := func(action string) int {
				body := fmt.Sprintf(`{"action":%q,"channel":"alpha","sessionId":"viewer","playbackMode":%q}`, action, mode)
				req := httptest.NewRequest(http.MethodPost, "/api/jellyfin/webhook", strings.NewReader(body))
				req.Header.Set("X-Jellych-Secret", "secret")
				rec := httptest.NewRecorder()
				f.api.Handler().ServeHTTP(rec, req)
				return rec.Code
			}
			if mode == "unknown" {
				if status := send("start"); status != 400 || starts != 0 {
					t.Fatalf("invalid mode: status=%d starts=%d", status, starts)
				}
				return
			}
			if status := send("start"); status != 200 {
				t.Fatalf("start=%d", status)
			}
			wantStarts := 1
			if mode == "proxy" {
				wantStarts = 0
			}
			if starts != wantStarts || f.api.playback.GetPlayingCounts(f.api.now())["alpha"] != 1 {
				t.Fatalf("starts=%d counts=%v", starts, f.api.playback.GetPlayingCounts(f.api.now()))
			}
			if status := send("stop"); status != 200 || len(f.api.playback.GetPlayingCounts(f.api.now())) != 0 {
				t.Fatalf("stop=%d", status)
			}
		})
	}
}
