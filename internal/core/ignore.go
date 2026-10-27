package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const ignoreStoreVersion = 1

// IgnoreRule records one "these images are not duplicates" group. It is keyed
// by the members' content hashes (or paths when a hash is unavailable), so
// moving/renaming a group does not make it appear again.
type IgnoreRule struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"` // exact|similar
	Mode      string   `json:"mode"` // sha|path
	Values    []string `json:"values"`
	Label     string   `json:"label,omitempty"`
	Count     int      `json:"count,omitempty"`
	CreatedAt int64    `json:"createdAt"`
}

type ignoreDisk struct {
	Version int          `json:"version"`
	Rules   []IgnoreRule `json:"rules"`
}

// IgnoreStore persists suppressed groups between scans.
type IgnoreStore struct {
	path  string
	mu    sync.Mutex
	rules []IgnoreRule
}

func NewIgnoreStore(path string) *IgnoreStore {
	s := &IgnoreStore{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var disk ignoreDisk
	if err := json.Unmarshal(data, &disk); err != nil || disk.Version != ignoreStoreVersion {
		return s
	}
	s.rules = disk.Rules
	return s
}

func (s *IgnoreStore) Rules() []IgnoreRule {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]IgnoreRule, len(s.rules))
	copy(out, s.rules)
	return out
}

func (s *IgnoreStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rules)
}

func groupIgnoreSignature(g *Group) (string, []string) {
	if g == nil {
		return "", nil
	}
	mode := "sha"
	values := make([]string, 0, len(g.Files))
	seen := make(map[string]bool, len(g.Files))
	for _, f := range g.Files {
		if f == nil || f.SHA256 == "" {
			mode = "path"
			break
		}
		v := strings.ToLower(f.SHA256)
		if !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	if mode == "path" {
		values = values[:0]
		for _, f := range g.Files {
			if f == nil {
				continue
			}
			v := lowerPath(f.Path)
			if !seen[v] {
				seen[v] = true
				values = append(values, v)
			}
		}
	}
	sort.Strings(values)
	return mode, values
}

func (s *IgnoreStore) AddGroup(g *Group) (IgnoreRule, bool, error) {
	mode, values := groupIgnoreSignature(g)
	if len(values) == 0 {
		return IgnoreRule{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.Mode == mode && r.Kind == g.Kind && equalStrings(r.Values, values) {
			return r, false, nil
		}
	}
	label := "未命名分组"
	if g.Keeper != nil && g.Keeper.Name != "" {
		label = g.Keeper.Name
	}
	if len(g.Files) > 1 {
		label += " 等 " + strconv.Itoa(len(g.Files)) + " 张"
	}
	rule := IgnoreRule{
		ID:        randomID(),
		Kind:      g.Kind,
		Mode:      mode,
		Values:    values,
		Label:     label,
		Count:     len(g.Files),
		CreatedAt: time.Now().UnixMilli(),
	}
	s.rules = append(s.rules, rule)
	if err := s.saveLocked(); err != nil {
		s.rules = s.rules[:len(s.rules)-1]
		return IgnoreRule{}, false, err
	}
	return rule, true, nil
}

func (s *IgnoreStore) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rules {
		if r.ID != id {
			continue
		}
		removed := s.rules[i]
		s.rules = append(s.rules[:i], s.rules[i+1:]...)
		if err := s.saveLocked(); err != nil {
			s.rules = append(s.rules[:i], append([]IgnoreRule{removed}, s.rules[i:]...)...)
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (s *IgnoreStore) Matches(g *Group) bool {
	if s == nil || g == nil {
		return false
	}
	mode, values := groupIgnoreSignature(g)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.Mode == mode && r.Kind == g.Kind && equalStrings(r.Values, values) {
			return true
		}
	}
	return false
}

func (s *IgnoreStore) FilterGroups(groups []*Group) []*Group {
	if s == nil || s.Len() == 0 {
		return groups
	}
	out := groups[:0]
	for _, g := range groups {
		if !s.Matches(g) {
			out = append(out, g)
		}
	}
	return out
}

func (s *IgnoreStore) saveLocked() error {
	data, err := json.Marshal(ignoreDisk{Version: ignoreStoreVersion, Rules: s.rules})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
