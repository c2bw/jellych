package manager

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c2bw/jellych/server/api"
	twitchapi "github.com/c2bw/jellych/twitch/api"
	"github.com/c2bw/jellych/twitch/client"
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

func TestStartReturnsConfigDirectoryErrors(t *testing.T) {
	tmp := t.TempDir()
	baseFile := filepath.Join(tmp, "not-a-directory")
	if err := os.WriteFile(baseFile, []byte("x"), 0644); err != nil {
		t.Fatalf("failed to create base file: %v", err)
	}

	_, err := Start(baseFile)
	if err == nil {
		t.Fatal("expected Start to return config directory error")
	}
}

func TestStartCreatesEmptySQLiteDatabaseAndIgnoresLegacyJSON(t *testing.T) {
	tmp := t.TempDir()
	legacyPath := filepath.Join(tmp, "channels.json")
	legacy := []byte(`[{"name":"jankos"}]`)
	if err := os.WriteFile(legacyPath, legacy, 0644); err != nil {
		t.Fatalf("failed to write legacy channels file: %v", err)
	}
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(m.channels) != 0 || len(m.vods) != 0 || len(m.vodBlacklist) != 0 {
		t.Fatalf("expected new database to start empty, got %#v %#v %#v", m.channels, m.vods, m.vodBlacklist)
	}
	if _, err := os.Stat(filepath.Join(tmp, databaseFile)); err != nil {
		t.Fatalf("expected database file to exist: %v", err)
	}
	unchanged, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("failed to read legacy channels file: %v", err)
	}
	if string(unchanged) != string(legacy) {
		t.Fatalf("expected legacy file to remain unchanged, got %s", unchanged)
	}
}

func TestConfigurationPersistsInSQLiteAcrossRestart(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	if _, err := m.AddChannel("jankos"); err != nil {
		t.Fatalf("failed to add channel: %v", err)
	}
	vod := api.VOD{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Test", Channel: "Jankos", Logo: "logo", Date: "date", Duration: "2h3m4s"}
	if err := m.AddVOD(vod); err != nil {
		t.Fatalf("failed to add vod: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}
	m, err = Start(tmp)
	if err != nil {
		t.Fatalf("expected restart to succeed, got %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(m.channels) != 1 || m.channels[0].Name != "jankos" {
		t.Fatalf("expected channel to survive restart, got %#v", m.channels)
	}
	if got := m.ListVODs(); len(got) != 1 || got[0] != vod {
		t.Fatalf("expected vod metadata to survive restart, got %#v", got)
	}
}

func TestStartRejectsNewerSchemaVersion(t *testing.T) {
	tmp := t.TempDir()
	db := openRawDatabase(t, tmp)
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatalf("failed to set schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	_, err := Start(tmp)
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
}

func TestStartMigratesVersionOneVODDuration(t *testing.T) {
	tmp := t.TempDir()
	db := openRawDatabase(t, tmp)
	for _, statement := range []string{
		`CREATE TABLE channels (name TEXT PRIMARY KEY, icon_url TEXT NOT NULL DEFAULT '', position INTEGER NOT NULL UNIQUE)`,
		`CREATE TABLE vods (id TEXT PRIMARY KEY, url TEXT NOT NULL, title TEXT NOT NULL DEFAULT '', channel TEXT NOT NULL DEFAULT '', logo TEXT NOT NULL DEFAULT '', date TEXT NOT NULL DEFAULT '', position INTEGER NOT NULL UNIQUE)`,
		`CREATE TABLE vod_blacklist (id TEXT PRIMARY KEY)`,
		`INSERT INTO vods (id, url, title, position) VALUES ('123', 'https://www.twitch.tv/videos/123', 'Twitch VOD 123', 0)`,
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("failed to prepare version one database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected migration to succeed, got %v", err)
	}
	defer m.Close()
	vods := m.ListVODs()
	if len(vods) != 1 || vods[0].Duration != "" {
		t.Fatalf("expected migrated VOD with empty duration, got %#v", vods)
	}
	var version int
	if err := m.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("failed to read schema version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("expected schema version %d, got %d", currentSchemaVersion, version)
	}
}

func TestRefreshSavedVODsUpdatesDurationAndPrunesUnavailableVODs(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	for _, id := range []string{"100", "200", "300", "400"} {
		if err := m.AddVOD(api.VOD{ID: id, URL: "https://www.twitch.tv/videos/" + id}); err != nil {
			t.Fatalf("failed to add VOD %s: %v", id, err)
		}
	}

	previous := fetchVODsByIDsContext
	fetchVODsByIDsContext = func(_ context.Context, _, _ string, ids []string) (*twitchapi.VideosResponse, error) {
		if len(ids) != 4 {
			t.Fatalf("expected one batched metadata lookup, got %#v", ids)
		}
		return &twitchapi.VideosResponse{Data: []twitchapi.Video{
			{ID: "100", Duration: "2h3m4s"},
			{ID: "400", Duration: "1h"},
		}}, nil
	}
	t.Cleanup(func() { fetchVODsByIDsContext = previous })

	m.refreshSavedVODs(context.Background(), &client.TwitchClient{}, func(id string, remove func() error) (bool, error) {
		if id == "300" {
			return false, nil
		}
		err := remove()
		return err == nil, err
	}, true)

	got := m.ListVODs()
	if len(got) != 3 || got[0].ID != "100" || got[0].Duration != "2h3m4s" || got[1].ID != "300" || got[2].ID != "400" {
		t.Fatalf("expected available and downloaded VODs to remain with refreshed duration, got %#v", got)
	}
	if _, ok := m.vodBlacklist["200"]; !ok {
		t.Fatal("expected pruned VOD to be blacklisted")
	}
}

func TestRefreshSavedVODsDoesNotPruneAfterMetadataFailure(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.AddVOD(api.VOD{ID: "100", URL: "https://www.twitch.tv/videos/100"}); err != nil {
		t.Fatal(err)
	}
	previous := fetchVODsByIDsContext
	fetchVODsByIDsContext = func(context.Context, string, string, []string) (*twitchapi.VideosResponse, error) {
		return nil, errors.New("temporary Twitch failure")
	}
	t.Cleanup(func() { fetchVODsByIDsContext = previous })

	m.refreshSavedVODs(context.Background(), &client.TwitchClient{}, nil, true)
	if got := m.ListVODs(); len(got) != 1 || got[0].ID != "100" {
		t.Fatalf("expected VOD to survive failed metadata refresh, got %#v", got)
	}
}

func TestFailedSchemaMigrationRollsBack(t *testing.T) {
	tmp := t.TempDir()
	db := openRawDatabase(t, tmp)
	if _, err := db.Exec(`CREATE TABLE channels (broken TEXT)`); err != nil {
		t.Fatalf("failed to create conflicting table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	if _, err := Start(tmp); err == nil {
		t.Fatal("expected schema migration to fail")
	}
	db = openRawDatabase(t, tmp)
	defer db.Close()
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("failed to read schema version: %v", err)
	}
	if version != 0 {
		t.Fatalf("expected failed migration to leave schema version 0, got %d", version)
	}
}

func TestAddVODDoesNotChangeMemoryWhenDatabaseWriteFails(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	err = m.AddVOD(api.VOD{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Test"})
	if err == nil {
		t.Fatal("expected database write to fail")
	}
	if got := m.ListVODs(); len(got) != 0 {
		t.Fatalf("expected in-memory vod state to remain unchanged, got %#v", got)
	}
}

func TestAddChannelDoesNotDeadlock(t *testing.T) {
	tmp := t.TempDir()
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

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
	t.Cleanup(func() { _ = m.Close() })
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
	t.Cleanup(func() { _ = m.Close() })
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

	blacklist := readBlacklist(t, m.db)
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
	t.Cleanup(func() { _ = m.Close() })
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
	m, err := Start(tmp)
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if _, err := m.db.Exec(`INSERT INTO vod_blacklist (id) VALUES ('123')`); err != nil {
		t.Fatalf("failed to seed vod blacklist: %v", err)
	}
	m.vodBlacklist["123"] = struct{}{}

	if err := m.AddVOD(api.VOD{ID: "123", URL: "https://www.twitch.tv/videos/123", Title: "Manual"}); err != nil {
		t.Fatalf("expected manual add to succeed, got %v", err)
	}

	blacklist := readBlacklist(t, m.db)
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
		Duration:     "2h3m4s",
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
	if vod.Duration != "2h3m4s" {
		t.Fatalf("expected duration, got %q", vod.Duration)
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

func readBlacklist(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM vod_blacklist ORDER BY id`)
	if err != nil {
		t.Fatalf("failed to read vod blacklist: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("failed to scan vod blacklist: %v", err)
		}
		ids = append(ids, id)
	}
	return ids
}

func openRawDatabase(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, databaseFile))
	if err != nil {
		t.Fatalf("failed to open raw database: %v", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("failed to connect to raw database: %v", err)
	}
	return db
}
