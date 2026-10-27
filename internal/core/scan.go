package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// candidate is a discovered image file before analysis.
type candidate struct {
	path string
	size int64
	mod  int64
}

func lowerPath(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

func (o ScanOptions) excluded(name string) bool {
	for _, ex := range o.ExcludeDirs {
		if ex != "" && strings.EqualFold(ex, name) {
			return true
		}
	}
	return false
}

// collectFiles walks the scan roots and returns image candidates.
func collectFiles(ctx context.Context, roots []string, opts ScanOptions, excludePrefixes []string, report func(found int)) ([]candidate, []Issue, error) {
	var out []candidate
	var issues []Issue
	seen := make(map[string]struct{})
	excludedPrefixes := make([]string, 0, len(excludePrefixes))
	for _, p := range excludePrefixes {
		if p != "" {
			excludedPrefixes = append(excludedPrefixes, lowerPath(p)+string(os.PathSeparator))
		}
	}
	isExcludedPath := func(p string) bool {
		lp := lowerPath(p) + string(os.PathSeparator)
		for _, pref := range excludedPrefixes {
			if strings.HasPrefix(lp, pref) {
				return true
			}
		}
		return false
	}
	add := func(path string, info os.FileInfo) {
		key := lowerPath(path)
		if _, ok := seen[key]; ok {
			return
		}
		if isExcludedPath(path) {
			return
		}
		if info.Size() < int64(opts.MinFileSizeKB)*1024 {
			return
		}
		seen[key] = struct{}{}
		out = append(out, candidate{path: path, size: info.Size(), mod: info.ModTime().UnixMilli()})
	}
	addIssue := func(path string, err error) {
		if len(issues) >= 200 {
			return
		}
		issues = append(issues, Issue{Path: path, Message: err.Error()})
	}

	for _, root := range roots {
		root = filepath.Clean(root)
		st, err := os.Stat(root)
		if err != nil {
			addIssue(root, err)
			continue
		}
		if !st.IsDir() {
			if SupportedImageExtensions[strings.ToLower(filepath.Ext(root))] {
				add(root, st)
			}
			continue
		}
		if opts.IncludeSubfolders {
			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					addIssue(path, err)
					if d != nil && d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				name := d.Name()
				if d.IsDir() {
					if path == root {
						return nil
					}
					if (!opts.IncludeHidden && pathIsHidden(path, name)) || opts.excluded(name) || isExcludedPath(path) {
						return filepath.SkipDir
					}
					return nil
				}
				if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
					return nil
				}
				if !opts.IncludeHidden && pathIsHidden(path, name) {
					return nil
				}
				if !SupportedImageExtensions[strings.ToLower(filepath.Ext(name))] {
					return nil
				}
				info, err := d.Info()
				if err != nil {
					addIssue(path, err)
					return nil
				}
				add(path, info)
				if len(out)%200 == 0 {
					report(len(out))
				}
				return nil
			})
			if err != nil && ctx.Err() == nil {
				addIssue(root, err)
			}
		} else {
			entries, err := os.ReadDir(root)
			if err != nil {
				addIssue(root, err)
				continue
			}
			for _, d := range entries {
				if ctx.Err() != nil {
					break
				}
				if d.IsDir() {
					continue
				}
				if !opts.IncludeHidden && pathIsHidden(filepath.Join(root, d.Name()), d.Name()) {
					continue
				}
				if !SupportedImageExtensions[strings.ToLower(filepath.Ext(d.Name()))] {
					continue
				}
				info, err := d.Info()
				if err != nil {
					addIssue(d.Name(), err)
					continue
				}
				if info.Mode()&os.ModeSymlink != 0 {
					continue
				}
				add(filepath.Join(root, d.Name()), info)
			}
		}
	}
	report(len(out))
	sort.Slice(out, func(i, j int) bool { return lowerPath(out[i].path) < lowerPath(out[j].path) })
	return out, issues, nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// analysisMetrics aggregates per-file timings across the worker pool.
type analysisMetrics struct {
	hashNs        atomic.Int64
	decodeNs      atomic.Int64
	fingerprintNs atomic.Int64
	cacheHits     atomic.Int64
	cacheMisses   atomic.Int64
	workers       int
}

func analysisWorkers() int {
	if raw := strings.TrimSpace(os.Getenv("PICDUP_WORKERS")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 32 {
				n = 32
			}
			return n
		}
	}
	n := runtime.NumCPU()
	if n > 16 {
		n = 16
	}
	if n < 2 {
		n = 2
	}
	return n
}

func cachedFile(c candidate, entry FingerprintEntry) *File {
	f := &File{
		Path:       c.path,
		Name:       filepath.Base(c.path),
		Dir:        filepath.Dir(c.path),
		Size:       c.size,
		ModTime:    c.mod,
		Width:      entry.Width,
		Height:     entry.Height,
		Format:     entry.Format,
		SHA256:     entry.SHA256,
		Error:      entry.Error,
		PHash:      entry.PHash,
		PHashCrop:  entry.PHashCrop,
		PHashOuter: entry.PHashOuter,
		DHash:      entry.DHash,
		Hist:       entry.Hist,
		Detail:     entry.Detail,
	}
	if f.Format == "" {
		f.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(c.path)), ".")
	}
	return f
}

func entryFromFile(f *File) FingerprintEntry {
	return FingerprintEntry{
		SHA256:     f.SHA256,
		Width:      f.Width,
		Height:     f.Height,
		Format:     f.Format,
		PHash:      f.PHash,
		PHashCrop:  f.PHashCrop,
		PHashOuter: f.PHashOuter,
		DHash:      f.DHash,
		Hist:       f.Hist,
		Detail:     f.Detail,
		Error:      f.Error,
	}
}

type fingerprintShare struct {
	done chan struct{}
	info imageInfo
	err  error
}

type fingerprintStore struct {
	mu    sync.Mutex
	items map[string]*fingerprintShare
}

func newFingerprintStore() *fingerprintStore {
	return &fingerprintStore{items: make(map[string]*fingerprintShare)}
}

func (s *fingerprintStore) acquire(sha string) (*fingerprintShare, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if share, ok := s.items[sha]; ok {
		return share, false
	}
	share := &fingerprintShare{done: make(chan struct{})}
	s.items[sha] = share
	return share, true
}

// analyseFile hashes and fingerprints one candidate. Thumbnails are generated
// lazily by the HTTP handler, so scanning never encodes unused JPEGs.
func analyseFile(ctx context.Context, c candidate, cache *FingerprintCache, shares *fingerprintStore, m *analysisMetrics) (*File, error) {
	f := &File{
		Path:    c.path,
		Name:    filepath.Base(c.path),
		Dir:     filepath.Dir(c.path),
		Size:    c.size,
		ModTime: c.mod,
		Format:  strings.TrimPrefix(strings.ToLower(filepath.Ext(c.path)), "."),
	}

	var key string
	if cache != nil {
		key = cache.Key(c.path, c.size, c.mod)
		if entry, ok := cache.Get(key); ok {
			m.cacheHits.Add(1)
			return cachedFile(c, entry), nil
		}
		m.cacheMisses.Add(1)
	}

	hashStart := time.Now()
	sha, err := sha256File(c.path)
	m.hashNs.Add(time.Since(hashStart).Nanoseconds())
	if err != nil {
		return nil, fmt.Errorf("读取失败: %w", err)
	}
	f.SHA256 = sha
	// Very large files still participate in exact matching, but decoding them is
	// risky and slow, so they are excluded from perceptual analysis.
	if c.size > 512<<20 {
		f.Error = "文件过大，仅参与精确查重"
		if cache != nil {
			cache.Put(key, entryFromFile(f))
		}
		return f, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Exact duplicates share one decode+fingerprint pass. Hashing still happens
	// for every path so the duplicate group is complete, but identical bytes are
	// only ever decoded once.
	share, owner := shares.acquire(sha)
	if owner {
		var info imageInfo
		var fpErr error
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					fpErr = fmt.Errorf("无法解码: %v", rec)
				}
			}()
			decodeStart := time.Now()
			img, format, orientation, err := decodeImage(c.path)
			m.decodeNs.Add(time.Since(decodeStart).Nanoseconds())
			if err != nil {
				fpErr = fmt.Errorf("无法解码: %w", err)
				return
			}
			if ctx.Err() != nil {
				fpErr = ctx.Err()
				return
			}
			fpStart := time.Now()
			info = fingerprintImage(img, format, orientation)
			m.fingerprintNs.Add(time.Since(fpStart).Nanoseconds())
		}()
		share.info = info
		share.err = fpErr
		close(share.done)
	} else {
		select {
		case <-share.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if share.err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		f.Error = share.err.Error()
		return f, nil
	}

	info := share.info
	if info.Format != "" {
		f.Format = info.Format
	}
	f.Width, f.Height = info.Width, info.Height
	f.PHash = info.PHash
	f.PHashCrop = info.PHashCrop
	f.PHashOuter = info.PHashOuter
	f.DHash = info.DHash
	f.Hist = info.Hist
	f.Detail = info.Detail
	if cache != nil && ctx.Err() == nil {
		cache.Put(key, entryFromFile(f))
	}
	return f, nil
}

// safeAnalyseFile isolates decoder panics so one corrupt file cannot abort a scan.
func safeAnalyseFile(ctx context.Context, c candidate, cache *FingerprintCache, shares *fingerprintStore, m *analysisMetrics) (f *File, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("无法解码: %v", rec)
			f = nil
		}
	}()
	return analyseFile(ctx, c, cache, shares, m)
}

// analyzeFilesWithCache runs the worker pool and returns per-stage metrics.
func analyzeFilesWithCache(ctx context.Context, candidates []candidate, cache *FingerprintCache, onProgress func(processed, failed int, current string)) ([]*File, []Issue, *analysisMetrics) {
	metrics := &analysisMetrics{}
	workers := analysisWorkers()
	if workers > len(candidates) {
		workers = len(candidates)
	}
	if workers < 1 {
		workers = 1
	}
	metrics.workers = workers
	shares := newFingerprintStore()

	jobs := make(chan candidate)
	results := make(chan *File, workers*2)
	issues := make(chan Issue, 512)
	var wg sync.WaitGroup
	var processed, failed int
	var mu sync.Mutex

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if ctx.Err() != nil {
					continue
				}
				f, err := safeAnalyseFile(ctx, c, cache, shares, metrics)
				if err != nil {
					if ctx.Err() != nil {
						continue
					}
					select {
					case issues <- Issue{Path: c.path, Message: err.Error()}:
					default:
					}
					mu.Lock()
					failed++
					processed++
					p, fl := processed, failed
					mu.Unlock()
					onProgress(p, fl, c.path)
					continue
				}
				results <- f
				mu.Lock()
				processed++
				if f.Error != "" {
					failed++
				}
				p, fl := processed, failed
				mu.Unlock()
				onProgress(p, fl, c.path)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, c := range candidates {
			select {
			case jobs <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
		close(issues)
	}()
	var files []*File
	for f := range results {
		files = append(files, f)
	}
	var issueList []Issue
	for i := range issues {
		if len(issueList) < 200 {
			issueList = append(issueList, i)
		}
	}
	return files, issueList, metrics
}

// analyzeFiles keeps the original two-value API for existing callers/tests.
func analyzeFiles(ctx context.Context, candidates []candidate, _ *ThumbCache, onProgress func(processed, failed int, current string)) ([]*File, []Issue) {
	files, issues, _ := analyzeFilesWithCache(ctx, candidates, nil, onProgress)
	return files, issues
}

func fillGroupStats(stats *Stats, groups []*Group) {
	for _, g := range groups {
		switch g.Kind {
		case "exact":
			stats.ExactGroups++
			stats.ExactFiles += len(g.Files)
			stats.ReclaimableExact += g.WastedBytes
		case "similar":
			stats.SimilarGroups++
			stats.SimilarFiles += len(g.Files)
			stats.ReclaimableSimilar += g.WastedBytes
		}
	}
}
