package config

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"time"
)

// Every default below is also written out, with the same value, in
// shelf.example.yaml — the file's promise is "the value shown is the built-in
// default, so deleting a line changes nothing".
// TestExampleFile_everyValueIsTheBuiltInDefault holds the two to that promise.
const (
	appName = "shelf"

	defaultListen              = "0.0.0.0"
	defaultPort                = 8080
	defaultReadHeaderTimeout   = 10 * time.Second
	defaultShutdownGrace       = 10 * time.Second
	defaultMaxDepth            = 3
	defaultCoverMaxLooseImages = 3
	defaultQuality             = 82
	defaultFormat              = "jpeg" // CON-003: jpeg only in v1
	defaultMaxSourceBytes      = int64(64 << 20)
	defaultPDFWorkers          = 1
	defaultPDFDefaultWidth     = 1400
	defaultPDFMaxWidth         = 3000
	defaultRecentlyAddedDays   = 14 // amendment A-8 (ruling E-9)
	defaultPrefetch            = 4
	defaultReadingDirection    = "ltr"
	defaultDisplayMode         = "single"
	defaultFitMode             = "height" // amendment A-2 (was "contain" in arch §3.2)
	defaultTheme               = "system"
	defaultSessionTTL          = 720 * time.Hour
	defaultLogLevel            = "info"
	defaultLogFormat           = "text"
)

// defaultThumbnailWidths is amendment A-1: derived from the real rendered sizes
// of the UI at 2× DPR (impl-plan §0.4), not guessed. A request snaps up to the
// nearest entry, so a set that does not match what the UI asks for silently
// doubles bandwidth.
func defaultThumbnailWidths() []int { return []int{120, 240, 400, 640} }

// defaults is the whole configuration as it stands before a file is read.
// Worker counts are still 0 ("choose for me") and the storage directories are
// still empty ("use the per-OS location"); resolve turns both into real values.
func defaults() *Config {
	return &Config{
		Server: Server{
			Listen:              defaultListen,
			Port:                defaultPort,
			BasePath:            "",
			ReadHeaderTimeout:   defaultReadHeaderTimeout,
			ShutdownGrace:       defaultShutdownGrace,
			TrustedProxyHeaders: false,
			// Amendment A-11 / ruling E-26. Written out rather than left to the
			// zero value so that the one security-relevant default in this block
			// is visible next to the others.
			AllowRootEditing: false,
			// Amendment A-12 / ruling E-40, and the same reasoning: an empty list
			// means the directory picker refuses every request, so an operator
			// who never heard of this key is served exactly the pre-E-40
			// product. Empty and non-nil, like `scan.exclude_globs` below —
			// shelf.example.yaml prints every default verbatim, and `[]` there
			// must equal what this line produces.
			BrowseBases: []string{},
		},
		Scan: Scan{
			OnStart:             true,
			Workers:             0,
			MaxDepth:            defaultMaxDepth,
			FollowSymlinks:      false,
			CoverMaxLooseImages: defaultCoverMaxLooseImages,
			ExcludeGlobs:        []string{},
			IncludeGlobs:        []string{}, // amendment A-3
		},
		Thumbnails: Thumbnails{
			Widths:         defaultThumbnailWidths(),
			Quality:        defaultQuality,
			Format:         defaultFormat,
			Workers:        0,
			CoverFirst:     true,
			AVIFEnabled:    true,
			MaxSourceBytes: defaultMaxSourceBytes,
		},
		PDF: PDF{
			Enabled:      true,
			Workers:      defaultPDFWorkers,
			DefaultWidth: defaultPDFDefaultWidth,
			MaxWidth:     defaultPDFMaxWidth,
			CacheRenders: true,
		},
		Library: Library{
			RecentlyAddedDays: defaultRecentlyAddedDays,
		},
		Reader: Reader{
			Prefetch:         defaultPrefetch,
			ReadingDirection: defaultReadingDirection,
			DisplayMode:      defaultDisplayMode,
			FitMode:          defaultFitMode,
			Theme:            defaultTheme,
		},
		Auth: nil, // no `auth:` block means no password (decisions E-8)
		Log: Log{
			Level:        defaultLogLevel,
			Format:       defaultLogFormat,
			HTTPRequests: true,
		},
	}
}

// defaultRoot is what a `roots:` entry looks like before its keys are read.
func defaultRoot() Root { return Root{Enabled: true} }

// defaultAuth is what an `auth:` block looks like before its keys are read.
func defaultAuth() Auth { return Auth{SessionTTL: defaultSessionTTL} }

// scanWorkersFor is `0 => min(8, max(2, NumCPU/2))`: archive reading is I/O
// bound, and more than eight readers buys nothing.
func scanWorkersFor(numCPU int) int { return min(8, max(2, numCPU/2)) }

// thumbWorkersFor is `0 => min(4, NumCPU)`: decoding is CPU and RAM bound at
// roughly 25 MiB peak per in-flight decode.
func thumbWorkersFor(numCPU int) int { return min(4, max(1, numCPU)) }

// resolve turns "choose for me" values into the real ones: worker counts,
// per-OS storage directories, the normalised width set and the session key
// path. After it, what a consumer reads is what the server does.
func (c *Config) resolve(env environment) error {
	if c.Scan.Workers == 0 {
		c.Scan.Workers = scanWorkersFor(runtime.NumCPU())
	}
	if c.Thumbnails.Workers == 0 {
		c.Thumbnails.Workers = thumbWorkersFor(runtime.NumCPU())
	}

	slices.Sort(c.Thumbnails.Widths)
	c.Thumbnails.Widths = slices.Compact(c.Thumbnails.Widths)

	dir, err := c.resolveDir(c.Storage.DataDir, env.dataDir)
	if err != nil {
		return c.src.errf("storage.data_dir", "%s", err)
	}
	c.Storage.DataDir = dir

	dir, err = c.resolveDir(c.Storage.CacheDir, env.cacheDir)
	if err != nil {
		return c.src.errf("storage.cache_dir", "%s", err)
	}
	c.Storage.CacheDir = dir

	if c.Auth != nil && c.Auth.SessionKeyFile == "" {
		c.Auth.SessionKeyFile = filepath.Join(c.Storage.DataDir, "session.key")
	}
	return nil
}

// resolveDir keeps a configured directory (made absolute so it cannot depend on
// the working directory later) or falls back to the per-OS default.
func (c *Config) resolveDir(configured string, fallback func() (string, error)) (string, error) {
	if configured == "" {
		return fallback()
	}
	abs, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %q to an absolute path: %w", configured, err)
	}
	return abs, nil
}
