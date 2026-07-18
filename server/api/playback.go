package api

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const playSessionTTL = 25 * time.Second
const idleStopTTL = 30 * time.Second
const idleSweepInterval = 5 * time.Second
const maxPlaybackSessionIDBytes = 256
const maxPlaybackSessionsPerChannel = 256
const jellyfinSessionMaxAge = 24 * time.Hour

// PlaybackTracker owns playback sessions and idle timers for one API instance.
type PlaybackTracker struct {
	playSessions map[string]map[string]time.Time
	// jellyfinSessions tracks sessions that originate from Jellyfin webhooks.
	// These sessions survive heartbeat expiry but retain a maximum lifetime.
	jellyfinSessions map[string]map[string]time.Time
	playMu           sync.Mutex
	idleTimers       map[string]time.Time
	idleMu           sync.Mutex
	now              func() time.Time
}

func newPlaybackTracker(now func() time.Time) *PlaybackTracker {
	return &PlaybackTracker{now: now}
}

// RecordPlaying updates the last-seen timestamp for a playback session.
func (p *PlaybackTracker) RecordPlaying(channel, sessionID string, now time.Time) bool {
	p.playMu.Lock()
	defer p.playMu.Unlock()
	if sessionID == "" || len(sessionID) > maxPlaybackSessionIDBytes {
		return false
	}
	if p.playSessions == nil {
		p.playSessions = make(map[string]map[string]time.Time)
	}
	p.pruneExpiredLocked(now)
	if p.playSessions[channel] == nil {
		p.playSessions[channel] = make(map[string]time.Time)
	}
	if _, exists := p.playSessions[channel][sessionID]; !exists && len(p.playSessions[channel]) >= maxPlaybackSessionsPerChannel {
		return false
	}
	p.playSessions[channel][sessionID] = now
	return true
}

// StopPlaying removes a playback session immediately.
func (p *PlaybackTracker) StopPlaying(channel, sessionID string) {
	p.playMu.Lock()
	defer p.playMu.Unlock()
	if sessions := p.playSessions[channel]; sessions != nil {
		delete(sessions, sessionID)
		if len(sessions) == 0 {
			delete(p.playSessions, channel)
		}
	}
	// Also remove any Jellyfin marker for this session so it won't be kept alive.
	if p.jellyfinSessions != nil {
		if js := p.jellyfinSessions[channel]; js != nil {
			delete(js, sessionID)
			if len(js) == 0 {
				delete(p.jellyfinSessions, channel)
			}
		}
	}
}

// GetPlayingCounts returns active playback counts per channel after pruning.
func (p *PlaybackTracker) GetPlayingCounts(now time.Time) map[string]int {
	p.playMu.Lock()
	defer p.playMu.Unlock()
	p.pruneExpiredLocked(now)
	if p.playSessions == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(p.playSessions))
	for channel, sessions := range p.playSessions {
		out[channel] = len(sessions)
	}
	return out
}

func (p *PlaybackTracker) pruneExpiredLocked(now time.Time) {
	for channel, sessions := range p.jellyfinSessions {
		for sessionID, markedAt := range sessions {
			if now.Sub(markedAt) >= jellyfinSessionMaxAge {
				delete(sessions, sessionID)
			}
		}
		if len(sessions) == 0 {
			delete(p.jellyfinSessions, channel)
		}
	}
	if p.playSessions == nil {
		return
	}
	cutoff := now.Add(-playSessionTTL)
	for channel, sessions := range p.playSessions {
		for sessionID, lastSeen := range sessions {
			if p.isJellyfinSessionLocked(channel, sessionID, now) {
				continue
			}
			if lastSeen.Before(cutoff) {
				delete(sessions, sessionID)
			}
		}
		if len(sessions) == 0 {
			delete(p.playSessions, channel)
		}
	}
}

func (p *PlaybackTracker) isJellyfinSessionLocked(channel, sessionID string, now time.Time) bool {
	if p.jellyfinSessions == nil {
		return false
	}
	js := p.jellyfinSessions[channel]
	if js == nil {
		return false
	}
	markedAt, ok := js[sessionID]
	if !ok {
		return false
	}
	if now.Sub(markedAt) < jellyfinSessionMaxAge {
		return true
	}
	delete(js, sessionID)
	if len(js) == 0 {
		delete(p.jellyfinSessions, channel)
	}
	return false
}

// MarkJellyfinSession records that a session was started via a Jellyfin webhook
// and should not be auto-pruned until an explicit stop is received.
// MarkJellyfinSession records a persistent Jellyfin playback session.
func (p *PlaybackTracker) MarkJellyfinSession(channel, sessionID string) bool {
	p.playMu.Lock()
	defer p.playMu.Unlock()
	if sessionID == "" || len(sessionID) > maxPlaybackSessionIDBytes {
		return false
	}
	if p.jellyfinSessions == nil {
		p.jellyfinSessions = make(map[string]map[string]time.Time)
	}
	now := p.now()
	p.pruneExpiredLocked(now)
	if p.jellyfinSessions[channel] == nil {
		p.jellyfinSessions[channel] = make(map[string]time.Time)
	}
	if _, exists := p.jellyfinSessions[channel][sessionID]; !exists && len(p.jellyfinSessions[channel]) >= maxPlaybackSessionsPerChannel {
		return false
	}
	p.jellyfinSessions[channel][sessionID] = now
	return true
}

// StartIdleMonitor reconciles this API instance's playback sessions until ctx
// is canceled.
func (a *API) StartIdleMonitor(ctx context.Context) {
	go a.idleMonitorLoop(ctx)
}

func (a *API) idleMonitorLoop(ctx context.Context) {
	ticker := time.NewTicker(idleSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.reconcileIdleStreams(a.now())
		case <-ctx.Done():
			return
		}
	}
}

func (a *API) reconcileIdleStreams(now time.Time) {
	counts := a.playback.GetPlayingCounts(now)
	active := a.streams.ActiveChannels()
	activeSet := make(map[string]struct{}, len(active))
	for _, name := range active {
		activeSet[name] = struct{}{}
	}

	var stopList []string

	a.playback.idleMu.Lock()
	if a.playback.idleTimers == nil {
		a.playback.idleTimers = make(map[string]time.Time)
	}
	for _, channel := range active {
		count := counts[channel]
		if count > 0 {
			delete(a.playback.idleTimers, channel)
			continue
		}
		startedAt, ok := a.playback.idleTimers[channel]
		if !ok {
			a.playback.idleTimers[channel] = now
			continue
		}
		if now.Sub(startedAt) >= idleStopTTL {
			delete(a.playback.idleTimers, channel)
			stopList = append(stopList, channel)
		}
	}
	for channel := range a.playback.idleTimers {
		if _, ok := activeSet[channel]; !ok {
			delete(a.playback.idleTimers, channel)
		}
	}
	a.playback.idleMu.Unlock()

	for _, channel := range stopList {
		if err := a.streams.StopChannel(channel); err != nil {
			slog.Error("failed to stop idle stream", "channel", channel, "error", err)
		}
	}
}
