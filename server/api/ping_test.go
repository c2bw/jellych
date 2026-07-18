package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c2bw/jellych/stream"
)

type apiRoundTripFunc func(*http.Request) (*http.Response, error)

func (f apiRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type apiTestFixture struct {
	state *APIState
	api   *API
}

func newAPITestFixture(t *testing.T) *apiTestFixture {
	t.Helper()
	state := &APIState{}
	return &apiTestFixture{state: state, api: newAPI(state, Dependencies{})}
}

func TestVODPlaylistProxiesCrossOriginMedia(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789"}})
	fixture.api.streams.ResolveVODPlaylist = func(context.Context, string) ([]byte, error) {
		return []byte("#EXTM3U\n#EXTINF:4,\nhttps://d3fi1amfgojobc.cloudfront.net/path/0.ts\n"), nil
	}

	playlistReq := httptest.NewRequest(http.MethodGet, "/vod/123456789/index.m3u8", nil)
	playlistRec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(playlistRec, playlistReq)
	if playlistRec.Code != http.StatusOK {
		t.Fatalf("expected playlist status %d, got %d: %s", http.StatusOK, playlistRec.Code, playlistRec.Body.String())
	}
	if strings.Contains(playlistRec.Body.String(), "cloudfront.net") {
		t.Fatalf("playlist still exposed cross-origin media URL: %q", playlistRec.Body.String())
	}
	var mediaPath string
	for _, line := range strings.Split(playlistRec.Body.String(), "\n") {
		if strings.HasPrefix(line, "/vod/123456789/media/") {
			mediaPath = line
			break
		}
	}
	if mediaPath == "" {
		t.Fatalf("expected local media proxy URL, got %q", playlistRec.Body.String())
	}

	originalClient := fixture.api.vodMediaHTTPClient
	t.Cleanup(func() { fixture.api.vodMediaHTTPClient = originalClient })
	fixture.api.vodMediaHTTPClient = &http.Client{Transport: apiRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://d3fi1amfgojobc.cloudfront.net/path/0.ts" {
			t.Fatalf("unexpected upstream URL %q", req.URL.String())
		}
		if got := req.Header.Get("Range"); got != "bytes=0-3" {
			t.Fatalf("expected Range header to be forwarded, got %q", got)
		}
		return &http.Response{
			StatusCode:    http.StatusPartialContent,
			Header:        http.Header{"Content-Type": {"video/mp2t"}, "Content-Range": {"bytes 0-3/8"}, "Content-Length": {"4"}},
			Body:          io.NopCloser(strings.NewReader("data")),
			ContentLength: 4,
		}, nil
	})}

	mediaReq := httptest.NewRequest(http.MethodGet, mediaPath, nil)
	mediaReq.Header.Set("Range", "bytes=0-3")
	mediaRec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(mediaRec, mediaReq)
	if mediaRec.Code != http.StatusPartialContent || mediaRec.Body.String() != "data" {
		t.Fatalf("expected proxied partial media, got status=%d body=%q", mediaRec.Code, mediaRec.Body.String())
	}
	if got := mediaRec.Header().Get("Content-Range"); got != "bytes 0-3/8" {
		t.Fatalf("expected Content-Range response header, got %q", got)
	}
}

func TestPingEndpoint(t *testing.T) {
	fixture := newAPITestFixture(t)
	tests := []string{"/api/ping", "/api/ping/"}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			fixture.api.Handler().ServeHTTP(rec, req)

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

func TestVODPresetsEndpointReturnsResolvedCommands(t *testing.T) {
	fixture := newAPITestFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/vod-presets", nil)
	rec := httptest.NewRecorder()

	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var commands map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &commands); err != nil {
		t.Fatalf("decode preset commands: %v", err)
	}
	if !strings.Contains(commands["hevc"], "-x265-params pools=") {
		t.Fatalf("expected resolved HEVC threading, got %q", commands["hevc"])
	}
	if !strings.Contains(commands["vp9"], "-row-mt 1") {
		t.Fatalf("expected resolved VP9 threading, got %q", commands["vp9"])
	}
}

func TestControlAPISecretProtectsMutations(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetControlAPISecret("test-secret")

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/status", strings.NewReader("[]"))
	unauthorizedRec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, unauthorizedRec.Code)
	}

	authorized := httptest.NewRequest(http.MethodPost, "/api/status", strings.NewReader("[]"))
	authorized.Header.Set("Authorization", "Bearer test-secret")
	authorizedRec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(authorizedRec, authorized)
	if authorizedRec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, authorizedRec.Code)
	}
}

func TestIsConfiguredChannel(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetChannels([]string{"jankos"})

	if !fixture.state.IsConfiguredChannel(" JANKOS ") {
		t.Fatal("expected configured channel lookup to normalize the name")
	}
	if fixture.state.IsConfiguredChannel("caedrel") {
		t.Fatal("did not expect an unknown channel to be configured")
	}
}

func TestAuthorizeJellyfinWebhook(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetJellyfinWebhookSecret("shared-secret")

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

			gotOK := fixture.api.authorizeJellyfinWebhook(rec, req)

			if gotOK != tt.wantOK {
				t.Fatalf("expected ok=%v, got %v", tt.wantOK, gotOK)
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}
		})
	}
}

func TestJellyfinWebhookRejectsUnsupportedActions(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing action",
			body: `{"channel":"jankos","sessionId":"session-1"}`,
		},
		{
			name: "unknown action",
			body: `{"action":"sotp","channel":"jankos","sessionId":"session-1"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAPITestFixture(t)
			fixture.state.SetChannels([]string{"jankos"})
			fixture.state.SetJellyfinWebhookSecret("shared-secret")

			req := httptest.NewRequest(http.MethodPost, "/api/jellyfin/webhook", strings.NewReader(test.body))
			req.Header.Set("X-Jellych-Secret", "shared-secret")
			rec := httptest.NewRecorder()
			fixture.api.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			if counts := fixture.api.playback.GetPlayingCounts(time.Now()); len(counts) != 0 {
				t.Fatalf("unsupported action recorded playback sessions: %#v", counts)
			}
		})
	}
}

func TestPlayingEndpointRejectsUnsupportedActions(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing action",
			body: `{"sessionId":"session-1"}`,
		},
		{
			name: "unknown action",
			body: `{"action":"pnig","sessionId":"session-1"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAPITestFixture(t)
			fixture.state.SetChannels([]string{"jankos"})

			req := httptest.NewRequest(http.MethodPost, "/api/playing/jankos", strings.NewReader(test.body))
			rec := httptest.NewRecorder()
			fixture.api.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			if counts := fixture.api.playback.GetPlayingCounts(time.Now()); len(counts) != 0 {
				t.Fatalf("unsupported action recorded playback sessions: %#v", counts)
			}
		})
	}
}

func TestStopChannelIsIdempotentWhenNotRunning(t *testing.T) {
	fixture := newAPITestFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/stop/notrunning", nil)
	rec := httptest.NewRecorder()

	fixture.api.Handler().ServeHTTP(rec, req)

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
	fixture := newAPITestFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/stop/notrunning", nil)
	rec := httptest.NewRecorder()

	fixture.api.Handler().ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("expected text/plain content type, got %q", got)
	}
}

func TestBuildM3UIncludesLogoMetadata(t *testing.T) {
	playlist := (&APIState{}).BuildM3U(
		[]string{"jankos"},
		[]Status{{Name: "jankos", Online: true, Viewers: 42}},
		map[string]string{"jankos": "https://cdn.test/jankos.png"},
	)
	if !strings.Contains(playlist, `tvg-logo="https://cdn.test/jankos.png"`) {
		t.Fatalf("expected tvg-logo metadata in playlist, got %q", playlist)
	}
}

func TestBuildM3UEscapesLogoMetadata(t *testing.T) {
	playlist := (&APIState{}).BuildM3U(
		[]string{"jankos"},
		[]Status{{Name: "jankos", Online: true}},
		map[string]string{"jankos": `https://cdn.test/path\"logo".png`},
	)
	if !strings.Contains(playlist, `tvg-logo="https://cdn.test/path\\\"logo\".png"`) {
		t.Fatalf("expected escaped tvg-logo metadata, got %q", playlist)
	}
}

func TestBuildM3USkipsOfflineChannels(t *testing.T) {
	playlist := (&APIState{}).BuildM3U(
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
	fixture := newAPITestFixture(t)
	fixture.state.SetPlaylistBaseURL("https://jellych.test")

	playlist := fixture.state.BuildVODM3U([]VOD{{
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
	fixture := newAPITestFixture(t)
	fixture.state.SetVODs([]VOD{{
		ID:  "123456789",
		URL: "https://www.twitch.tv/videos/123456789",
	}})
	req := httptest.NewRequest(http.MethodPost, "/api/vods/123456789/download", nil)
	rec := httptest.NewRecorder()

	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "vod downloads folder is not configured") {
		t.Fatalf("expected missing folder message, got %q", rec.Body.String())
	}
}

func TestDownloadVODAcceptsPresetsAndDefaultsToOriginal(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want stream.VODDownloadPreset
	}{
		{name: "empty body", want: stream.VODDownloadPresetOriginal},
		{name: "omitted preset", body: `{}`, want: stream.VODDownloadPresetOriginal},
		{name: "original", body: `{"preset":"original"}`, want: stream.VODDownloadPresetOriginal},
		{name: "h264", body: `{"preset":"h264"}`, want: stream.VODDownloadPresetH264},
		{name: "hevc", body: `{"preset":"hevc"}`, want: stream.VODDownloadPresetHEVC},
		{name: "vp9", body: `{"preset":"vp9"}`, want: stream.VODDownloadPresetVP9},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newAPITestFixture(t)
			fixture.state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789", Duration: "2h3m4s"}})
			var got stream.VODDownloadPreset
			var gotDuration time.Duration
			fixture.api.streams.StartVODDownload = func(_ context.Context, _, _, _, _ string, preset stream.VODDownloadPreset, duration time.Duration) error {
				got = preset
				gotDuration = duration
				return nil
			}

			req := httptest.NewRequest(http.MethodPost, "/api/vods/123456789/download", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			fixture.api.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
			}
			if got != tt.want {
				t.Fatalf("expected preset %q, got %q", tt.want, got)
			}
			if gotDuration != 2*time.Hour+3*time.Minute+4*time.Second {
				t.Fatalf("expected Twitch duration to reach downloader, got %s", gotDuration)
			}
		})
	}
}

func TestDownloadVODRejectsInvalidPresetPayloads(t *testing.T) {
	for _, body := range []string{`{"preset":"av1"}`, `{`, `null`, `{"preset":"h264","extra":true}`} {
		t.Run(body, func(t *testing.T) {
			fixture := newAPITestFixture(t)
			fixture.state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789"}})
			called := false
			fixture.api.streams.StartVODDownload = func(context.Context, string, string, string, string, stream.VODDownloadPreset, time.Duration) error {
				called = true
				return nil
			}

			req := httptest.NewRequest(http.MethodPost, "/api/vods/123456789/download", strings.NewReader(body))
			rec := httptest.NewRecorder()
			fixture.api.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
			}
			if called {
				t.Fatal("did not expect downloader to be called")
			}
		})
	}
}

func TestDownloadVODRejectsOversizedPayload(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789"}})
	called := false
	fixture.api.streams.StartVODDownload = func(context.Context, string, string, string, string, stream.VODDownloadPreset, time.Duration) error {
		called = true
		return nil
	}
	body := `{"preset":"original","padding":"` + strings.Repeat("x", maxJSONRequestBytes) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/vods/123456789/download", strings.NewReader(body))
	rec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status %d, got %d: %s", http.StatusRequestEntityTooLarge, rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("did not expect downloader to be called")
	}
}

func TestConvertVODPassesValidatedPreset(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789"}})
	var gotID string
	var gotPreset stream.VODDownloadPreset
	fixture.api.streams.ConvertVODDownload = func(_ context.Context, id string, preset stream.VODDownloadPreset) error {
		gotID = id
		gotPreset = preset
		return nil
	}
	req := httptest.NewRequest(http.MethodPost, "/api/vods/123456789/convert", strings.NewReader(`{"preset":"hevc"}`))
	rec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	if gotID != "123456789" || gotPreset != stream.VODDownloadPresetHEVC {
		t.Fatalf("unexpected conversion request: id=%q preset=%q", gotID, gotPreset)
	}
}

func TestConvertVODMapsOriginalTargetError(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789"}})
	fixture.api.streams.ConvertVODDownload = func(context.Context, string, stream.VODDownloadPreset) error {
		return stream.ErrVODConversionTargetOriginal
	}
	req := httptest.NewRequest(http.MethodPost, "/api/vods/123456789/convert", strings.NewReader(`{"preset":"original"}`))
	rec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestRemoveVODCascadesDownloadedArtifacts(t *testing.T) {
	fixture := newAPITestFixture(t)
	dir := t.TempDir()
	fixture.setVODDownloader(dir, 30*24*time.Hour)
	fixture.state.SetVODs([]VOD{{
		ID:  "123456789",
		URL: "https://www.twitch.tv/videos/123456789",
	}})
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatalf("failed to create downloaded vod file: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/vods/123456789", nil)
	rec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if _, ok := fixture.state.FindVOD("123456789"); ok {
		t.Fatal("expected vod metadata to be removed")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected downloaded file to be removed, got %v", err)
	}
}

func TestDeleteVODDownloadDoesNotRequireMetadata(t *testing.T) {
	fixture := newAPITestFixture(t)
	dir := t.TempDir()
	fixture.setVODDownloader(dir, 30*24*time.Hour)
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("orphaned"), 0644); err != nil {
		t.Fatalf("failed to create orphaned vod file: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/vods/123456789/download", nil)
	rec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected orphaned file to be removed, got %v", err)
	}
}

func TestGetVODPlaylistReturnsForbiddenForRestrictedManifest(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.state.SetVODs([]VOD{{
		ID:  "123456789",
		URL: "https://www.twitch.tv/videos/123456789",
	}})
	fixture.api.streams.ResolveVODPlaylist = func(context.Context, string) ([]byte, error) {
		return nil, fmt.Errorf("%w: Twitch rejected the VOD manifest", stream.ErrVODManifestRestricted)
	}

	req := httptest.NewRequest(http.MethodGet, "/vod/123456789/index.m3u8", nil)
	rec := httptest.NewRecorder()

	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "subscriber-only") {
		t.Fatalf("expected restricted VOD message, got %q", rec.Body.String())
	}
}

func TestVODListIncludesDownloadStatus(t *testing.T) {
	fixture := newAPITestFixture(t)
	dir := t.TempDir()
	fixture.setVODDownloader(dir, 30*24*time.Hour)
	fixture.state.SetVODs([]VOD{{
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

	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var got []struct {
		ID                  string    `json:"id"`
		Downloaded          bool      `json:"downloaded"`
		DownloadSize        int64     `json:"downloadSize"`
		DownloadRate        int64     `json:"downloadRate"`
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
	if got[0].DownloadSize != int64(len("downloaded")) {
		t.Fatalf("expected download size %d, got %d", len("downloaded"), got[0].DownloadSize)
	}
	wantDeletionAt := completedAt.Add(30 * 24 * time.Hour)
	if !got[0].EstimatedDeletionAt.Equal(wantDeletionAt) {
		t.Fatalf("expected deletion estimate %v, got %v", wantDeletionAt, got[0].EstimatedDeletionAt)
	}
}

func TestVODListIncludesUnknownDownloadSize(t *testing.T) {
	fixture := newAPITestFixture(t)
	fixture.setVODDownloader(t.TempDir(), 30*24*time.Hour)
	fixture.state.SetVODs([]VOD{{
		ID:  "123456789",
		URL: "https://www.twitch.tv/videos/123456789",
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/vods", nil)
	rec := httptest.NewRecorder()

	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var got []struct {
		DownloadSize int64 `json:"downloadSize"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one vod, got %d", len(got))
	}
	if got[0].DownloadSize != 0 {
		t.Fatalf("expected unknown download size to be 0, got %d", got[0].DownloadSize)
	}
}

func TestGetVODDownloadProgressEndpoint(t *testing.T) {
	fixture := newAPITestFixture(t)
	dir := t.TempDir()
	fixture.setVODDownloader(dir, 30*24*time.Hour)
	fixture.state.SetVODs([]VOD{{
		ID:  "123456789",
		URL: "https://www.twitch.tv/videos/123456789",
	}})
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("downloaded"), 0644); err != nil {
		t.Fatalf("failed to create downloaded vod file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vods/123456789/download", nil)
	rec := httptest.NewRecorder()

	fixture.api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	var got struct {
		Active     bool  `json:"active"`
		Downloaded bool  `json:"downloaded"`
		TotalSize  int64 `json:"totalSize"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got.Active {
		t.Fatal("did not expect completed download to be active")
	}
	if !got.Downloaded {
		t.Fatal("expected completed download status")
	}
	if got.TotalSize != int64(len("downloaded")) {
		t.Fatalf("expected total size %d, got %d", len("downloaded"), got.TotalSize)
	}
}

func TestBuildVODM3UEscapesMetadataAttributes(t *testing.T) {
	playlist := (&APIState{}).BuildVODM3U([]VOD{{
		ID:    "123",
		URL:   "https://www.twitch.tv/videos/123",
		Title: `Title "With Quotes"`,
	}})

	if !strings.Contains(playlist, `tvg-name="Title \"With Quotes\""`) {
		t.Fatalf("expected escaped tvg-name, got %q", playlist)
	}
}

func TestBuildVODM3UStripsLineBreaksFromMetadata(t *testing.T) {
	playlist := (&APIState{}).BuildVODM3U([]VOD{{
		ID:      "123",
		URL:     "https://www.twitch.tv/videos/123",
		Title:   "safe\nhttps://evil.example/stream.m3u8",
		Channel: "channel\r\n#EXTINF:1,evil",
	}})

	if strings.Contains(playlist, "\nhttps://evil.example") || strings.Contains(playlist, "\n#EXTINF:1,evil") {
		t.Fatalf("metadata injected playlist lines: %q", playlist)
	}
}

func TestJSONHandlersRejectUnknownFieldsAndTrailingValues(t *testing.T) {
	fixture := newAPITestFixture(t)
	for name, body := range map[string]string{
		"unknown":  `[{"name":"test","unknown":true}]`,
		"trailing": `[] []`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/status", strings.NewReader(body))
			rec := httptest.NewRecorder()
			fixture.api.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
			}
		})
	}
}

func TestStartStreamRequiresConfiguredChannel(t *testing.T) {
	fixture := newAPITestFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/stream/notconfigured", nil)
	rec := httptest.NewRecorder()
	fixture.api.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestExpiredJellyfinSessionIsPruned(t *testing.T) {
	fixture := newAPITestFixture(t)
	now := time.Now()
	fixture.api.playback.playSessions = map[string]map[string]time.Time{"test": {"session": now.Add(-jellyfinSessionMaxAge - time.Second)}}
	fixture.api.playback.jellyfinSessions = map[string]map[string]time.Time{"test": {"session": now.Add(-jellyfinSessionMaxAge - time.Second)}}
	if counts := fixture.api.playback.GetPlayingCounts(now); counts["test"] != 0 {
		t.Fatalf("expected expired Jellyfin session to be pruned, got %v", counts)
	}
}

func TestVodIDFromURL(t *testing.T) {
	got := VODIDFromURL("https://www.twitch.tv/someone/videos/123456789?filter=archives")
	if got != "123456789" {
		t.Fatalf("expected twitch vod id, got %q", got)
	}
}
