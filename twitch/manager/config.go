package manager

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/c2bw/jellych/server/api"
	"github.com/c2bw/jellych/stream"
	"github.com/c2bw/jellych/twitch/manager/channel"
	_ "modernc.org/sqlite"
)

const (
	databaseFile         = "jellych.db"
	currentSchemaVersion = 2
)

func openDatabase(configPath string) (*sql.DB, error) {
	if err := os.MkdirAll(configPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %w", err)
	}
	dbPath := filepath.Join(configPath, databaseFile)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open config database: %w", err)
	}
	// SQLite pragmas are connection-scoped. A single connection also serializes
	// the small number of configuration writes made by this process.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("failed to configure config database: %w", err)
		}
	}
	if err := ensureSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func ensureSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("failed to read config database schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("config database schema version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	if version == currentSchemaVersion {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin config database migration: %w", err)
	}
	defer tx.Rollback()
	var statements []string
	if version == 0 {
		statements = append(statements,
			`CREATE TABLE channels (
			name TEXT PRIMARY KEY,
			icon_url TEXT NOT NULL DEFAULT '',
			position INTEGER NOT NULL UNIQUE
		)`,
			`CREATE TABLE vods (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			channel TEXT NOT NULL DEFAULT '',
			logo TEXT NOT NULL DEFAULT '',
			date TEXT NOT NULL DEFAULT '',
			duration TEXT NOT NULL DEFAULT '',
			position INTEGER NOT NULL UNIQUE
		)`,
			`CREATE TABLE vod_blacklist (id TEXT PRIMARY KEY)`)
	} else if version == 1 {
		statements = append(statements, `ALTER TABLE vods ADD COLUMN duration TEXT NOT NULL DEFAULT ''`)
	}
	statements = append(statements, fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion))
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("failed to migrate config database: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit config database migration: %w", err)
	}
	return nil
}

func (m *Manager) loadConfig() error {
	channels, err := loadChannels(m.db)
	if err != nil {
		return err
	}
	vods, err := loadVODs(m.db)
	if err != nil {
		return err
	}
	blacklist, err := loadVODBlacklist(m.db)
	if err != nil {
		return err
	}
	m.channels = channels
	m.vods = vods
	m.vodBlacklist = blacklist

	names := make([]string, 0, len(channels))
	logos := make(map[string]string)
	for _, c := range channels {
		names = append(names, c.Name)
		if c.IconURL != "" {
			logos[c.Name] = c.IconURL
		}
	}
	m.apiState.SetChannels(names)
	m.apiState.SetChannelLogos(logos)
	slog.Info("loaded configuration", "channels", len(channels), "vods", len(vods), "blacklist", len(blacklist))
	return nil
}

func loadChannels(db *sql.DB) ([]channel.Info, error) {
	rows, err := db.Query(`SELECT name, icon_url FROM channels ORDER BY position`)
	if err != nil {
		return nil, fmt.Errorf("failed to load channels: %w", err)
	}
	defer rows.Close()
	var items []channel.Info
	for rows.Next() {
		var item channel.Info
		if err := rows.Scan(&item.Name, &item.IconURL); err != nil {
			return nil, fmt.Errorf("failed to scan channel: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to load channels: %w", err)
	}
	normalized, changed, err := normalizeChannelConfig(items)
	if err != nil {
		return nil, err
	}
	if changed {
		return nil, fmt.Errorf("config database contains non-normalized channel data")
	}
	return normalized, nil
}

func loadVODs(db *sql.DB) ([]api.VOD, error) {
	rows, err := db.Query(`SELECT id, url, title, channel, logo, date, duration FROM vods ORDER BY position`)
	if err != nil {
		return nil, fmt.Errorf("failed to load vods: %w", err)
	}
	defer rows.Close()
	var items []api.VOD
	for rows.Next() {
		var item api.VOD
		if err := rows.Scan(&item.ID, &item.URL, &item.Title, &item.Channel, &item.Logo, &item.Date, &item.Duration); err != nil {
			return nil, fmt.Errorf("failed to scan vod: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to load vods: %w", err)
	}
	normalized, changed, err := normalizeVODConfig(items)
	if err != nil {
		return nil, err
	}
	if changed {
		return nil, fmt.Errorf("config database contains non-normalized vod data")
	}
	return normalized, nil
}

func loadVODBlacklist(db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.Query(`SELECT id FROM vod_blacklist`)
	if err != nil {
		return nil, fmt.Errorf("failed to load vod blacklist: %w", err)
	}
	defer rows.Close()
	items := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan vod blacklist: %w", err)
		}
		if strings.TrimSpace(id) != id || api.ValidateVODID(id) != nil {
			return nil, fmt.Errorf("invalid vod id %q in config database", id)
		}
		items[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to load vod blacklist: %w", err)
	}
	return items, nil
}

func normalizeChannelConfig(channels []channel.Info) ([]channel.Info, bool, error) {
	normalized := make([]channel.Info, 0, len(channels))
	seen := make(map[string]struct{}, len(channels))
	changed := false
	for _, c := range channels {
		name := strings.ToLower(strings.TrimSpace(c.Name))
		iconURL := strings.TrimSpace(c.IconURL)
		changed = changed || name != c.Name || iconURL != c.IconURL
		if err := stream.ValidateChannelName(name); err != nil {
			return nil, false, fmt.Errorf("invalid channel name %q in config database: %w", c.Name, err)
		}
		if _, ok := seen[name]; ok {
			return nil, false, fmt.Errorf("duplicate channel name %q in config database", name)
		}
		seen[name] = struct{}{}
		normalized = append(normalized, channel.Info{Name: name, IconURL: iconURL})
	}
	return normalized, changed, nil
}

func normalizeVODConfig(vods []api.VOD) ([]api.VOD, bool, error) {
	normalized := make([]api.VOD, 0, len(vods))
	seen := make(map[string]struct{}, len(vods))
	changed := false
	for _, vod := range vods {
		next := api.PrepareVOD(vod)
		changed = changed || next != vod
		if err := api.ValidateVOD(next); err != nil {
			return nil, false, fmt.Errorf("invalid vod %q in config database: %w", vod.ID, err)
		}
		if _, ok := seen[next.ID]; ok {
			return nil, false, fmt.Errorf("duplicate vod id %q in config database", next.ID)
		}
		seen[next.ID] = struct{}{}
		normalized = append(normalized, next)
	}
	return normalized, changed, nil
}

func nextPositionContext(ctx context.Context, tx *sql.Tx, table string) (int, error) {
	var position int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM `+table).Scan(&position); err != nil {
		return 0, err
	}
	return position, nil
}

func (m *Manager) insertChannelContext(ctx context.Context, name, iconURL string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin channel update: %w", err)
	}
	defer tx.Rollback()
	position, err := nextPositionContext(ctx, tx, "channels")
	if err != nil {
		return fmt.Errorf("failed to determine channel position: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO channels (name, icon_url, position) VALUES (?, ?, ?)`, name, iconURL, position); err != nil {
		return fmt.Errorf("failed to save channel: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit channel update: %w", err)
	}
	return nil
}

func (m *Manager) deleteChannelContext(ctx context.Context, name string) error {
	result, err := m.db.ExecContext(ctx, `DELETE FROM channels WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("failed to remove channel: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return fmt.Errorf("failed to verify channel removal: %w", err)
		}
		return api.ErrChannelNotFound
	}
	return nil
}

func (m *Manager) insertVODContext(ctx context.Context, vod api.VOD) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin vod update: %w", err)
	}
	defer tx.Rollback()
	position, err := nextPositionContext(ctx, tx, "vods")
	if err != nil {
		return fmt.Errorf("failed to determine vod position: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vod_blacklist WHERE id = ?`, vod.ID); err != nil {
		return fmt.Errorf("failed to update vod blacklist: %w", err)
	}
	if err := insertVODRowContext(ctx, tx, vod, position); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit vod update: %w", err)
	}
	return nil
}

func (m *Manager) insertVODsContext(ctx context.Context, vods []api.VOD) error {
	if len(vods) == 0 {
		return nil
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin vod import: %w", err)
	}
	defer tx.Rollback()
	position, err := nextPositionContext(ctx, tx, "vods")
	if err != nil {
		return fmt.Errorf("failed to determine vod position: %w", err)
	}
	for _, vod := range vods {
		if err := insertVODRowContext(ctx, tx, vod, position); err != nil {
			return err
		}
		position++
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit vod import: %w", err)
	}
	return nil
}

func insertVODRowContext(ctx context.Context, tx *sql.Tx, vod api.VOD, position int) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO vods (id, url, title, channel, logo, date, duration, position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, vod.ID, vod.URL, vod.Title, vod.Channel, vod.Logo, vod.Date, vod.Duration, position)
	if err != nil {
		return fmt.Errorf("failed to save vod %q: %w", vod.ID, err)
	}
	return nil
}

func (m *Manager) updateVODDurationsContext(ctx context.Context, durations map[string]string) error {
	if len(durations) == 0 {
		return nil
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	m.mu.RLock()
	current := append([]api.VOD(nil), m.vods...)
	m.mu.RUnlock()

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin VOD duration update: %w", err)
	}
	defer tx.Rollback()
	updates := make(map[int]string)
	for i := range current {
		duration := strings.TrimSpace(durations[current[i].ID])
		if duration == "" || duration == current[i].Duration {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE vods SET duration = ? WHERE id = ?`, duration, current[i].ID); err != nil {
			return fmt.Errorf("failed to update VOD duration: %w", err)
		}
		updates[i] = duration
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit VOD duration update: %w", err)
	}

	m.mu.Lock()
	for i, duration := range updates {
		m.vods[i].Duration = duration
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) deleteVODContext(ctx context.Context, id string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin vod removal: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM vods WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to remove vod: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return fmt.Errorf("failed to verify vod removal: %w", err)
		}
		return api.ErrVODNotFound
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vod_blacklist (id) VALUES (?) ON CONFLICT(id) DO NOTHING`, id); err != nil {
		return fmt.Errorf("failed to update vod blacklist: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit vod removal: %w", err)
	}
	return nil
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for id := range in {
		out[id] = struct{}{}
	}
	return out
}
