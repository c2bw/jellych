package stream

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

var ErrVODDownloadsDisabled = errors.New("vod downloads folder not configured")
var ErrVODDownloadAlreadyStarted = errors.New("vod download already started")
var ErrVODDownloadAlreadyExists = errors.New("vod download already exists")
var ErrVODDownloadNotFound = errors.New("vod download not found")
var ErrVODDownloadsStopping = errors.New("vod downloads stopping")
var ErrVODDownloadProtected = errors.New("vod has an active or completed download")
var ErrVODRemovalInProgress = errors.New("vod removal in progress")
var ErrVODConversionRequiresOriginal = errors.New("vod conversion requires an original download")
var ErrVODConversionTargetOriginal = errors.New("vod conversion target must be compressed")

const vodURLResolutionTimeout = 20 * time.Second
const vodDownloadCleanupInterval = time.Hour
const defaultVODDownloadRetention = 30 * 24 * time.Hour
const vodDownloadStopTimeout = 5 * time.Second
const vodMetadataProbeTimeout = 5 * time.Second
const vodMetadataProbeRetryDelay = 30 * time.Second
const vodPartialDirName = ".jellych-partials"

var vodDownloadIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type vodDownload struct {
	progress            VODDownloadProgress
	lastTotalSize       int64
	lastTotalSizeUpdate time.Time
	cmd                 *exec.Cmd
	done                chan struct{}
	cancel              context.CancelFunc
	stopping            bool
	finishOnce          sync.Once
	tempPath            string
	outputPath          string
	startedAt           time.Time
	totalDuration       time.Duration
	processedDuration   time.Duration
}

// VODDownloadProgress is a snapshot of an active or completed VOD download.
type VODDownloadProgress struct {
	Active          bool      `json:"active"`
	Downloaded      bool      `json:"downloaded"`
	Progress        string    `json:"progress,omitempty"`
	TotalSize       int64     `json:"totalSize,omitempty"`
	BytesPerSecond  int64     `json:"bytesPerSecond,omitempty"`
	Speed           string    `json:"speed,omitempty"`
	Preset          string    `json:"preset,omitempty"`
	Operation       string    `json:"operation,omitempty"`
	OriginalSize    int64     `json:"originalSize,omitempty"`
	ETASeconds      int64     `json:"etaSeconds,omitempty"`
	DurationSeconds float64   `json:"-"`
	VideoCodec      string    `json:"videoCodec,omitempty"`
	VideoWidth      int       `json:"videoWidth,omitempty"`
	VideoHeight     int       `json:"videoHeight,omitempty"`
	TotalBitrate    int64     `json:"totalBitrate,omitempty"`
	StartedAt       time.Time `json:"startedAt,omitempty,omitzero"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty,omitzero"`
}

type VODDownloader struct {
	sync.Mutex
	dir       string
	retention time.Duration
	active    map[string]*vodDownload
	removing  map[string]struct{}
	presets   map[string]vodPresetCacheEntry
	stopping  bool
}

type vodPresetCacheEntry struct {
	size     int64
	modTime  time.Time
	metadata vodDownloadMetadata
	retryAt  time.Time
}

type vodDownloadMetadata struct {
	Preset          VODDownloadPreset
	OriginalSize    int64
	DurationSeconds float64
	VideoCodec      string
	VideoWidth      int
	VideoHeight     int
	TotalBitrate    int64
}

var vodDownloadState = &VODDownloader{retention: defaultVODDownloadRetention}

// SetVODDownloadDir configures the folder used by manual VOD downloads.
func SetVODDownloadDir(dir string) {
	vodDownloadState.SetDir(dir)
}

// SetDir configures the folder used by manual VOD downloads.
func (d *VODDownloader) SetDir(dir string) {
	d.Lock()
	defer d.Unlock()
	d.dir = strings.TrimSpace(dir)
	d.presets = nil
}

// SetVODDownloadRetention configures how long completed VOD downloads are kept.
func SetVODDownloadRetention(retention time.Duration) {
	vodDownloadState.SetRetention(retention)
}

// SetRetention configures how long completed VOD downloads are kept.
func (d *VODDownloader) SetRetention(retention time.Duration) {
	d.Lock()
	defer d.Unlock()
	d.retention = retention
}
