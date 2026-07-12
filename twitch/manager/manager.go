package manager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/c2bw/jellych/server/api"
	twitchapi "github.com/c2bw/jellych/twitch/api"
	"github.com/c2bw/jellych/twitch/client"
	"github.com/c2bw/jellych/twitch/manager/channel"
)

const latestVODImportLimit = 5
const vodSyncInterval = 15 * time.Minute
const vodPruneEverySyncs = 2

var fetchVODsByIDsContext = twitchapi.VideosByIDsContext

type Manager struct {
	db           *sql.DB
	channels     []channel.Info
	vods         []api.VOD
	vodBlacklist map[string]struct{}
	statuses     []channel.Status
	twitchClient *client.TwitchClient
	mu           sync.RWMutex
}

func Start(configPath string) (*Manager, error) {
	db, err := openDatabase(configPath)
	if err != nil {
		return nil, err
	}
	m := &Manager{db: db, channels: []channel.Info{}}
	if err := m.loadConfig(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return m, nil
}

// Close releases the configuration database.
func (m *Manager) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	return m.db.Close()
}

func (m *Manager) SetTwitchClient(c *client.TwitchClient) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.twitchClient = c
}

func (m *Manager) ListVODs() []api.VOD {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]api.VOD(nil), m.vods...)
}

func (m *Manager) FindVOD(id string) (api.VOD, bool) {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, vod := range m.vods {
		if vod.ID == id {
			return vod, true
		}
	}
	return api.VOD{}, false
}

func (m *Manager) AddChannel(name string) (string, error) {
	iconURL := m.fetchChannelIconURL(name)

	m.mu.Lock()
	for _, c := range m.channels {
		if c.Name == name {
			m.mu.Unlock()
			return "", api.ErrChannelAlreadyExists
		}
	}
	if err := m.insertChannel(name, iconURL); err != nil {
		m.mu.Unlock()
		return "", err
	}
	m.channels = append(m.channels, channel.Info{Name: name, IconURL: iconURL})
	m.mu.Unlock()
	return iconURL, nil
}

func (m *Manager) AddVOD(vod api.VOD) error {
	var err error
	vod, err = m.enrichVOD(vod)
	if err != nil {
		return err
	}
	vod = api.PrepareVOD(vod)
	if err := api.ValidateVOD(vod); err != nil {
		return err
	}

	m.mu.Lock()
	for _, existing := range m.vods {
		if existing.ID == vod.ID {
			m.mu.Unlock()
			return api.ErrVODAlreadyExists
		}
	}
	if err := m.insertVOD(vod); err != nil {
		m.mu.Unlock()
		return err
	}
	delete(m.vodBlacklist, vod.ID)
	m.vods = append(m.vods, vod)
	m.mu.Unlock()
	return nil
}

func (m *Manager) enrichVOD(vod api.VOD) (api.VOD, error) {
	vod = api.PrepareVOD(vod)
	if vod.ID == "" {
		return vod, nil
	}

	m.mu.RLock()
	c := m.twitchClient
	m.mu.RUnlock()
	if c == nil {
		return vod, nil
	}

	videos, err := twitchapi.VideosByID(c.ClientID(), c.AccessToken(), vod.ID)
	if err != nil {
		return vod, fmt.Errorf("failed to fetch Twitch VOD metadata: %w", err)
	}
	if len(videos.Data) == 0 {
		return vod, fmt.Errorf("twitch VOD metadata not found for id %s", vod.ID)
	}

	enriched := vodFromTwitchVideo(videos.Data[0])
	if enriched.ID == "" {
		enriched.ID = vod.ID
	}
	if enriched.URL == "" {
		enriched.URL = vod.URL
	}
	if enriched.Title == "" {
		enriched.Title = vod.Title
	}
	return api.PrepareVOD(enriched), nil
}

func (m *Manager) addImportedVODs(items []api.VOD) (int, error) {
	if len(items) == 0 {
		return 0, nil
	}

	m.mu.Lock()
	existing := make(map[string]struct{}, len(m.vods)+len(items))
	for _, vod := range m.vods {
		existing[vod.ID] = struct{}{}
	}
	blacklisted := cloneStringSet(m.vodBlacklist)

	next := append([]api.VOD(nil), m.vods...)
	added := 0
	for _, vod := range items {
		vod = api.PrepareVOD(vod)
		if err := api.ValidateVOD(vod); err != nil {
			slog.Warn("skipping invalid imported vod", "id", vod.ID, "url", vod.URL, "error", err)
			continue
		}
		if _, ok := existing[vod.ID]; ok {
			continue
		}
		if _, ok := blacklisted[vod.ID]; ok {
			continue
		}
		existing[vod.ID] = struct{}{}
		next = append(next, vod)
		added++
	}
	if added == 0 {
		m.mu.Unlock()
		return 0, nil
	}

	toInsert := append([]api.VOD(nil), next[len(m.vods):]...)
	if err := m.insertVODs(toInsert); err != nil {
		m.mu.Unlock()
		return 0, err
	}
	m.vods = next
	m.mu.Unlock()

	return added, nil
}

func (m *Manager) RemoveVOD(id string) error {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	for i, vod := range m.vods {
		if vod.ID == id {
			if err := m.deleteVOD(id); err != nil {
				m.mu.Unlock()
				return err
			}
			m.vods = append(m.vods[:i], m.vods[i+1:]...)
			m.vodBlacklist[id] = struct{}{}
			m.mu.Unlock()
			return nil
		}
	}
	m.mu.Unlock()
	return api.ErrVODNotFound
}

func (m *Manager) RemoveChannel(name string) error {
	m.mu.Lock()
	for i, c := range m.channels {
		if c.Name == name {
			if err := m.deleteChannel(name); err != nil {
				m.mu.Unlock()
				return err
			}
			m.channels = append(m.channels[:i], m.channels[i+1:]...)
			m.mu.Unlock()
			return nil
		}
	}
	m.mu.Unlock()
	return api.ErrChannelNotFound
}

func (m *Manager) UpdateStatus(ctx context.Context, c *client.TwitchClient) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		names := m.channelNames()

		if len(names) == 0 {
			m.mu.Lock()
			m.statuses = nil
			m.mu.Unlock()
			api.SetChannelStatus(nil)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}

		status, err := channel.FetchStatusContext(ctx, c, names)
		if err != nil {
			slog.Error("failed to fetch channel status", "error", err)
		} else {
			status = normalizeStatuses(names, status)
			logStatuses(status)

			m.mu.Lock()
			m.statuses = status
			m.mu.Unlock()
			api.SetChannelStatus(toAPIStatuses(status))
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// SyncVODs imports recent VODs and refreshes saved metadata every 15 minutes.
// Confirmed-missing VODs are pruned at startup and every second sync.
func (m *Manager) SyncVODs(ctx context.Context, c *client.TwitchClient, pruneVOD func(string, func() error) (bool, error)) {
	ticker := time.NewTicker(vodSyncInterval)
	defer ticker.Stop()
	for syncNumber := 0; ; syncNumber++ {
		m.importLatestVODsOnce(ctx, c)
		m.refreshSavedVODs(ctx, c, pruneVOD, syncNumber%vodPruneEverySyncs == 0)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) refreshSavedVODs(ctx context.Context, c *client.TwitchClient, pruneVOD func(string, func() error) (bool, error), prune bool) {
	if c == nil {
		return
	}
	vods := m.ListVODs()
	ids := make([]string, 0, len(vods))
	for _, vod := range vods {
		ids = append(ids, vod.ID)
	}
	if len(ids) == 0 || ctx.Err() != nil {
		return
	}
	response, err := fetchVODsByIDsContext(ctx, c.ClientID(), c.AccessToken(), ids)
	if err != nil {
		slog.Warn("failed to refresh saved Twitch VODs", "error", err)
		return
	}
	available := make(map[string]twitchapi.Video, len(response.Data))
	durations := make(map[string]string)
	for _, video := range response.Data {
		available[video.ID] = video
		if duration := strings.TrimSpace(video.Duration); duration != "" {
			durations[video.ID] = duration
		}
	}
	if err := m.updateVODDurations(durations); err != nil {
		slog.Warn("failed to update Twitch VOD durations", "error", err)
	}
	if !prune {
		return
	}
	for _, vod := range vods {
		if _, ok := available[vod.ID]; ok {
			continue
		}
		removeMetadata := func() error { return m.RemoveVOD(vod.ID) }
		var err error
		removed := false
		if pruneVOD != nil {
			removed, err = pruneVOD(vod.ID, removeMetadata)
		} else {
			err = removeMetadata()
			removed = err == nil
		}
		if err != nil && !errors.Is(err, api.ErrVODNotFound) {
			slog.Warn("failed to prune expired VOD", "id", vod.ID, "error", err)
			continue
		}
		if removed {
			slog.Info("pruned expired Twitch VOD", "id", vod.ID)
		}
	}
}

func (m *Manager) importLatestVODsOnce(ctx context.Context, c *client.TwitchClient) {
	if c == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	default:
	}

	names := m.channelNames()
	if len(names) == 0 {
		return
	}

	users, err := twitchapi.UserInfoContext(ctx, c.ClientID(), c.AccessToken(), names)
	if err != nil {
		slog.Error("failed to fetch Twitch users for VOD import", "error", err)
		return
	}

	var imported []api.VOD
	for _, user := range users.Data {
		select {
		case <-ctx.Done():
			return
		default:
		}
		videos, err := twitchapi.VideosByUserContext(ctx, c.ClientID(), c.AccessToken(), user.ID, latestVODImportLimit)
		if err != nil {
			slog.Error("failed to fetch latest Twitch VODs", "channel", user.Login, "error", err)
			continue
		}
		for _, video := range videos.Data {
			vod := vodFromTwitchVideo(video)
			if vod.ID != "" {
				imported = append(imported, vod)
			}
		}
	}

	added, err := m.addImportedVODs(imported)
	if err != nil {
		slog.Error("failed to save imported VODs", "error", err)
		return
	}
	if added > 0 {
		slog.Info("imported latest Twitch VODs", "added", added)
	}
}

func (m *Manager) channelNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.channels))
	for _, c := range m.channels {
		names = append(names, c.Name)
	}
	return names
}

func normalizeStatuses(names []string, status []channel.Status) []channel.Status {
	// Ensure offline channels are represented in the status list.
	statusByName := make(map[string]channel.Status, len(status))
	for _, s := range status {
		statusByName[strings.ToLower(s.Name)] = s
	}
	for _, name := range names {
		lower := strings.ToLower(name)
		if _, ok := statusByName[lower]; !ok {
			status = append(status, channel.Status{Name: name, Online: false})
		}
	}

	// sort statuses: online first, then by viewers descending; offline at the end
	sort.SliceStable(status, func(i, j int) bool {
		a := status[i]
		b := status[j]
		if a.Online && !b.Online {
			return true
		}
		if !a.Online && b.Online {
			return false
		}
		if a.Online && b.Online {
			return a.Viewers > b.Viewers
		}
		// both offline — keep original order
		return false
	})
	return status
}

func logStatuses(status []channel.Status) {
	for _, s := range status {
		slog.Debug("channel status", "name", s.Name, "online", s.Online, "viewers", s.Viewers, "game", s.Game)
	}
}

func toAPIStatuses(status []channel.Status) []api.Status {
	apiStatus := make([]api.Status, 0, len(status))
	for _, s := range status {
		apiStatus = append(apiStatus, api.Status{
			Name:    strings.ToLower(s.Name),
			Online:  s.Online,
			Viewers: s.Viewers,
			Title:   s.Title,
			Game:    s.Game,
		})
	}
	return apiStatus
}

func vodFromTwitchVideo(video twitchapi.Video) api.VOD {
	id := strings.TrimSpace(video.ID)
	title := strings.TrimSpace(video.Title)
	if title == "" && id != "" {
		title = "Twitch VOD " + id
	}
	return api.VOD{
		ID:       id,
		URL:      strings.TrimSpace(video.URL),
		Title:    title,
		Channel:  twitchVideoChannel(video),
		Logo:     normalizeTwitchThumbnail(video.ThumbnailURL),
		Date:     twitchVideoDate(video),
		Duration: strings.TrimSpace(video.Duration),
	}
}

func twitchVideoChannel(video twitchapi.Video) string {
	if userName := strings.TrimSpace(video.UserName); userName != "" {
		return userName
	}
	return strings.TrimSpace(video.UserLogin)
}

func twitchVideoDate(video twitchapi.Video) string {
	if publishedAt := strings.TrimSpace(video.PublishedAt); publishedAt != "" {
		return publishedAt
	}
	return strings.TrimSpace(video.CreatedAt)
}

func normalizeTwitchThumbnail(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = strings.ReplaceAll(raw, "%{width}", "320")
	raw = strings.ReplaceAll(raw, "%{height}", "180")
	return raw
}

func (m *Manager) fetchChannelIconURL(name string) string {
	m.mu.RLock()
	c := m.twitchClient
	m.mu.RUnlock()
	if c == nil {
		return ""
	}
	iconURL, err := channel.FetchIconURL(c, name)
	if err != nil {
		slog.Warn("failed to fetch channel icon", "channel", name, "error", err)
		return ""
	}
	return strings.TrimSpace(iconURL)
}
