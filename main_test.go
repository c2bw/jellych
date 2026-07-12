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

func TestLocalLiveBaseURL(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{name: "port only", addr: ":8080", want: "http://127.0.0.1:8080"},
		{name: "IPv4 wildcard", addr: "0.0.0.0:8080", want: "http://127.0.0.1:8080"},
		{name: "IPv6 wildcard", addr: "[::]:8080", want: "http://127.0.0.1:8080"},
		{name: "IPv6 loopback", addr: "[::1]:8080", want: "http://[::1]:8080"},
		{name: "IPv6 address", addr: "[2001:db8::1]:9000", want: "http://[2001:db8::1]:9000"},
		{name: "scoped IPv6 address", addr: "[fe80::1%eth0]:8080", want: "http://[fe80::1%25eth0]:8080"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := localLiveBaseURL(test.addr)
			if err != nil {
				t.Fatalf("localLiveBaseURL(%q): %v", test.addr, err)
			}
			if got != test.want {
				t.Fatalf("localLiveBaseURL(%q) = %q; want %q", test.addr, got, test.want)
			}
		})
	}
}
