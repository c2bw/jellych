package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseVODRetentionDaysDefaultsToThirtyDays(t *testing.T) {
	got, err := parseVODRetentionDays("")
	if err != nil {
		t.Fatalf("expected empty value to use the default, got %v", err)
	}
	if want := 30 * 24 * time.Hour; got != want {
		t.Fatalf("expected retention %v, got %v", want, got)
	}
}

func TestParseVODRetentionDaysAcceptsPositiveInteger(t *testing.T) {
	got, err := parseVODRetentionDays(" 7 ")
	if err != nil {
		t.Fatalf("expected positive integer to parse, got %v", err)
	}
	if want := 7 * 24 * time.Hour; got != want {
		t.Fatalf("expected retention %v, got %v", want, got)
	}
}

func TestParseVODRetentionDaysRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"invalid", "0", "-1", "999999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			_, err := parseVODRetentionDays(value)
			if err == nil {
				t.Fatalf("expected %q to fail", value)
			}
			if !strings.Contains(err.Error(), "VOD_RETENTION_DAYS") {
				t.Fatalf("expected error to name VOD_RETENTION_DAYS, got %v", err)
			}
		})
	}
}
