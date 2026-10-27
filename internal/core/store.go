package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// SettingsStore persists user preferences as JSON.
type SettingsStore struct {
	path string
	mu   sync.RWMutex
	s    Settings
}

// NewSettingsStore loads settings from configDir/settings.json.
func NewSettingsStore(configDir string) (*SettingsStore, error) {
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}
	s := DefaultSettings()
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err == nil {
		_ = json.Unmarshal(data, &s)
	}
	normalizeSettings(&s)
	return &SettingsStore{path: filepath.Join(configDir, "settings.json"), s: s}, nil
}

// Get returns a copy of the current settings.
func (s *SettingsStore) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.s
	out.RecentRoots = append([]string(nil), s.s.RecentRoots...)
	out.ExcludeDirs = append([]string(nil), s.s.ExcludeDirs...)
	return out
}

// Update applies mut to the settings and persists them.
func (s *SettingsStore) Update(mut func(*Settings)) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mut(&s.s)
	normalizeSettings(&s.s)
	out := s.s
	out.RecentRoots = append([]string(nil), s.s.RecentRoots...)
	out.ExcludeDirs = append([]string(nil), s.s.ExcludeDirs...)
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return out, err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return out, err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return out, err
	}
	return out, nil
}

func normalizeSettings(s *Settings) {
	if s.Similarity < 0 {
		s.Similarity = 0
	}
	if s.Similarity > 24 {
		s.Similarity = 24
	}
	if s.MinFileSizeKB < 0 {
		s.MinFileSizeKB = 0
	}
	if s.CleanupMode != "recycle" {
		s.CleanupMode = "trash"
	}
	s.KeeperMode = normalizeKeeperMode(s.KeeperMode)
	switch s.Theme {
	case "light", "system", "dark":
	default:
		s.Theme = "dark"
	}
	if s.RecentRoots == nil {
		s.RecentRoots = []string{}
	}
	if s.ExcludeDirs == nil {
		s.ExcludeDirs = []string{}
	}
	// De-duplicate recent roots and cap the list.
	seen := map[string]bool{}
	out := s.RecentRoots[:0]
	for _, r := range s.RecentRoots {
		key := lowerPath(r)
		if r == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	if len(out) > 8 {
		out = out[:8]
	}
	s.RecentRoots = out
}
