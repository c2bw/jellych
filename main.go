package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/c2bw/jellych/server"
	"github.com/c2bw/jellych/server/api"
	"github.com/c2bw/jellych/stream"
	"github.com/c2bw/jellych/twitch"
	"github.com/c2bw/jellych/twitch/client"
)

// version is overridden at build time for release artifacts.
var version = "dev"

//go:embed html
var webAssets embed.FS

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Set log level to debug for more verbose output, overridable by environment variable (e.g. LOG_LEVEL=info)
	var logLevel slog.Level
	logLevel, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err == nil {
		slog.Info("Set LOG_LEVEL from environment", "level", logLevel)
	} else {
		slog.Info("Invalid LOG_LEVEL, defaulting to INFO", "error", err)
		logLevel = slog.LevelInfo
	}
	slog.SetLogLoggerLevel(logLevel)
	slog.Info("Starting Jellych", "version", version)
	//Parse command line flags
	addr := flag.String("addr", ":8080", "HTTP listen address")
	configPath := flag.String("config", "/data/config", "directory containing the SQLite configuration database")
	vodsPath := flag.String("vods", "/data/vods", "folder where manually downloaded VODs are saved")
	flag.Parse()

	serverURL, err := parseServerURL(os.Getenv("SERVER_URL"))
	if err != nil {
		slog.Error("invalid server url", "error", err)
		os.Exit(1)
	}
	apiState := &api.APIState{}
	apiState.SetPlaylistBaseURL(serverURL)
	vodRetention, err := parseVODRetentionDays(os.Getenv("VOD_RETENTION_DAYS"))
	if err != nil {
		slog.Error("invalid VOD retention", "error", err)
		os.Exit(1)
	}
	slog.Info("VOD retention", "days", int(vodRetention/(24*time.Hour)))

	webhookSecret, err := getRequiredEnv("JELLYFIN_WEBHOOK_SECRET")
	if err != nil {
		slog.Error("missing Jellyfin webhook secret", "error", err)
		os.Exit(1)
	}
	apiState.SetJellyfinWebhookSecret(webhookSecret)
	apiState.SetControlAPISecret(os.Getenv("JELLYCH_API_SECRET"))

	clientID, err := getRequiredEnv("TWITCH_CLIENT_ID")
	if err != nil {
		slog.Error("missing Twitch client id", "error", err)
		os.Exit(1)
	}
	clientSecret, err := getRequiredEnv("TWITCH_CLIENT_SECRET")
	if err != nil {
		slog.Error("missing Twitch client secret", "error", err)
		os.Exit(1)
	}
	liveBaseURL, err := localLiveBaseURL(*addr)
	if err != nil {
		slog.Error("failed to resolve local live url", "error", err)
		os.Exit(1)
	}
	streamServices := stream.NewServices(liveBaseURL)
	streamServices.Downloads.SetDir(*vodsPath)
	streamServices.Downloads.SetRetention(vodRetention)
	slog.Info("Startup paths", "config", *configPath, "liveURL", liveBaseURL)
	// Create the Twitch client
	twitchClient, err := client.NewClientContext(ctx, clientID, clientSecret)
	if err != nil {
		slog.Error("failed to create Twitch client", "error", err)
		os.Exit(1)
	}
	// Initialize the Twitch manager before exposing the HTTP listener.
	stopStatus, err := twitch.StartContext(ctx, twitchClient, *configPath, apiState, streamServices.Downloads)
	if err != nil {
		slog.Error("failed to start Twitch manager", "error", err)
		twitchClient.Close()
		os.Exit(1)
	}
	// Start the HTTP server only after all application dependencies are ready.
	appAPI := api.NewWithDependencies(apiState, api.Dependencies{
		Streams: api.NewStreamOperations(streamServices.Streams, streamServices.Downloads),
	})
	srv, err := server.StartWithAssets(*addr, webAssets, server.Handlers{
		API:       appAPI.Handler(),
		Live:      streamServices.LiveHandler(apiState.IsConfiguredChannel),
		LiveWrite: streamServices.LiveWriteHandler(),
	})
	if err != nil {
		slog.Error("failed to start HTTP server", "error", err)
		stopStatus()
		twitchClient.Close()
		os.Exit(1)
	}

	appAPI.StartIdleMonitor(ctx)
	streamServices.Downloads.StartCleanup(ctx)
	<-ctx.Done()
	// shutdown HTTP server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if stopStatus != nil {
		stopStatus()
	}
	twitchClient.Close()
	// stop stream processes
	if err := streamServices.Downloads.Stop(); err != nil {
		slog.Warn("failed to stop VOD downloads cleanly", "error", err)
	}
	if err := streamServices.Streams.Stop(); err != nil {
		slog.Warn("failed to stop live streams cleanly", "error", err)
	}
}

func parseServerURL(raw string) (string, error) {
	if strings.IndexFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("SERVER_URL must not contain control characters")
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("SERVER_URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("SERVER_URL is invalid: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("SERVER_URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("SERVER_URL must include a hostname")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("SERVER_URL must not include credentials")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", fmt.Errorf("SERVER_URL must not include a query string")
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("SERVER_URL must not include a fragment")
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	return strings.TrimRight(parsed.String(), "/"), nil
}

func parseVODRetentionDays(value string) (time.Duration, error) {
	const defaultDays = 30

	value = strings.TrimSpace(value)
	if value == "" {
		return defaultDays * 24 * time.Hour, nil
	}

	days, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("VOD_RETENTION_DAYS must be a positive integer: %w", err)
	}
	maxDays := int64((1<<63 - 1) / int64(24*time.Hour))
	if days <= 0 || days > maxDays {
		return 0, fmt.Errorf("VOD_RETENTION_DAYS must be between 1 and %d", maxDays)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func localLiveBaseURL(addr string) (string, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return "", fmt.Errorf("listen address is required")
	}
	if strings.HasPrefix(trimmed, ":") {
		return "http://127.0.0.1" + trimmed, nil
	}
	host, port, err := net.SplitHostPort(trimmed)
	if err != nil {
		if strings.Count(trimmed, ":") == 1 {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 && parts[1] != "" {
				return (&url.URL{Scheme: "http", Host: net.JoinHostPort(parts[0], parts[1])}).String(), nil
			}
		}
		return "", err
	}
	if host == "0.0.0.0" || host == "" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String(), nil
}
