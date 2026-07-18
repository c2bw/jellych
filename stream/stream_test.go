package stream

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildFFmpegHLSArgsUsesRelativeSegmentURLs(t *testing.T) {
	args := buildFFmpegHLSArgsWithToken("https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8", testLiveGeneration, "test-token")

	if slices.Contains(args, "-hls_base_url") {
		t.Fatal("did not expect -hls_base_url; segment URLs must remain relative to the playlist")
	}

	if got, want := args[len(args)-1], "http://127.0.0.1/_live-write/testchannel/index.m3u8"; got != want {
		t.Fatalf("expected playlist URL %q, got %q", want, got)
	}
}

func TestBuildFFmpegHLSArgsKeepsTrailingSegments(t *testing.T) {
	args := buildFFmpegHLSArgsWithToken("https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8", testLiveGeneration, "test-token")

	thresholdIndex := slices.Index(args, "-hls_delete_threshold")
	if thresholdIndex == -1 || thresholdIndex+1 >= len(args) {
		t.Fatal("expected -hls_delete_threshold argument")
	}
	if got, want := args[thresholdIndex+1], "30"; got != want {
		t.Fatalf("expected hls delete threshold %q, got %q", want, got)
	}
}

func TestBuildFFmpegHLSArgsUsesUniqueUncachedSegments(t *testing.T) {
	args := buildFFmpegHLSArgsWithToken("https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8", testLiveGeneration, "test-token")

	assertArgValue(t, args, "-hls_start_number_source", "epoch")
	assertArgValue(t, args, "-hls_allow_cache", "0")
}

func TestBuildFFmpegHLSArgsIncludesGenerationHeader(t *testing.T) {
	args := buildFFmpegHLSArgsWithToken("https://example.test/upstream.m3u8", "http://127.0.0.1/_live-write/testchannel/index.m3u8", testLiveGeneration, "test-token")

	headerIndex := slices.Index(args, "-headers")
	if headerIndex == -1 || headerIndex+1 >= len(args) {
		t.Fatal("expected -headers argument")
	}
	if !strings.Contains(args[headerIndex+1], liveWriteGenerationHeader+": "+testLiveGeneration+"\r\n") {
		t.Fatalf("expected generation header in %q", args[headerIndex+1])
	}
}

func TestLiveWriteCommitDoesNotHoldRegistryLock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &manager{
		ctx:        ctx,
		cancel:     cancel,
		state:      streamRunning,
		generation: testLiveGeneration,
		startTime:  time.Now(),
		done:       make(chan struct{}),
	}
	var registryMu sync.Mutex
	managers := map[string]*manager{"testchannel": m}
	registry := newStreamRegistry(&registryMu, &managers)
	registry.clearChannel = func(string) {}

	commitStarted := make(chan struct{})
	releaseCommit := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCommit) }) }
	defer release()
	commitDone := make(chan bool, 1)
	go func() {
		commitDone <- registry.commitLiveWrite("testchannel", testLiveGeneration, func() {
			close(commitStarted)
			<-releaseCommit
		})
	}()

	select {
	case <-commitStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("live write commit did not start")
	}

	finishDone := make(chan struct{})
	go func() {
		registry.finishManager("testchannel", m)
		close(finishDone)
	}()

	activeResult := make(chan bool, 1)
	go func() { activeResult <- registry.IsChannelActive("testchannel") }()
	select {
	case active := <-activeResult:
		if !active {
			t.Fatal("stream became inactive before the in-flight commit completed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("registry operation blocked behind live media commit")
	}

	select {
	case <-finishDone:
		t.Fatal("stream teardown completed before the in-flight commit")
	default:
	}

	release()
	if committed := <-commitDone; !committed {
		t.Fatal("current live writer commit was rejected")
	}
	select {
	case <-finishDone:
	case <-time.After(2 * time.Second):
		t.Fatal("stream teardown did not resume after the commit")
	}
	if registry.IsChannelActive("testchannel") {
		t.Fatal("stream remained active after teardown")
	}
}

func assertArgValue(t *testing.T, args []string, name, want string) {
	t.Helper()
	index := slices.Index(args, name)
	if index == -1 || index+1 >= len(args) {
		t.Fatalf("expected %s argument", name)
	}
	if got := args[index+1]; got != want {
		t.Fatalf("expected %s value %q, got %q", name, want, got)
	}
}

type fakeStreamCommand struct {
	started     chan struct{}
	signaled    chan struct{}
	killed      chan struct{}
	exit        chan struct{}
	startedOnce sync.Once
	signalOnce  sync.Once
	killOnce    sync.Once
	exitOnce    sync.Once
}

func newFakeStreamCommand() *fakeStreamCommand {
	return &fakeStreamCommand{
		started:  make(chan struct{}),
		signaled: make(chan struct{}),
		killed:   make(chan struct{}),
		exit:     make(chan struct{}),
	}
}

func (c *fakeStreamCommand) StderrPipe() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (c *fakeStreamCommand) Start() error {
	c.startedOnce.Do(func() { close(c.started) })
	return nil
}

func (c *fakeStreamCommand) Wait() error {
	<-c.exit
	return nil
}

func (c *fakeStreamCommand) Signal(os.Signal) error {
	c.signalOnce.Do(func() { close(c.signaled) })
	return nil
}

func (c *fakeStreamCommand) Kill() error {
	c.killOnce.Do(func() { close(c.killed) })
	c.finish()
	return nil
}

func (c *fakeStreamCommand) PID() int {
	return 42
}

func (c *fakeStreamCommand) finish() {
	c.exitOnce.Do(func() { close(c.exit) })
}

func TestStopChannelKeepsManagerReservedUntilProcessExits(t *testing.T) {
	var registryMu sync.Mutex
	managers := make(map[string]*manager)
	registry := newStreamRegistry(&registryMu, &managers)
	registry.liveBaseURL = func() string { return "http://127.0.0.1:8080" }
	cmd := newFakeStreamCommand()
	registry.resolve = successfulTestResolver
	registry.newCommand = func(string, string, string) streamCommand { return cmd }
	registry.resetChannel = func(string) {}
	cleared := make(chan struct{})
	var clearOnce sync.Once
	registry.clearChannel = func(string) { clearOnce.Do(func() { close(cleared) }) }
	registry.stopTimeout = time.Second

	if err := registry.Start("testchannel"); err != nil {
		t.Fatalf("failed to start test stream: %v", err)
	}
	<-cmd.started

	stopErr := make(chan error, 1)
	go func() { stopErr <- registry.StopChannel("testchannel") }()
	<-cmd.signaled

	if !registry.IsChannelActive("testchannel") {
		t.Fatal("expected stopping channel to remain reserved")
	}
	if err := registry.Start("testchannel"); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("expected restart during shutdown to fail with ErrAlreadyStarted, got %v", err)
	}
	select {
	case <-cleared:
		t.Fatal("live data was cleared before the process exited")
	default:
	}

	cmd.finish()
	if err := <-stopErr; err != nil {
		t.Fatalf("expected graceful stop, got %v", err)
	}
	if registry.IsChannelActive("testchannel") {
		t.Fatal("expected channel reservation to be released after process exit")
	}
	select {
	case <-cleared:
	default:
		t.Fatal("expected live data to be cleared before stop returned")
	}
}

func TestStopChannelWaitsForForcedKillToComplete(t *testing.T) {
	var registryMu sync.Mutex
	managers := make(map[string]*manager)
	registry := newStreamRegistry(&registryMu, &managers)
	registry.liveBaseURL = func() string { return "http://127.0.0.1:8080" }
	cmd := newFakeStreamCommand()
	registry.resolve = successfulTestResolver
	registry.newCommand = func(string, string, string) streamCommand { return cmd }
	registry.resetChannel = func(string) {}
	cleared := false
	registry.clearChannel = func(string) { cleared = true }
	registry.stopTimeout = 10 * time.Millisecond

	if err := registry.Start("testchannel"); err != nil {
		t.Fatalf("failed to start test stream: %v", err)
	}
	err := registry.StopChannel("testchannel")
	if !errors.Is(err, ErrStopTimeout) {
		t.Fatalf("expected forced stop to report ErrStopTimeout, got %v", err)
	}
	select {
	case <-cmd.killed:
	default:
		t.Fatal("expected process to be killed after graceful timeout")
	}
	if registry.IsChannelActive("testchannel") {
		t.Fatal("expected killed process to be removed from registry")
	}
	if !cleared {
		t.Fatal("expected live data to be cleared after kill completed")
	}
}

func TestStopChannelCancelsInProgressStartup(t *testing.T) {
	var registryMu sync.Mutex
	managers := make(map[string]*manager)
	registry := newStreamRegistry(&registryMu, &managers)
	registry.liveBaseURL = func() string { return "http://127.0.0.1:8080" }
	resolving := make(chan struct{})
	registry.resolve = func(ctx context.Context, _ string) (string, string, error) {
		close(resolving)
		<-ctx.Done()
		return "", "", ctx.Err()
	}
	registry.resetChannel = func(string) {}
	cleared := false
	registry.clearChannel = func(string) { cleared = true }
	registry.stopTimeout = time.Second

	startErr := make(chan error, 1)
	go func() { startErr <- registry.Start("testchannel") }()
	<-resolving

	if err := registry.StopChannel("testchannel"); err != nil {
		t.Fatalf("expected startup cancellation to stop cleanly, got %v", err)
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected start to return context cancellation, got %v", err)
	}
	if registry.IsChannelActive("testchannel") {
		t.Fatal("expected canceled startup to release channel reservation")
	}
	if !cleared {
		t.Fatal("expected canceled startup to clear live data")
	}
}

func successfulTestResolver(context.Context, string) (string, string, error) {
	return "https://example.test/live.m3u8", "source", nil
}
