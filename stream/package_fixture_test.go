package stream

import "sync"

type liveTestFixture struct {
	store      *LiveStore
	registry   *StreamRegistry
	service    *LiveService
	managers   map[string]*manager
	writeToken string
}

func newLiveTestFixture() *liveTestFixture {
	store := NewIsolatedLiveStore()

	var registryMu sync.Mutex
	managers := make(map[string]*manager)
	registry := newStreamRegistry(&registryMu, &managers)
	registry.getObject = store.GetObject
	registry.resetChannel = store.ResetChannel
	registry.clearChannel = store.ClearChannel

	writeToken := newLiveWriteToken()
	service := &LiveService{
		store:      store,
		registry:   registry,
		start:      registry.Start,
		writeToken: writeToken,
	}
	return &liveTestFixture{
		store:      store,
		registry:   registry,
		service:    service,
		managers:   managers,
		writeToken: writeToken,
	}
}

func (f *liveTestFixture) installWriter(channel, generation string) {
	f.registry.mu.Lock()
	f.managers[channel] = &manager{
		state:      streamRunning,
		generation: generation,
		done:       make(chan struct{}),
	}
	f.registry.mu.Unlock()
}
