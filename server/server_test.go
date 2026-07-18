package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStartReturnsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve test listener: %v", err)
	}
	defer ln.Close()

	srv, err := Start(ln.Addr().String(), testHandlers())
	if err == nil {
		if srv != nil {
			_ = srv.Close()
		}
		t.Fatal("expected Start to return listen error for an address already in use")
	}
	if srv != nil {
		t.Fatalf("expected nil server on listen error, got %#v", srv)
	}
}

func TestSecurityHeaders(t *testing.T) {
	handler := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	csp := rec.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"style-src 'self'",
		"media-src 'self' blob:",
		"worker-src 'self' blob:",
	} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("Content-Security-Policy %q does not contain %q", csp, directive)
		}
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q; want no-referrer", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q; want nosniff", got)
	}
}

func TestStartConfiguresHTTPTimeouts(t *testing.T) {
	srv, err := Start("127.0.0.1:0", testHandlers())
	if err != nil {
		t.Fatalf("expected Start to succeed, got %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("expected ReadHeaderTimeout to be 5s, got %v", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 30*time.Second {
		t.Fatalf("expected ReadTimeout to be 30s, got %v", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 30*time.Second {
		t.Fatalf("expected WriteTimeout to be 30s, got %v", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 2*time.Minute {
		t.Fatalf("expected IdleTimeout to be 2m, got %v", srv.IdleTimeout)
	}
}

func testHandlers() Handlers {
	notFound := http.NotFoundHandler()
	return Handlers{API: notFound, Live: notFound, LiveWrite: notFound}
}
