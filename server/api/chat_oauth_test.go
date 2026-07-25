package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestChatOAuthStartBuildsAuthorizationRedirect(t *testing.T) {
	random := bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
	app := newAPI(&APIState{}, Dependencies{ChatOAuth: ChatOAuthConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		ServerURL:    "https://jellych.test",
		Random:       random,
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/chat/oauth/start", nil)
	recorder := httptest.NewRecorder()

	app.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d; want %d: %s", recorder.Code, http.StatusFound, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if location.Scheme != "https" || location.Host != "id.twitch.tv" || location.Path != "/oauth2/authorize" {
		t.Fatalf("redirect = %q; want Twitch authorize endpoint", location.String())
	}
	query := location.Query()
	if query.Get("response_type") != "code" ||
		query.Get("client_id") != "client-id" ||
		query.Get("redirect_uri") != "https://jellych.test/api/chat/oauth/callback" ||
		query.Get("scope") != "chat:read chat:edit" ||
		query.Get("state") == "" {
		t.Fatalf("unexpected authorization query: %v", query)
	}

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d; want one state cookie", len(cookies))
	}
	stateCookie := cookies[0]
	if stateCookie.Name != chatOAuthStateCookie || stateCookie.Value != query.Get("state") {
		t.Fatalf("state cookie = %#v; want redirect state", stateCookie)
	}
	if !stateCookie.HttpOnly || !stateCookie.Secure || stateCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("state cookie security attributes = %#v", stateCookie)
	}
}

func TestChatOAuthCallbackExchangesAndValidatesToken(t *testing.T) {
	var requests []*http.Request
	httpClient := &http.Client{Transport: apiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request)
		switch request.URL.Path {
		case "/oauth2/token":
			if err := request.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			if request.Method != http.MethodPost ||
				request.Form.Get("client_id") != "client-id" ||
				request.Form.Get("client_secret") != "client-secret" ||
				request.Form.Get("code") != "authorization-code" ||
				request.Form.Get("grant_type") != "authorization_code" ||
				request.Form.Get("redirect_uri") != "https://jellych.test/api/chat/oauth/callback" {
				t.Fatalf("unexpected token request: method=%s form=%v", request.Method, request.Form)
			}
			return jsonResponse(http.StatusOK, `{"access_token":"user-token","expires_in":14400,"scope":["chat:read","chat:edit"],"token_type":"bearer"}`), nil
		case "/oauth2/validate":
			if request.Header.Get("Authorization") != "OAuth user-token" {
				t.Fatalf("validation authorization = %q", request.Header.Get("Authorization"))
			}
			return jsonResponse(http.StatusOK, `{"client_id":"client-id","login":"TestViewer","user_id":"123","scopes":["chat:read","chat:edit"],"expires_in":14000}`), nil
		default:
			t.Fatalf("unexpected Twitch request: %s", request.URL)
			return nil, nil
		}
	})}
	app := newAPI(&APIState{}, Dependencies{ChatOAuth: ChatOAuthConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		ServerURL:    "https://jellych.test",
		HTTPClient:   httpClient,
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/chat/oauth/callback?code=authorization-code&state=expected-state", nil)
	request.AddCookie(&http.Cookie{Name: chatOAuthStateCookie, Value: "expected-state"})
	recorder := httptest.NewRecorder()

	app.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(requests) != 2 {
		t.Fatalf("Twitch requests = %d; want token exchange and validation", len(requests))
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-origin="https://jellych.test"`,
		`data-access-token="user-token"`,
		`data-login="testviewer"`,
		`data-expires-in="14000"`,
		`src="/html/js/chat_oauth_callback.js"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("callback body missing %q: %s", expected, body)
		}
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q; want no-store", recorder.Header().Get("Cache-Control"))
	}
	cleared := recorder.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != chatOAuthStateCookie || cleared[0].MaxAge != -1 {
		t.Fatalf("cleared cookies = %#v", cleared)
	}
}

func TestChatOAuthCallbackRejectsInvalidStateWithoutCallingTwitch(t *testing.T) {
	called := false
	app := newAPI(&APIState{}, Dependencies{ChatOAuth: ChatOAuthConfig{
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		ServerURL:    "http://jellych.test",
		HTTPClient: &http.Client{Transport: apiRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			called = true
			return nil, nil
		})},
	}})
	request := httptest.NewRequest(http.MethodGet, "/api/chat/oauth/callback?code=authorization-code&state=wrong", nil)
	request.AddCookie(&http.Cookie{Name: chatOAuthStateCookie, Value: "expected"})
	recorder := httptest.NewRecorder()

	app.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want %d", recorder.Code, http.StatusBadRequest)
	}
	if called {
		t.Fatal("invalid state must not call Twitch")
	}
	if strings.Contains(recorder.Body.String(), "authorization-code") {
		t.Fatal("callback response leaked authorization code")
	}
}

func TestChatOAuthRoutesAreUnavailableWithoutConfiguration(t *testing.T) {
	app := newAPI(&APIState{}, Dependencies{})
	for _, path := range []string{"/api/chat/oauth/start", "/api/chat/oauth/callback"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d; want %d", path, recorder.Code, http.StatusServiceUnavailable)
		}
	}
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
