package thumbs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FR-THM-008 — cache usage and purge.
//
// Two operations, both of which must be safe to run against a cache directory
// that is being written to, and neither of which may touch anything outside it.
// The safety property is structural rather than defensive: a purge selects a
// [Kind], which is a closed enumeration, and never a path. There is no string a
// caller can pass that turns into a directory of their choosing.

// UsageEntry is one row of `GET /api/cache/usage` (arch §7.9).
type UsageEntry struct {
	Kind  Kind  `json:"kind"`
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Usage is the whole payload of `GET /api/cache/usage`.
type Usage struct {
	ComputedAt time.Time
	Entries    []UsageEntry
	TotalBytes int64
	TotalFiles int64
	CacheDir   string
}

// PurgeResult is the body of `DELETE /api/cache` (arch §7.9).
type PurgeResult struct {
	DeletedFiles int64 `json:"deleted_files"`
	FreedBytes   int64 `json:"freed_bytes"`
}

// usageCache memoises the walk. arch §7.9 pins the window at 60 s: at 1.36 M
// files a full walk is not something a settings dialog should trigger on every
// poll, and the number is an estimate the moment it is produced anyway.
type usageCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	at    time.Time
	value Usage
	valid bool
}

// Usage reports per-kind file counts and bytes, reusing a walk younger than
// UsageTTL.
//
// A missing cache directory is not an error: it reports zeros. That is
// FR-THM-007 seen from the reporting side — the answer to "someone deleted the
// cache" is "the cache is empty", not a 500.
func (s *Service) Usage(ctx context.Context) (Usage, error) {
	s.usage.mu.Lock()
	if s.usage.valid && s.now().Sub(s.usage.at) < s.usage.ttl {
		u := s.usage.value
		s.usage.mu.Unlock()
		return u, nil
	}
	s.usage.mu.Unlock()

	u := Usage{ComputedAt: s.now(), CacheDir: s.cache.dir, Entries: make([]UsageEntry, 0, len(kinds))}
	for _, k := range kinds {
		files, bytes, err := walkSize(ctx, s.cache.kindDir(k))
		if err != nil {
			return Usage{}, err
		}
		u.Entries = append(u.Entries, UsageEntry{Kind: k, Files: files, Bytes: bytes})
		u.TotalFiles += files
		u.TotalBytes += bytes
	}

	s.usage.mu.Lock()
	s.usage.value, s.usage.at, s.usage.valid = u, u.ComputedAt, true
	s.usage.mu.Unlock()
	return u, nil
}

// Purge removes one kind of cached file, or all of them, and reports what it
// freed (FR-THM-008).
//
// `kind` is matched against the [Kind] enumeration before anything is touched.
// An unrecognised value is [ErrUnknownKind] and nothing is deleted — which is
// the entire reason `DELETE /api/cache?kind=` cannot be turned into an
// arbitrary `rm -rf`.
func (s *Service) Purge(ctx context.Context, kind string) (PurgeResult, error) {
	targets, err := purgeTargets(kind)
	if err != nil {
		return PurgeResult{}, err
	}

	var out PurgeResult
	for _, k := range targets {
		dir := s.cache.kindDir(k)
		// Layer 2 of the same guarantee, and it is not decoration: if a future
		// edit ever lets a caller-controlled string reach kindDir, this
		// assertion is what turns the bug into a refusal instead of a deletion.
		if !withinDir(dir, s.cache.dir) {
			return out, fmt.Errorf("%w: %q resolves outside the cache directory", ErrUnknownKind, kind)
		}
		files, bytes, err := walkSize(ctx, dir)
		if err != nil {
			return out, err
		}
		if err := os.RemoveAll(dir); err != nil {
			return out, fmt.Errorf("purging %s cache: %w", k, err)
		}
		out.DeletedFiles += files
		out.FreedBytes += bytes
	}

	s.usage.mu.Lock()
	s.usage.valid = false
	s.usage.mu.Unlock()
	// A purge is a user saying "try again": replaying a ten-minute-old
	// undecodable verdict afterwards would make the button look broken.
	s.forgetNegatives()
	return out, nil
}

// purgeTargets maps the wire value onto the kinds to remove.
func purgeTargets(kind string) ([]Kind, error) {
	switch Kind(kind) {
	case KindAll:
		return kinds, nil
	case KindThumbs, KindPDF, KindWazero:
		return []Kind{Kind(kind)}, nil
	default:
		return nil, fmt.Errorf("%w %q (want thumbs, pdf, wazero or all)", ErrUnknownKind, kind)
	}
}

// walkSize counts regular files and their bytes under dir.
//
// Every per-entry error is swallowed on purpose: the cache is being written to
// while this runs, so a file that vanishes between the readdir and the stat is
// the expected case, not a failure. Only the caller's cancellation stops it.
func walkSize(ctx context.Context, dir string) (files, bytes int64, err error) {
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// A directory that disappeared under us, or was never there.
			return nil //nolint:nilerr // deliberate: see the doc comment
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		files++
		bytes += info.Size()
		return nil
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("walking %s: %w", filepath.Base(dir), err)
	}
	return files, bytes, nil
}

// withinDir reports whether path is dir or lives under it.
func withinDir(path, dir string) bool {
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if cleanPath == cleanDir {
		return true
	}
	return len(cleanPath) > len(cleanDir) &&
		cleanPath[:len(cleanDir)] == cleanDir &&
		cleanPath[len(cleanDir)] == filepath.Separator
}
