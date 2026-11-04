package tools

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
)

// VisionTool handles image analysis and browser screenshots
type VisionTool struct {
	maxImageSize int64 // max size in bytes (e.g., 5MB)
}

// NewVisionTool creates a new vision tool
func NewVisionTool() *VisionTool {
	return &VisionTool{
		maxImageSize: 5 * 1024 * 1024, // 5MB default
	}
}

// ImageInput represents an image for analysis
type ImageInput struct {
	Path        string // File path
	URL         string // Remote URL
	Base64      string // Inline base64
	Format      string // "png", "jpeg"
	Description string // User-provided context
	IsClipboard bool   // From clipboard
}

// LoadImageFromPath loads an image file
func (vt *VisionTool) LoadImageFromPath(path string) (*ImageInput, error) {
	// Verify file exists
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("image file not found: %w", err)
	}

	// Check size
	if info.Size() > vt.maxImageSize {
		return nil, fmt.Errorf("image too large: %d bytes (max %d)", info.Size(), vt.maxImageSize)
	}

	// Read file
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read image: %w", err)
	}

	// Detect format
	format := detectImageFormat(data)
	if format == "" {
		return nil, fmt.Errorf("unsupported image format")
	}

	// Encode as base64
	b64 := base64.StdEncoding.EncodeToString(data)

	return &ImageInput{
		Path:   path,
		Base64: b64,
		Format: format,
	}, nil
}

// LoadImageFromURL fetches and encodes a remote image
func (vt *VisionTool) LoadImageFromURL(url string) (*ImageInput, error) {
	// Would implement HTTP fetch here
	// For MVP, return error (requires HTTP client)
	return nil, fmt.Errorf("remote image loading not yet implemented")
}

// LoadImageFromClipboard loads image from system clipboard
func (vt *VisionTool) LoadImageFromClipboard() (*ImageInput, error) {
	// Would use OS-specific clipboard API
	// For MVP, return error (requires platform-specific code)
	return nil, fmt.Errorf("clipboard image loading not yet implemented")
}

// AnalyzeImage generates a description of the image
func (vt *VisionTool) AnalyzeImage(img *ImageInput) (string, error) {
	if img == nil {
		return "", fmt.Errorf("no image provided")
	}

	// Placeholder for actual vision analysis
	// In production, this would call the LLM's vision capabilities
	description := fmt.Sprintf(
		"Image Analysis:\n- Format: %s\n- Size: %d bytes\n- Source: %s\n- Context: %s\n\n(Actual vision analysis handled by LLM model)",
		img.Format,
		len([]byte(img.Base64)),
		img.Path,
		img.Description,
	)

	return description, nil
}

// TakeScreenshot captures browser or desktop screenshot
func (vt *VisionTool) TakeScreenshot(target string) (*ImageInput, error) {
	// Placeholder for screenshot functionality
	// Would integrate with browser automation or scrot/gnome-screenshot

	if target == "browser" {
		return nil, fmt.Errorf("browser screenshot requires browser tool integration")
	}

	if target == "desktop" {
		return nil, fmt.Errorf("desktop screenshot not yet implemented (requires platform-specific tools)")
	}

	return nil, fmt.Errorf("unknown screenshot target: %s", target)
}

// CompressImage reduces image size while maintaining quality
func (vt *VisionTool) CompressImage(img *ImageInput) (*ImageInput, error) {
	if img.Format != "jpeg" && img.Format != "png" {
		return img, nil // Already compressed or unsupported
	}

	// Decode base64
	data, err := base64.StdEncoding.DecodeString(img.Base64)
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	// Decode image
	imgData, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image data: %w", err)
	}

	// Compress to JPEG with quality 75
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, imgData, &jpeg.Options{Quality: 75}); err != nil {
		return nil, fmt.Errorf("failed to compress image: %w", err)
	}

	compressedB64 := base64.StdEncoding.EncodeToString(buf.Bytes())

	return &ImageInput{
		Base64: compressedB64,
		Format: "jpeg",
		Path:   img.Path,
	}, nil
}

// SaveImageToFile writes image to disk
func (vt *VisionTool) SaveImageToFile(img *ImageInput, outputPath string) error {
	if img == nil {
		return fmt.Errorf("no image to save")
	}

	// Decode base64
	data, err := base64.StdEncoding.DecodeString(img.Base64)
	if err != nil {
		return fmt.Errorf("failed to decode image: %w", err)
	}

	// Create output directory if needed
	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Write file
	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write image file: %w", err)
	}

	return nil
}

// Helper: detect image format from magic bytes
func detectImageFormat(data []byte) string {
	if len(data) < 4 {
		return ""
	}

	// PNG: 89 50 4E 47
	if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
		return "png"
	}

	// JPEG: FF D8 FF
	if data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "jpeg"
	}

	// GIF: 47 49 46
	if data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46 {
		return "gif"
	}

	// WebP: RIFF ... WEBP
	if len(data) > 12 && data[0] == 0x52 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x46 {
		if data[8] == 0x57 && data[9] == 0x45 && data[10] == 0x42 && data[11] == 0x50 {
			return "webp"
		}
	}

	return ""
}

// BrowserVisionHelper integrates with browser tool for screenshots
type BrowserVisionHelper struct {
	visionTool *VisionTool
}

// NewBrowserVisionHelper creates a helper for browser + vision integration
func NewBrowserVisionHelper() *BrowserVisionHelper {
	return &BrowserVisionHelper{
		visionTool: NewVisionTool(),
	}
}

// ScreenshotAndAnalyze takes a screenshot and analyzes it
func (bvh *BrowserVisionHelper) ScreenshotAndAnalyze(target string, context string) (string, error) {
	// Take screenshot
	screenshot, err := bvh.visionTool.TakeScreenshot(target)
	if err != nil {
		return "", fmt.Errorf("screenshot failed: %w", err)
	}

	screenshot.Description = context

	// Analyze
	analysis, err := bvh.visionTool.AnalyzeImage(screenshot)
	if err != nil {
		return "", fmt.Errorf("analysis failed: %w", err)
	}

	return analysis, nil
}

// ImageCommand represents /image command
func ImageCommand(imagePath string, context string) (string, error) {
	vt := NewVisionTool()

	img, err := vt.LoadImageFromPath(imagePath)
	if err != nil {
		return "", err
	}

	img.Description = context

	analysis, err := vt.AnalyzeImage(img)
	if err != nil {
		return "", err
	}

	return analysis, nil
}
