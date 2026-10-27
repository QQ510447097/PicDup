package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestCorpusIntegration exercises the full pipeline against the checked-in
// corpus. It is skipped automatically when the corpus is absent.
func TestCorpusIntegration(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "corpus")
	if _, err := os.Stat(dir); err != nil {
		t.Skip("test corpus not present")
	}
	thumbs, err := NewThumbCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultSettings().Options([]string{dir}).Options
	cands, _, err := collectFiles(context.Background(), []string{dir}, opts, nil, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	files, issues := analyzeFiles(context.Background(), cands, thumbs, func(_, _ int, _ string) {})
	if len(issues) > 0 {
		t.Logf("read issues: %+v", issues)
	}
	groups, stats, _ := BuildGroups(context.Background(), files, opts, func(float64, string) {})
	t.Logf("scanned=%d exact=%d similar=%d", len(files), stats.ExactGroups, stats.SimilarGroups)
	for _, g := range groups {
		names := make([]string, 0, len(g.Files))
		for _, f := range g.Files {
			names = append(names, f.Name)
		}
		t.Logf("%s group: %v", g.Kind, names)
	}
	if stats.ExactGroups != 3 {
		t.Errorf("expected 3 exact groups, got %d", stats.ExactGroups)
	}
	if stats.SimilarGroups != 3 {
		t.Errorf("expected 3 similar groups, got %d", stats.SimilarGroups)
	}
	want := map[string][]string{
		"photo_1.jpg": {"photo_1_crop.jpg", "photo_1_half.jpg", "photo_1_low.jpg", "photo_1_contrast.jpg"},
		"scene_3.jpg": {"scene_3_crop.jpg", "scene_3_low.jpg", "scene_3_small.jpg", "scene_3_bright.jpg"},
		"scene_5.jpg": {"scene_5_gray.jpg", "scene_5_resize.png"},
	}
	for keeper, variants := range want {
		var found *Group
		for _, g := range groups {
			for _, f := range g.Files {
				if f.Name == keeper {
					found = g
					break
				}
			}
		}
		if found == nil {
			t.Errorf("keeper %s is not in any group", keeper)
			continue
		}
		names := map[string]bool{}
		for _, f := range found.Files {
			names[f.Name] = true
		}
		for _, variant := range variants {
			if !names[variant] {
				t.Errorf("expected %s to be grouped with %s", variant, keeper)
			}
		}
	}
}
