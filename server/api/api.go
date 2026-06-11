package api

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
)

type Channel struct {
	Name string `json:"name"`
}

type VOD struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Channel string `json:"channel,omitempty"`
	Logo    string `json:"logo,omitempty"`
	Date    string `json:"date,omitempty"`
}

var (
	channelNames  []string
	channelLogos  map[string]string
	chMu          sync.RWMutex
	store         channelStore
	vods          []VOD
	vodStoreRef   vodStore
	vodMu         sync.RWMutex
	statusList    []Status
	statusMu      sync.RWMutex
	webhookSecret string
	webhookMu     sync.RWMutex
)

var ErrChannelAlreadyExists = errors.New("channel already exists")
var ErrChannelNotFound = errors.New("channel not found")
var ErrVODAlreadyExists = errors.New("vod already exists")
var ErrVODNotFound = errors.New("vod not found")

var twitchVODURLRE = regexp.MustCompile(`(?i)^https?://(?:[\w-]+\.)?twitch\.tv/(?:[\w-]+/)?v(?:ideos?)?/(\d+)(?:[/?#].*)?$`)
var vodIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type channelStore interface {
	AddChannel(name string) (string, error)
	RemoveChannel(name string) error
}

type vodStore interface {
	ListVODs() []VOD
	FindVOD(id string) (VOD, bool)
	AddVOD(vod VOD) error
	RemoveVOD(id string) error
}

// SetChannelStore configures the persistence backend for channel changes.
func SetChannelStore(s channelStore) {
	chMu.Lock()
	defer chMu.Unlock()
	store = s
}

// SetVODStore configures the persistence backend for VOD changes.
func SetVODStore(s vodStore) {
	vodMu.Lock()
	defer vodMu.Unlock()
	vodStoreRef = s
}

func configuredVODStore() vodStore {
	vodMu.RLock()
	defer vodMu.RUnlock()
	return vodStoreRef
}

// SetJellyfinWebhookSecret configures the shared secret required by
// POST /api/jellyfin/webhook via the X-Jellych-Secret header.
func SetJellyfinWebhookSecret(secret string) {
	webhookMu.Lock()
	defer webhookMu.Unlock()
	webhookSecret = strings.TrimSpace(secret)
}

func jellyfinWebhookSecret() string {
	webhookMu.RLock()
	defer webhookMu.RUnlock()
	return webhookSecret
}

// SetChannels replaces the in-memory channel list.
func SetChannels(c []string) {
	chMu.Lock()
	defer chMu.Unlock()
	channelNames = append([]string{}, c...)
}

// SetChannelLogos replaces the in-memory channel logo mapping.
func SetChannelLogos(logos map[string]string) {
	chMu.Lock()
	defer chMu.Unlock()
	if logos == nil {
		channelLogos = nil
		return
	}
	channelLogos = make(map[string]string, len(logos))
	for name, logo := range logos {
		channelLogos[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(logo)
	}
}

// GetChannelLogos returns a copy of the in-memory channel logo mapping.
func GetChannelLogos() map[string]string {
	chMu.RLock()
	defer chMu.RUnlock()
	out := make(map[string]string, len(channelLogos))
	for name, logo := range channelLogos {
		out[name] = logo
	}
	return out
}

// Status represents the current status of a channel as exposed by the API.
type Status struct {
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Viewers int    `json:"viewers,omitempty"`
	Title   string `json:"title,omitempty"`
	Game    string `json:"game,omitempty"`
}

// SetChannelStatus replaces the in-memory channel status list.
func SetChannelStatus(s []Status) {
	statusMu.Lock()
	defer statusMu.Unlock()
	statusList = append([]Status{}, s...)
}

// GetChannelStatus returns a copy of the in-memory channel status list.
func GetChannelStatus() []Status {
	statusMu.RLock()
	defer statusMu.RUnlock()
	return append([]Status{}, statusList...)
}

// GetChannels returns a copy of the in-memory channel list.
func GetChannels() []string {
	chMu.RLock()
	defer chMu.RUnlock()
	return append([]string{}, channelNames...)
}

func SetVODs(items []VOD) {
	vodMu.Lock()
	defer vodMu.Unlock()
	vods = cloneVODs(items)
}

func GetVODs() []VOD {
	if store := configuredVODStore(); store != nil {
		return store.ListVODs()
	}

	vodMu.RLock()
	defer vodMu.RUnlock()
	return cloneVODs(vods)
}

func AddVOD(vod VOD) error {
	vod = PrepareVOD(vod)
	if err := ValidateVOD(vod); err != nil {
		return err
	}

	if store := configuredVODStore(); store != nil {
		return store.AddVOD(vod)
	}

	vodMu.Lock()
	defer vodMu.Unlock()
	for _, existing := range vods {
		if existing.ID == vod.ID {
			return ErrVODAlreadyExists
		}
	}
	vods = append(vods, vod)
	return nil
}

func RemoveVOD(id string) error {
	id = strings.TrimSpace(id)
	if store := configuredVODStore(); store != nil {
		return store.RemoveVOD(id)
	}

	vodMu.Lock()
	defer vodMu.Unlock()
	for i, vod := range vods {
		if vod.ID == id {
			vods = slices.Delete(vods, i, i+1)
			return nil
		}
	}
	return ErrVODNotFound
}

func FindVOD(id string) (VOD, bool) {
	id = strings.TrimSpace(id)
	if store := configuredVODStore(); store != nil {
		return store.FindVOD(id)
	}

	vodMu.RLock()
	defer vodMu.RUnlock()
	for _, vod := range vods {
		if vod.ID == id {
			return vod, true
		}
	}
	return VOD{}, false
}

// AddChannel adds a channel to the in-memory list if it doesn't already exist.
func AddChannel(name string) error {
	chMu.Lock()
	defer chMu.Unlock()
	if slices.Contains(channelNames, name) {
		return ErrChannelAlreadyExists
	}
	iconURL := ""
	if store != nil {
		var err error
		iconURL, err = store.AddChannel(name)
		if err != nil {
			return err
		}
	}
	channelNames = append(channelNames, name)
	if iconURL != "" {
		if channelLogos == nil {
			channelLogos = make(map[string]string)
		}
		channelLogos[name] = iconURL
	}
	return nil
}

func cloneVODs(items []VOD) []VOD {
	return append([]VOD{}, items...)
}

// NormalizeVOD trims all user-controlled string fields on a VOD.
func NormalizeVOD(vod VOD) VOD {
	vod.ID = strings.TrimSpace(vod.ID)
	vod.URL = strings.TrimSpace(vod.URL)
	vod.Title = strings.TrimSpace(vod.Title)
	vod.Channel = strings.TrimSpace(vod.Channel)
	vod.Logo = strings.TrimSpace(vod.Logo)
	vod.Date = strings.TrimSpace(vod.Date)
	return vod
}

// PrepareVOD normalizes a VOD and fills fields that can be derived locally.
func PrepareVOD(vod VOD) VOD {
	vod = NormalizeVOD(vod)
	if vod.ID == "" {
		vod.ID = VODIDFromURL(vod.URL)
	}
	if vod.Title == "" && vod.ID != "" {
		vod.Title = "Twitch VOD " + vod.ID
	}
	return vod
}

// ValidateVOD checks that a VOD has a valid id and HTTP(S) URL.
func ValidateVOD(vod VOD) error {
	if err := ValidateVODID(vod.ID); err != nil {
		return err
	}
	if vod.URL == "" {
		return fmt.Errorf("vod url required")
	}
	u, err := url.Parse(vod.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid vod url")
	}
	return nil
}

// ValidateVODID checks whether an id is safe to use in URLs and filenames.
func ValidateVODID(id string) error {
	if id == "" {
		return fmt.Errorf("vod id required")
	}
	if !vodIDRE.MatchString(id) {
		return fmt.Errorf("invalid vod id")
	}
	return nil
}

// VODIDFromURL extracts the numeric Twitch VOD id from a Twitch VOD URL.
func VODIDFromURL(raw string) string {
	match := twitchVODURLRE.FindStringSubmatch(strings.TrimSpace(raw))
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

// RemoveChannel removes a channel by name from the in-memory list.
func RemoveChannel(name string) error {
	chMu.Lock()
	defer chMu.Unlock()
	idx := slices.Index(channelNames, name)
	if idx < 0 {
		return ErrChannelNotFound
	}
	if store != nil {
		if err := store.RemoveChannel(name); err != nil {
			return err
		}
	}
	channelNames = slices.Delete(channelNames, idx, idx+1)
	if channelLogos != nil {
		delete(channelLogos, name)
	}
	return nil
}
