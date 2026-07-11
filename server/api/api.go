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

type APIState struct {
	channelNames  []string
	channelLogos  map[string]string
	chMu          sync.RWMutex
	store         channelStore
	channelOps    map[string]struct{}
	vods          []VOD
	vodStoreRef   vodStore
	vodMu         sync.RWMutex
	statusList    []Status
	statusMu      sync.RWMutex
	webhookSecret string
	webhookMu     sync.RWMutex
	controlSecret string
	controlMu     sync.RWMutex
}

var defaultState = &APIState{}

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
	defaultState.SetChannelStore(s)
}

func (s *APIState) SetChannelStore(store channelStore) {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	s.store = store
}

// SetVODStore configures the persistence backend for VOD changes.
func SetVODStore(s vodStore) {
	defaultState.SetVODStore(s)
}

func (s *APIState) SetVODStore(store vodStore) {
	s.vodMu.Lock()
	defer s.vodMu.Unlock()
	s.vodStoreRef = store
}

func (s *APIState) configuredVODStore() vodStore {
	s.vodMu.RLock()
	defer s.vodMu.RUnlock()
	return s.vodStoreRef
}

// SetJellyfinWebhookSecret configures the shared secret required by
// POST /api/jellyfin/webhook via the X-Jellych-Secret header.
func SetJellyfinWebhookSecret(secret string) {
	defaultState.SetJellyfinWebhookSecret(secret)
}

func (s *APIState) SetJellyfinWebhookSecret(secret string) {
	s.webhookMu.Lock()
	defer s.webhookMu.Unlock()
	s.webhookSecret = strings.TrimSpace(secret)
}

func (s *APIState) jellyfinWebhookSecret() string {
	s.webhookMu.RLock()
	defer s.webhookMu.RUnlock()
	return s.webhookSecret
}

// SetControlAPISecret configures optional authentication for mutating API routes.
func SetControlAPISecret(secret string) {
	defaultState.SetControlAPISecret(secret)
}

func (s *APIState) SetControlAPISecret(secret string) {
	s.controlMu.Lock()
	s.controlSecret = strings.TrimSpace(secret)
	s.controlMu.Unlock()
}

func (s *APIState) controlAPISecret() string {
	s.controlMu.RLock()
	defer s.controlMu.RUnlock()
	return s.controlSecret
}

// SetChannels replaces the in-memory channel list.
func SetChannels(c []string) {
	defaultState.SetChannels(c)
}

func (s *APIState) SetChannels(c []string) {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	s.channelNames = append([]string{}, c...)
}

// SetChannelLogos replaces the in-memory channel logo mapping.
func SetChannelLogos(logos map[string]string) {
	defaultState.SetChannelLogos(logos)
}

func (s *APIState) SetChannelLogos(logos map[string]string) {
	s.chMu.Lock()
	defer s.chMu.Unlock()
	if logos == nil {
		s.channelLogos = nil
		return
	}
	s.channelLogos = make(map[string]string, len(logos))
	for name, logo := range logos {
		s.channelLogos[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(logo)
	}
}

// GetChannelLogos returns a copy of the in-memory channel logo mapping.
func GetChannelLogos() map[string]string {
	return defaultState.GetChannelLogos()
}

func (s *APIState) GetChannelLogos() map[string]string {
	s.chMu.RLock()
	defer s.chMu.RUnlock()
	out := make(map[string]string, len(s.channelLogos))
	for name, logo := range s.channelLogos {
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
	defaultState.SetChannelStatus(s)
}

func (s *APIState) SetChannelStatus(statuses []Status) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	s.statusList = append([]Status{}, statuses...)
}

// GetChannelStatus returns a copy of the in-memory channel status list.
func GetChannelStatus() []Status {
	return defaultState.GetChannelStatus()
}

func (s *APIState) GetChannelStatus() []Status {
	s.statusMu.RLock()
	defer s.statusMu.RUnlock()
	return append([]Status{}, s.statusList...)
}

// GetChannels returns a copy of the in-memory channel list.
func GetChannels() []string {
	return defaultState.GetChannels()
}

func (s *APIState) GetChannels() []string {
	s.chMu.RLock()
	defer s.chMu.RUnlock()
	return append([]string{}, s.channelNames...)
}

// IsConfiguredChannel reports whether name is present in the configured
// channel list used to build live playlists.
func IsConfiguredChannel(name string) bool {
	return defaultState.IsConfiguredChannel(name)
}

func (s *APIState) IsConfiguredChannel(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	s.chMu.RLock()
	defer s.chMu.RUnlock()
	return slices.Contains(s.channelNames, name)
}

func SetVODs(items []VOD) {
	defaultState.SetVODs(items)
}

func (s *APIState) SetVODs(items []VOD) {
	s.vodMu.Lock()
	defer s.vodMu.Unlock()
	s.vods = cloneVODs(items)
}

func GetVODs() []VOD {
	return defaultState.GetVODs()
}

func (s *APIState) GetVODs() []VOD {
	if store := s.configuredVODStore(); store != nil {
		return store.ListVODs()
	}

	s.vodMu.RLock()
	defer s.vodMu.RUnlock()
	return cloneVODs(s.vods)
}

func AddVOD(vod VOD) error {
	return defaultState.AddVOD(vod)
}

func (s *APIState) AddVOD(vod VOD) error {
	vod = PrepareVOD(vod)
	if store := s.configuredVODStore(); store != nil {
		return store.AddVOD(vod)
	}
	if err := ValidateVOD(vod); err != nil {
		return err
	}

	s.vodMu.Lock()
	defer s.vodMu.Unlock()
	for _, existing := range s.vods {
		if existing.ID == vod.ID {
			return ErrVODAlreadyExists
		}
	}
	s.vods = append(s.vods, vod)
	return nil
}

func RemoveVOD(id string) error {
	return defaultState.RemoveVOD(id)
}

func (s *APIState) RemoveVOD(id string) error {
	id = strings.TrimSpace(id)
	if store := s.configuredVODStore(); store != nil {
		return store.RemoveVOD(id)
	}

	s.vodMu.Lock()
	defer s.vodMu.Unlock()
	for i, vod := range s.vods {
		if vod.ID == id {
			s.vods = slices.Delete(s.vods, i, i+1)
			return nil
		}
	}
	return ErrVODNotFound
}

func FindVOD(id string) (VOD, bool) {
	return defaultState.FindVOD(id)
}

func (s *APIState) FindVOD(id string) (VOD, bool) {
	id = strings.TrimSpace(id)
	if store := s.configuredVODStore(); store != nil {
		return store.FindVOD(id)
	}

	s.vodMu.RLock()
	defer s.vodMu.RUnlock()
	for _, vod := range s.vods {
		if vod.ID == id {
			return vod, true
		}
	}
	return VOD{}, false
}

// AddChannel adds a channel to the in-memory list if it doesn't already exist.
func AddChannel(name string) error {
	return defaultState.AddChannel(name)
}

func (s *APIState) AddChannel(name string) error {
	s.chMu.Lock()
	if slices.Contains(s.channelNames, name) {
		s.chMu.Unlock()
		return ErrChannelAlreadyExists
	}
	if s.channelOps == nil {
		s.channelOps = make(map[string]struct{})
	}
	if _, active := s.channelOps[name]; active {
		s.chMu.Unlock()
		return ErrChannelAlreadyExists
	}
	s.channelOps[name] = struct{}{}
	store := s.store
	s.chMu.Unlock()

	iconURL := ""
	if store != nil {
		var err error
		iconURL, err = store.AddChannel(name)
		if err != nil {
			s.chMu.Lock()
			delete(s.channelOps, name)
			s.chMu.Unlock()
			return err
		}
	}
	s.chMu.Lock()
	defer s.chMu.Unlock()
	delete(s.channelOps, name)
	s.channelNames = append(s.channelNames, name)
	if iconURL != "" {
		if s.channelLogos == nil {
			s.channelLogos = make(map[string]string)
		}
		s.channelLogos[name] = iconURL
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
	if vod.URL == "" && vod.ID != "" {
		vod.URL = "https://www.twitch.tv/videos/" + vod.ID
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
	return defaultState.RemoveChannel(name)
}

func (s *APIState) RemoveChannel(name string) error {
	s.chMu.Lock()
	idx := slices.Index(s.channelNames, name)
	if idx < 0 {
		s.chMu.Unlock()
		return ErrChannelNotFound
	}
	if s.channelOps == nil {
		s.channelOps = make(map[string]struct{})
	}
	if _, active := s.channelOps[name]; active {
		s.chMu.Unlock()
		return ErrChannelNotFound
	}
	s.channelOps[name] = struct{}{}
	store := s.store
	s.chMu.Unlock()

	if store != nil {
		if err := store.RemoveChannel(name); err != nil {
			s.chMu.Lock()
			delete(s.channelOps, name)
			s.chMu.Unlock()
			return err
		}
	}
	s.chMu.Lock()
	defer s.chMu.Unlock()
	delete(s.channelOps, name)
	idx = slices.Index(s.channelNames, name)
	if idx < 0 {
		return ErrChannelNotFound
	}
	s.channelNames = slices.Delete(s.channelNames, idx, idx+1)
	if s.channelLogos != nil {
		delete(s.channelLogos, name)
	}
	return nil
}
