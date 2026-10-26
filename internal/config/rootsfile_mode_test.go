package config

import (
	"os"
	"testing"
)

// TestRootsFile_writeKeepsANonDefaultMode — writeFileAtomic's doc says "the
// mode is copied from the file being replaced". The sibling test starts from
// 0600, which is also what os.CreateTemp creates, so it cannot tell a copied
// mode from a hard-coded 0600. This one starts from 0640, which only copying
// can reproduce.
func TestRootsFile_writeKeepsANonDefaultMode(t *testing.T) {
	t.Parallel()
	const start = 0o640
	path, _ := copyExample(t)
	if err := os.Chmod(path, start); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if err := AddRoot(path, Root{Name: "docs", Path: "/mnt/media/docs", Label: "도서"}); err != nil {
		t.Fatalf("AddRoot: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != start {
		t.Errorf("mode = %v, want %v: the replaced file must keep the mode it had", got, os.FileMode(start))
	}

	// The .bak is created by os.WriteFile and so passes through the umask; the
	// documented property is only that it is never wider than the original.
	bak, err := os.Stat(path + BackupSuffix)
	if err != nil {
		t.Fatalf("stat .bak: %v", err)
	}
	if got := bak.Mode().Perm(); got&^start != 0 {
		t.Errorf(".bak mode = %v, wider than the original %v", got, os.FileMode(start))
	}
}
