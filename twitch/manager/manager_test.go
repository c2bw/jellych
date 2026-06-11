package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c2bw/jellych/server/api"
	twitchapi "github.com/c2bw/jellych/twitch/api"
	"github.com/c2bw/jellych/twitch/manager/channel"
)

func TestNormalizeChannelConfigLowercasesAndTrims(t *testing.T) {
	got, changed, err := normalizeChannelConfig([]channel.Info{
		{Name: " Jankos ", IconURL: " https://cdn.test/jankos.png "},
		{Name: "CAEDREL"},
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !changed {
		t.Fatal("expected config to be marked changed")
	}

	want := []string{"jankos", "caedrel"}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("expected channel %d to be %q, got %q", i, name, got[i].Name)
		}
	}
	if got[0].IconURL != "https://cdn.test/jankos.png" {
		t.Fatalf("expected normalized icon URL, got %q", got[0].IconURL)
	}
}

func TestNormalizeChannelConfigRejectsInvalidNames(t *testing.T) {
	_, _, err := normalizeChannelConfig([]channel.Info{{Name: "../bad"}})
	if err == nil {
		t.Fatal("expected invalid channel name error")
	}
	if !strings.Contains(err.Error(), "invalid channel name") {
		t.Fatalf("expected invalid channel name error, got %v", err)
	}
}

func TestNormalizeChannelConfigRejectsDuplicateNames(t *testing.T) {
	_, _, err := normalizeChannelConfig([]channel.Info{
		{Name: "Jankos"},
		{Name: "jankos"},
	})
	if err == nil {
		t.Fatal("expected duplicate channel name error")
	}
	if !strings.Contains(err.Error(), "duplicate channel name") {
		t.Fatalf("expected duplicate channel name error, got %v", err)
	}
}

func TestNormalizeVODConfigDerivesIDAndTitle(t *testing.T) {
	got, changed, err := normalizeVODConfig([]api.VOD{{
		URL: " https://www.twitch.tv/videos/123456789 ",
	}})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !changed {
		t.Fatal("expected config to be marked changed")
	}
	if got[0].ID != "123456789" {
		t.Fatalf("expected derived vod id, got %q", got[0].ID)
	}
	if got[0].Title != "Twitch VOD 123456789" {
		t.Fatalf("expected default title, got %q", got[0].Title)
	}
}

func TestNormalizeVODConfigKeepsChannelAndTitleSeparate(t *testing.T) {
	got, changed, err := normalizeVODConfig([]api.VOD{{
		ID:      "123",
		URL:     "https://www.twitch.tv/videos/123",
		Channel: "Streamer",
		Title:   "Great stream",
	}})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if changed {
		t.Fatal("did not expect config with explicit channel to be changed")
	}
	if got[0].Channel != "Streamer" {
		t.Fatalf("expected channel to stay set, got %q", got[0].Channel)
	}
	if got[0].Title != "Great stream" {
		t.Fatalf("expected title to stay unchanged, got %q", got[0].Title)
	}
}

func TestNormalizeVODConfigRejectsDuplicateIDs(t *testing.T) {
	_, _, err := normalizeVODConfig([]api.VOD{
		{ID: "123", URL: "https://www.twitch.tv/videos/123"},
		{ID: "123", URL: "https://www.twitch.tv/videos/123"},
	})
	if err == nil {
		t.Fatal("expected duplicate vod id error")
	}
	if !strings.Contains(err.Error(), "duplicate vod id") {
		t.Fatalf("expected duplicate vod id error, got %v", err)
	}
}

func TestLoadOrCreateReturnsCreateErrors(t *testing.T) {
	tmp := t.TempDir()
	baseFile := filepath.Join(tmp, "not-a-directory")
	if err := os.WriteFile(baseFile, []byte("x"), 0644); err != nil {
		t.Fatalf("failed to create base file: %v", err)
	}

	_, err := loadOrCreate(baseFile, channelsFile, []byte("[]"))
	if err == nil {
		t.Fatal("expected loadOrCreate to return create error")
	}
}

func TestStartRewritesNormalizedChannelsConfig(t *testing.T) {
	tmp := t.TempDir()
	initial := []byte("[{\"name\":\" Jankos \"},{\"name\":\"CAEDREL\"}]")
	if err := os.WriteFile(filepath.Join(tmp, channelsFile), initial, 0644); err != nil {
		t.Fatalf("failed to write initial channels file: %v", err)
	}

	if _, err := Start(tmp); err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}

	rewritten, err := os.ReadFile(filepath.Join(tmp, channelsFile))
	if err != nil {
		t.Fatalf("failed to read rewritten channels file: %v", err)
	}

	var got []channel.Info
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("failed to parse rewritten channels file: %v", err)
	}

	want := []string{"jankos", "caedrel"}
	if len(got) != len(want) {
		t.Fatalf("expected %d channels, got %d", len(want), len(got))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Fatalf("expected channel %d to be %q, got %q", i, name, got[i].Name)
		}
	}
}

func TestStartRewritesNormalizedVODConfig(t *testing.T) {
	tmp := t.TempDir()
	initial := []byte("[{\"url\":\" https://www.twitch.tv/videos/123456789 \"}]")
	if err := os.WriteFile(filepath.Join(tmp, vodsFile), initial, 0644); err != nil {
		t.Fatalf("failed to write initial vods file: %v", err)
	}

	if _, err := Start(tmp); err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}

	rewritten, err := os.ReadFile(filepath.Join(tmp, vodsFile))
	if err != nil {
		t.Fatalf("failed to read rewritten vods file: %v", err)
	}

	var got []api.VOD
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatalf("failed to parse rewritten vods file: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one vod, got %d", len(got))
	}
	if got[0].ID != "123456789" {
		t.Fatalf("expected derived vod id, got %q", got[0].ID)
	}
}

func TestAddChannelDoesNotDeadlock(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := m.AddChannel("jankos")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected AddChannel to succeed, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddChannel timed out; possible lock regression")
	}
}

func TestAddImportedVODsDeduplicatesAndSyncsAPI(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	api.SetVODStore(m)
	t.Cleanup(func() {
		api.SetVODStore(nil)
		api.SetVODs(nil)
	})

	added, err := m.addImportedVODs([]api.VOD{
		{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "First"},
		{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Duplicate"},
		{ID: "456", URL: "https://www.twitch.tv/videos/456", Title: "Second"},
	})
	if err != nil {
		t.Fatalf("expected import to succeed, got %v", err)
	}
	if added != 2 {
		t.Fatalf("expected 2 imported VODs, got %d", added)
	}

	vods := api.GetVODs()
	if len(vods) != 2 {
		t.Fatalf("expected API VOD list to contain 2 items, got %d", len(vods))
	}

	added, err = m.addImportedVODs([]api.VOD{
		{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Existing"},
	})
	if err != nil {
		t.Fatalf("expected duplicate import to succeed, got %v", err)
	}
	if added != 0 {
		t.Fatalf("expected duplicate import to add 0 VODs, got %d", added)
	}
}

func TestRemoveVODBlacklistsAndSkipsFutureImports(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	api.SetVODStore(m)
	t.Cleanup(func() {
		api.SetVODStore(nil)
		api.SetVODs(nil)
	})

	added, err := m.addImportedVODs([]api.VOD{
		{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "First"},
	})
	if err != nil {
		t.Fatalf("expected initial import to succeed, got %v", err)
	}
	if added != 1 {
		t.Fatalf("expected initial import to add 1 VOD, got %d", added)
	}

	if err := m.RemoveVOD("123"); err != nil {
		t.Fatalf("expected remove to succeed, got %v", err)
	}

	blacklistData, err := os.ReadFile(filepath.Join(tmp, vodBlacklistFile))
	if err != nil {
		t.Fatalf("failed to read vod blacklist: %v", err)
	}
	var blacklist []string
	if err := json.Unmarshal(blacklistData, &blacklist); err != nil {
		t.Fatalf("failed to parse vod blacklist: %v", err)
	}
	if len(blacklist) != 1 || blacklist[0] != "123" {
		t.Fatalf("expected blacklist to contain removed VOD, got %#v", blacklist)
	}

	added, err = m.addImportedVODs([]api.VOD{
		{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Removed"},
		{ID: "456", URL: "https://www.twitch.tv/videos/456", Title: "New"},
	})
	if err != nil {
		t.Fatalf("expected later import to succeed, got %v", err)
	}
	if added != 1 {
		t.Fatalf("expected only non-blacklisted VOD to be imported, got %d", added)
	}

	vods := api.GetVODs()
	if len(vods) != 1 || vods[0].ID != "456" {
		t.Fatalf("expected API VOD list to contain only VOD 456, got %#v", vods)
	}
}

func TestAPIVODStoreUsesManagerAsSourceOfTruth(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	api.SetVODStore(m)
	t.Cleanup(func() {
		api.SetVODStore(nil)
		api.SetVODs(nil)
	})

	if err := api.AddVOD(api.VOD{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Manual"}); err != nil {
		t.Fatalf("expected manual API add to succeed, got %v", err)
	}
	added, err := m.addImportedVODs([]api.VOD{
		{ID: "456", URL: "https://www.twitch.tv/videos/456", Title: "Imported"},
	})
	if err != nil {
		t.Fatalf("expected import to succeed, got %v", err)
	}
	if added != 1 {
		t.Fatalf("expected one imported VOD, got %d", added)
	}

	vods := api.GetVODs()
	if len(vods) != 2 {
		t.Fatalf("expected API VOD list to contain manual and imported VODs, got %#v", vods)
	}
	seen := map[string]bool{}
	for _, vod := range vods {
		seen[vod.ID] = true
	}
	if !seen["123"] || !seen["456"] {
		t.Fatalf("expected API VOD list to preserve both owners' changes, got %#v", vods)
	}
}

func TestAddVODRemovesIDFromBlacklist(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, vodBlacklistFile), []byte(`["123"]`), 0644); err != nil {
		t.Fatalf("failed to write initial vod blacklist: %v", err)
	}
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}

	if err := m.AddVOD(api.VOD{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Manual"}); err != nil {
		t.Fatalf("expected manual add to succeed, got %v", err)
	}

	blacklistData, err := os.ReadFile(filepath.Join(tmp, vodBlacklistFile))
	if err != nil {
		t.Fatalf("failed to read vod blacklist: %v", err)
	}
	var blacklist []string
	if err := json.Unmarshal(blacklistData, &blacklist); err != nil {
		t.Fatalf("failed to parse vod blacklist: %v", err)
	}
	if len(blacklist) != 0 {
		t.Fatalf("expected manual add to unblacklist VOD, got %#v", blacklist)
	}
}

func TestVODFromTwitchVideoUsesMetadata(t *testing.T) {
	vod := vodFromTwitchVideo(twitchapi.Video{
		ID:           "123",
		UserName:     "Streamer",
		Title:        "Great stream",
		PublishedAt:  "2026-06-02T12:34:56Z",
		URL:          "https://www.twitch.tv/videos/123",
		ThumbnailURL: "https://static-cdn.test/thumb-%{width}x%{height}.jpg",
	})

	if vod.ID != "123" {
		t.Fatalf("expected VOD id, got %q", vod.ID)
	}
	if vod.Channel != "Streamer" {
		t.Fatalf("expected channel, got %q", vod.Channel)
	}
	if vod.Title != "Great stream" {
		t.Fatalf("expected raw title, got %q", vod.Title)
	}
	if vod.Logo != "https://static-cdn.test/thumb-320x180.jpg" {
		t.Fatalf("expected normalized thumbnail, got %q", vod.Logo)
	}
	if vod.Date != "2026-06-02T12:34:56Z" {
		t.Fatalf("expected published date, got %q", vod.Date)
	}
}

func TestVODFromTwitchVideoFallsBackToCreatedAt(t *testing.T) {
	vod := vodFromTwitchVideo(twitchapi.Video{
		ID:        "123",
		CreatedAt: "2026-06-01T10:00:00Z",
		URL:       "https://www.twitch.tv/videos/123",
	})

	if vod.Date != "2026-06-01T10:00:00Z" {
		t.Fatalf("expected created date fallback, got %q", vod.Date)
	}
}
