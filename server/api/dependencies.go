package api

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/c2bw/jellych/stream"
)

// StreamOperations contains the stateful stream operations used by the API.
// Callers must either leave the value empty or supply every operation so one
// API instance cannot accidentally span multiple stream backends.
type StreamOperations struct {
	Start                  func(string) error
	StopChannel            func(string) error
	ActiveChannels         func() []string
	PlaylistSegmentCount   func(string) (int, error)
	VODDownloadStatus      func(string) (bool, time.Time, error)
	GetVODDownloadProgress func(string) (stream.VODDownloadProgress, error)
	StartVODDownload       func(context.Context, string, string, string, string, stream.VODDownloadPreset, time.Duration) error
	ConvertVODDownload     func(context.Context, string, stream.VODDownloadPreset) error
	DeleteVODDownload      func(string) error
	OpenVODDownload        func(string) (*os.File, error)
	RemoveVODWithArtifacts func(string, func() error) error
	ResolveVODPlaylist     func(context.Context, string) ([]byte, error)
}

// Dependencies configures runtime services owned by an API instance. Zero
// fields use independently owned defaults.
type Dependencies struct {
	Streams            StreamOperations
	VODMediaHTTPClient *http.Client
	Now                func() time.Time
}

func defaultStreamOperations() StreamOperations {
	services := stream.NewServices("")
	return NewStreamOperations(services.Streams, services.Downloads)
}

// NewStreamOperations adapts explicitly owned stream services for an API.
func NewStreamOperations(registry *stream.StreamRegistry, downloads *stream.VODDownloader) StreamOperations {
	return StreamOperations{
		Start:                  registry.Start,
		StopChannel:            registry.StopChannel,
		ActiveChannels:         registry.ActiveChannels,
		PlaylistSegmentCount:   registry.PlaylistSegmentCount,
		VODDownloadStatus:      downloads.Status,
		GetVODDownloadProgress: downloads.Progress,
		StartVODDownload:       downloads.StartWithPresetAndDuration,
		ConvertVODDownload:     downloads.Convert,
		DeleteVODDownload:      downloads.Delete,
		OpenVODDownload:        downloads.Open,
		RemoveVODWithArtifacts: downloads.RemoveWithArtifacts,
		ResolveVODPlaylist:     stream.ResolveVODPlaylist,
	}
}

func fillStreamOperationDefaults(operations StreamOperations) StreamOperations {
	if operations.empty() {
		return defaultStreamOperations()
	}
	if !operations.complete() {
		panic("api: StreamOperations must be either empty or complete")
	}
	return operations
}

func (operations StreamOperations) empty() bool {
	return operations.Start == nil && operations.StopChannel == nil && operations.ActiveChannels == nil &&
		operations.PlaylistSegmentCount == nil && operations.VODDownloadStatus == nil &&
		operations.GetVODDownloadProgress == nil && operations.StartVODDownload == nil &&
		operations.ConvertVODDownload == nil && operations.DeleteVODDownload == nil &&
		operations.OpenVODDownload == nil && operations.RemoveVODWithArtifacts == nil &&
		operations.ResolveVODPlaylist == nil
}

func (operations StreamOperations) complete() bool {
	return operations.Start != nil && operations.StopChannel != nil && operations.ActiveChannels != nil &&
		operations.PlaylistSegmentCount != nil && operations.VODDownloadStatus != nil &&
		operations.GetVODDownloadProgress != nil && operations.StartVODDownload != nil &&
		operations.ConvertVODDownload != nil && operations.DeleteVODDownload != nil &&
		operations.OpenVODDownload != nil && operations.RemoveVODWithArtifacts != nil &&
		operations.ResolveVODPlaylist != nil
}
