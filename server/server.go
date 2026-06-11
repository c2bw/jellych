package server

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/c2bw/jellych/server/api"
	"github.com/c2bw/jellych/stream"
)

func Start(addr string) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	// Combine API handler with static HTML and stream routes.
	mux := http.NewServeMux()
	apiHandler := api.Handler()
	mux.Handle("/api/", apiHandler)
	mux.Handle("/vod/", apiHandler)
	mux.HandleFunc("/watch", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./html/watch.html")
	})
	mux.HandleFunc("/watch/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./html/watch.html")
	})
	mux.HandleFunc("/vods", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./html/vods.html")
	})
	mux.HandleFunc("/vods/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "./html/vods.html")
	})
	mux.Handle("/html/", http.StripPrefix("/html/", http.FileServer(http.Dir("./html"))))
	mux.Handle("/live/", stream.LiveHandler())
	mux.Handle("/_live-write/", stream.LiveWriteHandler())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, "./html/watch.html")
			return
		}
		http.NotFound(w, r)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
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
