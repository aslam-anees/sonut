// Package imageinput is the single shared loader for local image files used by
// every input surface (CLI exec --image, TUI /image). It reads a file, sniffs +
// normalizes its media type against the allow-list, enforces the per-image size
// cap, and returns a raw-bytes ImageBlock. Keeping it here means the CLI and TUI
// never duplicate the read/sniff/normalize/cap logic.
package imageinput

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

// MaxImageBytes is the per-image decoded-size cap (10 MiB). Bytes above this are
// rejected at every input boundary so an unbounded request body never reaches a
// provider.
const MaxImageBytes = 10 << 20

// maxVisionEdgePx is the longest side kept after load. Local hybrid models pay
// vision encode + prefill on the first request (and used to re-encode every
// turn — fixed). Retina 2–4k screenshots on CPU mmproj were multi-minute.
// 512 keeps UI layouts legible for coding screenshots while materially cutting
// image-encoder and vision-prefill cost. First-turn vision prefill is the only
// place images ride after follow-up strip, so this is the highest-impact local
// latency knob for screenshot requests.
const maxVisionEdgePx = 512

// LoadFile reads the image at path (resolved against workspaceRoot when
// relative), validates its type and size, and returns a raw-bytes ImageBlock.
// Errors are plain (callers wrap them into surface-specific usage/notice text).
func LoadFile(path string, workspaceRoot string) (sonutruntime.ImageBlock, error) {
	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(workspaceRoot, resolved)
	}

	// Reject oversized files via Stat BEFORE reading them into memory, so a huge
	// file never allocates a multi-gigabyte buffer just to be discarded by the
	// post-read cap. A missing file surfaces the same "not found" notice as a
	// failed read.
	info, err := os.Stat(resolved)
	if err != nil {
		return sonutruntime.ImageBlock{}, fmt.Errorf("image file not found: %s", path)
	}
	// Reject non-regular files (directories, FIFOs, devices) up front. os.Stat
	// follows symlinks, so a symlink to a regular file still passes. This guards
	// against os.Open blocking forever on a writerless FIFO (an --image/ /image
	// path pointing at a named pipe would otherwise hang the process/UI).
	if !info.Mode().IsRegular() {
		return sonutruntime.ImageBlock{}, fmt.Errorf("image file must be a regular file: %s", path)
	}
	if info.Size() > MaxImageBytes {
		return sonutruntime.ImageBlock{}, fmt.Errorf("image %s is larger than the 10 MiB limit", path)
	}

	// Bounded read: the os.Stat above is only a fast-path hint (a non-regular
	// file reports a misleading size, and the file can grow between Stat and the
	// read). A LimitReader of MaxImageBytes+1 is the real bound — at most one byte
	// past the cap is ever buffered, so an oversized or unbounded source (e.g. a
	// FIFO) can never allocate a multi-gigabyte buffer just to be discarded.
	file, err := os.Open(resolved)
	if err != nil {
		// Keep the real cause: a permission or I/O failure reported as "not
		// found" sends users hunting for a file that exists.
		return sonutruntime.ImageBlock{}, fmt.Errorf("cannot open image %s: %w", path, err)
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxImageBytes+1))
	if err != nil {
		return sonutruntime.ImageBlock{}, fmt.Errorf("cannot read image %s: %w", path, err)
	}

	// The LimitReader yields at most MaxImageBytes+1 bytes; more than the cap means
	// the source was oversized (caught here regardless of any stat/read race).
	if len(data) > MaxImageBytes {
		return sonutruntime.ImageBlock{}, fmt.Errorf("image %s is larger than the 10 MiB limit", path)
	}

	sniffLen := len(data)
	if sniffLen > 512 {
		sniffLen = 512
	}
	mediaType := sonutruntime.NormalizeImageMediaType(http.DetectContentType(data[:sniffLen]))
	if mediaType == "" {
		return sonutruntime.ImageBlock{}, fmt.Errorf("unsupported image type for %s (allowed: png, jpeg, gif, webp)", path)
	}

	// Downscale large images before they reach the model. UI screenshots stay
	// lossless PNG so small labels, borders, and spacing survive normalization;
	// photographs stay JPEG. This follows Grok Build's format-aware re-encode
	// strategy while retaining @code's conservative 512px vision-token budget.
	if scaled, ok := downscaleForVision(data); ok {
		return sonutruntime.ImageBlock{MediaType: imageMediaType(scaled), Data: scaled}, nil
	}
	return sonutruntime.ImageBlock{MediaType: mediaType, Data: data}, nil
}

// downscaleForVision re-encodes img with longest edge ≤ maxVisionEdgePx.
// PNG/GIF inputs use lossless PNG (typical UI screenshots); JPEG inputs stay
// JPEG. Bilinear resampling avoids the jagged text and one-pixel borders caused
// by the old nearest-neighbor path.
// Returns ok=false when the image is already small enough or cannot be decoded.
func downscaleForVision(data []byte) ([]byte, bool) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	w, h := cfg.Width, cfg.Height
	if w <= 0 || h <= 0 {
		return nil, false
	}
	long := w
	if h > long {
		long = h
	}
	if long <= maxVisionEdgePx {
		// Still recompress huge-but-small-dimension screenshots (rare) only when
		// the payload itself is heavy; otherwise leave bytes alone.
		if len(data) <= 256*1024 {
			return nil, false
		}
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	nw, nh := w, h
	if long > maxVisionEdgePx {
		scale := float64(maxVisionEdgePx) / float64(long)
		nw = int(float64(w) * scale)
		nh = int(float64(h) * scale)
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// Bilinear interpolation, implemented locally to keep the input path free of
	// another runtime dependency. Work is bounded by maxVisionEdgePx².
	for y := 0; y < nh; y++ {
		sy := (float64(y)+0.5)*float64(h)/float64(nh) - 0.5
		for x := 0; x < nw; x++ {
			sx := (float64(x)+0.5)*float64(w)/float64(nw) - 0.5
			dst.Set(x, y, bilinearAt(src, sx, sy))
		}
	}
	var buf bytes.Buffer
	if format == "png" || format == "gif" {
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := encoder.Encode(&buf, dst); err != nil {
			return nil, false
		}
	} else {
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
			return nil, false
		}
	}
	// Prefer the original when recompress somehow got larger.
	if buf.Len() >= len(data) && long <= maxVisionEdgePx {
		return nil, false
	}
	return buf.Bytes(), true
}

// DownscaleBytes is the shared entry for clipboard / PDF-page paths that already
// hold raw image bytes (not a file). Same rules as LoadFile's post-read step.
func DownscaleBytes(data []byte, mediaType string) (sonutruntime.ImageBlock, bool) {
	if scaled, ok := downscaleForVision(data); ok {
		return sonutruntime.ImageBlock{MediaType: imageMediaType(scaled), Data: scaled}, true
	}
	mt := mediaType
	if mt == "" {
		sniffLen := len(data)
		if sniffLen > 512 {
			sniffLen = 512
		}
		mt = sonutruntime.NormalizeImageMediaType(http.DetectContentType(data[:sniffLen]))
	}
	if mt == "" {
		return sonutruntime.ImageBlock{}, false
	}
	return sonutruntime.ImageBlock{MediaType: mt, Data: data}, true
}

func imageMediaType(data []byte) string {
	sniffLen := len(data)
	if sniffLen > 512 {
		sniffLen = 512
	}
	if sniffLen == 0 {
		return "image/jpeg"
	}
	if mediaType := sonutruntime.NormalizeImageMediaType(http.DetectContentType(data[:sniffLen])); mediaType != "" {
		return mediaType
	}
	return "image/jpeg"
}

func bilinearAt(src image.Image, x, y float64) color.RGBA {
	bounds := src.Bounds()
	x0 := int(x)
	y0 := int(y)
	if x < 0 {
		x0 = bounds.Min.X
	}
	if y < 0 {
		y0 = bounds.Min.Y
	}
	x1 := x0 + 1
	y1 := y0 + 1
	if x0 < bounds.Min.X {
		x0 = bounds.Min.X
	}
	if y0 < bounds.Min.Y {
		y0 = bounds.Min.Y
	}
	if x1 >= bounds.Max.X {
		x1 = bounds.Max.X - 1
	}
	if y1 >= bounds.Max.Y {
		y1 = bounds.Max.Y - 1
	}
	fx := x - float64(int(x))
	fy := y - float64(int(y))
	if fx < 0 {
		fx = 0
	}
	if fy < 0 {
		fy = 0
	}
	c00 := color.RGBAModel.Convert(src.At(x0, y0)).(color.RGBA)
	c10 := color.RGBAModel.Convert(src.At(x1, y0)).(color.RGBA)
	c01 := color.RGBAModel.Convert(src.At(x0, y1)).(color.RGBA)
	c11 := color.RGBAModel.Convert(src.At(x1, y1)).(color.RGBA)
	blend := func(a, b, c, d uint8) uint8 {
		top := float64(a)*(1-fx) + float64(b)*fx
		bottom := float64(c)*(1-fx) + float64(d)*fx
		return uint8(top*(1-fy) + bottom*fy + 0.5)
	}
	return color.RGBA{
		R: blend(c00.R, c10.R, c01.R, c11.R),
		G: blend(c00.G, c10.G, c01.G, c11.G),
		B: blend(c00.B, c10.B, c01.B, c11.B),
		A: blend(c00.A, c10.A, c01.A, c11.A),
	}
}
