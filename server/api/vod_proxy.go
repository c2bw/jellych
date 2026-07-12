package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/c2bw/jellych/stream"
)

const vodMediaTokenTTL = 12 * time.Hour
const maxVODMediaTokens = 100_000
const maxProxiedVODMediaBytes = 256 << 20
const maxProxiedVODPlaylistBytes = 16 << 20

var vodMediaHTTPClient = &http.Client{Timeout: 2 * time.Minute}

var errVODMediaObjectTooLarge = errors.New("vod media object too large")

type vodMediaTarget struct {
	vodID     string
	url       string
	expiresAt time.Time
}

type vodMediaRegistry struct {
	sync.Mutex
	byToken map[string]vodMediaTarget
	byURL   map[string]string
}

var defaultVODMediaRegistry vodMediaRegistry

func (r *vodMediaRegistry) register(vodID, rawURL string, now time.Time) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", fmt.Errorf("invalid upstream VOD media URL")
	}
	key := vodID + "\x00" + parsed.String()

	r.Lock()
	defer r.Unlock()
	if token := r.byURL[key]; token != "" {
		target, ok := r.byToken[token]
		if ok && now.Before(target.expiresAt) {
			target.expiresAt = now.Add(vodMediaTokenTTL)
			r.byToken[token] = target
			return token, nil
		}
		delete(r.byToken, token)
		delete(r.byURL, key)
	}
	if len(r.byToken) >= maxVODMediaTokens {
		return "", fmt.Errorf("VOD media proxy capacity exceeded")
	}
	if r.byToken == nil {
		r.byToken = make(map[string]vodMediaTarget)
		r.byURL = make(map[string]string)
	}
	var token string
	for token == "" {
		var tokenBytes [16]byte
		if _, err := rand.Read(tokenBytes[:]); err != nil {
			return "", fmt.Errorf("failed to create VOD media token: %w", err)
		}
		candidate := hex.EncodeToString(tokenBytes[:])
		if _, exists := r.byToken[candidate]; !exists {
			token = candidate
		}
	}
	r.byToken[token] = vodMediaTarget{vodID: vodID, url: parsed.String(), expiresAt: now.Add(vodMediaTokenTTL)}
	r.byURL[key] = token
	return token, nil
}

func (r *vodMediaRegistry) lookup(vodID, token string, now time.Time) (string, bool) {
	r.Lock()
	defer r.Unlock()
	target, ok := r.byToken[token]
	if !ok {
		return "", false
	}
	if !now.Before(target.expiresAt) {
		r.deleteTokenLocked(token, target)
		return "", false
	}
	if target.vodID != vodID {
		return "", false
	}
	target.expiresAt = now.Add(vodMediaTokenTTL)
	r.byToken[token] = target
	return target.url, true
}

func (r *vodMediaRegistry) prune(now time.Time) {
	r.Lock()
	defer r.Unlock()
	r.pruneLocked(now)
}

func (r *vodMediaRegistry) pruneLocked(now time.Time) {
	for token, target := range r.byToken {
		if now.Before(target.expiresAt) {
			continue
		}
		r.deleteTokenLocked(token, target)
	}
}

func (r *vodMediaRegistry) deleteTokenLocked(token string, target vodMediaTarget) {
	delete(r.byToken, token)
	delete(r.byURL, target.vodID+"\x00"+target.url)
}

func (a *API) rewriteVODPlaylist(vodID string, playlist []byte) ([]byte, error) {
	now := time.Now()
	defaultVODMediaRegistry.prune(now)
	return stream.RewriteHLSPlaylistURLs(playlist, func(rawURL string) (string, error) {
		trimmed := strings.TrimSpace(rawURL)
		if strings.HasPrefix(trimmed, "data:") {
			return rawURL, nil
		}
		token, err := defaultVODMediaRegistry.register(vodID, trimmed, now)
		if err != nil {
			return "", err
		}
		return "/vod/" + url.PathEscape(vodID) + "/media/" + token, nil
	})
}

func (a *API) handleGetVODMedia(w http.ResponseWriter, r *http.Request) {
	vodID := strings.TrimSpace(r.PathValue("id"))
	if _, ok := a.state.FindVOD(vodID); !ok {
		http.NotFound(w, r)
		return
	}
	targetURL, ok := defaultVODMediaRegistry.lookup(vodID, strings.TrimSpace(r.PathValue("token")), time.Now())
	if !ok {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, r.Method, targetURL, nil)
	if err != nil {
		http.Error(w, "invalid upstream media request", http.StatusBadGateway)
		return
	}
	for _, header := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if value := r.Header.Get(header); value != "" {
			upstream.Header.Set(header, value)
		}
	}
	resp, err := vodMediaHTTPClient.Do(upstream)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("failed to proxy VOD media", "id", vodID, "error", err)
		}
		http.Error(w, "failed to fetch VOD media", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if isVODPlaylistResponse(targetURL, resp.Header.Get("Content-Type")) && resp.StatusCode >= 200 && resp.StatusCode < 300 && r.Method == http.MethodGet {
		playlistURL := targetURL
		if resp.Request != nil && resp.Request.URL != nil {
			playlistURL = resp.Request.URL.String()
		}
		a.proxyNestedVODPlaylist(w, vodID, playlistURL, resp)
		return
	}
	if resp.ContentLength > maxProxiedVODMediaBytes {
		http.Error(w, "VOD media object too large", http.StatusBadGateway)
		return
	}
	copyVODMediaHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead || resp.StatusCode == http.StatusNotModified {
		return
	}
	if err := copyVODMediaObject(w, resp.Body, maxProxiedVODMediaBytes); err != nil {
		if errors.Is(err, errVODMediaObjectTooLarge) {
			slog.Warn("aborting oversized VOD media response", "id", vodID, "limit", maxProxiedVODMediaBytes)
		} else if r.Context().Err() == nil {
			slog.Warn("VOD media response ended before it could be completed", "id", vodID, "error", err)
		}
		// Headers may already contain a successful upstream status. Abort the
		// HTTP stream so clients observe a transport failure instead of treating
		// a partial media object as a complete 2xx response.
		panic(http.ErrAbortHandler)
	}
}

func copyVODMediaObject(dst io.Writer, src io.Reader, maxBytes int64) error {
	_, err := io.CopyN(dst, src, maxBytes)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	extra, err := io.CopyN(io.Discard, src, 1)
	if extra > 0 {
		return errVODMediaObjectTooLarge
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (a *API) proxyNestedVODPlaylist(w http.ResponseWriter, vodID, targetURL string, resp *http.Response) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProxiedVODPlaylistBytes+1))
	if err != nil || len(body) > maxProxiedVODPlaylistBytes {
		http.Error(w, "upstream VOD playlist too large", http.StatusBadGateway)
		return
	}
	normalized, err := stream.NormalizeHLSPlaylistURLs(targetURL, body)
	if err != nil {
		http.Error(w, "invalid upstream VOD playlist", http.StatusBadGateway)
		return
	}
	rewritten, err := a.rewriteVODPlaylist(vodID, normalized)
	if err != nil {
		http.Error(w, "failed to prepare VOD playlist", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, max-age=30")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(rewritten)
}

func isVODPlaylistResponse(rawURL, contentType string) bool {
	parsed, err := url.Parse(rawURL)
	return strings.Contains(strings.ToLower(contentType), "mpegurl") || (err == nil && strings.EqualFold(path.Ext(parsed.Path), ".m3u8"))
}

func copyVODMediaHeaders(dst, src http.Header) {
	for _, header := range []string{"Accept-Ranges", "Cache-Control", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified"} {
		if value := src.Get(header); value != "" {
			dst.Set(header, value)
		}
	}
}
