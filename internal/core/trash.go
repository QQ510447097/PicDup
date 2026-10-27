package core

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TrashManager implements the reversible quarantine used by PicDup.
type TrashManager struct {
	dir       string
	indexPath string
	mu        sync.Mutex
	batches   []TrashBatch
}

// NewTrashManager opens (and reconciles) the quarantine directory.
func NewTrashManager(dir string) (*TrashManager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	t := &TrashManager{dir: dir, indexPath: filepath.Join(dir, "index.json")}
	_ = t.load()
	t.reconcile()
	return t, nil
}

func (t *TrashManager) load() error {
	data, err := os.ReadFile(t.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var idx struct {
		Batches []TrashBatch `json:"batches"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return err
	}
	t.batches = idx.Batches
	sort.SliceStable(t.batches, func(i, j int) bool { return t.batches[i].CreatedAt.After(t.batches[j].CreatedAt) })
	return nil
}

func (t *TrashManager) reconcile() {
	t.mu.Lock()
	defer t.mu.Unlock()
	changed := false
	kept := t.batches[:0]
	for _, b := range t.batches {
		items := b.Items[:0]
		var total int64
		for _, it := range b.Items {
			if st, err := os.Stat(it.StoredPath); err == nil && !st.IsDir() {
				items = append(items, it)
				total += it.Size
			} else {
				changed = true
			}
		}
		b.Items = items
		b.TotalSize = total
		if len(b.Items) > 0 {
			kept = append(kept, b)
		} else {
			changed = true
		}
	}
	t.batches = kept
	if changed {
		_ = t.saveLocked()
	}
}

func (t *TrashManager) saveLocked() error {
	idx := struct {
		Batches []TrashBatch `json:"batches"`
	}{Batches: t.batches}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp := t.indexPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, t.indexPath)
}

func newBatchID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

func safeBaseName(name string) string {
	name = filepath.Base(name)
	r := []rune(name)
	if len(r) > 100 {
		ext := filepath.Ext(name)
		base := []rune(strings.TrimSuffix(name, ext))
		if len(base) > 90 {
			base = base[:90]
		}
		return string(base) + ext
	}
	return name
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.CopyBuffer(out, in, make([]byte, 1<<20))
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	return nil
}

// moveFile renames, falling back to copy+delete across volumes.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// Move paths into the quarantine. All paths must be validated by the caller.
func (t *TrashManager) Move(paths []string) (CleanupResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	batch := TrashBatch{ID: newBatchID(), CreatedAt: time.Now(), Mode: "trash"}
	batchDir := filepath.Join(t.dir, "batches", batch.ID)
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		return CleanupResult{}, err
	}
	result := CleanupResult{Mode: "trash"}
	for i, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			result.Failed = append(result.Failed, TrashFailure{Path: p, Error: err.Error()})
			continue
		}
		if st.IsDir() {
			result.Failed = append(result.Failed, TrashFailure{Path: p, Error: "不是文件"})
			continue
		}
		dst := filepath.Join(batchDir, fmt.Sprintf("%05d_%s", i, safeBaseName(filepath.Base(p))))
		if err := moveFile(p, dst); err != nil {
			result.Failed = append(result.Failed, TrashFailure{Path: p, Error: err.Error()})
			continue
		}
		item := TrashItem{
			OriginalPath: p,
			StoredPath:   dst,
			Size:         st.Size(),
			ModTime:      st.ModTime().UnixMilli(),
			Name:         filepath.Base(p),
		}
		batch.Items = append(batch.Items, item)
		batch.TotalSize += item.Size
		result.Moved++
		result.Bytes += item.Size
	}
	if len(batch.Items) > 0 {
		t.batches = append([]TrashBatch{batch}, t.batches...)
		_ = t.saveLocked()
		result.BatchID = batch.ID
	} else {
		_ = os.RemoveAll(batchDir)
	}
	if len(result.Failed) == 0 {
		result.Failed = nil
	}
	return result, nil
}

// MoveToRecycleBin sends files to the Windows Recycle Bin (non-reversible from
// the app's perspective).
func (t *TrashManager) MoveToRecycleBin(paths []string) (CleanupResult, error) {
	moved, bytes, failures := recycleFiles(paths)
	result := CleanupResult{Mode: "recycle", Moved: moved, Bytes: bytes, Failed: failures}
	if len(result.Failed) == 0 {
		result.Failed = nil
	}
	return result, nil
}

// List returns a copy of all batches.
func (t *TrashManager) List() []TrashBatch {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TrashBatch, len(t.batches))
	copy(out, t.batches)
	return out
}

// Stats returns batch/file/byte totals.
func (t *TrashManager) Stats() (batches, files int, bytes int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range t.batches {
		batches++
		files += len(b.Items)
		bytes += b.TotalSize
	}
	return
}

// Restore puts every file in a batch back at its original location, renaming on
// collisions. Files that cannot be restored stay in the batch.
func (t *TrashManager) Restore(batchID string) (RestoreResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := -1
	for i := range t.batches {
		if t.batches[i].ID == batchID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return RestoreResult{}, fmt.Errorf("隔离批次不存在")
	}
	b := &t.batches[idx]
	var res RestoreResult
	remaining := make([]TrashItem, 0, len(b.Items))
	for _, it := range b.Items {
		if _, err := os.Stat(it.StoredPath); err != nil {
			res.Failed = append(res.Failed, TrashFailure{Path: it.OriginalPath, Error: "隔离文件已丢失"})
			continue
		}
		target := it.OriginalPath
		if _, err := os.Stat(target); err == nil {
			target = conflictName(target)
		}
		if err := moveFile(it.StoredPath, target); err != nil {
			res.Failed = append(res.Failed, TrashFailure{Path: it.OriginalPath, Error: err.Error()})
			remaining = append(remaining, it)
			continue
		}
		res.Restored++
	}
	b.Items = remaining
	var total int64
	for _, it := range remaining {
		total += it.Size
	}
	b.TotalSize = total
	if len(b.Items) == 0 {
		_ = os.RemoveAll(filepath.Join(t.dir, "batches", b.ID))
		t.batches = append(t.batches[:idx], t.batches[idx+1:]...)
	}
	_ = t.saveLocked()
	if len(res.Failed) == 0 {
		res.Failed = nil
	}
	return res, nil
}

func conflictName(path string) string {
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (恢复 %d)%s", stem, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return fmt.Sprintf("%s (恢复 %d)%s", stem, time.Now().UnixNano(), ext)
}

// DeleteBatch permanently removes a batch.
func (t *TrashManager) DeleteBatch(batchID string) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.batches {
		if t.batches[i].ID == batchID {
			n := len(t.batches[i].Items)
			if err := os.RemoveAll(filepath.Join(t.dir, "batches", batchID)); err != nil {
				return 0, err
			}
			t.batches = append(t.batches[:i], t.batches[i+1:]...)
			_ = t.saveLocked()
			return n, nil
		}
	}
	return 0, fmt.Errorf("隔离批次不存在")
}

// Empty permanently removes every quarantined file.
func (t *TrashManager) Empty() (int, int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	files := 0
	var bytes int64
	for _, b := range t.batches {
		files += len(b.Items)
		bytes += b.TotalSize
	}
	if err := os.RemoveAll(filepath.Join(t.dir, "batches")); err != nil {
		return 0, 0, err
	}
	t.batches = nil
	_ = t.saveLocked()
	return files, bytes, nil
}

// FindStored returns the trash item that owns a stored path.
func (t *TrashManager) FindStored(path string) (*TrashItem, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	lp := lowerPath(path)
	for _, b := range t.batches {
		for i := range b.Items {
			if lowerPath(b.Items[i].StoredPath) == lp {
				it := b.Items[i]
				return &it, true
			}
		}
	}
	return nil, false
}

// Dir returns the quarantine root.
func (t *TrashManager) Dir() string { return t.dir }
