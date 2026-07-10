package manager

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/c2bw/jellych/server/api"
	"github.com/c2bw/jellych/stream"
	"github.com/c2bw/jellych/twitch/manager/channel"
)

const channelsFile = "channels.json"
const vodsFile = "vods.json"
const vodBlacklistFile = "vods_blacklist.json"
const vodStateFile = "vod_state.json"

type persistedVODState struct {
	VODs      []api.VOD `json:"vods"`
	Blacklist []string  `json:"blacklist"`
}

func (m *Manager) loadChannels() error {
	var channels []channel.Info
	b, err := loadOrCreate(m.configPath, channelsFile, []byte("[]"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &channels); err != nil {
		slog.Error("failed to parse channels", "error", err)
		return err
	}
	channels, changed, err := normalizeChannelConfig(channels)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.channels = channels
	m.mu.Unlock()
	if changed {
		if err := m.saveChannels(channels); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(channels))
	logos := make(map[string]string)
	for _, c := range channels {
		names = append(names, c.Name)
		if c.IconURL != "" {
			logos[c.Name] = c.IconURL
		}
	}
	api.SetChannels(names)
	api.SetChannelLogos(logos)
	slog.Info("loaded channels", "count", len(channels))
	return nil
}

func (m *Manager) loadVODState() error {
	joined := filepath.Join(m.configPath, vodStateFile)
	b, err := os.ReadFile(joined)
	migrated := false
	if errors.Is(err, os.ErrNotExist) {
		b, err = m.readLegacyVODState()
		migrated = true
	}
	if err != nil {
		return err
	}

	var state persistedVODState
	if err := json.Unmarshal(b, &state); err != nil {
		return fmt.Errorf("failed to parse %s: %w", vodStateFile, err)
	}
	vods, vodsChanged, err := normalizeVODConfig(state.VODs)
	if err != nil {
		return err
	}
	blacklist, blacklistChanged, err := normalizeVODBlacklist(state.Blacklist)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.vods = vods
	m.vodBlacklist = blacklist
	if migrated || vodsChanged || blacklistChanged {
		err = m.saveVODStateLocked()
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	slog.Info("loaded vod state", "vods", len(vods), "blacklist", len(blacklist), "migrated", migrated)
	return nil
}

func (m *Manager) readLegacyVODState() ([]byte, error) {
	readLegacy := func(file string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(m.configPath, file))
		if errors.Is(err, os.ErrNotExist) {
			return []byte("[]"), nil
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read legacy %s: %w", file, err)
		}
		return b, nil
	}

	vodData, err := readLegacy(vodsFile)
	if err != nil {
		return nil, err
	}
	blacklistData, err := readLegacy(vodBlacklistFile)
	if err != nil {
		return nil, err
	}
	var state persistedVODState
	if err := json.Unmarshal(vodData, &state.VODs); err != nil {
		return nil, fmt.Errorf("failed to parse legacy %s: %w", vodsFile, err)
	}
	if err := json.Unmarshal(blacklistData, &state.Blacklist); err != nil {
		return nil, fmt.Errorf("failed to parse legacy %s: %w", vodBlacklistFile, err)
	}
	return json.Marshal(state)
}

func normalizeChannelConfig(channels []channel.Info) ([]channel.Info, bool, error) {
	normalized := make([]channel.Info, 0, len(channels))
	seen := make(map[string]struct{}, len(channels))
	changed := false

	for _, c := range channels {
		name := strings.ToLower(strings.TrimSpace(c.Name))
		if name != c.Name {
			changed = true
		}
		iconURL := strings.TrimSpace(c.IconURL)
		if iconURL != c.IconURL {
			changed = true
		}
		if err := stream.ValidateChannelName(name); err != nil {
			return nil, false, fmt.Errorf("invalid channel name %q in %s: %w", c.Name, channelsFile, err)
		}
		if _, ok := seen[name]; ok {
			return nil, false, fmt.Errorf("duplicate channel name %q in %s", name, channelsFile)
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
		if next != vod {
			changed = true
		}
		if err := api.ValidateVOD(next); err != nil {
			return nil, false, fmt.Errorf("invalid vod %q in %s: %w", vod.ID, vodsFile, err)
		}
		if _, ok := seen[next.ID]; ok {
			return nil, false, fmt.Errorf("duplicate vod id %q in %s", next.ID, vodsFile)
		}
		seen[next.ID] = struct{}{}
		normalized = append(normalized, next)
	}

	return normalized, changed, nil
}

func normalizeVODBlacklist(ids []string) (map[string]struct{}, bool, error) {
	blacklist := make(map[string]struct{}, len(ids))
	changed := false
	for _, id := range ids {
		normalized := strings.TrimSpace(id)
		if normalized != id {
			changed = true
		}
		if normalized == "" {
			changed = true
			continue
		}
		if err := api.ValidateVODID(normalized); err != nil {
			return nil, false, fmt.Errorf("invalid vod id %q in %s", id, vodBlacklistFile)
		}
		if _, ok := blacklist[normalized]; ok {
			changed = true
			continue
		}
		blacklist[normalized] = struct{}{}
	}
	return blacklist, changed, nil
}

func (m *Manager) saveChannels(channels []channel.Info) error {
	return saveJSON(m.configPath, channelsFile, "channels", channels)
}

func (m *Manager) saveVODStateLocked() error {
	state := persistedVODState{
		VODs:      append([]api.VOD{}, m.vods...),
		Blacklist: setToSortedStrings(m.vodBlacklist),
	}
	return saveJSONValue(m.configPath, vodStateFile, "vod state", state)
}

func (m *Manager) snapshotVODStateLocked() vodState {
	return vodState{
		vods:      append([]api.VOD(nil), m.vods...),
		blacklist: cloneStringSet(m.vodBlacklist),
	}
}

func (m *Manager) restoreVODStateLocked(state vodState) {
	m.vods = state.vods
	m.vodBlacklist = state.blacklist
}

func saveJSON[T any](base, file, label string, value []T) error {
	return saveJSONValue(base, file, label, value)
}

func saveJSONValue(base, file, label string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		slog.Error("failed to serialize "+label, "error", err)
		return err
	}
	if err := writeToFile(base, file, b); err != nil {
		return err
	}
	slog.Info("saved " + label)
	return nil
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	if in == nil {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for id := range in {
		out[id] = struct{}{}
	}
	return out
}

func setToSortedStrings(in map[string]struct{}) []string {
	out := make([]string, 0, len(in))
	for id := range in {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func loadOrCreate(base, file string, defaultData []byte) ([]byte, error) {
	joined := filepath.Join(base, file)
	b, err := os.ReadFile(joined)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(joined), 0755); err != nil {
				slog.Error("failed to create directory", "path", base, "file", file, "error", err)
				return nil, err
			}
			if err := writeToFile(base, file, defaultData); err != nil {
				slog.Error("failed to create file", "path", joined, "error", err)
				return nil, err
			} else {
				b = defaultData
			}
		} else {
			slog.Error("failed to read file", "path", joined, "error", err)
			return defaultData, err
		}
	}
	return b, nil
}

func writeToFile(base, file string, data []byte) error {
	joined := filepath.Join(base, file)
	dir := filepath.Dir(joined)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(file)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary config file: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(0644); err != nil {
		_ = temp.Close()
		return fmt.Errorf("failed to set temporary config permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("failed to write temporary config file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("failed to sync temporary config file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("failed to close temporary config file: %w", err)
	}
	if err := os.Rename(tempPath, joined); err != nil {
		slog.Error("failed to replace file", "path", joined, "error", err)
		return err
	}
	return nil
}
