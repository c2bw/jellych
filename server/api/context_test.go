package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type contextCaptureStore struct {
	channelAddContext    context.Context
	channelRemoveContext context.Context
	vodAddContext        context.Context
	vodRemoveContext     context.Context
}

func (s *contextCaptureStore) AddChannelContext(ctx context.Context, _ string) (string, error) {
	s.channelAddContext = ctx
	return "", errors.New("stop after capturing channel context")
}

func (s *contextCaptureStore) RemoveChannelContext(ctx context.Context, _ string) error {
	s.channelRemoveContext = ctx
	return nil
}

func (*contextCaptureStore) ListVODs() []VOD {
	return nil
}

func (*contextCaptureStore) FindVOD(string) (VOD, bool) {
	return VOD{}, false
}

func (s *contextCaptureStore) AddVODContext(ctx context.Context, _ VOD) error {
	s.vodAddContext = ctx
	return errors.New("stop after capturing vod context")
}

func (s *contextCaptureStore) RemoveVODContext(ctx context.Context, _ string) error {
	s.vodRemoveContext = ctx
	return nil
}

func TestMutationContextsPropagateToStores(t *testing.T) {
	type contextKey struct{}
	requestContext := context.WithValue(context.Background(), contextKey{}, "request-value")
	store := &contextCaptureStore{}
	state := &APIState{}
	state.SetChannelStore(store)
	state.SetVODStore(store)
	handler := NewWithState(state).Handler()

	channelRequest := httptest.NewRequest(http.MethodPost, "/api/channels/add", strings.NewReader(`{"name":"jankos"}`)).WithContext(requestContext)
	handler.ServeHTTP(httptest.NewRecorder(), channelRequest)
	if store.channelAddContext == nil || store.channelAddContext.Value(contextKey{}) != "request-value" {
		t.Fatal("channel store did not receive the HTTP request context")
	}

	vodRequest := httptest.NewRequest(http.MethodPost, "/api/vods", strings.NewReader(`{"id":"123","url":"https://www.twitch.tv/videos/123"}`)).WithContext(requestContext)
	handler.ServeHTTP(httptest.NewRecorder(), vodRequest)
	if store.vodAddContext == nil || store.vodAddContext.Value(contextKey{}) != "request-value" {
		t.Fatal("VOD store did not receive the HTTP request context")
	}

	state.SetChannels([]string{"jankos"})
	if err := state.RemoveChannelContext(requestContext, "jankos"); err != nil {
		t.Fatalf("remove channel: %v", err)
	}
	if store.channelRemoveContext == nil || store.channelRemoveContext.Value(contextKey{}) != "request-value" {
		t.Fatal("channel store did not receive the removal context")
	}

	if err := state.RemoveVODContext(requestContext, "123"); err != nil {
		t.Fatalf("remove VOD: %v", err)
	}
	if store.vodRemoveContext == nil || store.vodRemoveContext.Value(contextKey{}) != "request-value" {
		t.Fatal("VOD store did not receive the removal context")
	}
}
