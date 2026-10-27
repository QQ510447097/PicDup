package core

import (
	"context"
	"math"
	"math/bits"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// BuildGroups computes exact-duplicate groups and similar-image groups.
func BuildGroups(ctx context.Context, files []*File, opts ScanOptions, report func(fraction float64, message string)) ([]*Group, Stats, string) {
	var stats Stats
	var note string
	bySHA := make(map[string][]*File, len(files))
	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
		if f.SHA256 != "" {
			bySHA[f.SHA256] = append(bySHA[f.SHA256], f)
		}
	}
	stats.TotalBytes = totalBytes

	groups := make([]*Group, 0, 128)
	// ---- Exact duplicates -------------------------------------------------
	for _, fs := range bySHA {
		if len(fs) < 2 {
			continue
		}
		sort.Slice(fs, func(i, j int) bool { return lowerPath(fs[i].Path) < lowerPath(fs[j].Path) })
		keeper := chooseKeeper(fs, "exact", opts.KeeperMode)
		g := newGroup("exact", fs, keeper)
		groups = append(groups, g)
	}
	if report != nil {
		report(0.05, "已完成精确查重")
	}

	// ---- Similar images ----------------------------------------------------
	if opts.FindSimilar {
		// One representative per distinct content hash keeps the pairwise pass small.
		repBySHA := make(map[string]*File, len(bySHA))
		for sha, fs := range bySHA {
			var rep *File
			for _, f := range fs {
				if f.Width < 32 || f.Height < 32 || f.Detail < 0.008 || f.Error != "" {
					continue
				}
				if rep == nil || keeperBetter(f, rep, "similar", opts.KeeperMode) {
					rep = f
				}
			}
			if rep != nil {
				repBySHA[sha] = rep
			}
		}
		reps := make([]*File, 0, len(repBySHA))
		for _, f := range repBySHA {
			reps = append(reps, f)
		}
		sort.Slice(reps, func(i, j int) bool { return lowerPath(reps[i].Path) < lowerPath(reps[j].Path) })
		stats.UniqueImages = len(reps)
		if len(reps) > 30000 {
			note = "图片数量超过 30,000 张，已跳过相似图片比对以保护性能；仍可完成精确查重。"
		} else if len(reps) > 1 {
			threshold := opts.Similarity
			if threshold < 0 {
				threshold = 0
			}
			if threshold > 24 {
				threshold = 24
			}
			parent := make([]int, len(reps))
			for i := range parent {
				parent[i] = i
			}
			var find func(int) int
			find = func(x int) int {
				for parent[x] != x {
					parent[x] = parent[parent[x]]
					x = parent[x]
				}
				return x
			}
			union := func(a, b int) {
				ra, rb := find(a), find(b)
				if ra != rb {
					parent[rb] = ra
				}
			}
			n := len(reps)
			edges := make(chan [2]int, 4096)
			var wg sync.WaitGroup
			workers := runtime.NumCPU()
			if workers > 16 {
				workers = 16
			}
			if workers < 2 {
				workers = 2
			}
			var cursor int64 = -1
			var lastReport int64 = -1
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						if ctx.Err() != nil {
							return
						}
						i := int(atomic.AddInt64(&cursor, 1))
						if i >= n {
							return
						}
						a := reps[i]
						for j := i + 1; j < n; j++ {
							b := reps[j]
							if similarPair(a, b, threshold) {
								select {
								case edges <- [2]int{i, j}:
								case <-ctx.Done():
									return
								}
							}
						}
						if report != nil {
							cur := atomic.LoadInt64(&cursor)
							if cur-atomic.LoadInt64(&lastReport) >= int64(n/40+1) {
								atomic.StoreInt64(&lastReport, cur)
								report(0.05+0.85*float64(cur)/float64(n), "正在比对感知特征")
							}
						}
					}
				}()
			}
			go func() {
				wg.Wait()
				close(edges)
			}()
			for e := range edges {
				union(e[0], e[1])
			}
			if ctx.Err() != nil {
				return nil, stats, ""
			}
			components := map[int][]*File{}
			for i := range reps {
				root := find(i)
				components[root] = append(components[root], reps[i])
			}
			for _, comp := range components {
				if len(comp) < 2 {
					continue
				}
				for _, cluster := range splitCluster(comp, threshold) {
					if len(cluster) < 2 {
						continue
					}
					members := make([]*File, 0, len(cluster)*2)
					seen := make(map[string]bool)
					for _, rep := range cluster {
						for _, f := range bySHA[rep.SHA256] {
							if !seen[f.Path] {
								seen[f.Path] = true
								members = append(members, f)
							}
						}
					}
					if len(members) < 2 {
						continue
					}
					keeper := chooseKeeper(members, "similar", opts.KeeperMode)
					g := newGroup("similar", members, keeper)
					groups = append(groups, g)
				}
			}
		}
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Kind != groups[j].Kind {
			return groups[i].Kind == "exact"
		}
		if groups[i].WastedBytes != groups[j].WastedBytes {
			return groups[i].WastedBytes > groups[j].WastedBytes
		}
		return len(groups[i].Files) > len(groups[j].Files)
	})
	fillGroupStats(&stats, groups)
	if report != nil {
		report(1, "分组完成")
	}
	return groups, stats, note
}

func newGroup(kind string, files []*File, keeper *File) *Group {
	g := &Group{Kind: kind, Files: files, Keeper: keeper}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	if keeper != nil {
		g.TotalBytes = total
		g.WastedBytes = total - keeper.Size
		if g.WastedBytes < 0 {
			g.WastedBytes = 0
		}
		if kind == "similar" {
			minSim := 100.0
			for _, f := range files {
				sim := similarityPercent(keeper, f)
				if sim < minSim {
					minSim = sim
				}
			}
			g.Similarity = minSim
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		if keeper != nil {
			if files[i] == keeper {
				return true
			}
			if files[j] == keeper {
				return false
			}
		}
		if kind == "similar" {
			si := similarityPercent(keeper, files[i])
			sj := similarityPercent(keeper, files[j])
			if si != sj {
				return si > sj
			}
		}
		return lowerPath(files[i].Path) < lowerPath(files[j].Path)
	})
	g.ID = groupID(kind, files)
	return g
}

func normalizeKeeperMode(mode string) string {
	switch mode {
	case "resolution", "largest", "newest", "oldest", "shortest":
		return mode
	default:
		return "resolution"
	}
}

func chooseKeeper(files []*File, kind, mode string) *File {
	if len(files) == 0 {
		return nil
	}
	best := files[0]
	for _, f := range files[1:] {
		if keeperBetter(f, best, kind, mode) {
			best = f
		}
	}
	return best
}

func keeperBetter(a, b *File, kind, mode string) bool {
	if b == nil {
		return true
	}
	mode = normalizeKeeperMode(mode)
	switch mode {
	case "largest":
		if a.Size != b.Size {
			return a.Size > b.Size
		}
	case "newest":
		if a.ModTime != b.ModTime {
			return a.ModTime > b.ModTime
		}
	case "oldest":
		if a.ModTime != b.ModTime {
			return a.ModTime < b.ModTime
		}
	case "shortest":
		if len(a.Path) != len(b.Path) {
			return len(a.Path) < len(b.Path)
		}
	default:
		if kind == "similar" {
			if a.Pixels() != b.Pixels() {
				return a.Pixels() > b.Pixels()
			}
			if a.Size != b.Size {
				return a.Size > b.Size
			}
		} else {
			if a.ModTime != b.ModTime {
				return a.ModTime < b.ModTime
			}
		}
	}
	// Stable, predictable tie-breakers after the chosen preference.
	if a.Size != b.Size && mode != "largest" {
		return a.Size > b.Size
	}
	if a.Pixels() != b.Pixels() {
		return a.Pixels() > b.Pixels()
	}
	if len(a.Path) != len(b.Path) {
		return len(a.Path) < len(b.Path)
	}
	return lowerPath(a.Path) < lowerPath(b.Path)
}

func similarityPercent(a, b *File) float64 {
	if a == nil || b == nil {
		return 0
	}
	if a.SHA256 != "" && a.SHA256 == b.SHA256 {
		return 100
	}
	d := pHashDistance(a, b)
	return math.Round((1-float64(d)/64)*1000) / 10
}

// SimilarityPercent returns the visual similarity percentage between two files.
func SimilarityPercent(a, b *File) float64 { return similarityPercent(a, b) }

// pHashDistance returns the smallest hamming distance across full and central
// crop fingerprints, so modest crops still compare as similar.
func pHashDistance(a, b *File) int {
	d := bits.OnesCount64(a.PHash ^ b.PHash)
	if a.PHashCrop != 0 && b.PHashCrop != 0 {
		if v := bits.OnesCount64(a.PHash ^ b.PHashCrop); v < d {
			d = v
		}
		if v := bits.OnesCount64(a.PHashCrop ^ b.PHash); v < d {
			d = v
		}
		if v := bits.OnesCount64(a.PHashCrop ^ b.PHashCrop); v < d {
			d = v
		}
	}
	if a.PHashOuter != 0 && b.PHashOuter != 0 {
		if v := bits.OnesCount64(a.PHash ^ b.PHashOuter); v < d {
			d = v
		}
		if v := bits.OnesCount64(a.PHashOuter ^ b.PHash); v < d {
			d = v
		}
		if v := bits.OnesCount64(a.PHashOuter ^ b.PHashOuter); v < d {
			d = v
		}
	}
	return d
}

func pHashWithin(a, b *File, threshold int) bool {
	full := bits.OnesCount64(a.PHash ^ b.PHash)
	if full <= threshold {
		return true
	}
	// Crops shift the DCT grid. Only pay for the extra comparisons when the
	// full-frame fingerprints are already in the same neighborhood.
	if full > threshold+12 {
		return false
	}
	candidates := []uint64{a.PHashCrop, a.PHashOuter}
	others := []uint64{b.PHashCrop, b.PHashOuter}
	for _, ca := range candidates {
		for _, cb := range others {
			n := 0
			if ca != 0 {
				n++
			}
			if cb != 0 {
				n++
			}
			if n < 2 {
				continue
			}
			if bits.OnesCount64(ca^cb) <= threshold {
				return true
			}
		}
	}
	if a.PHashCrop != 0 {
		if bits.OnesCount64(a.PHashCrop^b.PHash) <= threshold || bits.OnesCount64(a.PHash^b.PHashCrop) <= threshold {
			return true
		}
	}
	if a.PHashOuter != 0 {
		if bits.OnesCount64(a.PHashOuter^b.PHash) <= threshold || bits.OnesCount64(a.PHash^b.PHashOuter) <= threshold {
			return true
		}
	}
	return false
}

func similarPair(a, b *File, threshold int) bool {
	if a == nil || b == nil {
		return false
	}
	if !pHashWithin(a, b, threshold) {
		return false
	}
	hist := histogramIntersection(a.Hist, b.Hist)
	if hist < 0.5 {
		return false
	}
	// The difference hash is noisy on textured photos, so it only guards weak
	// colour-histogram matches (for example unrelated flat images).
	if hist < 0.7 && bits.OnesCount64(a.DHash^b.DHash) > threshold+8 {
		return false
	}
	// Ignore extreme aspect-ratio mismatches only when the colour histograms are
	// also weak; crops often change aspect ratio, so this stays permissive.
	arA, arB := a.Aspect(), b.Aspect()
	if arA > 0 && arB > 0 {
		ratio := arA / arB
		if ratio < 1 {
			ratio = 1 / ratio
		}
		if ratio > 2.6 && hist < 0.72 {
			return false
		}
	}
	return true
}

// splitCluster breaks a connected component into cliques around a medoid so
// long chains (A~B, B~C, A!~C) do not create giant false groups.
func splitCluster(cluster []*File, threshold int) [][]*File {
	remaining := append([]*File(nil), cluster...)
	var out [][]*File
	for len(remaining) >= 2 {
		seedIdx := 0
		bestScore := math.MaxInt
		for i, a := range remaining {
			score := 0
			for j, b := range remaining {
				if i == j {
					continue
				}
				score += pHashDistance(a, b)
			}
			if score < bestScore {
				bestScore = score
				seedIdx = i
			}
		}
		seed := remaining[seedIdx]
		group := []*File{seed}
		rest := make([]*File, 0, len(remaining)-1)
		for i, f := range remaining {
			if i == seedIdx {
				continue
			}
			if similarPair(seed, f, threshold) {
				group = append(group, f)
			} else {
				rest = append(rest, f)
			}
		}
		if len(group) >= 2 {
			out = append(out, group)
		}
		remaining = rest
	}
	return out
}

// groupFilesSorted returns files ordered for display.
func groupFilesSorted(g *Group) []*File {
	out := append([]*File(nil), g.Files...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i] == g.Keeper {
			return true
		}
		if out[j] == g.Keeper {
			return false
		}
		if g.Kind == "similar" {
			si := similarityPercent(g.Keeper, out[i])
			sj := similarityPercent(g.Keeper, out[j])
			if si != sj {
				return si > sj
			}
		}
		return strings.ToLower(out[i].Path) < strings.ToLower(out[j].Path)
	})
	return out
}
