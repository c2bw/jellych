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

// ErrStopTimeout indicates a stream process did not exit before the shutdown timeout.
var ErrStopTimeout = errors.New("stop timeout")
var ErrAlreadyStarted = errors.New("stream already started")
var ErrStreamsStopping = errors.New("streams are stopping")

const defaultStreamStopTimeout = 5 * time.Second

var channelNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// ValidateChannelName enforces a conservative channel name format to avoid path traversal.
func ValidateChannelName(name string) error {
	if !channelNameRE.MatchString(name) {
		return fmt.Errorf("invalid channel name")
	}
	return nil
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
	return ok && m != nil
}

// PlaylistSegmentCount returns the current number of media segment entries
// in the channel's index.m3u8 playlist.
func (r *StreamRegistry) PlaylistSegmentCount(channel string) (int, error) {
	if err := ValidateChannelName(channel); err != nil {
		return 0, err
	}

	playlist := r.getObject(channel, "index.m3u8")
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
	ctx            context.Context
	cancel         context.CancelFunc
	cmd            streamCommand
	writeMu        sync.Mutex
	state          streamState
	generation     string
	startTime      time.Time
	done           chan struct{}
	finishOnce     sync.Once
	stopInProgress bool
}

type streamState uint8

const (
	streamStarting streamState = iota
	streamRunning
	streamStopping
)

type streamCommand interface {
	StderrPipe() (io.ReadCloser, error)
	Start() error
	Wait() error
	Signal(os.Signal) error
	Kill() error
	PID() int
}

type execStreamCommand struct {
	cmd *exec.Cmd
}

func (c *execStreamCommand) StderrPipe() (io.ReadCloser, error) {
	return c.cmd.StderrPipe()
}

func (c *execStreamCommand) Start() error {
	return c.cmd.Start()
}

func (c *execStreamCommand) Wait() error {
	return c.cmd.Wait()
}

func (c *execStreamCommand) Signal(signal os.Signal) error {
	if c.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return c.cmd.Process.Signal(signal)
}

func (c *execStreamCommand) Kill() error {
	if c.cmd.Process == nil {
		return os.ErrProcessDone
	}
	return c.cmd.Process.Kill()
}

func (c *execStreamCommand) PID() int {
	if c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

type StreamRegistry struct {
	mu              *sync.Mutex
	managers        *map[string]*manager
	resolve         func(context.Context, string) (string, string, error)
	newCommand      func(string, string, string) streamCommand
	liveBaseURL     func() string
	getObject       func(string, string) []byte
	resetChannel    func(string)
	clearChannel    func(string)
	stopTimeout     time.Duration
	stopsInProgress int
}

func newStreamRegistry(mu *sync.Mutex, managers *map[string]*manager) *StreamRegistry {
	var storeMu sync.RWMutex
	items := make(map[string]map[string][]byte)
	store := NewLiveStore(&storeMu, &items)
	token := newLiveWriteToken()
	return &StreamRegistry{
		mu:       mu,
		managers: managers,
		resolve:  resolveLiveStreamURL,
		newCommand: func(inputURL, playlistURL, generation string) streamCommand {
			return newFFmpegCommandWithToken(inputURL, playlistURL, generation, token)
		},
		liveBaseURL:  func() string { return "" },
		getObject:    store.GetObject,
		resetChannel: store.ResetChannel,
		clearChannel: store.ClearChannel,
		stopTimeout:  defaultStreamStopTimeout,
	}
}

func (r *StreamRegistry) managerMap() map[string]*manager {
	if *r.managers == nil {
		*r.managers = make(map[string]*manager)
	}
	return *r.managers
}

// Start begins streaming for the given channel. It returns an error if
// streaming is already started or if commands fail to start.
func (r *StreamRegistry) Start(channel string) error {
	if err := ValidateChannelName(channel); err != nil {
		return err
	}
	configuredLiveBaseURL := r.liveBaseURL()
	if configuredLiveBaseURL == "" {
		return errors.New("live base url not configured")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &manager{
		ctx:        ctx,
		cancel:     cancel,
		state:      streamStarting,
		generation: newLiveWriteToken(),
		startTime:  time.Now(),
		done:       make(chan struct{}),
	}

	r.mu.Lock()
	if r.stopsInProgress > 0 {
		r.mu.Unlock()
		cancel()
		return ErrStreamsStopping
	}
	managers := r.managerMap()
	if _, ok := managers[channel]; ok {
		r.mu.Unlock()
		cancel()
		return ErrAlreadyStarted
	}
	managers[channel] = m
	r.resetChannel(channel)
	r.mu.Unlock()

	inputURL, streamName, err := r.resolve(m.ctx, channel)
	if err != nil {
		return r.abortStart(channel, m, err)
	}
	slog.Info("resolved stream url", "channel", channel, "stream", streamName, "elapsed", time.Since(m.startTime))

	playlistURL := strings.TrimRight(configuredLiveBaseURL, "/") + liveWritePrefix + channel + "/index.m3u8"
	cmd := r.newCommand(inputURL, playlistURL, m.generation)
	stderrFF, err := cmd.StderrPipe()
	if err != nil {
		return r.abortStart(channel, m, fmt.Errorf("failed to get ffmpeg stderr pipe: %w", err))
	}

	r.mu.Lock()
	managers = *r.managers
	if managers == nil || managers[channel] != m || m.state == streamStopping {
		r.mu.Unlock()
		return r.abortStart(channel, m, context.Canceled)
	}
	m.cmd = cmd
	if err := cmd.Start(); err != nil {
		m.cmd = nil
		r.mu.Unlock()
		return r.abortStart(channel, m, fmt.Errorf("failed to start ffmpeg: %w", err))
	}
	m.state = streamRunning
	pid := cmd.PID()
	r.mu.Unlock()

	go logCommandOutput("ffmpeg", channel, stderrFF)

	go func() {
		err := cmd.Wait()
		if err != nil {
			slog.Error("ffmpeg exited", "channel", channel, "error", err)
		} else {
			slog.Info("ffmpeg exited normally", "channel", channel)
		}
		r.finishManager(channel, m)
	}()

	slog.Info("started", "ffmpeg_pid", pid, "channel", channel)
	return nil
}

func newFFmpegCommandWithToken(inputURL, playlistURL, generation, token string) streamCommand {
	return &execStreamCommand{
		cmd: exec.Command("ffmpeg", buildFFmpegHLSArgsWithToken(inputURL, playlistURL, generation, token)...),
	}
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
	r.finishManager(channel, m)
	return err
}

// Stop attempts to gracefully stop any running stream. It's safe to call
// multiple times; it returns nil if no stream was running.
func (r *StreamRegistry) Stop() error {
	r.mu.Lock()
	managers := *r.managers
	r.stopsInProgress++
	channels := make([]string, 0, len(managers))
	for channel := range managers {
		channels = append(channels, channel)
	}
	r.mu.Unlock()

	errCh := make(chan error, len(channels))
	var wg sync.WaitGroup
	for _, channel := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.StopChannel(channel); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)

	var stopErrs []error
	for err := range errCh {
		stopErrs = append(stopErrs, err)
	}
	r.mu.Lock()
	r.stopsInProgress--
	r.mu.Unlock()
	return errors.Join(stopErrs...)
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
	if m.state != streamStopping {
		m.state = streamStopping
		m.cancel()
	}
	initiated := !m.stopInProgress
	if initiated {
		m.stopInProgress = true
	}
	cmd := m.cmd
	done := m.done
	r.mu.Unlock()

	if !initiated {
		if waitForManagerDone(done, 2*r.stopTimeout) {
			return nil
		}
		return fmt.Errorf("%w: timed out waiting for channel %s to stop", ErrStopTimeout, channel)
	}
	err := r.stopManager(channel, m, cmd)
	if err != nil {
		r.mu.Lock()
		if managers := *r.managers; managers != nil && managers[channel] == m {
			m.stopInProgress = false
		}
		r.mu.Unlock()
	}
	return err
}

func (r *StreamRegistry) stopManager(channel string, m *manager, cmd streamCommand) error {
	if cmd == nil {
		if waitForManagerDone(m.done, r.stopTimeout) {
			return nil
		}
		return fmt.Errorf("%w: timed out stopping stream startup for %s", ErrStopTimeout, channel)
	}

	signalErr := cmd.Signal(os.Interrupt)
	if signalErr == nil || errors.Is(signalErr, os.ErrProcessDone) {
		if waitForManagerDone(m.done, r.stopTimeout) {
			slog.Info("stream process terminated", "channel", channel, "elapsed", time.Since(m.startTime))
			return nil
		}
	} else {
		slog.Debug("graceful stream stop unavailable", "channel", channel, "error", signalErr)
	}

	if err := cmd.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("failed to kill stream process for %s: %w", channel, err)
	}
	if !waitForManagerDone(m.done, r.stopTimeout) {
		return fmt.Errorf("%w: killed stream process for %s did not exit", ErrStopTimeout, channel)
	}
	return fmt.Errorf("%w: stream process for %s required a forced kill", ErrStopTimeout, channel)
}

func (r *StreamRegistry) finishManager(channel string, m *manager) {
	m.finishOnce.Do(func() {
		m.cancel()

		// Reject new writes before waiting for this stream's in-flight commit.
		// Keep the manager reserved while clearing its objects so a replacement
		// stream cannot be started and then cleared by this generation.
		r.mu.Lock()
		managers := *r.managers
		current := managers != nil && managers[channel] == m
		if current {
			m.state = streamStopping
		}
		r.mu.Unlock()

		m.writeMu.Lock()
		if current {
			r.clearChannel(channel)
			r.mu.Lock()
			if managers := *r.managers; managers != nil && managers[channel] == m {
				delete(managers, channel)
			}
			r.mu.Unlock()
		}
		m.writeMu.Unlock()

		close(m.done)
		slog.Info("stream lifecycle finished", "channel", channel, "elapsed", time.Since(m.startTime))
	})
}

func (r *StreamRegistry) commitLiveWrite(channel, generation string, commit func()) bool {
	// Capture the manager under the registry lock, then use its write mutex to
	// serialize commits with teardown. Revalidate after acquiring writeMu: the
	// manager may have stopped while this write was waiting.
	r.mu.Lock()
	managers := *r.managers
	var m *manager
	if managers != nil {
		m = managers[channel]
	}
	r.mu.Unlock()
	if m == nil {
		return false
	}

	m.writeMu.Lock()
	defer m.writeMu.Unlock()

	r.mu.Lock()
	current := r.isCurrentLiveWriterLocked(channel, generation) && (*r.managers)[channel] == m
	r.mu.Unlock()
	if !current {
		return false
	}

	commit()
	return true
}

func (r *StreamRegistry) isCurrentLiveWriter(channel, generation string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.isCurrentLiveWriterLocked(channel, generation)
}

func (r *StreamRegistry) isCurrentLiveWriterLocked(channel, generation string) bool {
	if generation == "" {
		return false
	}
	managers := *r.managers
	if managers == nil {
		return false
	}
	m := managers[channel]
	if m == nil || m.state != streamRunning || m.generation != generation {
		return false
	}
	return true
}

func buildFFmpegHLSArgsWithToken(inputURL, playlistURL, generation, token string) []string {
	args := []string{
		"-i", inputURL,
		"-c", "copy",
		"-f", "hls",
		"-method", "PUT",
		"-headers", liveWriteTokenHeader + ": " + token + "\r\n" +
			liveWriteGenerationHeader + ": " + generation + "\r\n",
		"-hls_time", "1",
		"-hls_list_size", "30",
		"-hls_delete_threshold", "30",
		"-hls_start_number_source", "epoch",
		"-hls_allow_cache", "0",
		"-hls_flags", "delete_segments",
	}

	// Keep segment URIs relative to the playlist. Clients may access Jellych
	// through localhost, a LAN address, or a reverse proxy; relative URIs make
	// every segment use the same origin that served index.m3u8.
	return append(args, playlistURL)
}

func waitForManagerDone(done <-chan struct{}, timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
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
