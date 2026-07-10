package stream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	twitch "github.com/c2bw/twitch-url-extractor"
)

var mu sync.Mutex
var mgrs map[string]*manager
var startLiveChannel = Start
var defaultStreamRegistry = NewStreamRegistry(&mu, &mgrs)

// ErrStopTimeout indicates a stream process did not exit before the shutdown timeout.
var ErrStopTimeout = errors.New("stop timeout")
var ErrAlreadyStarted = errors.New("stream already started")

var channelNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// ValidateChannelName enforces a conservative channel name format to avoid path traversal.
func ValidateChannelName(name string) error {
	if !channelNameRE.MatchString(name) {
		return fmt.Errorf("invalid channel name")
	}
	return nil
}

// ActiveChannels returns a slice of channel names that currently have
// active stream managers.
func ActiveChannels() []string {
	return defaultStreamRegistry.ActiveChannels()
}

// ActiveChannels returns a slice of channel names that currently have active
// stream managers.
func (r *StreamRegistry) ActiveChannels() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	managers := *r.managers
	if len(managers) == 0 {
		return nil
	}
	out := make([]string, 0, len(managers))
	for k := range managers {
		out = append(out, k)
	}
	return out
}

// IsChannelActive reports whether a channel currently has an active stream manager.
func IsChannelActive(channel string) bool {
	return defaultStreamRegistry.IsChannelActive(channel)
}

// IsChannelActive reports whether a channel currently has an active stream manager.
func (r *StreamRegistry) IsChannelActive(channel string) bool {
	if err := ValidateChannelName(channel); err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	managers := *r.managers
	if managers == nil {
		return false
	}
	m, ok := managers[channel]
	return ok && m != nil && m.started
}

// PlaylistSegmentCount returns the current number of media segment entries
// in the channel's index.m3u8 playlist.
func PlaylistSegmentCount(channel string) (int, error) {
	if err := ValidateChannelName(channel); err != nil {
		return 0, err
	}

	playlist := getLiveObject(channel, "index.m3u8")
	if playlist == nil {
		return 0, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(string(playlist)))
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}

	return count, nil
}

type manager struct {
	ctx    context.Context
	cancel context.CancelFunc
	// processes
	ffmpegCmd *exec.Cmd
	// bookkeeping
	started   bool
	startTime time.Time
	// wait groups / channels
	doneFF chan error
}

type StreamRegistry struct {
	mu       *sync.Mutex
	managers *map[string]*manager
}

func NewStreamRegistry(mu *sync.Mutex, managers *map[string]*manager) *StreamRegistry {
	return &StreamRegistry{mu: mu, managers: managers}
}

func (r *StreamRegistry) managerMap() map[string]*manager {
	if *r.managers == nil {
		*r.managers = make(map[string]*manager)
	}
	return *r.managers
}

// Start begins streaming for the given channel. It returns an error if
// streaming is already started or if commands fail to start.
func Start(channel string) error {
	return defaultStreamRegistry.Start(channel)
}

// Start begins streaming for the given channel. It returns an error if
// streaming is already started or if commands fail to start.
func (r *StreamRegistry) Start(channel string) error {
	if err := ValidateChannelName(channel); err != nil {
		return err
	}
	configuredLiveBaseURL := getLiveBaseURL()
	if configuredLiveBaseURL == "" {
		return errors.New("live base url not configured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &manager{
		ctx:       ctx,
		cancel:    cancel,
		doneFF:    make(chan error, 1),
		startTime: time.Now(),
	}

	r.mu.Lock()
	managers := r.managerMap()
	if existing, ok := managers[channel]; ok && existing.started {
		r.mu.Unlock()
		cancel()
		return ErrAlreadyStarted
	}
	// reserve this channel to avoid concurrent double-starts
	m.started = true
	managers[channel] = m
	r.mu.Unlock()

	resetLiveChannel(channel)

	inputURL, streamName, err := resolveLiveStreamURL(m.ctx, channel)
	if err != nil {
		return r.abortStart(channel, m, err)
	}
	slog.Info("resolved stream url", "channel", channel, "stream", streamName, "elapsed", time.Since(m.startTime))

	playlistURL := strings.TrimRight(configuredLiveBaseURL, "/") + liveWritePrefix + channel + "/index.m3u8"
	m.ffmpegCmd = exec.CommandContext(m.ctx, "ffmpeg", buildFFmpegHLSArgs(channel, inputURL, playlistURL, getServerBaseURL())...)
	stderrFF, err := m.ffmpegCmd.StderrPipe()
	if err != nil {
		return r.abortStart(channel, m, fmt.Errorf("failed to get ffmpeg stderr pipe: %w", err))
	}

	if err := m.ffmpegCmd.Start(); err != nil {
		return r.abortStart(channel, m, fmt.Errorf("failed to start ffmpeg: %w", err))
	}

	go logCommandOutput("ffmpeg", channel, stderrFF)

	go func() {
		err := m.ffmpegCmd.Wait()
		if err != nil {
			slog.Error("ffmpeg exited", "error", err)
		} else {
			slog.Info("ffmpeg exited normally")
		}
		m.doneFF <- err
		close(m.doneFF)
	}()

	go func() {
		select {
		case <-m.doneFF:
			slog.Info("ffmpeg finished", "elapsed", time.Since(m.startTime))
			cancel()
		case <-m.ctx.Done():
			// stop requested
		}

		// Remove manager from global map when finished
		r.deleteManager(channel, m)
	}()

	slog.Info("started", "ffmpeg_pid", m.ffmpegCmd.Process.Pid, "channel", channel)
	return nil
}

func resolveLiveStreamURL(ctx context.Context, channel string) (string, string, error) {
	streams, err := twitch.NewClient(nil).Streams(ctx, "https://twitch.tv/"+channel)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve stream URL: %w", err)
	}
	stream, ok := twitch.BestStream(streams)
	if !ok {
		return "", "", fmt.Errorf("no playable stream URL returned")
	}
	if err := validateHTTPURL(stream.URL); err != nil {
		return "", "", fmt.Errorf("stream URL extractor returned invalid stream url: %w", err)
	}
	return stream.URL, stream.Name, nil
}

func (r *StreamRegistry) abortStart(channel string, m *manager, err error) error {
	close(m.doneFF)
	r.deleteManager(channel, m)
	m.cancel()
	return err
}

// Stop attempts to gracefully stop any running stream. It's safe to call
// multiple times; it returns nil if no stream was running.
func Stop() error {
	return defaultStreamRegistry.Stop()
}

// Stop attempts to gracefully stop any running stream. It's safe to call
// multiple times; it returns nil if no stream was running.
func (r *StreamRegistry) Stop() error {
	r.mu.Lock()
	managers := *r.managers
	if len(managers) == 0 {
		r.mu.Unlock()
		return nil
	}
	// copy keys and managers to stop outside lock
	copyMap := make(map[string]*manager, len(managers))
	for k, v := range managers {
		copyMap[k] = v
		delete(managers, k)
	}
	r.mu.Unlock()

	var lastErr error
	for _, m := range copyMap {
		if err := stopManager(m); err != nil {
			lastErr = err
		}
	}
	for channel := range copyMap {
		clearLiveChannel(channel)
	}
	return lastErr
}

// StopChannel stops the stream for a specific channel. Returns nil if there was no active stream.
func StopChannel(channel string) error {
	return defaultStreamRegistry.StopChannel(channel)
}

// StopChannel stops the stream for a specific channel. Returns nil if there was no active stream.
func (r *StreamRegistry) StopChannel(channel string) error {
	if err := ValidateChannelName(channel); err != nil {
		return err
	}
	r.mu.Lock()
	managers := *r.managers
	if managers == nil {
		r.mu.Unlock()
		return nil
	}
	m, ok := managers[channel]
	if !ok {
		r.mu.Unlock()
		return nil
	}
	delete(managers, channel)
	r.mu.Unlock()
	clearLiveChannel(channel)
	return stopManager(m)
}

func stopManager(m *manager) error {
	if m == nil {
		return nil
	}
	// cancel context to propagate to commands created with it
	m.cancel()

	// try graceful interrupt
	if m.ffmpegCmd != nil && m.ffmpegCmd.Process != nil {
		_ = m.ffmpegCmd.Process.Signal(os.Interrupt)
	}

	// wait with timeout for both to exit
	deadline := time.Now().Add(5 * time.Second)
	if m.ffmpegCmd == nil || m.ffmpegCmd.Process == nil {
		return nil
	}
	if !waitForDone(m.doneFF, deadline) {
		// force kill
		if m.ffmpegCmd != nil && m.ffmpegCmd.Process != nil {
			_ = m.ffmpegCmd.Process.Kill()
		}
		return fmt.Errorf("%w: timed out stopping processes; killed", ErrStopTimeout)
	}

	slog.Info("processes terminated", "elapsed", time.Since(m.startTime))
	return nil
}

func (r *StreamRegistry) deleteManager(channel string, m *manager) {
	r.mu.Lock()
	managers := *r.managers
	if managers != nil {
		if cur, ok := managers[channel]; ok && cur == m {
			delete(managers, channel)
			clearLiveChannel(channel)
		}
	}
	r.mu.Unlock()
}

func buildFFmpegHLSArgs(channel, inputURL, playlistURL, publicBaseURL string) []string {
	args := []string{
		"-i", inputURL,
		"-c", "copy",
		"-f", "hls",
		"-method", "PUT",
		"-headers", liveWriteTokenHeader + ": " + getLiveWriteToken() + "\r\n",
		"-hls_time", "1",
		"-hls_list_size", "30",
		"-hls_delete_threshold", "30",
		"-hls_start_number_source", "epoch",
		"-hls_allow_cache", "0",
		"-hls_flags", "delete_segments",
	}

	if publicBaseURL = strings.TrimSpace(publicBaseURL); publicBaseURL != "" {
		hlsBaseURL := strings.TrimRight(publicBaseURL, "/") + "/live/" + channel + "/"
		args = append(args, "-hls_base_url", hlsBaseURL)
	}

	return append(args, playlistURL)
}

func logCommandOutput(command, channel string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Debug(command, "channel", channel, "line", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		slog.Debug(command+" log stream ended with error", "channel", channel, "error", err)
	}
}

func waitForDone(ch <-chan error, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	select {
	case <-ch:
		return true
	case <-time.After(remaining):
		return false
	}
}
