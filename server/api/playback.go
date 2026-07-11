package api

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/c2bw/jellych/stream"
)

const playSessionTTL = 25 * time.Second
const idleStopTTL = 30 * time.Second
const idleSweepInterval = 5 * time.Second
const maxPlaybackSessionIDBytes = 256
const maxPlaybackSessionsPerChannel = 256
const jellyfinSessionMaxAge = 24 * time.Hour

var (
	playSessions map[string]map[string]time.Time
	// jellyfinSessions tracks sessions that originate from Jellyfin webhooks.
	// These sessions survive heartbeat expiry but retain a maximum lifetime.
	jellyfinSessions map[string]map[string]time.Time
	playMu           sync.Mutex
	idleTimers       map[string]time.Time
	idleMu           sync.Mutex
)

// RecordPlaying updates the last-seen timestamp for a playback session.
func RecordPlaying(channel, sessionID string, now time.Time) bool {
	playMu.Lock()
	defer playMu.Unlock()
	if sessionID == "" || len(sessionID) > maxPlaybackSessionIDBytes {
		return false
	}
	if playSessions == nil {
		playSessions = make(map[string]map[string]time.Time)
	}
	pruneExpiredLocked(now)
	if playSessions[channel] == nil {
		playSessions[channel] = make(map[string]time.Time)
	}
	if _, exists := playSessions[channel][sessionID]; !exists && len(playSessions[channel]) >= maxPlaybackSessionsPerChannel {
		return false
	}
	playSessions[channel][sessionID] = now
	return true
}

// StopPlaying removes a playback session immediately.
func StopPlaying(channel, sessionID string) {
	playMu.Lock()
	defer playMu.Unlock()
	if sessions := playSessions[channel]; sessions != nil {
		delete(sessions, sessionID)
		if len(sessions) == 0 {
			delete(playSessions, channel)
		}
	}
	// Also remove any Jellyfin marker for this session so it won't be kept alive.
	if jellyfinSessions != nil {
		if js := jellyfinSessions[channel]; js != nil {
			delete(js, sessionID)
			if len(js) == 0 {
				delete(jellyfinSessions, channel)
			}
		}
	}
}

// GetPlayingCounts returns active playback counts per channel after pruning.
func GetPlayingCounts(now time.Time) map[string]int {
	playMu.Lock()
	defer playMu.Unlock()
	pruneExpiredLocked(now)
	if playSessions == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(playSessions))
	for channel, sessions := range playSessions {
		out[channel] = len(sessions)
	}
	return out
}

func pruneExpiredLocked(now time.Time) {
	for channel, sessions := range jellyfinSessions {
		for sessionID, markedAt := range sessions {
			if now.Sub(markedAt) >= jellyfinSessionMaxAge {
				delete(sessions, sessionID)
			}
		}
		if len(sessions) == 0 {
			delete(jellyfinSessions, channel)
		}
	}
	if playSessions == nil {
		return
	}
	cutoff := now.Add(-playSessionTTL)
	for channel, sessions := range playSessions {
		for sessionID, lastSeen := range sessions {
			if isJellyfinSessionLocked(channel, sessionID, now) {
				continue
			}
			if lastSeen.Before(cutoff) {
				delete(sessions, sessionID)
			}
		}
		if len(sessions) == 0 {
			delete(playSessions, channel)
		}
	}
}

func isJellyfinSessionLocked(channel, sessionID string, now time.Time) bool {
	if jellyfinSessions == nil {
		return false
	}
	js := jellyfinSessions[channel]
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
		delete(jellyfinSessions, channel)
	}
	return false
}

// MarkJellyfinSession records that a session was started via a Jellyfin webhook
// and should not be auto-pruned until an explicit stop is received.
func MarkJellyfinSession(channel, sessionID string) bool {
	playMu.Lock()
	defer playMu.Unlock()
	if sessionID == "" || len(sessionID) > maxPlaybackSessionIDBytes {
		return false
	}
	if jellyfinSessions == nil {
		jellyfinSessions = make(map[string]map[string]time.Time)
	}
	now := time.Now()
	pruneExpiredLocked(now)
	if jellyfinSessions[channel] == nil {
		jellyfinSessions[channel] = make(map[string]time.Time)
	}
	if _, exists := jellyfinSessions[channel][sessionID]; !exists && len(jellyfinSessions[channel]) >= maxPlaybackSessionsPerChannel {
		return false
	}
	jellyfinSessions[channel][sessionID] = now
	return true
}

// StartIdleMonitor reconciles playback sessions until ctx is canceled.
func StartIdleMonitor(ctx context.Context) {
	go idleMonitorLoop(ctx)
}

func idleMonitorLoop(ctx context.Context) {
	ticker := time.NewTicker(idleSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			reconcileIdleStreams(now)
		case <-ctx.Done():
			return
		}
	}
}

func reconcileIdleStreams(now time.Time) {
	counts := GetPlayingCounts(now)
	active := stream.ActiveChannels()
	activeSet := make(map[string]struct{}, len(active))
	for _, name := range active {
		activeSet[name] = struct{}{}
	}

	var stopList []string

	idleMu.Lock()
	if idleTimers == nil {
		idleTimers = make(map[string]time.Time)
	}
	for _, channel := range active {
		count := counts[channel]
		if count > 0 {
			delete(idleTimers, channel)
			continue
		}
		startedAt, ok := idleTimers[channel]
		if !ok {
			idleTimers[channel] = now
			continue
		}
		if now.Sub(startedAt) >= idleStopTTL {
			delete(idleTimers, channel)
			stopList = append(stopList, channel)
		}
	}
	for channel := range idleTimers {
		if _, ok := activeSet[channel]; !ok {
			delete(idleTimers, channel)
		}
	}
	idleMu.Unlock()

	for _, channel := range stopList {
		if err := stream.StopChannel(channel); err != nil {
			slog.Error("failed to stop idle stream", "channel", channel, "error", err)
		}
	}
}
