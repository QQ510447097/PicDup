//go:build windows

package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecycleFiles(t *testing.T) {
	if !recycleSupported() {
		t.Skip("recycle bin unsupported")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "picdup-recycle-test.txt")
	if err := os.WriteFile(path, []byte("temporary test payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	moved, bytes, failures := recycleFiles([]string{path})
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	if moved != 1 || bytes == 0 {
		t.Fatalf("expected one recycled file, got moved=%d bytes=%d", moved, bytes)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after recycling: %v", err)
	}
}
