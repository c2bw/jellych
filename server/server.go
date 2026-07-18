package server

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

// Handlers contains the application-owned dynamic HTTP surfaces.
type Handlers struct {
	API       http.Handler
	Live      http.Handler
	LiveWrite http.Handler
}

func Start(addr string, handlers Handlers) (*http.Server, error) {
	return StartWithAssets(addr, os.DirFS("."), handlers)
}

// StartWithAssets starts the HTTP server with assets rooted above the html directory.
func StartWithAssets(addr string, assets fs.FS, handlers Handlers) (*http.Server, error) {
	if handlers.API == nil || handlers.Live == nil || handlers.LiveWrite == nil {
		return nil, fmt.Errorf("api, live, and live-write handlers are required")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	htmlFS, err := fs.Sub(assets, "html")
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	// Combine API handler with static HTML and stream routes.
	mux := http.NewServeMux()
	mux.Handle("/api/", handlers.API)
	mux.Handle("/vod/", handlers.API)
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, htmlFS, "watch.html")
	})
	mux.HandleFunc("/watch/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, htmlFS, "watch.html")
	})
	mux.HandleFunc("/vods", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, htmlFS, "vods.html")
	})
	mux.HandleFunc("/vods/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, htmlFS, "vods.html")
	})
	mux.Handle("/html/", http.StripPrefix("/html/", http.FileServerFS(htmlFS)))
	mux.Handle("/live/", handlers.Live)
	mux.Handle("/_live-write/", handlers.LiveWrite)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFileFS(w, r, htmlFS, "watch.html")
			return
		}
		http.NotFound(w, r)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		slog.Info("HTTP server listening", "addr", ln.Addr().String())
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("http listen", "error", err)
		}
	}()
	return srv, nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; connect-src 'self'; media-src 'self' blob:; worker-src 'self' blob:")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
