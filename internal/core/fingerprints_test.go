package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintCacheReusesAnalysis(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.jpg")
	writeJPEG(t, path, testImage(31, 320, 240), 90)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cands := []candidate{{path: path, size: info.Size(), mod: info.ModTime().UnixMilli()}}
	cachePath := filepath.Join(dir, "fingerprints.json")

	cache := NewFingerprintCache(cachePath)
	first, firstIssues, firstMetrics := analyzeFilesWithCache(context.Background(), cands, cache, func(int, int, string) {})
	if len(firstIssues) != 0 || len(first) != 1 {
		t.Fatalf("first analyze: files=%d issues=%v", len(first), firstIssues)
	}
	if firstMetrics.cacheMisses.Load() != 1 || firstMetrics.cacheHits.Load() != 0 {
		t.Fatalf("first cache metrics: hits=%d misses=%d", firstMetrics.cacheHits.Load(), firstMetrics.cacheMisses.Load())
	}
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}

	loaded := NewFingerprintCache(cachePath)
	second, secondIssues, secondMetrics := analyzeFilesWithCache(context.Background(), cands, loaded, func(int, int, string) {})
	if len(secondIssues) != 0 || len(second) != 1 {
		t.Fatalf("second analyze: files=%d issues=%v", len(second), secondIssues)
	}
	if secondMetrics.cacheHits.Load() != 1 || secondMetrics.cacheMisses.Load() != 0 {
		t.Fatalf("second cache metrics: hits=%d misses=%d", secondMetrics.cacheHits.Load(), secondMetrics.cacheMisses.Load())
	}
	if first[0].SHA256 != second[0].SHA256 || first[0].PHash != second[0].PHash || first[0].Width != second[0].Width {
		t.Fatalf("cached file differs: first=%+v second=%+v", first[0], second[0])
	}
	if second[0].ThumbKey != "" {
		t.Fatalf("analysis unexpectedly generated a thumbnail")
	}
}

func TestFingerprintCacheInvalidatesOnFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "changing.jpg")
	writeJPEG(t, path, testImage(41, 240, 180), 88)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cands := []candidate{{path: path, size: info.Size(), mod: info.ModTime().UnixMilli()}}
	cachePath := filepath.Join(dir, "fingerprints.json")
	cache := NewFingerprintCache(cachePath)
	_, _, _ = analyzeFilesWithCache(context.Background(), cands, cache, func(int, int, string) {})
	if err := cache.Save(); err != nil {
		t.Fatal(err)
	}

	writeJPEG(t, path, testImage(99, 320, 200), 88)
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	cands = []candidate{{path: path, size: info.Size(), mod: info.ModTime().UnixMilli()}}
	loaded := NewFingerprintCache(cachePath)
	_, _, metrics := analyzeFilesWithCache(context.Background(), cands, loaded, func(int, int, string) {})
	if metrics.cacheHits.Load() != 0 || metrics.cacheMisses.Load() != 1 {
		t.Fatalf("changed file should be a cache miss: hits=%d misses=%d", metrics.cacheHits.Load(), metrics.cacheMisses.Load())
	}
}
