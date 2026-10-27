package core

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	fingerprintCacheVersion = 1
	fingerprintMaxAge       = 90 * 24 * time.Hour
	fingerprintMaxEntries   = 50000
)

// FingerprintEntry is the cached result of hashing and analysing one image.
// The cache key already contains path, size and mtime, so an entry is only
// reused while the file is unchanged.
type FingerprintEntry struct {
	SHA256     string      `json:"sha256,omitempty"`
	Width      int         `json:"width,omitempty"`
	Height     int         `json:"height,omitempty"`
	Format     string      `json:"format,omitempty"`
	PHash      uint64      `json:"pHash,omitempty"`
	PHashCrop  uint64      `json:"pHashCrop,omitempty"`
	PHashOuter uint64      `json:"pHashOuter,omitempty"`
	DHash      uint64      `json:"dHash,omitempty"`
	Hist       [64]float32 `json:"hist,omitempty"`
	Detail     float32     `json:"detail,omitempty"`
	Error      string      `json:"error,omitempty"`
	CachedAt   int64       `json:"cachedAt,omitempty"`
}

type fingerprintDisk struct {
	Version int                         `json:"version"`
	Entries map[string]FingerprintEntry `json:"entries"`
}

// FingerprintCache persists image fingerprints between scans. It is optional:
// callers pass nil when caching is not desired (for example in unit tests).
type FingerprintCache struct {
	path    string
	mu      sync.Mutex
	entries map[string]FingerprintEntry
}

// NewFingerprintCache loads fingerprints.json. A missing or corrupt cache is
// treated as empty; cache failures must never prevent the app from starting.
func NewFingerprintCache(path string) *FingerprintCache {
	c := &FingerprintCache{path: path, entries: make(map[string]FingerprintEntry)}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var disk fingerprintDisk
	if err := json.Unmarshal(data, &disk); err != nil || disk.Version != fingerprintCacheVersion {
		return c
	}
	for key, entry := range disk.Entries {
		if key != "" {
			c.entries[key] = entry
		}
	}
	return c
}

// Key derives the cache key for a file. It intentionally matches the shape of
// ThumbCache.Key so both caches invalidate on the same file changes.
func (c *FingerprintCache) Key(path string, size, mod int64) string {
	p := strings.ToLower(filepath.Clean(path))
	sum := sha1.Sum([]byte(p + "|" + strconv.FormatInt(size, 10) + "|" + strconv.FormatInt(mod, 10)))
	return hex.EncodeToString(sum[:12])
}

// Get returns a cached entry and marks it as recently used.
func (c *FingerprintCache) Get(key string) (FingerprintEntry, bool) {
	if c == nil || key == "" {
		return FingerprintEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if ok {
		entry.CachedAt = time.Now().Unix()
		c.entries[key] = entry
	}
	return entry, ok
}

// Put stores an entry.
func (c *FingerprintCache) Put(key string, entry FingerprintEntry) {
	if c == nil || key == "" {
		return
	}
	entry.CachedAt = time.Now().Unix()
	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
}

// Len reports the number of cached entries.
func (c *FingerprintCache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Stats reports the number of entries and the cache file size.
func (c *FingerprintCache) Stats() (int, int64) {
	if c == nil {
		return 0, 0
	}
	c.mu.Lock()
	n := len(c.entries)
	path := c.path
	c.mu.Unlock()
	var bytes int64
	if path != "" {
		if info, err := os.Stat(path); err == nil {
			bytes = info.Size()
		}
	}
	return n, bytes
}

// Clear drops all entries and removes the cache file.
func (c *FingerprintCache) Clear() error {
	if c == nil || c.path == "" {
		return nil
	}
	c.mu.Lock()
	c.entries = make(map[string]FingerprintEntry)
	path := c.path
	c.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Save prunes stale entries and atomically writes the cache to disk.
func (c *FingerprintCache) Save() error {
	if c == nil || c.path == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	cutoff := time.Now().Add(-fingerprintMaxAge).Unix()
	filtered := make(map[string]FingerprintEntry, len(c.entries))
	for key, entry := range c.entries {
		if entry.CachedAt == 0 || entry.CachedAt >= cutoff {
			filtered[key] = entry
		}
	}
	if len(filtered) > fingerprintMaxEntries {
		type pair struct {
			key string
			at  int64
		}
		pairs := make([]pair, 0, len(filtered))
		for key, entry := range filtered {
			pairs = append(pairs, pair{key: key, at: entry.CachedAt})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].at > pairs[j].at })
		trimmed := make(map[string]FingerprintEntry, fingerprintMaxEntries)
		for _, p := range pairs[:fingerprintMaxEntries] {
			trimmed[p.key] = filtered[p.key]
		}
		filtered = trimmed
	}
	c.entries = filtered

	data, err := json.Marshal(fingerprintDisk{Version: fingerprintCacheVersion, Entries: filtered})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
