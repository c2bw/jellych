package stream

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	twitch "github.com/c2bw/twitch-url-extractor"
)

const maxVODPlaylistBytes = 16 << 20

// ResolveVODPlaylist resolves the upstream HLS URL and returns the playlist
// with relative URIs made absolute against the upstream playlist URL.
func ResolveVODPlaylist(ctx context.Context, vodURL string) ([]byte, error) {
	upstream, err := resolveVODStreamURL(ctx, vodURL)
	if err != nil {
		return nil, err
	}
	return FetchAndNormalizeHLSPlaylist(ctx, upstream)
}

func resolveVODStreamURL(ctx context.Context, vodURL string) (string, error) {
	vodURL = strings.TrimSpace(vodURL)
	if vodURL == "" {
		return "", fmt.Errorf("vod url is required")
	}

	streams, err := twitch.NewClient(nil).Streams(ctx, vodURL)
	if err != nil {
		return "", fmt.Errorf("failed to resolve stream URL: %w", err)
	}

	stream, ok := twitch.BestStream(streams)
	if !ok {
		return "", fmt.Errorf("no playable stream URL returned")
	}
	if err := validateHTTPURL(stream.URL); err != nil {
		return "", fmt.Errorf("stream URL extractor returned invalid stream url: %w", err)
	}
	return stream.URL, nil
}

// FetchAndNormalizeHLSPlaylist fetches an HLS playlist and makes relative
// segment, nested playlist, and key URIs absolute without proxying media.
func FetchAndNormalizeHLSPlaylist(ctx context.Context, playlistURL string) ([]byte, error) {
	if err := validateHTTPURL(playlistURL); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, playlistURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch upstream playlist: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upstream playlist returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxVODPlaylistBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read upstream playlist: %w", err)
	}
	if len(body) > maxVODPlaylistBytes {
		return nil, fmt.Errorf("upstream playlist too large")
	}
	return NormalizeHLSPlaylistURLs(playlistURL, body)
}

func NormalizeHLSPlaylistURLs(playlistURL string, data []byte) ([]byte, error) {
	base, err := url.Parse(playlistURL)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			b.WriteString(line)
		case strings.HasPrefix(trimmed, "#"):
			b.WriteString(rewriteURIAttributes(line, base))
		default:
			b.WriteString(resolveRelativeURL(line, base))
		}
		b.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func rewriteURIAttributes(line string, base *url.URL) string {
	const attr = `URI="`
	var out strings.Builder
	for {
		idx := strings.Index(line, attr)
		if idx < 0 {
			out.WriteString(line)
			return out.String()
		}
		out.WriteString(line[:idx+len(attr)])
		line = line[idx+len(attr):]
		end := strings.IndexByte(line, '"')
		if end < 0 {
			out.WriteString(line)
			return out.String()
		}
		out.WriteString(resolveRelativeURL(line[:end], base))
		line = line[end:]
	}
}

func resolveRelativeURL(raw string, base *url.URL) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.IsAbs() || strings.HasPrefix(trimmed, "data:") {
		return raw
	}
	return base.ResolveReference(parsed).String()
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("expected http or https url")
	}
	if u.Host == "" {
		return fmt.Errorf("missing host")
	}
	return nil
}
