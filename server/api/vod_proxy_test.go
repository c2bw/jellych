package api

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestCopyVODMediaObjectWithinLimit(t *testing.T) {
	for _, test := range []struct {
		name  string
		body  string
		limit int64
	}{
		{name: "under limit", body: "media", limit: 6},
		{name: "exact limit", body: "media", limit: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			var dst bytes.Buffer
			if err := copyVODMediaObject(&dst, bytes.NewBufferString(test.body), test.limit); err != nil {
				t.Fatalf("copy media object: %v", err)
			}
			if got := dst.String(); got != test.body {
				t.Fatalf("copied body = %q; want %q", got, test.body)
			}
		})
	}
}

func TestCopyVODMediaObjectRejectsOverflowWithoutWritingExtraByte(t *testing.T) {
	var dst bytes.Buffer
	err := copyVODMediaObject(&dst, bytes.NewBufferString("media!"), 5)
	if !errors.Is(err, errVODMediaObjectTooLarge) {
		t.Fatalf("copy error = %v; want %v", err, errVODMediaObjectTooLarge)
	}
	if got := dst.String(); got != "media" {
		t.Fatalf("copied body = %q; want only the five permitted bytes", got)
	}
}

func TestVODMediaRegistryRegisterDefersBulkPruning(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	expired := vodMediaTarget{
		vodID:     "old-vod",
		url:       "https://example.test/old.ts",
		expiresAt: now.Add(-time.Second),
	}
	registry := &vodMediaRegistry{
		byToken: map[string]vodMediaTarget{"expired-token": expired},
		byURL:   map[string]string{expired.vodID + "\x00" + expired.url: "expired-token"},
	}

	if _, err := registry.register("new-vod", "https://example.test/new.ts", now); err != nil {
		t.Fatalf("register media URL: %v", err)
	}
	if _, ok := registry.byToken["expired-token"]; !ok {
		t.Fatal("register unexpectedly scanned and pruned unrelated tokens")
	}

	registry.prune(now)
	if _, ok := registry.byToken["expired-token"]; ok {
		t.Fatal("explicit prune retained expired token")
	}
	if _, ok := registry.byURL[expired.vodID+"\x00"+expired.url]; ok {
		t.Fatal("explicit prune retained expired reverse lookup")
	}
	if len(registry.byToken) != 1 {
		t.Fatalf("explicit prune removed active token; token count = %d", len(registry.byToken))
	}
}

func TestVODMediaRegistryLookupOnlyChecksRequestedToken(t *testing.T) {
	now := time.Date(2026, time.July, 12, 12, 0, 0, 0, time.UTC)
	active := vodMediaTarget{
		vodID:     "active-vod",
		url:       "https://example.test/active.ts",
		expiresAt: now.Add(time.Minute),
	}
	expired := vodMediaTarget{
		vodID:     "old-vod",
		url:       "https://example.test/old.ts",
		expiresAt: now.Add(-time.Second),
	}
	registry := &vodMediaRegistry{
		byToken: map[string]vodMediaTarget{
			"active-token":  active,
			"expired-token": expired,
		},
		byURL: map[string]string{
			active.vodID + "\x00" + active.url:   "active-token",
			expired.vodID + "\x00" + expired.url: "expired-token",
		},
	}

	got, ok := registry.lookup(active.vodID, "active-token", now)
	if !ok || got != active.url {
		t.Fatalf("lookup = %q, %v; want %q, true", got, ok, active.url)
	}
	if _, ok := registry.byToken["expired-token"]; !ok {
		t.Fatal("lookup unexpectedly scanned and pruned unrelated tokens")
	}

	if _, ok := registry.lookup(expired.vodID, "expired-token", now); ok {
		t.Fatal("lookup accepted expired token")
	}
	if _, ok := registry.byToken["expired-token"]; ok {
		t.Fatal("lookup retained requested expired token")
	}
	if _, ok := registry.byURL[expired.vodID+"\x00"+expired.url]; ok {
		t.Fatal("lookup retained requested expired reverse lookup")
	}
}
