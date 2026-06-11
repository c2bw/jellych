package server

import (
	"net"
	"testing"
	"time"
)

func TestStartReturnsListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve test listener: %v", err)
	}
	defer ln.Close()

	srv, err := Start(ln.Addr().String())
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

func TestStartConfiguresHTTPTimeouts(t *testing.T) {
	srv, err := Start("127.0.0.1:0")
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
