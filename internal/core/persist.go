package core

import (
	"encoding/json"
	"os"
	"time"
)

type snapshotFile struct {
	File
	Hist [64]float32 `json:"hist,omitempty"`
}

type snapshotGroup struct {
	ID          string         `json:"id"`
	Kind        string         `json:"kind"`
	KeeperPath  string         `json:"keeperPath"`
	Similarity  float64        `json:"similarity"`
	TotalBytes  int64          `json:"totalBytes"`
	WastedBytes int64          `json:"wastedBytes"`
	Files       []snapshotFile `json:"files"`
}

type resultSnapshot struct {
	Version    int             `json:"version"`
	ID         string          `json:"id"`
	Roots      []string        `json:"roots"`
	Options    ScanOptions     `json:"options"`
	StartedAt  time.Time       `json:"startedAt"`
	FinishedAt time.Time       `json:"finishedAt"`
	Stats      Stats           `json:"stats"`
	Issues     []Issue         `json:"issues"`
	Note       string          `json:"note,omitempty"`
	Files      []snapshotFile  `json:"files,omitempty"`
	Groups     []snapshotGroup `json:"groups"`
}

const snapshotVersion = 4

func (e *Engine) buildSnapshot(r *Result) resultSnapshot {
	snap := resultSnapshot{
		Version:    snapshotVersion,
		ID:         r.ID,
		Roots:      r.Roots,
		Options:    r.Options,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
		Stats:      r.Stats,
		Issues:     r.Issues,
		Note:       r.Note,
	}
	for _, f := range r.Files {
		if f != nil {
			snap.Files = append(snap.Files, snapshotFile{File: *f, Hist: f.Hist})
		}
	}
	for _, g := range r.Groups {
		sg := snapshotGroup{
			ID:          g.ID,
			Kind:        g.Kind,
			Similarity:  g.Similarity,
			TotalBytes:  g.TotalBytes,
			WastedBytes: g.WastedBytes,
		}
		for _, f := range g.Files {
			if f != nil {
				sg.Files = append(sg.Files, snapshotFile{File: *f, Hist: f.Hist})
			}
		}
		if g.Keeper != nil {
			sg.KeeperPath = g.Keeper.Path
		}
		snap.Groups = append(snap.Groups, sg)
	}
	return snap
}

// saveResult persists the current scan result for the next launch.
func (e *Engine) saveResult() {
	e.persistMu.Lock()
	defer e.persistMu.Unlock()
	r := e.Result()
	path := e.paths.LastResultPath
	if r == nil {
		_ = os.Remove(path)
		return
	}
	data, err := json.Marshal(e.buildSnapshot(r))
	if err != nil {
		return
	}
	// Keep the persisted snapshot reasonable.
	if len(data) > 80<<20 {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func (e *Engine) saveResultAsync() {
	go e.saveResult()
}

func fileFromSnapshot(sf snapshotFile, existing map[string]*File) *File {
	if f, ok := existing[sf.File.Path]; ok {
		return f
	}
	file := sf.File
	file.Hist = sf.Hist
	return &file
}

// loadResult reads the last persisted result, if any.
func (e *Engine) loadResult() *Result {
	data, err := os.ReadFile(e.paths.LastResultPath)
	if err != nil {
		return nil
	}
	var snap resultSnapshot
	if err := json.Unmarshal(data, &snap); err != nil || (snap.Version != 3 && snap.Version != snapshotVersion) {
		return nil
	}
	r := &Result{
		ID:         snap.ID,
		Roots:      snap.Roots,
		Options:    snap.Options,
		StartedAt:  snap.StartedAt,
		FinishedAt: snap.FinishedAt,
		Stats:      snap.Stats,
		Issues:     snap.Issues,
		Note:       snap.Note,
		Partial:    snap.Version < snapshotVersion,
	}
	fileByPath := map[string]*File{}
	for _, sf := range snap.Files {
		file := sf.File
		file.Hist = sf.Hist
		f := &file
		if f.Path == "" {
			continue
		}
		fileByPath[f.Path] = f
		r.Files = append(r.Files, f)
	}
	for _, sg := range snap.Groups {
		files := make([]*File, 0, len(sg.Files))
		seen := map[string]bool{}
		for _, sf := range sg.Files {
			file := sf.File
			file.Hist = sf.Hist
			f := fileFromSnapshot(sf, fileByPath)
			if f == nil || f.Path == "" || seen[f.Path] {
				continue
			}
			seen[f.Path] = true
			files = append(files, f)
			if _, ok := fileByPath[f.Path]; !ok {
				fileByPath[f.Path] = f
				r.Files = append(r.Files, f)
			}
		}
		if len(files) < 2 {
			continue
		}
		var keeper *File
		for _, f := range files {
			if f.Path == sg.KeeperPath {
				keeper = f
				break
			}
		}
		if keeper == nil {
			keeper = chooseKeeper(files, sg.Kind, snap.Options.KeeperMode)
		}
		g := newGroup(sg.Kind, files, keeper)
		if sg.Similarity > 0 {
			g.Similarity = sg.Similarity
		}
		r.Groups = append(r.Groups, g)
	}
	fillResultIndex(r)
	// Recompute group-level stats; keep scan-level counts from the snapshot.
	r.Stats.ExactGroups, r.Stats.ExactFiles, r.Stats.ReclaimableExact = 0, 0, 0
	r.Stats.SimilarGroups, r.Stats.SimilarFiles, r.Stats.ReclaimableSimilar = 0, 0, 0
	fillGroupStats(&r.Stats, r.Groups)
	return r
}
