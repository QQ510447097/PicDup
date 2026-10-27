package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"picdup/internal/core"
)

// Options configures the HTTP API.
type Options struct {
	Desktop    bool
	Version    string
	Static     http.Handler
	PickFolder func() (string, error)
}

// Server exposes the PicDup API and frontend.
type Server struct {
	eng        *core.Engine
	opts       Options
	mux        *http.ServeMux
	previewSem chan struct{}
}

// New creates the API server.
func New(eng *core.Engine, opts Options) *Server {
	if opts.Version == "" {
		opts.Version = core.Version
	}
	s := &Server{eng: eng, opts: opts, mux: http.NewServeMux(), previewSem: make(chan struct{}, 2)}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/bootstrap", s.handleBootstrap)
	s.mux.HandleFunc("GET /api/scan", s.handleScanStatus)
	s.mux.HandleFunc("GET /api/scan/status", s.handleScanStatus)
	s.mux.HandleFunc("POST /api/scan", s.handleStartScan)
	s.mux.HandleFunc("POST /api/scan/cancel", s.handleCancelScan)
	s.mux.HandleFunc("POST /api/result/clear", s.handleClearResult)
	s.mux.HandleFunc("GET /api/results", s.handleResults)
	s.mux.HandleFunc("GET /api/suggest", s.handleSuggest)
	s.mux.HandleFunc("GET /api/groups/{id}", s.handleGroup)
	s.mux.HandleFunc("POST /api/ignore/group", s.handleIgnoreGroup)
	s.mux.HandleFunc("GET /api/ignore", s.handleIgnoreList)
	s.mux.HandleFunc("DELETE /api/ignore/{id}", s.handleIgnoreRemove)
	s.mux.HandleFunc("GET /api/thumb", s.handleThumb)
	s.mux.HandleFunc("GET /api/preview", s.handlePreview)
	s.mux.HandleFunc("POST /api/cleanup", s.handleCleanup)
	s.mux.HandleFunc("GET /api/trash", s.handleTrashList)
	s.mux.HandleFunc("POST /api/trash/restore", s.handleTrashRestore)
	s.mux.HandleFunc("POST /api/trash/delete", s.handleTrashDelete)
	s.mux.HandleFunc("POST /api/trash/empty", s.handleTrashEmpty)
	s.mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	s.mux.HandleFunc("POST /api/settings", s.handleUpdateSettings)
	s.mux.HandleFunc("POST /api/settings/reset", s.handleResetSettings)
	s.mux.HandleFunc("POST /api/folders/pick", s.handlePickFolder)
	s.mux.HandleFunc("GET /api/dropped", s.handleDropped)
	s.mux.HandleFunc("POST /api/open", s.handleOpen)
	s.mux.HandleFunc("GET /api/cache", s.handleCache)
	s.mux.HandleFunc("POST /api/cache/clear", s.handleCacheClear)
	s.mux.HandleFunc("POST /api/cache/clear-fingerprints", s.handleFingerprintCacheClear)
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "接口不存在")
	})
	if s.opts.Static != nil {
		s.mux.Handle("/", s.opts.Static)
	}
}

// SetPickFolder installs the native folder picker callback.
func (s *Server) SetPickFolder(fn func() (string, error)) {
	s.opts.PickFolder = fn
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.recoverMiddleware(s.headersMiddleware(s.originMiddleware(s.mux)))
}

func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeError(w, http.StatusInternalServerError, fmt.Sprintf("服务器内部错误: %v", rec))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) headersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
			ct := r.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "请求必须使用 application/json")
				return
			}
			origin := r.Header.Get("Origin")
			if strings.HasPrefix(origin, "http://") || strings.HasPrefix(origin, "https://") {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					writeError(w, http.StatusForbidden, "跨站请求已被拒绝")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------- view models ----------------

type counts struct {
	ExactGroups   int   `json:"exactGroups"`
	SimilarGroups int   `json:"similarGroups"`
	ExactFiles    int   `json:"exactFiles"`
	SimilarFiles  int   `json:"similarFiles"`
	ReclaimExact  int64 `json:"reclaimExact"`
	ReclaimSim    int64 `json:"reclaimSimilar"`
}

type memberView struct {
	Path       string  `json:"path"`
	Name       string  `json:"name"`
	Dir        string  `json:"dir"`
	Size       int64   `json:"size"`
	ModTime    int64   `json:"modTime"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	Format     string  `json:"format"`
	Thumb      string  `json:"thumb"`
	Similarity float64 `json:"similarity"`
	Keeper     bool    `json:"keeper"`
	Same       bool    `json:"same"`
	Pixels     int64   `json:"pixels"`
}

type candidateView struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Keeper bool   `json:"keeper"`
}

type groupView struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	Count      int             `json:"count"`
	TotalSize  int64           `json:"totalSize"`
	Wasted     int64           `json:"wasted"`
	Similarity float64         `json:"similarity"`
	Keeper     *memberView     `json:"keeper"`
	Members    []memberView    `json:"members"`
	Candidates []candidateView `json:"candidates"`
}

type resultInfo struct {
	ID         string           `json:"id"`
	Roots      []string         `json:"roots"`
	StartedAt  time.Time        `json:"startedAt"`
	FinishedAt time.Time        `json:"finishedAt"`
	Stats      core.Stats       `json:"stats"`
	Options    core.ScanOptions `json:"options"`
	Note       string           `json:"note,omitempty"`
	IssueCount int              `json:"issueCount"`
	Issues     []core.Issue     `json:"issues,omitempty"`
}

type trashBatchView struct {
	ID        string          `json:"id"`
	CreatedAt time.Time       `json:"createdAt"`
	TotalSize int64           `json:"totalSize"`
	Count     int             `json:"count"`
	Items     []trashItemView `json:"items"`
}

type trashItemView struct {
	Name         string `json:"name"`
	OriginalPath string `json:"originalPath"`
	Size         int64  `json:"size"`
	ModTime      int64  `json:"modTime"`
}

type trashSummary struct {
	Batches int   `json:"batches"`
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
}

type cacheSummary struct {
	Files            int   `json:"files"`
	Bytes            int64 `json:"bytes"`
	FingerprintFiles int   `json:"fingerprintFiles"`
	FingerprintBytes int64 `json:"fingerprintBytes"`
}

type bootstrapResponse struct {
	Version      string          `json:"version"`
	Platform     string          `json:"platform"`
	Desktop      bool            `json:"desktop"`
	Picker       bool            `json:"picker"`
	Recycle      bool            `json:"recycle"`
	RecycleNote  string          `json:"recycleNote,omitempty"`
	Settings     core.Settings   `json:"settings"`
	Status       core.ScanStatus `json:"status"`
	Result       *resultInfo     `json:"result,omitempty"`
	Trash        trashSummary    `json:"trash"`
	Cache        cacheSummary    `json:"cache"`
	Counts       counts          `json:"counts"`
	AppDataDir   string          `json:"appDataDir"`
	TrashDir     string          `json:"trashDir"`
	DataPortable bool            `json:"dataPortable"`
	DataWarnings []string        `json:"dataWarnings,omitempty"`
	Formats      []string        `json:"formats"`
	IgnoreCount  int             `json:"ignoreCount"`
}

// ---------------- handlers ----------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.opts.Version, "time": time.Now()})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	status := s.eng.Status()
	settings := s.eng.Settings()
	batches, files, bytes := s.eng.TrashStats()
	cacheFiles, cacheBytes := s.eng.ThumbStats()
	fingerprintFiles, fingerprintBytes := s.eng.FingerprintStats()
	resp := bootstrapResponse{
		Version:     s.opts.Version,
		Platform:    runtime.GOOS,
		Desktop:     s.opts.Desktop,
		Picker:      s.opts.PickFolder != nil,
		Recycle:     s.eng.RecycleSupported(),
		RecycleNote: s.eng.RecycleUnsupportedMessage(),
		Settings:    settings,
		Status:      status,
		Trash:       trashSummary{Batches: batches, Files: files, Bytes: bytes},
		Cache: cacheSummary{
			Files: cacheFiles, Bytes: cacheBytes,
			FingerprintFiles: fingerprintFiles, FingerprintBytes: fingerprintBytes,
		},
		AppDataDir:   s.eng.Paths().LocalDir,
		TrashDir:     s.eng.Paths().TrashDir,
		DataPortable: s.eng.Paths().Portable,
		DataWarnings: s.eng.Paths().Warnings,
		Formats:      core.SupportedFormats(),
		IgnoreCount:  len(s.eng.IgnoreRules()),
	}
	resp.Counts = countsFromResult(s.eng.Result())
	resp.Result = resultInfoFrom(s.eng.Result())
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Status())
}

func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	var req core.ScanRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	progress, err := s.eng.StartScan(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"progress": progress})
}

func (s *Server) handleCancelScan(w http.ResponseWriter, r *http.Request) {
	s.eng.CancelScan()
	writeJSON(w, http.StatusOK, s.eng.Status())
}

func (s *Server) handleClearResult(w http.ResponseWriter, r *http.Request) {
	s.eng.ClearResult()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if kind == "" {
		kind = "all"
	}
	if kind != "all" && kind != "exact" && kind != "similar" {
		writeError(w, http.StatusBadRequest, "无效的分组类型")
		return
	}
	sortBy := q.Get("sort")
	if sortBy == "" {
		sortBy = "wasted"
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 24
	}
	if limit > 100 {
		limit = 100
	}

	result := s.eng.Result()
	resp := struct {
		Groups    []groupView `json:"groups"`
		Total     int         `json:"total"`
		Offset    int         `json:"offset"`
		Limit     int         `json:"limit"`
		Stats     core.Stats  `json:"stats"`
		Counts    counts      `json:"counts"`
		HasResult bool        `json:"hasResult"`
		Kind      string      `json:"kind"`
	}{Offset: offset, Limit: limit, Kind: kind}
	if result == nil {
		resp.Groups = []groupView{}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Stats = result.Stats
	resp.Counts = countsFromResult(result)
	resp.HasResult = true

	filtered := make([]*core.Group, 0, len(result.Groups))
	for _, g := range result.Groups {
		if kind != "all" && g.Kind != kind {
			continue
		}
		if search != "" && !groupMatches(g, search) {
			continue
		}
		filtered = append(filtered, g)
	}
	switch sortBy {
	case "count":
		sort.SliceStable(filtered, func(i, j int) bool {
			if len(filtered[i].Files) != len(filtered[j].Files) {
				return len(filtered[i].Files) > len(filtered[j].Files)
			}
			return filtered[i].WastedBytes > filtered[j].WastedBytes
		})
	case "name":
		sort.SliceStable(filtered, func(i, j int) bool {
			return groupName(filtered[i]) < groupName(filtered[j])
		})
	default:
		sort.SliceStable(filtered, func(i, j int) bool {
			if filtered[i].WastedBytes != filtered[j].WastedBytes {
				return filtered[i].WastedBytes > filtered[j].WastedBytes
			}
			return len(filtered[i].Files) > len(filtered[j].Files)
		})
	}
	resp.Total = len(filtered)
	end := offset + limit
	if offset > len(filtered) {
		offset = len(filtered)
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	if end < offset {
		end = offset
	}
	page := filtered[offset:end]
	resp.Groups = make([]groupView, 0, len(page))
	for _, g := range page {
		resp.Groups = append(resp.Groups, s.groupView(g, 4))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSuggest(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if kind != "exact" && kind != "similar" {
		writeError(w, http.StatusBadRequest, "无效的分组类型")
		return
	}
	result := s.eng.Result()
	resp := struct {
		Paths  []string `json:"paths"`
		Count  int      `json:"count"`
		Bytes  int64    `json:"bytes"`
		Groups int      `json:"groups"`
	}{Paths: []string{}}
	if result != nil {
		for _, g := range result.Groups {
			if g.Kind != kind {
				continue
			}
			resp.Groups++
			for _, f := range g.Files {
				if g.Keeper != nil && f.Path == g.Keeper.Path {
					continue
				}
				resp.Paths = append(resp.Paths, f.Path)
				resp.Bytes += f.Size
			}
		}
	}
	resp.Count = len(resp.Paths)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result := s.eng.Result()
	if result == nil {
		writeError(w, http.StatusNotFound, "没有扫描结果")
		return
	}
	for _, g := range result.Groups {
		if g.ID == id {
			writeJSON(w, http.StatusOK, s.groupView(g, len(g.Files)))
			return
		}
	}
	writeError(w, http.StatusNotFound, "分组不存在或已更新")
}

func (s *Server) handleIgnoreGroup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		GroupID string `json:"groupId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rule, err := s.eng.IgnoreGroup(req.GroupID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rule":  rule,
		"rules": s.eng.IgnoreRules(),
	})
}

func (s *Server) handleIgnoreList(w http.ResponseWriter, r *http.Request) {
	rules := s.eng.IgnoreRules()
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules, "count": len(rules)})
}

func (s *Server) handleIgnoreRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	removed, err := s.eng.RestoreIgnore(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, "忽略规则不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rules": s.eng.IgnoreRules()})
}

func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "缺少图片路径")
		return
	}
	f, ok := s.eng.LookupFile(path)
	if !ok {
		writeError(w, http.StatusNotFound, "图片不在当前结果中")
		return
	}
	key := f.ThumbKey
	var err error
	if key == "" || !fileExists(s.eng.ThumbFilePath(key)) {
		key, err = s.eng.ThumbKey(f.Path)
		if err != nil {
			writeError(w, http.StatusNotFound, "无法生成缩略图: "+err.Error())
			return
		}
	}
	etag := `"` + key + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	http.ServeFile(w, r, s.eng.ThumbFilePath(key))
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, http.StatusBadRequest, "缺少图片路径")
		return
	}
	f, ok := s.eng.LookupFile(path)
	if !ok {
		writeError(w, http.StatusNotFound, "图片不在当前结果中")
		return
	}
	etag := fmt.Sprintf(`"%s-%d-preview"`, f.SHA256[:min(16, len(f.SHA256))], f.ModTime)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.previewSem <- struct{}{}
	data, err := s.eng.Preview(path, 1800)
	<-s.previewSem
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取图片: "+err.Error())
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Write(data)
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Paths []string `json:"paths"`
		Mode  string   `json:"mode"`
		Kind  string   `json:"kind"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.eng.Cleanup(req.Paths, req.Mode, req.Kind)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	status := s.eng.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"cleanup": res,
		"status":  status,
		"trash":   s.trashSummary(),
		"stats":   statsOf(s.eng.Result()),
	})
}

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	batches := s.eng.TrashList()
	views := make([]trashBatchView, 0, len(batches))
	for _, b := range batches {
		v := trashBatchView{ID: b.ID, CreatedAt: b.CreatedAt, TotalSize: b.TotalSize, Count: len(b.Items)}
		for _, it := range b.Items {
			v.Items = append(v.Items, trashItemView{Name: it.Name, OriginalPath: it.OriginalPath, Size: it.Size, ModTime: it.ModTime})
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"batches": views, "summary": s.trashSummary()})
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BatchID string `json:"batchId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := s.eng.RestoreTrash(req.BatchID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restore": res, "summary": s.trashSummary()})
}

func (s *Server) handleTrashDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BatchID string `json:"batchId"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	n, err := s.eng.DeleteTrashBatch(req.BatchID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n, "summary": s.trashSummary()})
}

func (s *Server) handleTrashEmpty(w http.ResponseWriter, r *http.Request) {
	files, bytes, err := s.eng.EmptyTrash()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "bytes": bytes, "summary": s.trashSummary()})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Settings())
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var settings core.Settings
	if !decodeJSON(w, r, &settings) {
		return
	}
	updated, err := s.eng.UpdateSettings(settings)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleResetSettings(w http.ResponseWriter, r *http.Request) {
	updated, err := s.eng.ResetSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	if s.opts.PickFolder == nil {
		writeError(w, http.StatusNotImplemented, "当前环境不支持系统文件夹选择器，请手动输入路径")
		return
	}
	path, err := s.opts.PickFolder()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(path) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

func (s *Server) handleDropped(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"paths": s.eng.ConsumeDropped()})
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		Reveal bool   `json:"reveal"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	_, inResult := s.eng.LookupFile(req.Path)
	paths := s.eng.Paths()
	appPath := samePath(req.Path, paths.LocalDir) || samePath(req.Path, paths.TrashDir)
	if !inResult && !appPath {
		writeError(w, http.StatusNotFound, "文件不在当前结果中")
		return
	}
	var err error
	if req.Reveal {
		err = revealPath(req.Path)
	} else {
		err = openPath(req.Path)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func cacheSnapshot(eng *core.Engine) cacheSummary {
	files, bytes := eng.ThumbStats()
	fingerprintFiles, fingerprintBytes := eng.FingerprintStats()
	return cacheSummary{
		Files: files, Bytes: bytes,
		FingerprintFiles: fingerprintFiles, FingerprintBytes: fingerprintBytes,
	}
}

func (s *Server) handleCache(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, cacheSnapshot(s.eng))
}

func (s *Server) handleCacheClear(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.ClearThumbs(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cacheSnapshot(s.eng))
}

func (s *Server) handleFingerprintCacheClear(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.ClearFingerprints(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cacheSnapshot(s.eng))
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ---------------- helpers ----------------

func (s *Server) groupView(g *core.Group, maxMembers int) groupView {
	v := groupView{
		ID:         g.ID,
		Kind:       g.Kind,
		Count:      len(g.Files),
		TotalSize:  g.TotalBytes,
		Wasted:     g.WastedBytes,
		Similarity: g.Similarity,
	}
	v.Keeper = memberViewOf(g.Keeper, g.Keeper)
	for _, f := range g.Files {
		v.Candidates = append(v.Candidates, candidateView{
			Path:   f.Path,
			Size:   f.Size,
			Keeper: g.Keeper != nil && f.Path == g.Keeper.Path,
		})
	}
	if maxMembers <= 0 {
		maxMembers = len(g.Files)
	}
	v.Members = make([]memberView, 0, maxMembers)
	for _, f := range g.Files {
		if len(v.Members) >= maxMembers {
			break
		}
		m := memberViewOf(f, g.Keeper)
		v.Members = append(v.Members, *m)
	}
	return v
}

func memberViewOf(f *core.File, keeper *core.File) *memberView {
	if f == nil {
		return nil
	}
	m := &memberView{
		Path:    f.Path,
		Name:    f.Name,
		Dir:     f.Dir,
		Size:    f.Size,
		ModTime: f.ModTime,
		Width:   f.Width,
		Height:  f.Height,
		Format:  f.Format,
		Pixels:  f.Pixels(),
		Same:    keeper != nil && f.SHA256 != "" && f.SHA256 == keeper.SHA256,
		Keeper:  keeper != nil && f.Path == keeper.Path,
	}
	if m.Same || m.Keeper {
		m.Similarity = 100
	} else if keeper != nil {
		m.Similarity = core.SimilarityPercent(f, keeper)
	}
	if f.Error == "" {
		m.Thumb = thumbURL(f)
	}
	return m
}

func thumbURL(f *core.File) string {
	v := url.Values{}
	v.Set("path", f.Path)
	v.Set("v", strconv.FormatInt(f.ModTime, 10))
	return "/api/thumb?" + v.Encode()
}

func groupMatches(g *core.Group, search string) bool {
	if g.Keeper != nil && (strings.Contains(strings.ToLower(g.Keeper.Name), search) || strings.Contains(strings.ToLower(g.Keeper.Path), search)) {
		return true
	}
	for _, f := range g.Files {
		if strings.Contains(strings.ToLower(f.Name), search) {
			return true
		}
	}
	return false
}

func groupName(g *core.Group) string {
	if g.Keeper != nil {
		return strings.ToLower(g.Keeper.Name)
	}
	if len(g.Files) > 0 {
		return strings.ToLower(g.Files[0].Name)
	}
	return ""
}

func countsFromResult(r *core.Result) counts {
	if r == nil {
		return counts{}
	}
	return counts{
		ExactGroups:   r.Stats.ExactGroups,
		SimilarGroups: r.Stats.SimilarGroups,
		ExactFiles:    r.Stats.ExactFiles,
		SimilarFiles:  r.Stats.SimilarFiles,
		ReclaimExact:  r.Stats.ReclaimableExact,
		ReclaimSim:    r.Stats.ReclaimableSimilar,
	}
}

func resultInfoFrom(r *core.Result) *resultInfo {
	if r == nil {
		return nil
	}
	info := &resultInfo{
		ID:         r.ID,
		Roots:      r.Roots,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
		Stats:      r.Stats,
		Options:    r.Options,
		Note:       r.Note,
		IssueCount: len(r.Issues),
	}
	if len(r.Issues) > 20 {
		info.Issues = r.Issues[:20]
	} else {
		info.Issues = r.Issues
	}
	return info
}

func statsOf(r *core.Result) core.Stats {
	if r == nil {
		return core.Stats{}
	}
	return r.Stats
}

func (s *Server) trashSummary() trashSummary {
	b, f, by := s.eng.TrashStats()
	return trashSummary{Batches: b, Files: f, Bytes: by}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if err == io.EOF {
			writeError(w, http.StatusBadRequest, "请求体为空")
			return false
		}
		writeError(w, http.StatusBadRequest, "请求格式错误: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}
