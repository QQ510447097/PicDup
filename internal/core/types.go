package core

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

const (
	AppName = "PicDup"
	Version = "1.0.0"
)

// SupportedImageExtensions is the set of file extensions scanned by default.
var SupportedImageExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".bmp":  true,
	".webp": true,
	".tif":  true,
	".tiff": true,
}

// SupportedFormats returns the display list of supported image extensions.
func SupportedFormats() []string {
	out := make([]string, 0, len(SupportedImageExtensions))
	for ext := range SupportedImageExtensions {
		out = append(out, strings.ToUpper(strings.TrimPrefix(ext, ".")))
	}
	sort.Strings(out)
	return out
}

// ScanOptions controls a single scan.
type ScanOptions struct {
	IncludeSubfolders bool     `json:"includeSubfolders"`
	FindSimilar       bool     `json:"findSimilar"`
	Similarity        int      `json:"similarity"` // pHash hamming threshold, 0..24
	IncludeHidden     bool     `json:"includeHidden"`
	MinFileSizeKB     int      `json:"minFileSizeKB"`
	KeeperMode        string   `json:"keeperMode,omitempty"`
	ExcludeDirs       []string `json:"excludeDirs,omitempty"`
}

// ScanRequest is posted by the UI to start a scan.
type ScanRequest struct {
	Roots   []string    `json:"roots"`
	Options ScanOptions `json:"options"`
}

// File is one image discovered during a scan.
type File struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"modTime"` // unix milliseconds
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Format   string `json:"format"`
	SHA256   string `json:"sha,omitempty"`
	ThumbKey string `json:"thumbKey,omitempty"`
	Error    string `json:"error,omitempty"`

	PHash      uint64      `json:"pHash,omitempty"`
	PHashCrop  uint64      `json:"pHashCrop,omitempty"`
	PHashOuter uint64      `json:"pHashOuter,omitempty"`
	DHash      uint64      `json:"dHash,omitempty"`
	Hist       [64]float32 `json:"-"`
	Detail     float32     `json:"detail,omitempty"`
}

// Pixels returns width*height.
func (f *File) Pixels() int64 { return int64(f.Width) * int64(f.Height) }

// Aspect returns width/height (0 when height is unknown).
func (f *File) Aspect() float64 {
	if f.Height == 0 {
		return 0
	}
	return float64(f.Width) / float64(f.Height)
}

// ModTimeValue returns the file modification time.
func (f *File) ModTimeValue() time.Time { return time.UnixMilli(f.ModTime) }

// Group is a set of duplicate or visually similar images.
type Group struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"` // "exact" or "similar"
	Files       []*File `json:"files"`
	Keeper      *File   `json:"-"`
	TotalBytes  int64   `json:"totalBytes"`
	WastedBytes int64   `json:"wastedBytes"`
	// Similarity is the lowest similarity (vs keeper) among members, in percent.
	Similarity float64 `json:"similarity"`
}

// KeeperPath returns the selected keeper path (empty when the group is empty).
func (g *Group) KeeperPath() string {
	if g.Keeper == nil {
		return ""
	}
	return g.Keeper.Path
}

// Stats summarises a scan.
// ScanTimings breaks a scan down into stages for profiling.
type ScanTimings struct {
	WalkMs        int64 `json:"walkMs"`
	AnalyzeMs     int64 `json:"analyzeMs"`
	GroupMs       int64 `json:"groupMs"`
	HashMs        int64 `json:"hashMs"`
	DecodeMs      int64 `json:"decodeMs"`
	FingerprintMs int64 `json:"fingerprintMs"`
	ThumbnailMs   int64 `json:"thumbnailMs"`
	CacheHits     int   `json:"cacheHits"`
	CacheMisses   int   `json:"cacheMisses"`
	Workers       int   `json:"workers"`
}

type Stats struct {
	FilesScanned       int         `json:"filesScanned"`
	FilesAnalyzed      int         `json:"filesAnalyzed"`
	FilesFailed        int         `json:"filesFailed"`
	FilesSkipped       int         `json:"filesSkipped"`
	UniqueImages       int         `json:"uniqueImages"`
	ExactGroups        int         `json:"exactGroups"`
	ExactFiles         int         `json:"exactFiles"`
	SimilarGroups      int         `json:"similarGroups"`
	SimilarFiles       int         `json:"similarFiles"`
	IgnoredGroups      int         `json:"ignoredGroups"`
	TotalBytes         int64       `json:"totalBytes"`
	ReclaimableExact   int64       `json:"reclaimableExact"`
	ReclaimableSimilar int64       `json:"reclaimableSimilar"`
	DurationMs         int64       `json:"durationMs"`
	Timings            ScanTimings `json:"timings"`
}

// Issue records a file that could not be read.
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Result is the outcome of a completed scan.
type Result struct {
	ID         string      `json:"id"`
	Roots      []string    `json:"roots"`
	Options    ScanOptions `json:"options"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt time.Time   `json:"finishedAt"`
	Groups     []*Group    `json:"groups"`
	Files      []*File     `json:"-"`
	Stats      Stats       `json:"stats"`
	Issues     []Issue     `json:"issues"`
	Note       string      `json:"note,omitempty"`
	Partial    bool        `json:"-"`

	fileMap map[string]*File
}

// Progress is polled by the UI while a scan runs.
type Progress struct {
	Phase          string  `json:"phase"` // idle|discovering|analyzing|comparing|finalizing|done|cancelled|error
	Percent        float64 `json:"percent"`
	FilesFound     int     `json:"filesFound"`
	FilesProcessed int     `json:"filesProcessed"`
	FilesFailed    int     `json:"filesFailed"`
	TotalFiles     int     `json:"totalFiles"`
	Current        string  `json:"current"`
	ElapsedMs      int64   `json:"elapsedMs"`
	GroupsExact    int     `json:"groupsExact"`
	GroupsSimilar  int     `json:"groupsSimilar"`
	Message        string  `json:"message"`
	Done           bool    `json:"done"`
}

// Settings are persisted between runs.
type Settings struct {
	Similarity        int      `json:"similarity"`
	FindSimilar       bool     `json:"findSimilar"`
	IncludeSubfolders bool     `json:"includeSubfolders"`
	IncludeHidden     bool     `json:"includeHidden"`
	MinFileSizeKB     int      `json:"minFileSizeKB"`
	KeeperMode        string   `json:"keeperMode"`  // resolution|largest|newest|oldest|shortest
	CleanupMode       string   `json:"cleanupMode"` // trash|recycle
	ConfirmCleanup    bool     `json:"confirmCleanup"`
	Theme             string   `json:"theme"` // dark|light|system
	AutoSelect        bool     `json:"autoSelect"`
	RecentRoots       []string `json:"recentRoots"`
	ExcludeDirs       []string `json:"excludeDirs"`
}

// DefaultSettings returns a safe, sensible starting configuration.
func DefaultSettings() Settings {
	return Settings{
		Similarity:        8,
		FindSimilar:       true,
		IncludeSubfolders: true,
		IncludeHidden:     false,
		MinFileSizeKB:     0,
		KeeperMode:        "resolution",
		CleanupMode:       "trash",
		ConfirmCleanup:    true,
		Theme:             "dark",
		AutoSelect:        true,
		RecentRoots:       []string{},
		ExcludeDirs:       []string{"$RECYCLE.BIN", "System Volume Information", "node_modules", ".git"},
	}
}

// Options converts settings into scan options for the given roots.
func (s Settings) Options(roots []string) ScanRequest {
	return ScanRequest{
		Roots: roots,
		Options: ScanOptions{
			IncludeSubfolders: s.IncludeSubfolders,
			FindSimilar:       s.FindSimilar,
			Similarity:        s.Similarity,
			IncludeHidden:     s.IncludeHidden,
			MinFileSizeKB:     s.MinFileSizeKB,
			KeeperMode:        s.KeeperMode,
			ExcludeDirs:       append([]string(nil), s.ExcludeDirs...),
		},
	}
}

// TrashItem is one file moved into the quarantine.
type TrashItem struct {
	OriginalPath string `json:"originalPath"`
	StoredPath   string `json:"storedPath"`
	Size         int64  `json:"size"`
	ModTime      int64  `json:"modTime"`
	Name         string `json:"name"`
}

// TrashBatch groups the files removed in one cleanup operation.
type TrashBatch struct {
	ID        string      `json:"id"`
	CreatedAt time.Time   `json:"createdAt"`
	Items     []TrashItem `json:"items"`
	TotalSize int64       `json:"totalSize"`
	Mode      string      `json:"mode"`
}

// TrashFailure describes a file that could not be moved.
type TrashFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

// CleanupResult is returned by the cleanup endpoint.
type CleanupResult struct {
	BatchID string         `json:"batchId"`
	Moved   int            `json:"moved"`
	Bytes   int64          `json:"bytes"`
	Failed  []TrashFailure `json:"failed"`
	Mode    string         `json:"mode"`
}

// RestoreResult is returned when a trash batch is restored.
type RestoreResult struct {
	Restored int            `json:"restored"`
	Failed   []TrashFailure `json:"failed"`
}

// groupID derives a stable id for a group from its kind and member paths.
func groupID(kind string, files []*File) string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, strings.ToLower(f.Path))
	}
	sort.Strings(paths)
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

// humanBytes is used by logs only.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(n) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return itoa(n/div) + " " + string("KMGTPE"[exp]) + "B"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [20]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
