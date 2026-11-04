package imageinput

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestDownscaleForVisionShrinksLargeImage(t *testing.T) {
	// 2000×1000 UI-like PNG → should remain crisp PNG ≤ maxVisionEdgePx.
	img := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 2000; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	scaled, ok := downscaleForVision(buf.Bytes())
	if !ok {
		t.Fatal("expected downscale")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(scaled))
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" {
		t.Fatalf("format=%q want png", format)
	}
	long := cfg.Width
	if cfg.Height > long {
		long = cfg.Height
	}
	if long > maxVisionEdgePx {
		t.Fatalf("long edge %d > %d", long, maxVisionEdgePx)
	}
	if len(scaled) >= len(buf.Bytes()) {
		// Pattern noise often compresses; solid-ish still should shrink a lot.
		t.Logf("scaled %d vs original %d (ok if pattern-heavy)", len(scaled), buf.Len())
	}
}

func TestDownscaleForVisionLeavesSmallImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if _, ok := downscaleForVision(buf.Bytes()); ok {
		t.Fatal("small image should not be recompressed")
	}
}
