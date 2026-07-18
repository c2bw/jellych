package api

import (
	"time"

	"github.com/c2bw/jellych/stream"
)

func (f *apiTestFixture) setVODDownloader(dir string, retention time.Duration) {
	services := stream.NewServices("")
	services.Downloads.SetDir(dir)
	services.Downloads.SetRetention(retention)
	f.api.streams = NewStreamOperations(services.Streams, services.Downloads)
}
