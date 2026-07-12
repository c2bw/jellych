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

func TestParseServerURL(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "http", value: "http://localhost:8080", want: "http://localhost:8080"},
		{name: "https", value: "HTTPS://jellych.example/", want: "https://jellych.example"},
		{name: "path prefix", value: "https://example.com/jellych///", want: "https://example.com/jellych"},
		{name: "IPv6", value: "http://[::1]:8080/", want: "http://[::1]:8080"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseServerURL(test.value)
			if err != nil {
				t.Fatalf("parseServerURL(%q): %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("parseServerURL(%q) = %q; want %q", test.value, got, test.want)
			}
		})
	}
}

func TestParseServerURLRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty"},
		{name: "missing scheme", value: "localhost:8080"},
		{name: "unsupported scheme", value: "ftp://example.com"},
		{name: "missing host", value: "https:///jellych"},
		{name: "credentials", value: "https://user:password@example.com"},
		{name: "query", value: "https://example.com?token=secret"},
		{name: "empty query", value: "https://example.com?"},
		{name: "fragment", value: "https://example.com/#section"},
		{name: "newline", value: "https://example.com\n#EXTINF:1,evil"},
		{name: "trailing control character", value: "https://example.com\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseServerURL(test.value)
			if err == nil {
				t.Fatalf("expected parseServerURL(%q) to fail", test.value)
			}
			if !strings.Contains(err.Error(), "SERVER_URL") {
				t.Fatalf("expected error to name SERVER_URL, got %v", err)
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

func TestEmbeddedPagesUseSelfHostedFrontendAssets(t *testing.T) {
	pages := []struct {
		path     string
		required []string
	}{
		{path: "html/watch.html", required: []string{"/html/assets/app.css", "/html/vendor/hls.min.js"}},
		{path: "html/vods.html", required: []string{"/html/assets/app.css"}},
	}

	for _, page := range pages {
		data, err := webAssets.ReadFile(page.path)
		if err != nil {
			t.Fatalf("read embedded page %q: %v", page.path, err)
		}
		contents := string(data)
		if strings.Contains(contents, "https://cdn") || strings.Contains(contents, "text/tailwindcss") {
			t.Fatalf("embedded page %q contains a runtime CDN or Tailwind compiler reference", page.path)
		}
		for _, required := range page.required {
			if !strings.Contains(contents, required) {
				t.Fatalf("embedded page %q does not reference %q", page.path, required)
			}
		}
	}

	for _, asset := range []string{"html/assets/app.css", "html/vendor/hls.min.js", "html/vendor/hls.LICENSE"} {
		data, err := webAssets.ReadFile(asset)
		if err != nil {
			t.Fatalf("read embedded frontend asset %q: %v", asset, err)
		}
		if len(data) == 0 {
			t.Fatalf("embedded frontend asset %q is empty", asset)
		}
	}
}
