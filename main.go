package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
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

const version = "0.0.6"

func main() {
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
	configPath := flag.String("config", "/data/config", "path to the channels config directory")
	vodsPath := flag.String("vods", "", "folder where manually downloaded VODs are saved")
	flag.Parse()

	serverURL, err := getRequiredEnv("SERVER_URL")
	if err != nil {
		slog.Error("missing server url", "error", err)
		os.Exit(1)
	}
	api.SetPlaylistBaseURL(serverURL)
	stream.SetVODDownloadDir(*vodsPath)
	vodRetention, err := parseVODRetentionDays(os.Getenv("VOD_RETENTION_DAYS"))
	if err != nil {
		slog.Error("invalid VOD retention", "error", err)
		os.Exit(1)
	}
	stream.SetVODDownloadRetention(vodRetention)
	slog.Info("VOD retention", "days", int(vodRetention/(24*time.Hour)))

	webhookSecret, err := getRequiredEnv("JELLYFIN_WEBHOOK_SECRET")
	if err != nil {
		slog.Error("missing Jellyfin webhook secret", "error", err)
		os.Exit(1)
	}
	api.SetJellyfinWebhookSecret(webhookSecret)

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
	slog.Info("Startup paths", "config", *configPath, "liveURL", liveBaseURL)
	// Create the Twitch client
	twitchClient, err := client.NewClient(clientID, clientSecret)
	if err != nil {
		slog.Error("failed to create Twitch client", "error", err)
		os.Exit(1)
	}
	//Start the HTTP server
	srv, err := server.Start(*addr)
	if err != nil {
		slog.Error("failed to start HTTP server", "error", err)
		os.Exit(1)
	}
	// Start the Twitch manager
	stopStatus, err := twitch.Start(twitchClient, *configPath, liveBaseURL)
	if err != nil {
		slog.Error("failed to start Twitch manager", "error", err)
		os.Exit(1)
	}

	// Handle graceful shutdown on interrupt signal
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stream.StartVODDownloadCleanup(ctx)
	<-ctx.Done()
	// shutdown HTTP server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	if stopStatus != nil {
		stopStatus()
	}
	// stop stream processes
	if err := stream.StopVODDownloads(); err != nil {
		slog.Warn("failed to stop VOD downloads cleanly", "error", err)
	}
	if err := stream.Stop(); err != nil {
		slog.Warn("failed to stop live streams cleanly", "error", err)
	}
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
				return "http://" + parts[0] + ":" + parts[1], nil
			}
		}
		return "", err
	}
	if host == "0.0.0.0" || host == "" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	return "http://" + host + ":" + port, nil
}
