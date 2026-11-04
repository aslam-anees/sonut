package theme

import "image/color"

// DarkBlueTheme defines @code's dark blue color palette
type DarkBlueTheme struct {
	// Primary colors (dark blue gradient)
	Primary      color.Color
	PrimaryLight color.Color
	PrimaryDark  color.Color

	// Accent colors (complementary to dark blue)
	Accent      color.Color
	AccentLight color.Color

	// Semantic colors
	Success color.Color
	Warning color.Color
	Error   color.Color
	Info    color.Color

	// Neutrals
	Background    color.Color
	Surface       color.Color
	SurfaceAlt    color.Color
	Text          color.Color
	TextSecondary color.Color
	TextTertiary  color.Color
	Border        color.Color
	Divider       color.Color

	// Interactive
	Link      color.Color
	LinkHover color.Color
	Selection color.Color
	Disabled  color.Color

	// Branding
	Brand color.Color // @code brand color
}

// ColorRGB creates an RGB color
type ColorRGB struct {
	R, G, B uint8
}

func (c ColorRGB) RGBA() (uint32, uint32, uint32, uint32) {
	r := uint32(c.R)
	g := uint32(c.G)
	b := uint32(c.B)
	return r << 8, g << 8, b << 8, 0xFFFF
}

// NewDarkBlueTheme creates the default @code dark blue theme
func NewDarkBlueTheme() *DarkBlueTheme {
	return &DarkBlueTheme{
		// Dark blue gradient (primary)
		// Navy: #001F3F, Dark Blue: #0051A5, Medium Blue: #0084FF, Light Blue: #00D4FF
		Primary:      ColorRGB{0, 81, 165},  // #0051A5 (Medium Dark Blue)
		PrimaryLight: ColorRGB{0, 132, 255}, // #0084FF (Bright Blue)
		PrimaryDark:  ColorRGB{0, 31, 63},   // #001F3F (Navy)

		// Cyan/Teal accent (complements dark blue)
		Accent:      ColorRGB{0, 212, 255},   // #00D4FF (Cyan)
		AccentLight: ColorRGB{100, 230, 255}, // Lighter cyan

		// Semantic colors
		Success: ColorRGB{16, 185, 129}, // #10B981 (Green)
		Warning: ColorRGB{245, 158, 11}, // #F59E0B (Amber)
		Error:   ColorRGB{239, 68, 68},  // #EF4444 (Red)
		Info:    ColorRGB{59, 130, 246}, // #3B82F6 (Blue)

		// Neutrals (dark background for terminal)
		Background:    ColorRGB{13, 13, 23},    // #0D0D17 (Almost black)
		Surface:       ColorRGB{25, 28, 45},    // #191C2D (Dark blue-gray)
		SurfaceAlt:    ColorRGB{35, 40, 60},    // #232C3C (Lighter surface)
		Text:          ColorRGB{230, 237, 243}, // #E6EDF3 (Off-white)
		TextSecondary: ColorRGB{160, 174, 192}, // #A0AEC0 (Gray-blue)
		TextTertiary:  ColorRGB{100, 116, 139}, // #647483 (Darker gray)
		Border:        ColorRGB{45, 60, 85},    // #2D3C55 (Blue-gray)
		Divider:       ColorRGB{30, 41, 59},    // #1E293B (Subtle)

		// Interactive
		Link:      ColorRGB{0, 212, 255}, // #00D4FF (Cyan)
		LinkHover: ColorRGB{0, 132, 255}, // #0084FF (Brighter)
		Selection: ColorRGB{0, 81, 165},  // #0051A5 (Primary)
		Disabled:  ColorRGB{75, 85, 99},  // #4B5563 (Muted)

		// Branding
		Brand: ColorRGB{0, 212, 255}, // #00D4FF (Bright Cyan)
	}
}

// TUIColorMap defines terminal color codes for common terminals
type TUIColorMap struct {
	// 256-color palette indices (for older terminals)
	Primary256       int // 24 (dark blue)
	Accent256        int // 51 (bright cyan)
	Success256       int // 42 (green)
	Warning256       int // 220 (orange)
	Error256         int // 196 (red)
	Text256          int // 15 (bright white)
	TextSecondary256 int // 8 (dark gray)

	// RGB values for true color (24-bit) terminals
	Theme *DarkBlueTheme
}

// NewTUIColorMap creates the terminal color map
func NewTUIColorMap() *TUIColorMap {
	return &TUIColorMap{
		Primary256:       24,  // Dark blue
		Accent256:        51,  // Bright cyan
		Success256:       42,  // Green
		Warning256:       220, // Orange
		Error256:         196, // Red
		Text256:          15,  // Bright white
		TextSecondary256: 8,   // Dark gray
		Theme:            NewDarkBlueTheme(),
	}
}

// SonutGradient returns a gradient string for ASCII art branding
func SonutGradient() string {
	return `
     🌙 @code 🌙
  Local AI. Always.
`
}

// HeaderStyle returns the header styling
func HeaderStyle() string {
	return "\033[38;5;51m" // Bright cyan (256-color)
}

// PrimaryStyle returns primary text styling
func PrimaryStyle() string {
	return "\033[38;5;24m" // Dark blue (256-color)
}

// SuccessStyle returns success text styling
func SuccessStyle() string {
	return "\033[38;5;42m" // Green (256-color)
}

// WarningStyle returns warning text styling
func WarningStyle() string {
	return "\033[38;5;220m" // Orange (256-color)
}

// ErrorStyle returns error text styling
func ErrorStyle() string {
	return "\033[38;5;196m" // Red (256-color)
}

// SecondaryStyle returns secondary text styling
func SecondaryStyle() string {
	return "\033[38;5;8m" // Dark gray (256-color)
}

// Reset resets all styling
func Reset() string {
	return "\033[0m"
}

// Styled applies a style to text
func Styled(text string, style string) string {
	return style + text + Reset()
}

// Colors available for use throughout @code
var (
	// Dark blue palette (primary)
	Navy       = "#001F3F"
	DarkBlue   = "#0051A5"
	MedBlue    = "#0084FF"
	BrightBlue = "#00D4FF"

	// Semantic colors
	Success = "#10B981"
	Warning = "#F59E0B"
	Error   = "#EF4444"
	Info    = "#3B82F6"

	// Text colors
	TextPrimary   = "#E6EDF3"
	TextSecondary = "#A0AEC0"
	TextTertiary  = "#647483"

	// Backgrounds
	BgPrimary   = "#0D0D17"
	BgSecondary = "#191C2D"
	BgTertiary  = "#232C3C"
)

// GetColorForStatus returns the color for a given status
func GetColorForStatus(status string) string {
	switch status {
	case "success", "ok":
		return Success
	case "warning":
		return Warning
	case "error", "fail":
		return Error
	case "info", "pending":
		return Info
	default:
		return TextSecondary
	}
}
