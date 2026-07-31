package thumbs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shelf/internal/ids"
)

// cache is the on-disk half of FR-THM-001/002: a permanent file cache under
// <cache_dir>, addressed by the arch §5.6 hash and fanned out two levels deep.
//
// It holds no state beyond its configuration. Every operation is a syscall on a
// path derived from the key, which is what makes deleting the directory
// underneath a running server a latency event rather than a correctness one
// (FR-THM-007).
type cache struct {
	// dir is an absolute <cache_dir>.
	dir string
	// format is the CON-003 encoder name. It is a hash input (D-18).
	format string
	// quality is the JPEG quality, also a hash input.
	quality int
	// ext is the file extension the format writes.
	ext string
}

// dirPerm and filePerm keep the cache private to the user running the server.
// It contains renderings of the user's library and nothing else needs to read
// it.
const (
	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

func newCache(dir, format string, quality int) (*cache, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving cache directory %q: %w", dir, err)
	}
	if format == "" {
		format = "jpeg"
	}
	// CON-003: JPEG only in v1. Refusing anything else here is what keeps the
	// promise that the format string in the hash describes the bytes on disk.
	if format != "jpeg" {
		return nil, fmt.Errorf("thumbs: unsupported thumbnail format %q (CON-003: jpeg only in v1)", format)
	}
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	return &cache{dir: filepath.Clean(abs), format: format, quality: quality, ext: ".jpg"}, nil
}

// key derives the cache key of one thumbnail. The hash input is arch §5.6's,
// spelled out in internal/ids so that the two cache domains cannot drift apart:
//
//	"shelf-thumb/1" ‖ 0 ‖ <id> ‖ 0 ‖ <page_no> ‖ 0 ‖ <width>
//	                ‖ 0 ‖ <format> ‖ 0 ‖ <quality> ‖ 0 ‖ <content_version>
//
// SHA-256, first 10 bytes, lowercase base32 → 16 characters.
func (c *cache) key(id string, pageNo, width int, contentVersion string) string {
	return ids.ThumbKey(id, pageNo, width, c.format, c.quality, contentVersion)
}

// pdfKey derives the cache key of one rasterised PDF page: the identical scheme
// under the `shelf-pdfpage/1` domain, so a 640 px thumbnail and a 640 px render
// of the same page never share a file (arch §5.6).
func (c *cache) pdfKey(bookID string, pageNo, width int, contentVersion string) string {
	return ids.PDFPageKey(bookID, pageNo, width, c.format, c.quality, contentVersion)
}

// kindDir is <cache_dir>/<kind>. kind comes from the closed [Kind] set, never
// from a caller-supplied string, so this cannot leave the cache directory.
func (c *cache) kindDir(kind Kind) string { return filepath.Join(c.dir, string(kind)) }

// path is FR-THM-002 literally: `ca/che/<hash>.jpg`, the two-level fan-out
// taken from the first four characters of the key.
//
// At the reference collection's 1.36 M pages a fully warm page cache would hold
// ~1 300 files per leaf across 1 024 leaves, which ext4, XFS, APFS and NTFS all
// handle without a directory-scan cliff.
func (c *cache) path(kind Kind, key string) string {
	return filepath.Join(c.kindDir(kind), key[0:2], key[2:4], key+c.ext)
}

// validKey rejects a key that could not have come from ids: the path builder
// slices the first four characters, and the base32 alphabet is what guarantees
// those cannot be `..` or a separator.
func validKey(key string) error {
	if !ids.Valid(key) {
		return fmt.Errorf("thumbs: malformed cache key %q", key)
	}
	return nil
}

// lookup stats a cached file. A missing file, a missing fan-out directory and a
// missing cache directory are all the same answer — false — which is the whole
// of FR-THM-007: there is no path through this package that trusts the cache to
// exist.
func (c *cache) lookup(kind Kind, key string) (Result, bool) {
	if err := validKey(key); err != nil {
		return Result{}, false
	}
	p := c.path(kind, key)
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return Result{}, false
	}
	// A zero-length file cannot be a JPEG. Treating it as a miss means a
	// truncated write from some earlier catastrophe self-heals instead of being
	// served forever.
	if fi.Size() == 0 {
		return Result{}, false
	}
	return Result{Key: key, Path: p, Size: fi.Size(), ModTime: fi.ModTime()}, true
}

// publishAttempts is how many times publish rebuilds the fan-out directory and
// starts over when the cache disappears underneath it.
//
// One attempt is not enough for FR-THM-007: `rm -rf <cache_dir>` can land
// between the MkdirAll and the rename, and the temp file goes with it. Five is
// far more than the race needs — a publish is tens of microseconds — and it is
// still bounded, so a cache directory that is permanently unwritable (a full
// disk, a bad mount) fails fast instead of spinning.
const publishAttempts = 5

// publish writes data and makes it visible atomically.
//
// The sequence is write-to-temp then rename, both inside the destination
// directory so the rename cannot cross a filesystem. Two properties follow, and
// both are tested:
//
//   - a reader never observes a partial file, because a reader only ever sees
//     the final name and rename(2) is atomic on every supported platform;
//   - a process killed mid-write leaves a `.tmp` file, never a truncated JPEG
//     under the real name. The next generation writes a fresh temp name.
//
// MkdirAll runs on every publish rather than once at startup. That is the
// difference between FR-THM-007 being a property and being a hope: the cache
// directory may have been deleted a microsecond ago.
func (c *cache) publish(kind Kind, key string, data []byte) (Result, error) {
	if err := validKey(key); err != nil {
		return Result{}, err
	}
	final := c.path(kind, key)
	dir := filepath.Dir(final)

	var err error
	for range publishAttempts {
		var res Result
		res, err = publishOnce(final, dir, key, data)
		if err == nil {
			return res, nil
		}
		// Anything but "it is not there any more" is a real failure: a full
		// disk, a permission problem, a read-only mount.
		if !errors.Is(err, os.ErrNotExist) {
			return Result{}, err
		}
	}
	return Result{}, err
}

func publishOnce(final, dir, key string, data []byte) (Result, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return Result{}, fmt.Errorf("creating thumbnail cache directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, key+".*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("creating temporary thumbnail file: %w", err)
	}
	tmpName := tmp.Name()
	// Remove the temp file on every failure path. After a successful rename the
	// name no longer exists and the removal is a harmless ENOENT.
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(filePerm); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = tmp.Close()
		return Result{}, fmt.Errorf("setting thumbnail file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return Result{}, fmt.Errorf("writing thumbnail: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("closing thumbnail: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return Result{}, fmt.Errorf("publishing thumbnail: %w", err)
	}

	res := Result{Key: key, Path: final, Size: int64(len(data)), ModTime: time.Now()}
	if fi, err := os.Stat(final); err == nil {
		res.Size, res.ModTime = fi.Size(), fi.ModTime()
	}
	return res, nil
}

// LookupPDFPage reports a cached full-size PDF render (FR-SRV-006 / arch §5.6).
// Rendering is internal/source's job; the cache layout is this package's, so
// the two domains stay in one file.
func (s *Service) LookupPDFPage(bookID string, pageNo, width int, contentVersion string) (Result, bool) {
	return s.cache.lookup(KindPDF, s.cache.pdfKey(bookID, pageNo, width, contentVersion))
}

// StorePDFPage publishes a rasterised PDF page under <cache_dir>/pdf, with the
// same atomic write-then-rename as a thumbnail.
func (s *Service) StorePDFPage(bookID string, pageNo, width int, contentVersion string, data []byte) (Result, error) {
	if len(data) == 0 {
		return Result{}, fmt.Errorf("thumbs: refusing to cache an empty pdf render of book %q page %d", bookID, pageNo)
	}
	return s.cache.publish(KindPDF, s.cache.pdfKey(bookID, pageNo, width, contentVersion), data)
}

// PDFPagePath reports where a rasterised page would live, for diagnostics and
// for the HTTP layer's ETag.
func (s *Service) PDFPagePath(bookID string, pageNo, width int, contentVersion string) (key, path string) {
	k := s.cache.pdfKey(bookID, pageNo, width, contentVersion)
	return k, s.cache.path(KindPDF, k)
}

// safeRel is path-traversal layer 2 for a cover file path that arrived from the
// index rather than from a book source.
//
// It is a deliberate duplicate of internal/source's unexported rule, and the
// element check is not redundant with filepath.IsLocal: on Linux
// filepath.IsLocal(`..\win`) is TRUE, because backslash is an ordinary filename
// byte there, so an index built on Linux and opened on Windows would otherwise
// hand `..\win` to the filesystem.
func safeRel(rel string) (string, error) {
	clean := filepath.ToSlash(rel)
	clean = strings.ReplaceAll(clean, `\`, "/")
	clean = filepath.ToSlash(filepath.Clean(filepath.FromSlash(clean)))
	if clean == "" || clean == "." || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("thumbs: unsafe relative path %q", rel)
	}
	if !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("thumbs: unsafe relative path %q", rel)
	}
	for _, el := range strings.Split(clean, "/") {
		if el == "" || el == "." || el == ".." {
			return "", fmt.Errorf("thumbs: unsafe relative path %q", rel)
		}
	}
	return clean, nil
}
