package api

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	chatOAuthStateCookie = "jellych_twitch_chat_oauth_state"
	chatOAuthStateMaxAge = 10 * time.Minute
	chatOAuthMaxCodeSize = 4096
	chatOAuthMaxResponse = 64 << 10
)

var chatOAuthResultTemplate = template.Must(template.New("chat-oauth-result").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Twitch chat login - Jellych</title>
  <link rel="icon" href="/html/assets/img/twitch_jellyfish_icon.svg" type="image/svg+xml">
  <link rel="stylesheet" href="/html/assets/app.css">
</head>
<body class="app-body oauth-result-body">
  <main id="chatOAuthResult"
    class="surface-card oauth-result-card"
    data-origin="{{.Origin}}"
    data-access-token="{{.AccessToken}}"
    data-login="{{.Login}}"
    data-expires-in="{{.ExpiresIn}}"
    data-error="{{.Error}}">
    <span class="eyebrow">Twitch chat</span>
    <h1>{{if .Error}}Login failed{{else}}Login complete{{end}}</h1>
    <p id="chatOAuthMessage">{{if .Error}}{{.Error}}{{else}}Returning to Jellych…{{end}}</p>
  </main>
  <script src="/html/js/chat_oauth_callback.js"></script>
</body>
</html>`))

// ChatOAuthConfig configures Twitch's authorization-code flow for chat users.
// The client secret is used only by the server and is never returned to the browser.
type ChatOAuthConfig struct {
	ClientID     string
	ClientSecret string
	ServerURL    string
	HTTPClient   *http.Client
	Random       io.Reader
}

type chatOAuthService struct {
	clientID     string
	clientSecret string
	serverURL    string
	redirectURI  string
	origin       string
	httpClient   *http.Client
	random       io.Reader
	secureCookie bool
}

type chatOAuthTokenResponse struct {
	AccessToken string   `json:"access_token"`
	ExpiresIn   int      `json:"expires_in"`
	Scope       []string `json:"scope"`
	TokenType   string   `json:"token_type"`
}

type chatOAuthValidation struct {
	ClientID  string   `json:"client_id"`
	Login     string   `json:"login"`
	UserID    string   `json:"user_id"`
	Scopes    []string `json:"scopes"`
	ExpiresIn int      `json:"expires_in"`
}

type chatOAuthResult struct {
	Origin      string
	AccessToken string
	Login       string
	ExpiresIn   int
	Error       string
}

func newChatOAuthService(config ChatOAuthConfig) *chatOAuthService {
	clientID := strings.TrimSpace(config.ClientID)
	clientSecret := strings.TrimSpace(config.ClientSecret)
	serverURL := strings.TrimRight(strings.TrimSpace(config.ServerURL), "/")
	if clientID == "" || clientSecret == "" || serverURL == "" {
		return nil
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}
	return &chatOAuthService{
		clientID:     clientID,
		clientSecret: clientSecret,
		serverURL:    serverURL,
		redirectURI:  serverURL + "/api/chat/oauth/callback",
		origin:       parsed.Scheme + "://" + parsed.Host,
		httpClient:   httpClient,
		random:       random,
		secureCookie: strings.EqualFold(parsed.Scheme, "https"),
	}
}

func (a *API) handleChatOAuthStart(w http.ResponseWriter, r *http.Request) {
	if a.chatOAuth == nil {
		http.Error(w, "Twitch chat OAuth is not configured", http.StatusServiceUnavailable)
		return
	}

	stateBytes := make([]byte, 32)
	if _, err := io.ReadFull(a.chatOAuth.random, stateBytes); err != nil {
		http.Error(w, "failed to initialize Twitch login", http.StatusInternalServerError)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(stateBytes)
	http.SetCookie(w, &http.Cookie{
		Name:     chatOAuthStateCookie,
		Value:    state,
		Path:     "/api/chat/oauth/callback",
		MaxAge:   int(chatOAuthStateMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   a.chatOAuth.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", a.chatOAuth.clientID)
	query.Set("redirect_uri", a.chatOAuth.redirectURI)
	query.Set("scope", "chat:read chat:edit")
	query.Set("state", state)
	destination := "https://id.twitch.tv/oauth2/authorize?" + query.Encode()
	http.Redirect(w, r, destination, http.StatusFound)
}

func (a *API) handleChatOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if a.chatOAuth == nil {
		http.Error(w, "Twitch chat OAuth is not configured", http.StatusServiceUnavailable)
		return
	}
	a.clearChatOAuthStateCookie(w)

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	cookie, err := r.Cookie(chatOAuthStateCookie)
	if err != nil || state == "" || len(state) > 512 ||
		subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		a.writeChatOAuthResult(w, http.StatusBadRequest, chatOAuthResult{
			Origin: a.chatOAuth.origin,
			Error:  "The Twitch login request could not be verified. Please try again.",
		})
		return
	}

	if providerError := strings.TrimSpace(r.URL.Query().Get("error")); providerError != "" {
		description := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if description == "" {
			description = providerError
		}
		a.writeChatOAuthResult(w, http.StatusBadRequest, chatOAuthResult{
			Origin: a.chatOAuth.origin,
			Error:  "Twitch login was not completed: " + description,
		})
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" || len(code) > chatOAuthMaxCodeSize {
		a.writeChatOAuthResult(w, http.StatusBadRequest, chatOAuthResult{
			Origin: a.chatOAuth.origin,
			Error:  "Twitch did not return a valid authorization code.",
		})
		return
	}

	token, err := a.chatOAuth.exchangeCode(r, code)
	if err != nil {
		a.writeChatOAuthResult(w, http.StatusBadGateway, chatOAuthResult{
			Origin: a.chatOAuth.origin,
			Error:  "Jellych could not complete the Twitch login.",
		})
		return
	}
	validation, err := a.chatOAuth.validateToken(r, token.AccessToken)
	if err != nil || validation.ClientID != a.chatOAuth.clientID ||
		!slices.Contains(validation.Scopes, "chat:read") ||
		!slices.Contains(validation.Scopes, "chat:edit") ||
		validation.Login == "" || validation.ExpiresIn <= 0 {
		a.writeChatOAuthResult(w, http.StatusBadGateway, chatOAuthResult{
			Origin: a.chatOAuth.origin,
			Error:  "Twitch returned a chat token that could not be validated.",
		})
		return
	}

	a.writeChatOAuthResult(w, http.StatusOK, chatOAuthResult{
		Origin:      a.chatOAuth.origin,
		AccessToken: token.AccessToken,
		Login:       strings.ToLower(validation.Login),
		ExpiresIn:   validation.ExpiresIn,
	})
}

func (s *chatOAuthService) exchangeCode(r *http.Request, code string) (*chatOAuthTokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", s.clientID)
	form.Set("client_secret", s.clientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", s.redirectURI)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "https://id.twitch.tv/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := readChatOAuthResponse(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange returned status %d", response.StatusCode)
	}
	var token chatOAuthTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("token exchange returned no access token")
	}
	return &token, nil
}

func (s *chatOAuthService) validateToken(r *http.Request, accessToken string) (*chatOAuthValidation, error) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, "https://id.twitch.tv/oauth2/validate", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "OAuth "+accessToken)
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := readChatOAuthResponse(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token validation returned status %d", response.StatusCode)
	}
	var validation chatOAuthValidation
	if err := json.Unmarshal(body, &validation); err != nil {
		return nil, err
	}
	return &validation, nil
}

func (a *API) clearChatOAuthStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     chatOAuthStateCookie,
		Value:    "",
		Path:     "/api/chat/oauth/callback",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   a.chatOAuth.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func readChatOAuthResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, chatOAuthMaxResponse+1))
	if err != nil {
		return nil, err
	}
	if len(data) > chatOAuthMaxResponse {
		return nil, fmt.Errorf("twitch OAuth response is too large")
	}
	return data, nil
}

func (a *API) writeChatOAuthResult(w http.ResponseWriter, status int, result chatOAuthResult) {
	var rendered bytes.Buffer
	if err := chatOAuthResultTemplate.Execute(&rendered, result); err != nil {
		http.Error(w, "failed to render Twitch login result", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(rendered.Bytes())
}
