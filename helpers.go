package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

func parseLevel(s string) (slog.Level, error) {
	var level slog.Level
	var err = level.UnmarshalText([]byte(strings.TrimSpace(s)))
	return level, err
}

func getRequiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("missing required env var: %s", name)
	}
	return value, nil
}
