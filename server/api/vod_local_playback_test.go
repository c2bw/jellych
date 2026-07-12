package api

import (
	"context"
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

func localPlaybackTestAPI(t *testing.T, operations StreamOperations) *API {
	t.Helper()
	state := &APIState{}
	state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789"}})
	return NewWithDependencies(state, Dependencies{Streams: operations})
}

func TestDownloadedVODPlaybackServesLocalMKV(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("local-vod-data"), 0644); err != nil {
		t.Fatal(err)
	}
	resolvedRemote := false
	api := localPlaybackTestAPI(t, StreamOperations{
		OpenVODDownload: func(string) (*os.File, error) { return os.Open(path) },
		ResolveVODPlaylist: func(context.Context, string) ([]byte, error) {
			resolvedRemote = true
			return nil, errors.New("remote resolver must not be called")
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/vod/123456789/file.mkv", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "local-vod-data" {
		t.Fatalf("local playback = status %d body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Fatalf("Content-Type = %q; want video/x-matroska", got)
	}
	if got := rec.Header().Get("Content-Disposition"); got != "inline" {
		t.Fatalf("Content-Disposition = %q; want inline", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "14" {
		t.Fatalf("Content-Length = %q; want 14", got)
	}
	if resolvedRemote {
		t.Fatal("downloaded VOD playback resolved Twitch")
	}
}

func TestDownloadedVODPlaybackSupportsRanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("local-vod-data"), 0644); err != nil {
		t.Fatal(err)
	}
	api := localPlaybackTestAPI(t, StreamOperations{
		OpenVODDownload: func(string) (*os.File, error) { return os.Open(path) },
	})

	req := httptest.NewRequest(http.MethodGet, "/vod/123456789/file.mkv", nil)
	req.Header.Set("Range", "bytes=6-8")
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent || rec.Body.String() != "vod" {
		t.Fatalf("range playback = status %d body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 6-8/14" {
		t.Fatalf("Content-Range = %q; want bytes 6-8/14", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q; want bytes", got)
	}
}

func TestDownloadedVODPlaybackSupportsHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "123456789.mkv")
	if err := os.WriteFile(path, []byte("local-vod-data"), 0644); err != nil {
		t.Fatal(err)
	}
	api := localPlaybackTestAPI(t, StreamOperations{
		OpenVODDownload: func(string) (*os.File, error) { return os.Open(path) },
	})

	req := httptest.NewRequest(http.MethodHead, "/vod/123456789/file.mkv", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD playback = status %d body length %d", rec.Code, rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Length"); got != "14" {
		t.Fatalf("Content-Length = %q; want 14", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Fatalf("Content-Type = %q; want video/x-matroska", got)
	}
}

func TestLocalVODEndpointRedirectsToTwitchPlaybackWithoutCompletedDownload(t *testing.T) {
	for _, openErr := range []error{stream.ErrVODDownloadsDisabled, stream.ErrVODDownloadNotFound} {
		t.Run(openErr.Error(), func(t *testing.T) {
			api := localPlaybackTestAPI(t, StreamOperations{
				OpenVODDownload: func(string) (*os.File, error) { return nil, openErr },
			})

			req := httptest.NewRequest(http.MethodGet, "/vod/123456789/file.mkv", nil)
			rec := httptest.NewRecorder()
			api.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusTemporaryRedirect {
				t.Fatalf("remote fallback status = %d; want %d", rec.Code, http.StatusTemporaryRedirect)
			}
			if got := rec.Header().Get("Location"); got != "/vod/123456789/index.m3u8" {
				t.Fatalf("Location = %q; want Twitch playback endpoint", got)
			}
		})
	}
}

func TestVODPlaybackReturnsErrorForLocalFilesystemFailure(t *testing.T) {
	resolvedRemote := false
	api := localPlaybackTestAPI(t, StreamOperations{
		OpenVODDownload: func(string) (*os.File, error) { return nil, errors.New("permission denied") },
		ResolveVODPlaylist: func(context.Context, string) ([]byte, error) {
			resolvedRemote = true
			return nil, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/vod/123456789/file.mkv", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("filesystem failure status = %d; want %d", rec.Code, http.StatusInternalServerError)
	}
	if resolvedRemote {
		t.Fatal("filesystem failure unexpectedly fell back to Twitch")
	}
}

func TestVODPlaylistEndpointAlwaysReturnsHLS(t *testing.T) {
	openedLocal := false
	api := localPlaybackTestAPI(t, StreamOperations{
		OpenVODDownload: func(string) (*os.File, error) {
			openedLocal = true
			return nil, errors.New("must not open local media from an HLS URL")
		},
		ResolveVODPlaylist: func(context.Context, string) ([]byte, error) {
			return []byte("#EXTM3U\n#EXT-X-ENDLIST\n"), nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/vod/123456789/index.m3u8", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "#EXT-X-ENDLIST") {
		t.Fatalf("HLS playback = status %d body %q", rec.Code, rec.Body.String())
	}
	if openedLocal {
		t.Fatal("HLS endpoint opened the local MKV")
	}
	if got := rec.Header().Get("Content-Type"); got != "application/vnd.apple.mpegurl" {
		t.Fatalf("Content-Type = %q; want HLS playlist", got)
	}
}

func TestVODM3UAdvertisesDownloadedVODAsMKV(t *testing.T) {
	state := &APIState{}
	state.SetPlaylistBaseURL("https://jellych.test")
	state.SetVODs([]VOD{{ID: "123456789", URL: "https://www.twitch.tv/videos/123456789"}})
	api := NewWithDependencies(state, Dependencies{Streams: StreamOperations{
		VODDownloadStatus: func(string) (bool, time.Time, error) { return true, time.Time{}, nil },
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/vods.m3u", nil)
	rec := httptest.NewRecorder()
	api.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("VOD M3U status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "https://jellych.test/vod/123456789/file.mkv") {
		t.Fatalf("VOD M3U did not advertise local MKV: %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/vod/123456789/index.m3u8") {
		t.Fatalf("VOD M3U still advertised Twitch HLS: %q", rec.Body.String())
	}
}
