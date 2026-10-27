package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Engine owns scan state, settings, thumbnails and the quarantine.
type Engine struct {
	mu        sync.RWMutex
	scanning  bool
	cleaning  bool
	cancel    context.CancelFunc
	progress  Progress
	result    *Result
	scanStart time.Time

	paths        Paths
	settings     *SettingsStore
	thumbs       *ThumbCache
	fingerprints *FingerprintCache
	ignore       *IgnoreStore
	trash        *TrashManager

	droppedMu sync.Mutex
	dropped   []string

	persistMu sync.Mutex
}

// ScanStatus is the polling payload for the UI.
type ScanStatus struct {
	Progress  Progress `json:"progress"`
	Scanning  bool     `json:"scanning"`
	Cleaning  bool     `json:"cleaning"`
	HasResult bool     `json:"hasResult"`
}

// NewEngine initialises the application backend.
func NewEngine(dataDirOverride string) (*Engine, error) {
	paths, err := ResolvePaths(dataDirOverride)
	if err != nil {
		return nil, err
	}
	settings, err := NewSettingsStore(paths.ConfigDir)
	if err != nil {
		return nil, err
	}
	thumbs, err := NewThumbCache(paths.ThumbsDir)
	if err != nil {
		return nil, err
	}
	fingerprints := NewFingerprintCache(paths.FingerprintsPath)
	ignore := NewIgnoreStore(paths.IgnoredPath)
	trash, err := NewTrashManager(paths.TrashDir)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		paths:        paths,
		settings:     settings,
		thumbs:       thumbs,
		fingerprints: fingerprints,
		ignore:       ignore,
		trash:        trash,
		progress:     Progress{Phase: "idle", Message: "准备就绪"},
	}
	e.result = e.loadResult()
	if e.result != nil {
		e.progress = Progress{Phase: "idle", Percent: 100, Message: "已载入上次扫描结果", Done: true}
	}
	go e.thumbs.Prune(45*24*time.Hour, 2<<30)
	return e, nil
}

// Paths returns the resolved app directories.
func (e *Engine) Paths() Paths { return e.paths }

// RecycleSupported reports whether OS Recycle Bin deletion is available.
func (e *Engine) RecycleSupported() bool { return recycleSupported() }

// Settings returns the current preferences.
func (e *Engine) Settings() Settings { return e.settings.Get() }

// UpdateSettings applies user preferences. Recent roots are owned by the
// engine and preserved here.
func (e *Engine) UpdateSettings(s Settings) (Settings, error) {
	cur := e.settings.Get()
	s.RecentRoots = cur.RecentRoots
	return e.settings.Update(func(dst *Settings) {
		*dst = s
	})
}

// StartScan validates and launches a scan in the background.
func (e *Engine) StartScan(req ScanRequest) (Progress, error) {
	roots, err := normalizeRoots(req.Roots)
	if err != nil {
		return Progress{}, err
	}
	if len(roots) == 0 {
		return Progress{}, fmt.Errorf("请至少选择一个文件夹")
	}
	opts := req.Options
	if opts.Similarity < 0 {
		opts.Similarity = 0
	}
	if opts.Similarity > 24 {
		opts.Similarity = 24
	}
	if opts.MinFileSizeKB < 0 {
		opts.MinFileSizeKB = 0
	}
	if opts.MinFileSizeKB > 10_000_000 {
		opts.MinFileSizeKB = 10_000_000
	}
	if len(opts.ExcludeDirs) == 0 {
		opts.ExcludeDirs = DefaultSettings().ExcludeDirs
	}

	e.mu.Lock()
	if e.scanning {
		e.mu.Unlock()
		return Progress{}, fmt.Errorf("已有扫描正在进行")
	}
	if e.cleaning {
		e.mu.Unlock()
		return Progress{}, fmt.Errorf("正在清理文件，请稍候")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.scanning = true
	e.scanStart = time.Now()
	e.progress = Progress{Phase: "discovering", Percent: 0, Message: "正在发现图片…"}
	e.mu.Unlock()

	go e.runScan(ctx, roots, opts)
	return e.Status().Progress, nil
}

func (e *Engine) runScan(ctx context.Context, roots []string, opts ScanOptions) {
	started := time.Now()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("scan panic: %v", r)
			e.finishScan("error", "扫描发生内部错误")
		}
	}()
	exclude := []string{e.paths.LocalDir, e.paths.ThumbsDir, e.paths.TrashDir}
	defer e.saveFingerprintCache()
	walkStart := time.Now()

	e.mu.Lock()
	e.progress.Percent = 3
	e.mu.Unlock()

	cands, collectIssues, err := collectFiles(ctx, roots, opts, exclude, func(found int) {
		e.updateProgress(func(p *Progress) {
			p.FilesFound = found
			p.Message = fmt.Sprintf("正在发现图片… 已找到 %d 张", found)
		})
	})
	if ctx.Err() != nil {
		e.finishScan("cancelled", "扫描已取消")
		return
	}
	if err != nil {
		e.finishScan("error", "扫描失败: "+err.Error())
		return
	}
	walkMs := time.Since(walkStart).Milliseconds()

	e.mu.Lock()
	e.progress.Phase = "analyzing"
	e.progress.TotalFiles = len(cands)
	e.progress.FilesFound = len(cands)
	e.progress.Percent = 5
	e.progress.Message = fmt.Sprintf("正在分析 %d 张图片…", len(cands))
	e.mu.Unlock()

	analyzeStart := time.Now()
	files, analyzeIssues, metrics := analyzeFilesWithCache(ctx, cands, e.fingerprints, func(processed, failed int, current string) {
		e.updateProgress(func(p *Progress) {
			p.FilesProcessed = processed
			p.FilesFailed = failed
			p.Current = current
			if len(cands) > 0 {
				p.Percent = 5 + 85*float64(processed)/float64(len(cands))
			}
		})
	})
	if ctx.Err() != nil {
		e.finishScan("cancelled", "扫描已取消")
		return
	}
	analyzeMs := time.Since(analyzeStart).Milliseconds()

	e.updateProgress(func(p *Progress) {
		p.Phase = "comparing"
		p.Percent = 90
		p.Message = "正在比对重复与相似图片…"
		p.Current = ""
	})
	groupStart := time.Now()
	groups, gstats, note := BuildGroups(ctx, files, opts, func(frac float64, msg string) {
		e.updateProgress(func(p *Progress) {
			p.Percent = 90 + 9*frac
			if msg != "" {
				p.Message = msg
			}
		})
	})
	if ctx.Err() != nil {
		e.finishScan("cancelled", "扫描已取消")
		return
	}
	groupMs := time.Since(groupStart).Milliseconds()

	ignoredGroups := 0
	if e.ignore != nil && e.ignore.Len() > 0 {
		before := len(groups)
		groups = e.ignore.FilterGroups(groups)
		ignoredGroups = before - len(groups)
		if ignoredGroups > 0 {
			clearGroupStats(&gstats)
			fillGroupStats(&gstats, groups)
			gstats.IgnoredGroups = ignoredGroups
			note = appendNote(note, fmt.Sprintf("已按忽略规则隐藏 %d 组。", ignoredGroups))
		}
	}

	stats := gstats
	stats.FilesScanned = len(files)
	stats.FilesFailed = 0
	stats.FilesAnalyzed = 0
	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
		if f.Error == "" {
			stats.FilesAnalyzed++
		} else {
			stats.FilesFailed++
		}
	}
	stats.TotalBytes = totalBytes
	stats.DurationMs = time.Since(started).Milliseconds()
	stats.Timings = ScanTimings{
		WalkMs:        walkMs,
		AnalyzeMs:     analyzeMs,
		GroupMs:       groupMs,
		HashMs:        metrics.hashNs.Load() / int64(time.Millisecond),
		DecodeMs:      metrics.decodeNs.Load() / int64(time.Millisecond),
		FingerprintMs: metrics.fingerprintNs.Load() / int64(time.Millisecond),
		ThumbnailMs:   0,
		CacheHits:     int(metrics.cacheHits.Load()),
		CacheMisses:   int(metrics.cacheMisses.Load()),
		Workers:       metrics.workers,
	}

	issues := make([]Issue, 0, len(collectIssues)+len(analyzeIssues))
	issues = append(issues, collectIssues...)
	issues = append(issues, analyzeIssues...)
	if len(issues) > 200 {
		issues = issues[:200]
	}
	result := &Result{
		ID:         randomID(),
		Roots:      append([]string(nil), roots...),
		Options:    opts,
		StartedAt:  started,
		FinishedAt: time.Now(),
		Groups:     groups,
		Files:      files,
		Stats:      stats,
		Issues:     issues,
		Note:       note,
	}
	fillResultIndex(result)

	e.mu.Lock()
	e.result = result
	e.scanning = false
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.progress = Progress{
		Phase:          "done",
		Percent:        100,
		FilesFound:     len(files),
		FilesProcessed: len(files),
		FilesFailed:    stats.FilesFailed,
		TotalFiles:     len(files),
		ElapsedMs:      stats.DurationMs,
		GroupsExact:    stats.ExactGroups,
		GroupsSimilar:  stats.SimilarGroups,
		Message:        fmt.Sprintf("扫描完成，发现 %d 组重复、%d 组相似图片", stats.ExactGroups, stats.SimilarGroups),
		Done:           true,
	}
	e.mu.Unlock()

	e.LogSummary(result)
	e.saveResultAsync()
	_, _ = e.settings.Update(func(s *Settings) {
		merged := []string{roots[0]}
		for _, r := range s.RecentRoots {
			if !pathEqual(r, roots[0]) {
				merged = append(merged, r)
			}
		}
		s.RecentRoots = merged
	})
}

func (e *Engine) finishScan(phase, message string) {
	e.mu.Lock()
	e.scanning = false
	e.cleaning = false
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	e.progress.Phase = phase
	e.progress.Done = true
	e.progress.Message = message
	if phase == "error" {
		e.progress.Percent = 0
	}
	e.ProgressElapsedLocked()
	e.mu.Unlock()
}

func (e *Engine) updateProgress(fn func(*Progress)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(&e.progress)
	e.ProgressElapsedLocked()
}

func (e *Engine) ProgressElapsedLocked() {
	if e.scanStart.IsZero() {
		return
	}
	e.progress.ElapsedMs = time.Since(e.scanStart).Milliseconds()
}

// Status returns current progress and liveness flags.
func (e *Engine) Status() ScanStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := e.progress
	if e.scanning {
		p.ElapsedMs = time.Since(e.scanStart).Milliseconds()
	}
	return ScanStatus{Progress: p, Scanning: e.scanning, Cleaning: e.cleaning, HasResult: e.result != nil}
}

// CancelScan requests cancellation of the running scan.
func (e *Engine) CancelScan() {
	e.mu.RLock()
	cancel := e.cancel
	e.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// Result returns the current scan result (immutable; replaced on cleanup).
func (e *Engine) Result() *Result {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.result
}

// ClearResult discards the current result (the persisted copy is removed).
func (e *Engine) ClearResult() {
	e.mu.Lock()
	e.result = nil
	e.progress = Progress{Phase: "idle", Message: "准备就绪"}
	e.mu.Unlock()
	e.saveResultAsync()
}

// IgnoreGroup records the current group as "not duplicates" and removes it
// from the current result. The members stay available for other groups.
func (e *Engine) IgnoreGroup(groupID string) (IgnoreRule, error) {
	e.mu.RLock()
	if e.scanning || e.cleaning {
		e.mu.RUnlock()
		return IgnoreRule{}, fmt.Errorf("扫描或清理进行中，无法记录忽略规则")
	}
	result := e.result
	e.mu.RUnlock()
	if result == nil {
		return IgnoreRule{}, fmt.Errorf("没有可操作的扫描结果")
	}

	var group *Group
	for _, g := range result.Groups {
		if g.ID == groupID {
			group = g
			break
		}
	}
	if group == nil {
		return IgnoreRule{}, fmt.Errorf("分组不存在或已更新")
	}
	rule, _, err := e.ignore.AddGroup(group)
	if err != nil {
		return IgnoreRule{}, fmt.Errorf("保存忽略规则失败: %w", err)
	}

	e.mu.Lock()
	current := e.result
	if current == result {
		kept := current.Groups[:0]
		for _, g := range current.Groups {
			if g.ID != groupID {
				kept = append(kept, g)
			}
		}
		current.Groups = kept
		clearGroupStats(&current.Stats)
		fillGroupStats(&current.Stats, current.Groups)
		current.Stats.IgnoredGroups = e.ignore.Len()
		fillResultIndex(current)
	}
	e.mu.Unlock()
	e.saveResultAsync()
	return rule, nil
}

// RestoreIgnore removes one ignore rule and rebuilds the current result from
// the files that were already analyzed.
func (e *Engine) RestoreIgnore(ruleID string) (bool, error) {
	e.mu.RLock()
	if e.scanning || e.cleaning {
		e.mu.RUnlock()
		return false, fmt.Errorf("扫描或清理进行中，无法恢复忽略规则")
	}
	result := e.result
	e.mu.RUnlock()
	if result != nil && result.Partial {
		return false, fmt.Errorf("上次结果来自旧版本，请重新扫描后再恢复忽略记录")
	}

	removed, err := e.ignore.Remove(ruleID)
	if err != nil {
		return false, fmt.Errorf("保存忽略规则失败: %w", err)
	}
	if !removed || result == nil {
		return removed, nil
	}

	e.mu.RLock()
	options := result.Options
	files := append([]*File(nil), result.Files...)
	e.mu.RUnlock()
	groups, gstats, note := BuildGroups(context.Background(), files, options, nil)
	before := len(groups)
	groups = e.ignore.FilterGroups(groups)
	ignoredGroups := before - len(groups)

	e.mu.Lock()
	current := e.result
	if current == result {
		current.Groups = groups
		current.Note = note
		stats := current.Stats
		stats.UniqueImages = gstats.UniqueImages
		clearGroupStats(&stats)
		fillGroupStats(&stats, groups)
		stats.IgnoredGroups = ignoredGroups
		current.Stats = stats
		fillResultIndex(current)
	}
	e.mu.Unlock()
	e.saveResultAsync()
	return true, nil
}

// IgnoreRules returns the persisted "not duplicates" rules.
func (e *Engine) IgnoreRules() []IgnoreRule {
	if e.ignore == nil {
		return nil
	}
	return e.ignore.Rules()
}

// LookupFile resolves a scan file by path.
func (e *Engine) LookupFile(path string) (*File, bool) {
	r := e.Result()
	if r == nil {
		return nil, false
	}
	f, ok := r.fileMap[lowerPath(path)]
	return f, ok
}

// ThumbKey ensures a thumbnail exists and returns its cache key.
func (e *Engine) ThumbKey(path string) (string, error) {
	f, ok := e.LookupFile(path)
	if !ok {
		return "", fmt.Errorf("文件不在当前扫描结果中")
	}
	if f.ThumbKey != "" && e.thumbs.Has(f.ThumbKey) {
		return f.ThumbKey, nil
	}
	return e.thumbs.Ensure(f.Path, f.Size, f.ModTime)
}

// ThumbFilePath returns the cache path for a key.
func (e *Engine) ThumbFilePath(key string) string { return e.thumbs.Path(key) }

// Preview renders a larger JPEG preview for the detail view.
func (e *Engine) Preview(path string, max int) ([]byte, error) {
	f, ok := e.LookupFile(path)
	if !ok {
		return nil, fmt.Errorf("文件不在当前扫描结果中")
	}
	if max <= 0 || max > 2400 {
		max = 1800
	}
	img, _, orientation, err := decodeImage(f.Path)
	if err != nil {
		return nil, err
	}
	thumb := renderThumb(img, orientation, max)
	return encodeJPEG(thumb, 88)
}

// Cleanup moves selected files to the quarantine or Recycle Bin and refreshes
// the current result.
func (e *Engine) Cleanup(paths []string, mode, kind string) (CleanupResult, error) {
	if kind != "exact" && kind != "similar" {
		return CleanupResult{}, fmt.Errorf("无效的分组类型")
	}
	if mode == "" {
		mode = e.settings.Get().CleanupMode
	}
	if mode != "trash" && mode != "recycle" {
		return CleanupResult{}, fmt.Errorf("无效的清理方式")
	}
	if mode == "recycle" && !recycleSupported() {
		return CleanupResult{}, fmt.Errorf("当前平台不支持回收站")
	}
	e.mu.Lock()
	if e.scanning {
		e.mu.Unlock()
		return CleanupResult{}, fmt.Errorf("扫描进行中，无法清理")
	}
	if e.cleaning {
		e.mu.Unlock()
		return CleanupResult{}, fmt.Errorf("正在清理，请稍候")
	}
	result := e.result
	if result == nil {
		e.mu.Unlock()
		return CleanupResult{}, fmt.Errorf("没有可清理的扫描结果")
	}
	e.cleaning = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.cleaning = false
		e.mu.Unlock()
	}()

	selected := make(map[string]*File)
	for _, p := range paths {
		key := lowerPath(p)
		if _, ok := selected[key]; ok {
			continue
		}
		selected[key] = nil
	}
	if len(selected) == 0 {
		return CleanupResult{}, fmt.Errorf("请先选择要清理的图片")
	}
	// Only files that belong to a group of the requested kind may be removed.
	allowed := make(map[string]*File)
	for _, g := range result.Groups {
		if g.Kind != kind {
			continue
		}
		for _, f := range g.Files {
			allowed[lowerPath(f.Path)] = f
		}
	}
	for key := range selected {
		f, ok := allowed[key]
		if !ok {
			return CleanupResult{}, fmt.Errorf("所选文件不在当前结果中，请刷新后重试")
		}
		selected[key] = f
	}
	// Every affected group must retain at least one file.
	for _, g := range result.Groups {
		if g.Kind != kind {
			continue
		}
		count := 0
		for _, f := range g.Files {
			if selected[lowerPath(f.Path)] != nil {
				count++
			}
		}
		if count == len(g.Files) {
			name := "该分组"
			if g.Keeper != nil {
				name = "「" + g.Keeper.Name + "」"
			}
			return CleanupResult{}, fmt.Errorf("%s所在分组至少需要保留一张图片", name)
		}
	}
	list := make([]string, 0, len(selected))
	skipped := make([]TrashFailure, 0)
	for _, f := range selected {
		st, statErr := os.Stat(f.Path)
		if statErr != nil {
			skipped = append(skipped, TrashFailure{Path: f.Path, Error: "文件已不存在，已跳过"})
			continue
		}
		// Never delete a file that changed since it was scanned.
		if st.Size() != f.Size || (f.ModTime != 0 && st.ModTime().UnixMilli() != f.ModTime) {
			skipped = append(skipped, TrashFailure{Path: f.Path, Error: "文件在扫描后被修改，已跳过"})
			continue
		}
		list = append(list, f.Path)
	}
	if len(list) == 0 {
		return CleanupResult{}, fmt.Errorf("所选文件都已发生变化，请重新扫描后再试")
	}
	sort.Slice(list, func(i, j int) bool { return lowerPath(list[i]) < lowerPath(list[j]) })

	var res CleanupResult
	var err error
	if mode == "recycle" {
		res, err = e.trash.MoveToRecycleBin(list)
	} else {
		res, err = e.trash.Move(list)
	}
	if err != nil {
		return CleanupResult{}, err
	}
	res.Failed = append(res.Failed, skipped...)
	// Determine what actually disappeared from disk.
	deleted := make(map[string]bool)
	for _, p := range list {
		if _, statErr := os.Stat(p); os.IsNotExist(statErr) {
			deleted[lowerPath(p)] = true
		}
	}
	if len(deleted) > 0 {
		e.mu.Lock()
		if e.result == result {
			e.result = filterResult(result, deleted)
		}
		e.mu.Unlock()
		e.saveResultAsync()
	}
	return res, nil
}

func filterResult(old *Result, deleted map[string]bool) *Result {
	r := &Result{
		ID:         old.ID,
		Roots:      old.Roots,
		Options:    old.Options,
		StartedAt:  old.StartedAt,
		FinishedAt: old.FinishedAt,
		Stats:      old.Stats,
		Issues:     old.Issues,
		Note:       old.Note,
	}
	seenGroups := make(map[string]bool)
	for _, g := range old.Groups {
		var kept []*File
		for _, f := range g.Files {
			if !deleted[lowerPath(f.Path)] {
				kept = append(kept, f)
			}
		}
		if len(kept) < 2 {
			continue
		}
		if g.Kind == "similar" && distinctSHA(kept) < 2 {
			continue
		}
		keeper := g.Keeper
		if keeper != nil && deleted[lowerPath(keeper.Path)] {
			keeper = nil
		}
		if keeper == nil {
			keeper = chooseKeeper(kept, g.Kind, r.Options.KeeperMode)
		}
		ng := newGroup(g.Kind, kept, keeper)
		if seenGroups[ng.ID] {
			continue
		}
		seenGroups[ng.ID] = true
		r.Groups = append(r.Groups, ng)
	}
	sort.SliceStable(r.Groups, func(i, j int) bool {
		if r.Groups[i].Kind != r.Groups[j].Kind {
			return r.Groups[i].Kind == "exact"
		}
		if r.Groups[i].WastedBytes != r.Groups[j].WastedBytes {
			return r.Groups[i].WastedBytes > r.Groups[j].WastedBytes
		}
		return len(r.Groups[i].Files) > len(r.Groups[j].Files)
	})
	r.Files = make([]*File, 0, len(old.Files))
	for _, f := range old.Files {
		if !deleted[lowerPath(f.Path)] {
			r.Files = append(r.Files, f)
		}
	}
	// Scan-level counters describe the scan itself; adjust them for the files
	// that were just removed, but do not rebuild them from the (possibly
	// snapshot-trimmed) file list.
	var deletedBytes int64
	for p := range deleted {
		if f := old.fileMap[lowerPath(p)]; f != nil {
			deletedBytes += f.Size
		}
	}
	removed := len(deleted)
	if r.Stats.FilesScanned >= removed {
		r.Stats.FilesScanned -= removed
	} else {
		r.Stats.FilesScanned = len(r.Files)
	}
	if r.Stats.FilesAnalyzed >= removed {
		r.Stats.FilesAnalyzed -= removed
	} else {
		r.Stats.FilesAnalyzed = len(r.Files)
	}
	if r.Stats.TotalBytes >= deletedBytes {
		r.Stats.TotalBytes -= deletedBytes
	}
	r.Stats.ExactGroups, r.Stats.ExactFiles, r.Stats.ReclaimableExact = 0, 0, 0
	r.Stats.SimilarGroups, r.Stats.SimilarFiles, r.Stats.ReclaimableSimilar = 0, 0, 0
	fillGroupStats(&r.Stats, r.Groups)
	fillResultIndex(r)
	return r
}

func clearGroupStats(stats *Stats) {
	stats.ExactGroups, stats.ExactFiles, stats.ReclaimableExact = 0, 0, 0
	stats.SimilarGroups, stats.SimilarFiles, stats.ReclaimableSimilar = 0, 0, 0
}

func appendNote(note, extra string) string {
	if note == "" {
		return extra
	}
	return note + " " + extra
}

func distinctSHA(files []*File) int {
	seen := make(map[string]struct{})
	for _, f := range files {
		seen[f.SHA256] = struct{}{}
	}
	return len(seen)
}

func fillResultIndex(r *Result) {
	r.fileMap = make(map[string]*File, len(r.Files))
	for _, f := range r.Files {
		r.fileMap[lowerPath(f.Path)] = f
	}
}

// TrashList returns all quarantine batches.
func (e *Engine) TrashList() []TrashBatch { return e.trash.List() }

// TrashStats returns quarantine totals.
func (e *Engine) TrashStats() (int, int, int64) { return e.trash.Stats() }

// RestoreTrash restores one batch.
func (e *Engine) RestoreTrash(batchID string) (RestoreResult, error) {
	return e.trash.Restore(batchID)
}

// DeleteTrashBatch permanently removes one batch.
func (e *Engine) DeleteTrashBatch(batchID string) (int, error) {
	return e.trash.DeleteBatch(batchID)
}

// EmptyTrash permanently removes everything in quarantine.
func (e *Engine) EmptyTrash() (int, int64, error) { return e.trash.Empty() }

// ThumbStats returns thumbnail cache usage.
func (e *Engine) ThumbStats() (int, int64) { return e.thumbs.Stats() }

// ClearThumbs clears the thumbnail cache.
func (e *Engine) ClearThumbs() error { return e.thumbs.Clear() }

// FingerprintStats returns fingerprint cache usage.
func (e *Engine) FingerprintStats() (int, int64) { return e.fingerprints.Stats() }

// ClearFingerprints drops the fingerprint cache.
func (e *Engine) ClearFingerprints() error { return e.fingerprints.Clear() }

// ResetSettings restores defaults while keeping the user's recent folders.
func (e *Engine) ResetSettings() (Settings, error) {
	def := DefaultSettings()
	def.RecentRoots = e.settings.Get().RecentRoots
	return e.settings.Update(func(dst *Settings) { *dst = def })
}

// RecycleUnsupportedMessage mirrors the recycle capability for the UI.
func (e *Engine) RecycleUnsupportedMessage() string {
	if recycleSupported() {
		return ""
	}
	return "当前平台不支持系统回收站，请使用隔离区"
}

// AddDropped records folders dropped onto the window.
func (e *Engine) AddDropped(paths []string) {
	e.droppedMu.Lock()
	defer e.droppedMu.Unlock()
	seen := make(map[string]bool, len(e.dropped))
	for _, p := range e.dropped {
		seen[lowerPath(p)] = true
	}
	for _, p := range paths {
		p = cleanInputPath(p)
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			if st.Mode().IsRegular() {
				p = filepath.Dir(p)
			} else {
				continue
			}
		}
		key := lowerPath(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		e.dropped = append(e.dropped, p)
	}
	if len(e.dropped) > 16 {
		e.dropped = e.dropped[len(e.dropped)-16:]
	}
}

// ConsumeDropped returns and clears folders dropped onto the window.
func (e *Engine) ConsumeDropped() []string {
	e.droppedMu.Lock()
	defer e.droppedMu.Unlock()
	out := e.dropped
	e.dropped = nil
	return out
}

// Flush persists the current result and fingerprint cache synchronously
// (used during shutdown).
func (e *Engine) Flush() {
	e.saveResult()
	e.saveFingerprintCache()
}

func (e *Engine) saveFingerprintCache() {
	if err := e.fingerprints.Save(); err != nil {
		log.Printf("save fingerprint cache: %v", err)
	}
}

// LogSummary writes a short scan summary to the log.
func (e *Engine) LogSummary(r *Result) {
	if r == nil {
		return
	}
	t := r.Stats.Timings
	log.Printf("scan %s: %d images, %d exact groups, %d similar groups, %s reclaimable, %dms | walk=%dms analyze=%dms group=%dms hash=%dms decode=%dms fp=%dms cache=%d/%d workers=%d",
		r.ID, r.Stats.FilesScanned, r.Stats.ExactGroups, r.Stats.SimilarGroups,
		humanBytes(r.Stats.ReclaimableExact+r.Stats.ReclaimableSimilar), r.Stats.DurationMs,
		t.WalkMs, t.AnalyzeMs, t.GroupMs, t.HashMs, t.DecodeMs, t.FingerprintMs,
		t.CacheHits, t.CacheMisses, t.Workers)
}

func normalizeRoots(roots []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, raw := range roots {
		p := cleanInputPath(raw)
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("无法访问文件夹: %s", p)
		}
		if !st.IsDir() {
			if st.Mode().IsRegular() && SupportedImageExtensions[strings.ToLower(filepath.Ext(p))] {
				p = filepath.Dir(p)
			} else {
				return nil, fmt.Errorf("不是有效的文件夹: %s", p)
			}
		}
		key := lowerPath(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
		if len(out) >= 16 {
			break
		}
	}
	return out, nil
}

func cleanInputPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, "\"")
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

func pathEqual(a, b string) bool { return lowerPath(a) == lowerPath(b) }

func randomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
