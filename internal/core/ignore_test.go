package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testFile(path, sha string) *File {
	return &File{Path: path, Name: filepath.Base(path), SHA256: sha, Size: 10}
}

func TestIgnoreStoreSuppressesMatchingGroups(t *testing.T) {
	store := NewIgnoreStore(filepath.Join(t.TempDir(), "ignored.json"))
	g1 := newGroup("similar", []*File{
		testFile(`D:\Pictures\a.jpg`, "aaa"),
		testFile(`D:\Pictures\b.jpg`, "bbb"),
	}, testFile(`D:\Pictures\a.jpg`, "aaa"))
	g2 := newGroup("similar", []*File{
		testFile(`E:\Backup\a.jpg`, "bbb"),
		testFile(`E:\Backup\b.jpg`, "aaa"),
	}, testFile(`E:\Backup\a.jpg`, "bbb"))
	g3 := newGroup("similar", []*File{
		testFile(`D:\Pictures\a.jpg`, "aaa"),
		testFile(`D:\Pictures\c.jpg`, "ccc"),
	}, testFile(`D:\Pictures\a.jpg`, "aaa"))

	rule, added, err := store.AddGroup(g1)
	if err != nil || !added || rule.ID == "" {
		t.Fatalf("add rule: added=%v err=%v", added, err)
	}
	if !store.Matches(g1) || !store.Matches(g2) {
		t.Fatal("same content set should be ignored regardless of paths/order")
	}
	if store.Matches(g3) {
		t.Fatal("a changed group should not be ignored")
	}
	if got := len(store.FilterGroups([]*Group{g1, g2, g3})); got != 1 {
		t.Fatalf("filtered groups = %d, want 1", got)
	}

	reloaded := NewIgnoreStore(store.path)
	if !reloaded.Matches(g1) {
		t.Fatal("rule was not persisted")
	}
	removed, err := reloaded.Remove(rule.ID)
	if err != nil || !removed {
		t.Fatalf("remove rule: removed=%v err=%v", removed, err)
	}
	if reloaded.Matches(g1) {
		t.Fatal("rule still matches after removal")
	}
}

func TestIgnoreStoreExactGroupKeyedBySHA(t *testing.T) {
	store := NewIgnoreStore(filepath.Join(t.TempDir(), "ignored.json"))
	g1 := newGroup("exact", []*File{
		testFile(`D:\A\one.jpg`, "same"),
		testFile(`D:\A\two.jpg`, "same"),
	}, testFile(`D:\A\one.jpg`, "same"))
	g2 := newGroup("exact", []*File{
		testFile(`E:\B\copy.jpg`, "same"),
		testFile(`E:\B\another.jpg`, "same"),
	}, testFile(`E:\B\copy.jpg`, "same"))
	if _, _, err := store.AddGroup(g1); err != nil {
		t.Fatal(err)
	}
	if !store.Matches(g2) {
		t.Fatal("exact duplicates with the same content should be ignored")
	}
}

func TestMigrateLegacyData(t *testing.T) {
	root := t.TempDir()
	legacyConfig := filepath.Join(root, "legacy-config")
	legacyLocal := filepath.Join(root, "legacy-local")
	portable := filepath.Join(root, "portable")
	if err := os.MkdirAll(filepath.Join(legacyConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyConfig, "settings.json"), []byte(`{"similarity":8}`), 0o644); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(legacyLocal, "trash")
	if err := os.MkdirAll(filepath.Join(trash, "batches", "b1"), 0o755); err != nil {
		t.Fatal(err)
	}
	itemPath := filepath.Join(trash, "batches", "b1", "00000_a.jpg")
	index, err := json.Marshal(map[string]any{
		"batches": []TrashBatch{{
			ID:        "b1",
			CreatedAt: time.Now(),
			Mode:      "trash",
			Items: []TrashItem{{
				OriginalPath: "D:/Pictures/a.jpg",
				StoredPath:   itemPath,
				Size:         4,
				Name:         "a.jpg",
			}},
			TotalSize: 4,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, "index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(itemPath, []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	warnings := migrateLegacyData(portable, legacyConfig, legacyLocal)
	if len(warnings) != 0 {
		t.Fatalf("migration warnings: %v", warnings)
	}
	if _, err := os.Stat(filepath.Join(portable, "settings.json")); err != nil {
		t.Fatalf("settings not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(portable, "trash", "index.json")); err != nil {
		t.Fatalf("trash not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trash, "index.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy trash should be moved, stat err=%v", err)
	}
	migratedIndex, err := os.ReadFile(filepath.Join(portable, "trash", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var loaded struct {
		Batches []TrashBatch `json:"batches"`
	}
	if err := json.Unmarshal(migratedIndex, &loaded); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Batches) != 1 || len(loaded.Batches[0].Items) != 1 {
		t.Fatalf("unexpected migrated index: %+v", loaded)
	}
	newItem := loaded.Batches[0].Items[0].StoredPath
	if !filepath.HasPrefix(newItem, filepath.Join(portable, "trash")) {
		t.Fatalf("stored path was not rewritten: %s", newItem)
	}
	if _, err := os.Stat(newItem); err != nil {
		t.Fatalf("migrated trash item missing: %v", err)
	}
}
