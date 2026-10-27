package core

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"sort"
	"strings"

	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// decodeImage decodes an image and its EXIF orientation.
func decodeImage(path string) (image.Image, string, int, error) {
	orientation := 1
	ext := strings.ToLower(fileExt(path))
	if ext == ".jpg" || ext == ".jpeg" {
		orientation = readJPEGOrientation(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", 1, err
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, format, orientation, err
	}
	return img, format, orientation, nil
}

func fileExt(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return ""
	}
	return path[i:]
}

// readJPEGOrientation parses the EXIF orientation tag from a JPEG file.
// It returns 1 when no tag is present or the file is malformed.
func readJPEGOrientation(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 1
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	n, _ := f.Read(buf)
	buf = buf[:n]
	if len(buf) < 4 || buf[0] != 0xFF || buf[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(buf) {
		if buf[i] != 0xFF {
			return 1
		}
		marker := buf[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(buf[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(buf) {
			return 1
		}
		if marker == 0xE1 && segLen >= 8 && string(buf[i+4:i+8]) == "Exif" {
			start := i + 10
			end := i + 2 + segLen
			if start < end {
				return parseTIFFOrientation(buf[start:end])
			}
		}
		i += 2 + segLen
	}
	return 1
}

func parseTIFFOrientation(data []byte) int {
	if len(data) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(data[2:4]) != 42 {
		return 1
	}
	off := int(order.Uint32(data[4:8]))
	if off < 8 || off+2 > len(data) {
		return 1
	}
	count := int(order.Uint16(data[off : off+2]))
	pos := off + 2
	for j := 0; j < count; j++ {
		if pos+12 > len(data) {
			return 1
		}
		tag := order.Uint16(data[pos : pos+2])
		typ := order.Uint16(data[pos+2 : pos+4])
		cnt := int(order.Uint32(data[pos+4 : pos+8]))
		if tag == 0x0112 && cnt >= 1 && typ == 3 {
			v := int(order.Uint16(data[pos+8 : pos+10]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
		pos += 12
	}
	return 1
}

// squareGray renders an image into a size x size grayscale grid, applying EXIF
// orientation. Values are 0..255.
func squareGray(img image.Image, size, orientation int) []float32 {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	rotated := image.NewNRGBA(image.Rect(0, 0, size, size))
	applyOrientationNRGBA(rotated, dst, orientation)
	return nrgbaToGray(rotated)
}

// squareGrayCrop hashes the central portion of an image, which makes the
// fingerprint resilient to modest crops.
func squareGrayCrop(img image.Image, size, orientation int, keep float64) []float32 {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	cw := int(float64(w) * keep)
	ch := int(float64(h) * keep)
	if cw < 8 {
		cw = w
	}
	if ch < 8 {
		ch = h
	}
	x0 := b.Min.X + (w-cw)/2
	y0 := b.Min.Y + (h-ch)/2
	sub := img
	if r, ok := img.(interface {
		SubImage(r image.Rectangle) image.Image
	}); ok {
		sub = r.SubImage(image.Rect(x0, y0, x0+cw, y0+ch))
	}
	return squareGray(sub, size, orientation)
}

// squareNRGBA renders an image into a size x size NRGBA grid with orientation.
func squareNRGBA(img image.Image, size, orientation int) *image.NRGBA {
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Src, nil)
	if orientation == 1 {
		return dst
	}
	rotated := image.NewNRGBA(image.Rect(0, 0, size, size))
	applyOrientationNRGBA(rotated, dst, orientation)
	return rotated
}

func nrgbaToGray(src *image.NRGBA) []float32 {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	out := make([]float32, w*h)
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride:]
		for x := 0; x < w; x++ {
			p := row[x*4:]
			// Composite over white so transparent pixels are not treated as black.
			a := float32(p[3]) / 255
			r := float32(p[0])*a + 255*(1-a)
			g := float32(p[1])*a + 255*(1-a)
			b := float32(p[2])*a + 255*(1-a)
			out[y*w+x] = 0.299*r + 0.587*g + 0.114*b
		}
	}
	return out
}

// applyOrientationNRGBA writes src into dst after applying an EXIF orientation.
// dst must have the correct (possibly swapped) dimensions for orientations 5-8.
func applyOrientationNRGBA(dst, src *image.NRGBA, orientation int) {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := dst.Bounds().Dx(), dst.Bounds().Dy()
	if orientation < 1 || orientation > 8 {
		orientation = 1
	}
	for y := 0; y < dh; y++ {
		drow := dst.Pix[y*dst.Stride:]
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch orientation {
			case 2:
				sx, sy = sw-1-x, y
			case 3:
				sx, sy = sw-1-x, sh-1-y
			case 4:
				sx, sy = x, sh-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, sh-1-x
			case 7:
				sx, sy = sw-1-y, sh-1-x
			case 8:
				sx, sy = sw-1-y, x
			default:
				sx, sy = x, y
			}
			if sx < 0 || sx >= sw || sy < 0 || sy >= sh {
				continue
			}
			sp := src.Pix[sy*src.Stride+sx*4:]
			dp := drow[x*4:]
			dp[0], dp[1], dp[2], dp[3] = sp[0], sp[1], sp[2], sp[3]
		}
	}
}

// resampleGray box-filters a grayscale grid.
func resampleGray(src []float32, sw, sh, dw, dh int) []float32 {
	out := make([]float32, dw*dh)
	for dy := 0; dy < dh; dy++ {
		sy0 := dy * sh / dh
		sy1 := (dy + 1) * sh / dh
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			sx0 := dx * sw / dw
			sx1 := (dx + 1) * sw / dw
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var sum float32
			var n int
			for y := sy0; y < sy1 && y < sh; y++ {
				for x := sx0; x < sx1 && x < sw; x++ {
					sum += src[y*sw+x]
					n++
				}
			}
			if n > 0 {
				out[dy*dw+dx] = sum / float32(n)
			}
		}
	}
	return out
}

var dctCos [32][32]float64

func init() {
	for u := 0; u < 32; u++ {
		for x := 0; x < 32; x++ {
			a := math.Sqrt(2.0 / 32.0)
			if u == 0 {
				a = math.Sqrt(1.0 / 32.0)
			}
			dctCos[u][x] = a * math.Cos(float64(2*x+1)*float64(u)*math.Pi/64.0)
		}
	}
}

// pHash computes a 64-bit DCT perceptual hash from a 32x32 grayscale grid.
func pHash(gray []float32) uint64 {
	if len(gray) < 32*32 {
		return 0
	}
	var tmp [32][32]float64
	for u := 0; u < 32; u++ {
		for y := 0; y < 32; y++ {
			var sum float64
			row := y * 32
			for x := 0; x < 32; x++ {
				sum += dctCos[u][x] * float64(gray[row+x])
			}
			tmp[u][y] = sum
		}
	}
	var coeff [8][8]float64
	vals := make([]float64, 0, 63)
	for v := 0; v < 8; v++ {
		for u := 0; u < 8; u++ {
			var sum float64
			for y := 0; y < 32; y++ {
				sum += dctCos[v][y] * tmp[u][y]
			}
			coeff[u][v] = sum
			if u != 0 || v != 0 {
				vals = append(vals, sum)
			}
		}
	}
	sort.Float64s(vals)
	median := vals[len(vals)/2]
	var hash uint64
	bit := 0
	for v := 0; v < 8; v++ {
		for u := 0; u < 8; u++ {
			if u == 0 && v == 0 {
				continue
			}
			if coeff[u][v] > median {
				hash |= 1 << uint(bit)
			}
			bit++
		}
	}
	return hash
}

// dHash computes a 64-bit difference hash from a 32x32 grayscale grid.
func dHash(gray []float32) uint64 {
	g := resampleGray(gray, 32, 32, 9, 8)
	var hash uint64
	bit := 0
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			if g[y*9+x] > g[y*9+x+1] {
				hash |= 1 << uint(bit)
			}
			bit++
		}
	}
	return hash
}

// colorHistogram returns a normalised 4x4x4 RGB histogram (64 bins).
func colorHistogram(img image.Image, orientation int) [64]float32 {
	var hist [64]float32
	src := squareNRGBA(img, 64, orientation)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	var total float32
	for y := 0; y < h; y++ {
		row := src.Pix[y*src.Stride:]
		for x := 0; x < w; x++ {
			p := row[x*4:]
			a := float32(p[3]) / 255
			r := (float32(p[0])*a + 255*(1-a)) / 256
			g := (float32(p[1])*a + 255*(1-a)) / 256
			b := (float32(p[2])*a + 255*(1-a)) / 256
			ri, gi, bi := int(r*4), int(g*4), int(b*4)
			if ri > 3 {
				ri = 3
			}
			if gi > 3 {
				gi = 3
			}
			if bi > 3 {
				bi = 3
			}
			hist[ri*16+gi*4+bi]++
			total++
		}
	}
	if total > 0 {
		for i := range hist {
			hist[i] /= total
		}
	}
	return hist
}

func histogramIntersection(a, b [64]float32) float32 {
	var sum float32
	for i := 0; i < 64; i++ {
		if a[i] < b[i] {
			sum += a[i]
		} else {
			sum += b[i]
		}
	}
	return sum
}

func grayStdDev(gray []float32) float32 {
	var sum, sumSq float64
	for _, v := range gray {
		sum += float64(v)
		sumSq += float64(v) * float64(v)
	}
	n := float64(len(gray))
	mean := sum / n
	variance := sumSq/n - mean*mean
	if variance < 0 {
		variance = 0
	}
	return float32(math.Sqrt(variance) / 255.0)
}

// imageInfo bundles the perceptual fingerprints of one image.
type imageInfo struct {
	PHash      uint64
	PHashCrop  uint64
	PHashOuter uint64
	DHash      uint64
	Hist       [64]float32
	Detail     float32
	Width      int
	Height     int
	Format     string
}

// fingerprintImage computes perceptual fingerprints from an already decoded image.
func fingerprintImage(img image.Image, format string, orientation int) imageInfo {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	info := imageInfo{Width: w, Height: h, Format: format}
	if orientation >= 5 && orientation <= 8 {
		info.Width, info.Height = h, w
	}
	gray := squareGray(img, 32, orientation)
	info.PHash = pHash(gray)
	info.PHashCrop = pHash(squareGrayCrop(img, 32, orientation, 0.78))
	info.PHashOuter = pHash(squareGrayCrop(img, 32, orientation, 0.90))
	info.DHash = dHash(gray)
	info.Hist = colorHistogram(img, orientation)
	info.Detail = grayStdDev(gray)
	return info
}

// analyseImage decodes a file and computes its perceptual fingerprints.
func analyseImage(path string) (imageInfo, image.Image, int, error) {
	img, format, orientation, err := decodeImage(path)
	if err != nil {
		return imageInfo{}, nil, orientation, err
	}
	return fingerprintImage(img, format, orientation), img, orientation, nil
}

// renderThumb scales an image to fit within maxDimension and applies EXIF
// orientation. The result is composited over a dark background.
func renderThumb(img image.Image, orientation, maxDimension int) *image.NRGBA {
	if maxDimension <= 0 {
		maxDimension = 480
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	dispW, dispH := w, h
	if orientation >= 5 && orientation <= 8 {
		dispW, dispH = h, w
	}
	scale := math.Min(float64(maxDimension)/float64(dispW), float64(maxDimension)/float64(dispH))
	if scale > 1 {
		scale = 1
	}
	tw := int(math.Round(float64(dispW) * scale))
	th := int(math.Round(float64(dispH) * scale))
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	rawW, rawH := tw, th
	if orientation >= 5 && orientation <= 8 {
		rawW, rawH = th, tw
	}
	raw := image.NewNRGBA(image.Rect(0, 0, rawW, rawH))
	xdraw.CatmullRom.Scale(raw, raw.Bounds(), img, b, xdraw.Src, nil)
	final := image.NewNRGBA(image.Rect(0, 0, tw, th))
	applyOrientationNRGBA(final, raw, orientation)
	// Composite transparency over the app's background colour.
	bg := color.NRGBA{R: 13, G: 21, B: 36, A: 255}
	for i := 0; i < len(final.Pix); i += 4 {
		a := uint32(final.Pix[i+3])
		if a == 255 {
			continue
		}
		inv := 255 - a
		final.Pix[i] = uint8((uint32(final.Pix[i])*a + uint32(bg.R)*inv) / 255)
		final.Pix[i+1] = uint8((uint32(final.Pix[i+1])*a + uint32(bg.G)*inv) / 255)
		final.Pix[i+2] = uint8((uint32(final.Pix[i+2])*a + uint32(bg.B)*inv) / 255)
		final.Pix[i+3] = 255
	}
	return final
}

// encodeJPEG encodes an image as JPEG.
func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf byteBuffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.b, nil
}

type byteBuffer struct{ b []byte }

func (b *byteBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}
