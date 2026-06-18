package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c2bw/jellych/stream"
)

func resetAPIStateForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		SetChannelStore(nil)
		SetVODStore(nil)
		SetChannels(nil)
		SetChannelLogos(nil)
		SetChannelStatus(nil)
		SetVODs(nil)
		SetJellyfinWebhookSecret("")
		SetPlaylistBaseURL("")
		stream.SetVODDownloadDir("")

		playMu.Lock()
		playSessions = nil
		jellyfinSessions = nil
		playMu.Unlock()

		idleMu.Lock()
		idleTimers = nil
		idleMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestPingEndpoint(t *testing.T) {
	tests := []string{"/api/ping", "/api/ping/"}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Fatalf("expected text/plain content type, got %q", got)
			}
			if got := rec.Body.String(); got != "pong" {
				t.Fatalf("expected pong response, got %q", got)
			}
		})
	}
}

func TestAuthorizeJellyfinWebhook(t *testing.T) {
	resetAPIStateForTest(t)
	SetJellyfinWebhookSecret("shared-secret")

	tests := []struct {
		name       string
		secret     string
		wantStatus int
		wantOK     bool
	}{
		{name: "valid", secret: "shared-secret", wantStatus: http.StatusOK, wantOK: true},
		{name: "invalid", secret: "wrong-secret", wantStatus: http.StatusUnauthorized, wantOK: false},
		{name: "missing", secret: "", wantStatus: http.StatusUnauthorized, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/jellyfin/webhook", nil)
			if tt.secret != "" {
				req.Header.Set("X-Jellych-Secret", tt.secret)
			}
			rec := httptest.NewRecorder()

			gotOK := authorizeJellyfinWebhook(rec, req)

			if gotOK != tt.wantOK {
				t.Fatalf("expected ok=%v, got %v", tt.wantOK, gotOK)
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}
		})
	}
}

func TestStopChannelIsIdempotentWhenNotRunning(t *testing.T) {
	resetAPIStateForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/stop/notrunning", nil)
	rec := httptest.NewRecorder()

	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Body.String(); got != "stopped" {
		t.Fatalf("expected stopped response, got %q", got)
	}
}

func TestIsIdempotentStopError(t *testing.T) {
	if !isIdempotentStopError(stream.ErrStopTimeout) {
		t.Fatal("expected ErrStopTimeout to be idempotent")
	}
	if !isIdempotentStopError(os.ErrProcessDone) {
		t.Fatal("expected os.ErrProcessDone to be idempotent")
	}
	if isIdempotentStopError(errors.New("other")) {
		t.Fatal("did not expect arbitrary error to be idempotent")
	}
}

func TestTextEndpointsUsePlainTextContentType(t *testing.T) {
	resetAPIStateForTest(t)
	req := httptest.NewRequest(http.MethodPost, "/api/stop/notrunning", nil)
	rec := httptest.NewRecorder()

	Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain content type, got %q", got)
	}
}

func TestBuildM3UIncludesLogoMetadata(t *testing.T) {
	playlist := BuildM3U(
		[]string{"jankos"},
		[]Status{{Name: "jankos", Online: true, Viewers: 42}},
		map[string]string{"jankos": "https://cdn.test/jankos.png"},
	)
	if !strings.Contains(playlist, `tvg-logo="https://cdn.test/jankos.png"`) {
		t.Fatalf("expected tvg-logo metadata in playlist, got %q", playlist)
	}
}

func TestBuildM3UEscapesLogoMetadata(t *testing.T) {
	playlist := BuildM3U(
		[]string{"jankos"},
		[]Status{{Name: "jankos", Online: true}},
		map[string]string{"jankos": `https://cdn.test/path\"logo".png`},
	)
	if !strings.Contains(playlist, `tvg-logo="https://cdn.test/path\\\"logo\".png"`) {
		t.Fatalf("expected escaped tvg-logo metadata, got %q", playlist)
	}
}

func TestBuildM3USkipsOfflineChannels(t *testing.T) {
	playlist := BuildM3U(
		[]string{"jankos", "caedrel"},
		[]Status{
			{Name: "jankos", Online: true, Viewers: 42},
			{Name: "caedrel", Online: false},
		},
		map[string]string{
			"jankos":  "https://cdn.test/jankos.png",
			"caedrel": "https://cdn.test/caedrel.png",
		},
	)
	if !strings.Contains(playlist, ",jankos\n") {
		t.Fatalf("expected online channel to be present, got %q", playlist)
	}
	if strings.Contains(playlist, ",caedrel\n") {
		t.Fatalf("expected offline channel to be omitted, got %q", playlist)
	}
}

func TestBuildVODM3UIncludesVODEntries(t *testing.T) {
	resetAPIStateForTest(t)
	SetPlaylistBaseURL("https://jellych.test")

	playlist := BuildVODM3U([]VOD{{
		ID:      "123456789",
		URL:     "https://www.twitch.tv/videos/123456789",
		Title:   "Nice Game",
		Channel: "Streamer",
		Logo:    "https://cdn.test/thumb.jpg",
		Date:    "2026-06-02T12:34:56Z",
	}})

	for _, want := range []string{
		`group-title="Recordings"`,
		`tvg-id="vod-123456789"`,
		`tvg-name="2026-06-02 - Streamer - Nice Game"`,
		`tvg-logo="https://cdn.test/thumb.jpg"`,
		`tvg-date="2026-06-02T12:34:56Z"`,
		",2026-06-02 - Streamer - Nice Game\n",
		"https://jellych.test/vod/123456789/index.m3u8",
	} {
		if !strings.Contains(playlist, want) {
			t.Fatalf("expected VOD playlist to contain %q, got %q", want, playlist)
		}
	}
}

func TestPrepareVODDerivesURLFromID(t *testing.T) {
	got := PrepareVOD(VOD{ID: "1234567890"})
	if got.URL != "https://www.twitch.tv/videos/1234567890" {
		t.Fatalf("expected Twitch VOD URL, got %q", got.URL)
	}
}

func TestDownloadVODRequiresConfiguredFolder(t *testing.T) {
	resetAPIStateForTest(t)
	SetVODs([]VOD{{
		ID:  "123456789",
		URL: "https://www.twitch.tv/videos/123456789",
	}})
	stream.SetVODDownloadDir("")

	req := httptest.NewRequest(http.MethodPost, "/api/vods/123456789/download", nil)
	rec := httptest.NewRecorder()

	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "vod downloads folder is not configured") {
		t.Fatalf("expected missing folder message, got %q", rec.Body.String())
	}
}

func TestVODListIncludesDownloadStatus(t *testing.T) {
	resetAPIStateForTest(t)
	dir := t.TempDir()
	stream.SetVODDownloadDir(dir)
	stream.SetVODDownloadRetention(30 * 24 * time.Hour)
	t.Cleanup(func() { stream.SetVODDownloadRetention(30 * 24 * time.Hour) })
	SetVODs([]VOD{{
		ID:  "123456789",
		URL: "https://www.twitch.tv/videos/123456789",
	}})
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatalf("failed to create downloaded vod file: %v", err)
	}
	completedAt := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, completedAt, completedAt); err != nil {
		t.Fatalf("failed to set download completion time: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vods", nil)
	rec := httptest.NewRecorder()

	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var got []struct {
		ID                  string    `json:"id"`
		Downloaded          bool      `json:"downloaded"`
		EstimatedDeletionAt time.Time `json:"estimatedDeletionAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one vod, got %d", len(got))
	}
	if !got[0].Downloaded {
		t.Fatalf("expected downloaded status for vod %q", got[0].ID)
	}
	wantDeletionAt := completedAt.Add(30 * 24 * time.Hour)
	if !got[0].EstimatedDeletionAt.Equal(wantDeletionAt) {
		t.Fatalf("expected deletion estimate %v, got %v", wantDeletionAt, got[0].EstimatedDeletionAt)
	}
}

func TestBuildVODM3UEscapesMetadataAttributes(t *testing.T) {
	playlist := BuildVODM3U([]VOD{{
		ID:    "123",
		URL:   "https://www.twitch.tv/videos/123",
		Title: `Title "With Quotes"`,
	}})

	if !strings.Contains(playlist, `tvg-name="Title \"With Quotes\""`) {
		t.Fatalf("expected escaped tvg-name, got %q", playlist)
	}
}

func TestVodIDFromURL(t *testing.T) {
	got := VODIDFromURL("https://www.twitch.tv/someone/videos/123456789?filter=archives")
	if got != "123456789" {
		t.Fatalf("expected twitch vod id, got %q", got)
	}
}
