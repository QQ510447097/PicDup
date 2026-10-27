package core

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCorpusDebug(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "corpus")
	thumbs, _ := NewThumbCache(t.TempDir())
	opts := DefaultSettings().Options([]string{dir}).Options
	cands, _, _ := collectFiles(context.Background(), []string{dir}, opts, nil, func(int) {})
	files, _ := analyzeFiles(context.Background(), cands, thumbs, func(_, _ int, _ string) {})
	byName := map[string]*File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	pairs := [][3]string{
		{"photo_1.jpg", "photo_1_crop.jpg", "crop"},
		{"scene_3.jpg", "scene_3_small.jpg", "small"},
		{"scene_3.jpg", "scene_3_low.jpg", "low"},
		{"scene_3.jpg", "scene_3_bright.jpg", "bright"},
		{"scene_3.jpg", "scene_3_crop.jpg", "crop"},
		{"scene_5.jpg", "scene_5_gray.jpg", "gray"},
		{"photo_1.jpg", "photo_1_half.jpg", "half"},
	}
	for _, p := range pairs {
		a, b := byName[p[0]], byName[p[1]]
		if a == nil || b == nil {
			t.Logf("missing %s %s", p[0], p[1])
			continue
		}
		t.Logf("%-8s full=%d crop=%d dhash=%d hist=%.3f pass=%v", p[2], popcount(a.PHash^b.PHash), pHashDistance(a, b), popcount(a.DHash^b.DHash), histogramIntersection(a.Hist, b.Hist), similarPair(a, b, 8))
	}
}
