package manager

import (
	"encoding/json"
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

func (m *Manager) loadVODs() error {
	var vods []api.VOD
	b, err := loadOrCreate(m.configPath, vodsFile, []byte("[]"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &vods); err != nil {
		slog.Error("failed to parse vods", "error", err)
		return err
	}
	vods, changed, err := normalizeVODConfig(vods)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.vods = vods
	m.mu.Unlock()
	if changed {
		if err := m.saveVODs(vods); err != nil {
			return err
		}
	}
	slog.Info("loaded vods", "count", len(vods))
	return nil
}

func (m *Manager) loadVODBlacklist() error {
	b, err := loadOrCreate(m.configPath, vodBlacklistFile, []byte("[]"))
	if err != nil {
		return err
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		slog.Error("failed to parse vod blacklist", "error", err)
		return err
	}
	blacklist, changed, err := normalizeVODBlacklist(ids)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.vodBlacklist = blacklist
	m.mu.Unlock()
	if changed {
		m.mu.Lock()
		err = m.saveVODBlacklistLocked()
		m.mu.Unlock()
		if err != nil {
			return err
		}
	}
	slog.Info("loaded vod blacklist", "count", len(blacklist))
	return nil
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

func (m *Manager) saveVODs(vods []api.VOD) error {
	return saveJSON(m.configPath, vodsFile, "vods", vods)
}

func (m *Manager) saveVODBlacklistLocked() error {
	ids := setToSortedStrings(m.vodBlacklist)
	return saveJSON(m.configPath, vodBlacklistFile, "vod blacklist", ids)
}

func (m *Manager) saveVODStateLocked() error {
	if err := m.saveVODs(m.vods); err != nil {
		return err
	}
	return m.saveVODBlacklistLocked()
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
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		slog.Error("failed to serialize "+label, "error", err)
		return err
	}
	if err := writeToFile(base, file, b); err != nil {
		return err
	}
	slog.Info("saved "+label, "count", len(value))
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
			if err := os.WriteFile(joined, defaultData, 0644); err != nil {
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
	if err := os.WriteFile(joined, data, 0644); err != nil {
		slog.Error("failed to write file", "path", joined, "error", err)
		return err
	}
	return nil
}
