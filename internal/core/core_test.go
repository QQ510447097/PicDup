package core

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	xdraw "golang.org/x/image/draw"
)

func testImage(seed int64, w, h int) *image.RGBA {
	rng := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c1 := color.RGBA{uint8(30 + seed*17), uint8(80 + seed*11), uint8(140 + seed*7), 255}
	c2 := color.RGBA{uint8(200 - seed*9), uint8(130 + seed*5), uint8(40 + seed*13), 255}
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h)
		for x := 0; x < w; x++ {
			s := float64(x) / float64(w)
			r := uint8(float64(c1.R)*(1-s) + float64(c2.R)*s)
			g := uint8(float64(c1.G)*(1-t) + float64(c2.G)*t)
			b := uint8(float64(c1.B)*t + float64(c2.B)*(1-s))
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	for i := 0; i < 14; i++ {
		cx := rng.Intn(w)
		cy := rng.Intn(h)
		r := 20 + rng.Intn(min(w, h)/4)
		col := color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255}
		for y := cy - r; y <= cy+r; y++ {
			for x := cx - r; x <= cx+r; x++ {
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				if (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r {
					img.SetRGBA(x, y, col)
				}
			}
		}
	}
	return img
}

func writeJPEG(t *testing.T, path string, img image.Image, quality int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatal(err)
	}
}

func resize(img image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, img.Bounds(), xdraw.Src, nil)
	return dst
}

func brighten(img image.Image, amount float64) *image.RGBA {
	b := img.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bb, a := img.At(x, y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				uint8(math.Min(255, float64(r>>8)*amount)),
				uint8(math.Min(255, float64(g>>8)*amount)),
				uint8(math.Min(255, float64(bb>>8)*amount)),
				uint8(a >> 8),
			})
		}
	}
	return dst
}

func crop(img image.Image) *image.RGBA {
	b := img.Bounds()
	dx, dy := b.Dx()/12, b.Dy()/12
	src := image.Rect(b.Min.X+dx, b.Min.Y+dy, b.Max.X-dx, b.Max.Y-dy)
	dst := image.NewRGBA(image.Rect(0, 0, src.Dx(), src.Dy()))
	xdraw.Copy(dst, dst.Bounds().Min, img, src, xdraw.Src, nil)
	return dst
}

func scanDir(t *testing.T, dir string) ([]*File, []*Group) {
	t.Helper()
	thumbs, err := NewThumbCache(filepath.Join(t.TempDir(), "thumbs"))
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultSettings().Options([]string{dir}).Options
	cands, _, err := collectFiles(context.Background(), []string{dir}, opts, nil, func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	files, issues := analyzeFiles(context.Background(), cands, thumbs, func(_, _ int, _ string) {})
	if len(issues) > 0 {
		t.Logf("issues: %+v", issues)
	}
	groups, _, _ := BuildGroups(context.Background(), files, opts, func(float64, string) {})
	return files, groups
}

func TestPHashVariants(t *testing.T) {
	dir := t.TempDir()
	base := testImage(3, 900, 600)
	writeJPEG(t, filepath.Join(dir, "base.jpg"), base, 92)
	writeJPEG(t, filepath.Join(dir, "resized.jpg"), resize(base, 450, 300), 88)
	writeJPEG(t, filepath.Join(dir, "recompressed.jpg"), base, 45)
	writeJPEG(t, filepath.Join(dir, "bright.jpg"), brighten(base, 1.12), 90)
	writeJPEG(t, filepath.Join(dir, "cropped.jpg"), crop(base), 90)
	writeJPEG(t, filepath.Join(dir, "other.jpg"), testImage(9, 900, 600), 90)

	files, groups := scanDir(t, dir)
	if len(files) != 6 {
		t.Fatalf("expected 6 files, got %d", len(files))
	}
	byName := map[string]*File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	baseFile := byName["base.jpg"]
	for _, name := range []string{"resized.jpg", "recompressed.jpg", "bright.jpg", "cropped.jpg"} {
		f := byName[name]
		if f == nil {
			t.Fatalf("missing %s", name)
		}
		d := pHashDistance(baseFile, f)
		t.Logf("%s: pHash distance=%d dHash=%d hist=%.3f detail=%.3f", name, d, popcount(baseFile.DHash^f.DHash), histogramIntersection(baseFile.Hist, f.Hist), f.Detail)
		if d > 12 {
			t.Errorf("%s should be similar to base, distance %d", name, d)
		}
	}
	if len(groups) == 0 {
		t.Fatalf("expected groups, got none")
	}
	for _, g := range groups {
		t.Logf("group %s: %d files sim=%.1f", g.Kind, len(g.Files), g.Similarity)
	}
	found := false
	for _, g := range groups {
		names := map[string]bool{}
		for _, f := range g.Files {
			names[f.Name] = true
		}
		if names["base.jpg"] && names["resized.jpg"] && names["recompressed.jpg"] {
			found = true
		}
		if names["other.jpg"] && len(g.Files) < 3 {
			t.Logf("other grouped with %v", names)
		}
	}
	if !found {
		t.Errorf("base/resized/recompressed were not grouped together")
	}
}

func TestExactDuplicates(t *testing.T) {
	dir := t.TempDir()
	base := testImage(11, 640, 480)
	writeJPEG(t, filepath.Join(dir, "a.jpg"), base, 90)
	data, err := os.ReadFile(filepath.Join(dir, "a.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.jpg", "sub/c.jpg"} {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeJPEG(t, filepath.Join(dir, "different.jpg"), testImage(21, 640, 480), 90)
	_, groups := scanDir(t, dir)
	exact := 0
	exactCount := 0
	for _, g := range groups {
		if g.Kind == "exact" {
			exact++
			exactCount = len(g.Files)
		}
	}
	if exact != 1 || exactCount != 3 {
		t.Fatalf("expected one exact group with 3 files, got %d groups, count %d", exact, exactCount)
	}
}

func TestTrashRestore(t *testing.T) {
	root := t.TempDir()
	trashDir := filepath.Join(root, "trash")
	srcDir := filepath.Join(root, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	src := filepath.Join(srcDir, "one.jpg")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tm, err := NewTrashManager(trashDir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tm.Move([]string{src})
	if err != nil {
		t.Fatal(err)
	}
	if res.Moved != 1 || res.BatchID == "" {
		t.Fatalf("unexpected cleanup result: %+v", res)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone")
	}
	rr, err := tm.Restore(res.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Restored != 1 {
		t.Fatalf("expected 1 restored, got %+v", rr)
	}
	data, err := os.ReadFile(src)
	if err != nil || string(data) != "hello" {
		t.Fatalf("restore content mismatch: %v %q", err, data)
	}
}

func popcount(v uint64) int {
	n := 0
	for v != 0 {
		v &= v - 1
		n++
	}
	return n
}

func TestNoFalsePositives(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 24; i++ {
		writeJPEG(t, filepath.Join(dir, "img"+string(rune('A'+i))+".jpg"), testImage(int64(i+1), 900, 600), 90)
	}
	_, groups := scanDir(t, dir)
	if len(groups) != 0 {
		for _, g := range groups {
			t.Logf("unexpected %s group with %d files sim=%.1f", g.Kind, len(g.Files), g.Similarity)
			for _, f := range g.Files {
				t.Logf("  %s", f.Name)
			}
		}
		t.Fatalf("expected no groups for distinct images, got %d", len(groups))
	}
}

func TestKeeperModes(t *testing.T) {
	old := &File{Path: `C:\pics\a.jpg`, Name: "a.jpg", Size: 100, Width: 100, Height: 80, ModTime: 100}
	newer := &File{Path: `C:\pics\sub\b.jpg`, Name: "b.jpg", Size: 200, Width: 120, Height: 90, ModTime: 300}
	longest := &File{Path: `C:\pics\deep\long-name.jpg`, Name: "long-name.jpg", Size: 50, Width: 90, Height: 70, ModTime: 200}
	files := []*File{old, newer, longest}

	cases := []struct {
		mode string
		want *File
	}{
		{"resolution", newer},
		{"largest", newer},
		{"newest", newer},
		{"oldest", old},
		{"shortest", old},
	}
	for _, tc := range cases {
		if got := chooseKeeper(files, "similar", tc.mode); got != tc.want {
			t.Errorf("chooseKeeper(%q) = %v, want %v", tc.mode, got.Path, tc.want.Path)
		}
	}
	if got := chooseKeeper([]*File{old, longest}, "exact", "newest"); got != longest {
		t.Errorf("newest exact keeper = %s", got.Path)
	}
	if got := normalizeKeeperMode(""); got != "resolution" {
		t.Errorf("normalizeKeeperMode(empty) = %q", got)
	}
}
