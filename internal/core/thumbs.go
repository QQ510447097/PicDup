package core

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// ThumbCache stores JPEG thumbnails on disk, keyed by path+size+mtime.
type ThumbCache struct {
	dir   string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewThumbCache creates (or opens) a thumbnail cache directory.
func NewThumbCache(dir string) (*ThumbCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &ThumbCache{dir: dir, locks: make(map[string]*sync.Mutex)}, nil
}

// Key derives the cache key for a file.
func (c *ThumbCache) Key(path string, size, mod int64) string {
	h := sha1.Sum([]byte(formatKey(path, size, mod)))
	return hex.EncodeToString(h[:12])
}

func formatKey(path string, size, mod int64) string {
	// Lower-case only the path portion to keep dedup stable on Windows.
	lower := make([]byte, len(path))
	for i := 0; i < len(path); i++ {
		ch := path[i]
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		lower[i] = ch
	}
	return string(lower) + "|" + strconv.FormatInt(size, 10) + "|" + strconv.FormatInt(mod, 10)
}

// Path returns the on-disk path of a cache entry.
func (c *ThumbCache) Path(key string) string {
	if key == "" {
		return ""
	}
	prefix := key
	if len(prefix) > 2 {
		prefix = prefix[:2]
	}
	return filepath.Join(c.dir, prefix, key+".jpg")
}

// Has reports whether a cache entry exists.
func (c *ThumbCache) Has(key string) bool {
	if key == "" {
		return false
	}
	st, err := os.Stat(c.Path(key))
	return err == nil && st.Size() > 0
}

// Put writes encoded thumbnail bytes atomically.
func (c *ThumbCache) Put(key string, data []byte) error {
	p := c.Path(key)
	if p == "" {
		return fmt.Errorf("empty thumb key")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// PutImage renders and stores a thumbnail for an already decoded image.
func (c *ThumbCache) PutImage(key string, img image.Image, orientation, max int) error {
	thumb := renderThumb(img, orientation, max)
	data, err := encodeJPEG(thumb, 82)
	if err != nil {
		return err
	}
	return c.Put(key, data)
}

func (c *ThumbCache) keyLock(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.locks[key]
	if !ok {
		l = &sync.Mutex{}
		c.locks[key] = l
	}
	return l
}

// Ensure makes sure a thumbnail exists, generating it from the source file.
func (c *ThumbCache) Ensure(path string, size, mod int64) (string, error) {
	key := c.Key(path, size, mod)
	if c.Has(key) {
		return key, nil
	}
	l := c.keyLock(key)
	l.Lock()
	defer l.Unlock()
	if c.Has(key) {
		return key, nil
	}
	img, _, orientation, err := decodeImage(path)
	if err != nil {
		return "", err
	}
	if err := c.PutImage(key, img, orientation, 640); err != nil {
		return "", err
	}
	return key, nil
}

// Stats reports cache size.
func (c *ThumbCache) Stats() (files int, bytes int64) {
	_ = filepath.Walk(c.dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		files++
		bytes += info.Size()
		return nil
	})
	return
}

// Clear removes every cached thumbnail.
func (c *ThumbCache) Clear() error {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(c.dir, e.Name()))
	}
	return nil
}

// Prune drops thumbnails older than maxAge, and then trims the cache to maxBytes
// (oldest first) when needed.
func (c *ThumbCache) Prune(maxAge time.Duration, maxBytes int64) {
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	var entries []entry
	var total int64
	_ = filepath.Walk(c.dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		entries = append(entries, entry{p, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	cutoff := time.Now().Add(-maxAge)
	kept := entries[:0]
	for _, e := range entries {
		if maxAge > 0 && e.mod.Before(cutoff) {
			if os.Remove(e.path) == nil {
				total -= e.size
				continue
			}
		}
		kept = append(kept, e)
	}
	if maxBytes > 0 && total > maxBytes {
		for i := 0; i < len(kept) && total > maxBytes; i++ {
			min := i
			for j := i + 1; j < len(kept); j++ {
				if kept[j].mod.Before(kept[min].mod) {
					min = j
				}
			}
			kept[i], kept[min] = kept[min], kept[i]
			if os.Remove(kept[i].path) == nil {
				total -= kept[i].size
			}
		}
	}
}
