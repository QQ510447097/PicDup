package core

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Paths contains all on-disk locations used by the app.
type Paths struct {
	ConfigDir        string
	LocalDir         string
	ThumbsDir        string
	TrashDir         string
	SettingsPath     string
	LastResultPath   string
	FingerprintsPath string
	IgnoredPath      string
	LogPath          string
	WebviewDataDir   string
	Portable         bool
	Warnings         []string
}

// ResolvePaths determines app directories. A --data override keeps all state
// in one explicit directory; otherwise the app is portable and keeps its data
// next to the executable when that directory is writable.
func ResolvePaths(override string) (Paths, error) {
	var configBase, localBase string
	var portable bool
	var warnings []string

	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return Paths{}, err
		}
		configBase = abs
		localBase = abs
	} else {
		legacyConfig, legacyLocal := systemDataDirs()
		if base, ok := portableDataDir(); ok {
			configBase = base
			localBase = base
			portable = true
			warnings = append(warnings, migrateLegacyData(base, legacyConfig, legacyLocal)...)
		} else {
			configBase = legacyConfig
			localBase = legacyLocal
			warnings = append(warnings, "软件目录不可写，数据暂存在系统应用数据目录。")
		}
	}

	p := Paths{
		ConfigDir:        configBase,
		LocalDir:         localBase,
		ThumbsDir:        filepath.Join(localBase, "thumbs"),
		TrashDir:         filepath.Join(localBase, "trash"),
		SettingsPath:     filepath.Join(configBase, "settings.json"),
		LastResultPath:   filepath.Join(localBase, "last-result.json"),
		FingerprintsPath: filepath.Join(localBase, "fingerprints.json"),
		IgnoredPath:      filepath.Join(configBase, "ignored.json"),
		LogPath:          filepath.Join(localBase, "picdup.log"),
		WebviewDataDir:   filepath.Join(localBase, "webview"),
		Portable:         portable,
		Warnings:         warnings,
	}
	for _, d := range []string{p.ConfigDir, p.LocalDir, p.ThumbsDir, p.TrashDir, p.WebviewDataDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return p, err
		}
	}
	for _, warning := range warnings {
		log.Printf("data: %s", warning)
	}
	return p, nil
}

// EnsureFrontendCacheDir is reserved for future use.
func (p Paths) String() string { return p.LocalDir }

func systemDataDirs() (string, string) {
	cfg, err := os.UserConfigDir()
	if err != nil || cfg == "" {
		cfg = "."
	}
	configBase := filepath.Join(cfg, "PicDup")
	cache, err := os.UserCacheDir()
	if err != nil || cache == "" {
		cache = cfg
	}
	localBase := filepath.Join(cache, "PicDup")
	return configBase, localBase
}

func portableDataDir() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	base := filepath.Join(filepath.Dir(exe), "PicDupData")
	if !writableDir(base) {
		return "", false
	}
	return base, true
}

func writableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe, err := os.CreateTemp(dir, ".write-test-")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

func migrateLegacyData(portableBase, legacyConfig, legacyLocal string) []string {
	var warnings []string
	if legacyConfig == "" || legacyLocal == "" {
		return warnings
	}
	// Settings and the last result are small and make the first portable launch
	// feel unchanged. Caches are intentionally regenerated.
	if err := copyIfMissing(filepath.Join(legacyConfig, "settings.json"), filepath.Join(portableBase, "settings.json")); err != nil {
		warnings = append(warnings, "旧设置迁移失败："+err.Error())
	}
	if err := copyIfMissing(filepath.Join(legacyLocal, "last-result.json"), filepath.Join(portableBase, "last-result.json")); err != nil {
		warnings = append(warnings, "上次结果迁移失败："+err.Error())
	}
	if err := moveDirIfMissing(filepath.Join(legacyLocal, "trash"), filepath.Join(portableBase, "trash")); err != nil {
		warnings = append(warnings, "旧隔离区迁移失败："+err.Error())
	}
	return warnings
}

func copyIfMissing(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	data, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func moveDirIfMissing(src, dst string) error {
	if _, err := os.Stat(filepath.Join(src, "index.json")); err != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dst, "index.json")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return rewriteTrashIndex(src, dst)
	}
	// Cross-volume fallback: copy, verify the index exists, then remove old.
	// Rename cannot replace an existing empty directory.
	if entries, err := os.ReadDir(dst); err == nil && len(entries) == 0 {
		_ = os.Remove(dst)
	}
	tmp := dst + ".migrating"
	_ = os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if _, err := os.Stat(filepath.Join(tmp, "index.json")); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := rewriteTrashIndex(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func rewriteTrashIndex(oldDir, newDir string) error {
	path := filepath.Join(newDir, "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var idx struct {
		Batches []TrashBatch `json:"batches"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		return err
	}
	changed := false
	for bi := range idx.Batches {
		for ii := range idx.Batches[bi].Items {
			oldPath := idx.Batches[bi].Items[ii].StoredPath
			rel, err := filepath.Rel(oldDir, oldPath)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				continue
			}
			idx.Batches[bi].Items[ii].StoredPath = filepath.Join(newDir, rel)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	data, err = json.Marshal(idx)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := in.Stat()
		if err != nil {
			_ = in.Close()
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		return closeOutErr
	})
}
