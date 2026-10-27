package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestResultSnapshotKeepsAllFilesAndHistograms(t *testing.T) {
	root := t.TempDir()
	e := &Engine{paths: Paths{LastResultPath: filepath.Join(root, "last-result.json")}}

	a := &File{Path: filepath.Join(root, "a.jpg"), Name: "a.jpg", SHA256: "a", Size: 10, Width: 100, Height: 80}
	b := &File{Path: filepath.Join(root, "b.jpg"), Name: "b.jpg", SHA256: "b", Size: 12, Width: 100, Height: 80}
	a.Hist[1] = 0.5
	b.Hist[2] = 0.25
	g := newGroup("similar", []*File{a, b}, a)
	r := &Result{
		ID:        "test",
		Roots:     []string{root},
		Options:   DefaultSettings().Options([]string{root}).Options,
		StartedAt: time.Now(),
		Groups:    []*Group{g},
		Files:     []*File{a, b},
		Stats:     Stats{FilesScanned: 2, FilesAnalyzed: 2, UniqueImages: 2},
	}
	fillResultIndex(r)
	e.result = r
	e.saveResult()
	if e.loadResult() == nil {
		t.Fatal("result did not reload")
	}

	// Simulate a restart: a persisted result must retain every analyzed file so
	// that restoring an ignored group can rebuild groups without decoding.
	e2 := &Engine{paths: Paths{LastResultPath: filepath.Join(root, "last-result.json")}}
	loaded := e2.loadResult()
	if loaded == nil {
		t.Fatal("second load failed")
	}
	if len(loaded.Files) != 2 {
		t.Fatalf("loaded files = %d, want 2", len(loaded.Files))
	}
	found := false
	for _, f := range loaded.Files {
		if f.Path == a.Path {
			found = true
			if f.Hist[1] != 0.5 {
				t.Fatalf("histogram was not persisted: %+v", f.Hist)
			}
		}
	}
	if !found {
		t.Fatal("missing persisted file")
	}
}
