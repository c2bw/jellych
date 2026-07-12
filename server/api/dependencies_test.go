package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAPIInstancesIsolateRuntimeStateAndOperations(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	stateOne := &APIState{}
	stateOne.SetChannels([]string{"one"})
	stateTwo := &APIState{}
	stateTwo.SetChannels([]string{"one"})

	var startsOne, startsTwo int
	apiOne := NewWithDependencies(stateOne, Dependencies{
		Now: func() time.Time { return now },
		Streams: StreamOperations{Start: func(string) error {
			startsOne++
			return nil
		}},
	})
	apiTwo := NewWithDependencies(stateTwo, Dependencies{
		Now: func() time.Time { return now },
		Streams: StreamOperations{Start: func(string) error {
			startsTwo++
			return nil
		}},
	})

	start := httptest.NewRequest(http.MethodPost, "/api/stream/one", nil)
	startResponse := httptest.NewRecorder()
	apiOne.Handler().ServeHTTP(startResponse, start)
	if startResponse.Code != http.StatusOK || startsOne != 1 || startsTwo != 0 {
		t.Fatalf("isolated stream start failed: status=%d startsOne=%d startsTwo=%d", startResponse.Code, startsOne, startsTwo)
	}

	playing := httptest.NewRequest(http.MethodPost, "/api/playing/one", strings.NewReader(`{"sessionId":"session","action":"start"}`))
	playingResponse := httptest.NewRecorder()
	apiOne.Handler().ServeHTTP(playingResponse, playing)
	if playingResponse.Code != http.StatusOK {
		t.Fatalf("record playback status = %d: %s", playingResponse.Code, playingResponse.Body.String())
	}
	if got := apiOne.playback.GetPlayingCounts(now)["one"]; got != 1 {
		t.Fatalf("first API playback count = %d; want 1", got)
	}
	if got := apiTwo.playback.GetPlayingCounts(now)["one"]; got != 0 {
		t.Fatalf("second API inherited playback count %d", got)
	}

	playlist, err := apiOne.rewriteVODPlaylist("vod", []byte("#EXTM3U\nhttps://example.test/segment.ts\n"))
	if err != nil {
		t.Fatalf("rewrite playlist: %v", err)
	}
	mediaPath := strings.TrimSpace(strings.Split(string(playlist), "\n")[1])
	token := mediaPath[strings.LastIndexByte(mediaPath, '/')+1:]
	if _, ok := apiOne.vodMediaRegistry.lookup("vod", token, now); !ok {
		t.Fatal("first API did not retain its VOD media token")
	}
	if _, ok := apiTwo.vodMediaRegistry.lookup("vod", token, now); ok {
		t.Fatal("second API inherited first API's VOD media token")
	}
}
