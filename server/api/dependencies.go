package api

import (
	"context"
	"net/http"
	"time"

	"github.com/c2bw/jellych/stream"
)

// StreamOperations contains the stateful stream operations used by the API.
// Supplying an instance lets callers isolate API servers in tests or run more
// than one API server with independent stream backends.
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
	RemoveVODWithArtifacts func(string, func() error) error
	ResolveVODPlaylist     func(context.Context, string) ([]byte, error)
}

// Dependencies configures runtime services owned by an API instance. Zero
// fields use the package's production implementations.
type Dependencies struct {
	Streams            StreamOperations
	VODMediaHTTPClient *http.Client
	Now                func() time.Time
}

func defaultStreamOperations() StreamOperations {
	return StreamOperations{
		Start:                  stream.Start,
		StopChannel:            stream.StopChannel,
		ActiveChannels:         stream.ActiveChannels,
		PlaylistSegmentCount:   stream.PlaylistSegmentCount,
		VODDownloadStatus:      stream.VODDownloadStatus,
		GetVODDownloadProgress: stream.GetVODDownloadProgress,
		StartVODDownload:       stream.StartVODDownloadWithPresetAndDuration,
		ConvertVODDownload:     stream.ConvertVODDownload,
		DeleteVODDownload:      stream.DeleteVODDownload,
		RemoveVODWithArtifacts: stream.RemoveVODWithArtifacts,
		ResolveVODPlaylist:     stream.ResolveVODPlaylist,
	}
}

func fillStreamOperationDefaults(operations StreamOperations) StreamOperations {
	defaults := defaultStreamOperations()
	if operations.Start == nil {
		operations.Start = defaults.Start
	}
	if operations.StopChannel == nil {
		operations.StopChannel = defaults.StopChannel
	}
	if operations.ActiveChannels == nil {
		operations.ActiveChannels = defaults.ActiveChannels
	}
	if operations.PlaylistSegmentCount == nil {
		operations.PlaylistSegmentCount = defaults.PlaylistSegmentCount
	}
	if operations.VODDownloadStatus == nil {
		operations.VODDownloadStatus = defaults.VODDownloadStatus
	}
	if operations.GetVODDownloadProgress == nil {
		operations.GetVODDownloadProgress = defaults.GetVODDownloadProgress
	}
	if operations.StartVODDownload == nil {
		operations.StartVODDownload = defaults.StartVODDownload
	}
	if operations.ConvertVODDownload == nil {
		operations.ConvertVODDownload = defaults.ConvertVODDownload
	}
	if operations.DeleteVODDownload == nil {
		operations.DeleteVODDownload = defaults.DeleteVODDownload
	}
	if operations.RemoveVODWithArtifacts == nil {
		operations.RemoveVODWithArtifacts = defaults.RemoveVODWithArtifacts
	}
	if operations.ResolveVODPlaylist == nil {
		operations.ResolveVODPlaylist = defaults.ResolveVODPlaylist
	}
	return operations
}
