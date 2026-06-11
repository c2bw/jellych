package api

import (
	"log/slog"
	"sync"
	"time"

	"github.com/c2bw/jellych/stream"
)

const playSessionTTL = 25 * time.Second
const idleStopTTL = 30 * time.Second
const idleSweepInterval = 5 * time.Second

var (
	playSessions map[string]map[string]time.Time
	// jellyfinSessions tracks sessions that originate from Jellyfin webhooks.
	// These sessions should only be removed when an explicit "stop" is received.
	jellyfinSessions map[string]map[string]struct{}
	playMu           sync.Mutex
	idleTimers       map[string]time.Time
	idleOnce         sync.Once
	idleMu           sync.Mutex
)

// RecordPlaying updates the last-seen timestamp for a playback session.
func RecordPlaying(channel, sessionID string, now time.Time) {
	playMu.Lock()
	defer playMu.Unlock()
	if playSessions == nil {
		playSessions = make(map[string]map[string]time.Time)
	}
	if playSessions[channel] == nil {
		playSessions[channel] = make(map[string]time.Time)
	}
	playSessions[channel][sessionID] = now
	pruneExpiredLocked(now)
}

// StopPlaying removes a playback session immediately.
func StopPlaying(channel, sessionID string) {
	playMu.Lock()
	defer playMu.Unlock()
	if playSessions == nil {
		return
	}
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
	if playSessions == nil {
		return map[string]int{}
	}
	pruneExpiredLocked(now)
	out := make(map[string]int, len(playSessions))
	for channel, sessions := range playSessions {
		out[channel] = len(sessions)
	}
	return out
}

func pruneExpiredLocked(now time.Time) {
	if playSessions == nil {
		return
	}
	cutoff := now.Add(-playSessionTTL)
	for channel, sessions := range playSessions {
		for sessionID, lastSeen := range sessions {
			if isJellyfinSessionLocked(channel, sessionID) {
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

func isJellyfinSessionLocked(channel, sessionID string) bool {
	if jellyfinSessions == nil {
		return false
	}
	js := jellyfinSessions[channel]
	if js == nil {
		return false
	}
	_, ok := js[sessionID]
	return ok
}

// MarkJellyfinSession records that a session was started via a Jellyfin webhook
// and should not be auto-pruned until an explicit stop is received.
func MarkJellyfinSession(channel, sessionID string) {
	playMu.Lock()
	defer playMu.Unlock()
	if jellyfinSessions == nil {
		jellyfinSessions = make(map[string]map[string]struct{})
	}
	if jellyfinSessions[channel] == nil {
		jellyfinSessions[channel] = make(map[string]struct{})
	}
	jellyfinSessions[channel][sessionID] = struct{}{}
}

func startIdleMonitor() {
	idleOnce.Do(func() {
		go idleMonitorLoop()
	})
}

func idleMonitorLoop() {
	ticker := time.NewTicker(idleSweepInterval)
	defer ticker.Stop()
	for now := range ticker.C {
		reconcileIdleStreams(now)
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
