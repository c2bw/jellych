package api

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SetPlaylistBaseURL configures the absolute server URL used in M3U entries.
// The application requires this to be set at startup.
func SetPlaylistBaseURL(raw string) {
	defaultState.SetPlaylistBaseURL(raw)
}

func (s *APIState) SetPlaylistBaseURL(raw string) {
	raw = strings.TrimSpace(raw)
	s.playlistMu.Lock()
	s.playlistURL = strings.TrimRight(raw, "/")
	s.playlistMu.Unlock()
}

func (s *APIState) playlistBaseURL() string {
	s.playlistMu.RLock()
	defer s.playlistMu.RUnlock()
	return s.playlistURL
}

// BuildM3U builds a live-channel M3U playlist from configured channels and statuses.
func BuildM3U(channels []string, statuses []Status, logos map[string]string) string {
	return defaultState.BuildM3U(channels, statuses, logos)
}

func (s *APIState) BuildM3U(channels []string, statuses []Status, logos map[string]string) string {
	statusByName := make(map[string]Status, len(statuses))
	for _, s := range statuses {
		statusByName[strings.ToLower(s.Name)] = s
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, ch := range channels {
		status, ok := statusByName[strings.ToLower(ch)]
		online := ok && status.Online
		if !online {
			continue
		}

		meta := `group-title="Online"`
		if ok && status.Viewers > 0 {
			meta += fmt.Sprintf(" viewers=\"%d\"", status.Viewers)
		}
		if logo := strings.TrimSpace(logos[strings.ToLower(ch)]); logo != "" {
			meta += fmt.Sprintf(" tvg-logo=\"%s\"", m3uAttr(logo))
		}
		b.WriteString(fmt.Sprintf("#EXTINF:-1 %s,%s\n", meta, ch))
		b.WriteString(s.playlistChannelURL(ch) + "\n")
	}

	return b.String()
}

func (s *APIState) playlistChannelURL(channel string) string {
	return s.playlistBaseURL() + "/live/" + url.PathEscape(channel) + "/index.m3u8"
}

func (s *APIState) playlistVODURL(id string) string {
	return s.playlistBaseURL() + "/vod/" + url.PathEscape(id) + "/index.m3u8"
}

// BuildVODM3U builds a VOD M3U playlist from persisted VOD metadata.
func BuildVODM3U(vods []VOD) string {
	return defaultState.BuildVODM3U(vods)
}

func (s *APIState) BuildVODM3U(vods []VOD) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, vod := range vods {
		vod = PrepareVOD(vod)
		if err := ValidateVOD(vod); err != nil {
			continue
		}
		vod.Title = m3uText(vod.Title)
		vod.Channel = m3uText(vod.Channel)
		vod.Logo = m3uText(vod.Logo)
		vod.Date = m3uText(vod.Date)
		title := vod.Title
		if title == "" {
			title = "VOD " + vod.ID
		}
		if vod.Channel != "" {
			title = vod.Channel + " - " + title
		}
		displayTitle := title
		if date := vodDisplayDate(vod.Date); date != "" {
			displayTitle = date + " - " + displayTitle
		}
		meta := fmt.Sprintf(`group-title="Recordings" tvg-id="vod-%s" tvg-name="%s"`, m3uAttr(vod.ID), m3uAttr(displayTitle))
		if vod.Logo != "" {
			meta += fmt.Sprintf(" tvg-logo=\"%s\"", m3uAttr(vod.Logo))
		}
		if vod.Date != "" {
			meta += fmt.Sprintf(" tvg-date=\"%s\"", m3uAttr(vod.Date))
		}
		b.WriteString(fmt.Sprintf("#EXTINF:-1 %s,%s\n", meta, displayTitle))
		b.WriteString(s.playlistVODURL(vod.ID) + "\n")
	}
	return b.String()
}

func vodDisplayDate(raw string) string {
	if raw = strings.TrimSpace(raw); raw == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Format("2006-01-02")
}

func m3uAttr(value string) string {
	value = stripM3UControlCharacters(value)
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func m3uText(value string) string {
	return stripM3UControlCharacters(value)
}

func stripM3UControlCharacters(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, value)
}
