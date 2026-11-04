// ripple.go adds a slow cosine "breathing" colour wave for the working status
// line: each character cycles through a dim→lime ramp blended from the brand
// accent, and the wave travels across the text one character per phase tick. The
// phase is sourced from the shared spinner clock (m.spinnerPhase, advanced by the
// existing spinner.TickMsg handler) so the ripple and the braille spinner animate
// in lock-step with no extra ticker.
package tui

import (
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

const activityLabelLifetime = 40

var activityScrambleGlyphs = []rune("<>/{}[]*+~")

// activityLabelView places a compact deterministic scramble field before the
// existing breathing label. spinnerPhase fully determines the frame, keeping
// renders reproducible and tests free of randomness.
func activityLabelView(label string, phase int, reducedMotion bool) string {
	if label == "" {
		return ""
	}
	if reducedMotion {
		return sonutTheme.accent.Render(label)
	}

	palette := ripplePalette()
	var field strings.Builder
	const fieldWidth = 3
	for index := range fieldWidth {
		// A tiny integer mix chooses a stable frame glyph without global rand.
		mixed := uint64(index+1)*0x9e3779b1 ^ uint64(phase+1)*0x85ebca6b
		glyph := activityScrambleGlyphs[mixed%uint64(len(activityScrambleGlyphs))]
		style := sonutTheme.faint
		if len(palette) > 1 {
			style = palette[(index+phase)%maxInt(1, len(palette)-1)]
		}
		field.WriteString(style.Render(string(glyph)))
	}
	wave := rippleText(label, palette, phase, maxInt(1, len([]rune(label))-1))
	return field.String() + " " + wave
}

// rippleLevel returns a palette index in [0, travel] computed from a cosine wave
// of wavelength waveLen at integer offset distance. The wave is the standard
// "breathing" half-wave-rectified cosine:
//
//	q     = distance mod waveLen
//	level = int( travel * (0.5 + 0.5*cos(2*Pi*q/waveLen)) )
//
// At distance == 0 the level is travel (brightest), at distance == waveLen/2 it
// is 0 (dimmest), and the cycle repeats every waveLen. A string rendered with
// rippleLevel(i+phase, ...) for i in [0, N) therefore shows a single cosine wave
// of colour that moves as phase advances.
//
// Edge cases:
//   - waveLen <= 0 returns 0 (no wave; flat dimmest entry)
//   - travel  <= 0 returns 0 (no palette; flat dimmest entry)
//   - distance < 0 mod-normalises to a non-negative value
//
// The function is intentionally pure: three ints in, an int out, depending only
// on the Go standard library math package — no model state, no rendering.
func rippleLevel(distance, travel, waveLen int) int {
	if travel <= 0 || waveLen <= 0 {
		return 0
	}
	// Normalise distance to [0, waveLen) so negative phases still behave: Go's %
	// can return a negative for negative inputs.
	q := distance % waveLen
	if q < 0 {
		q += waveLen
	}
	ratio := 0.5 + 0.5*math.Cos(2*math.Pi*float64(q)/float64(waveLen))
	return int(float64(travel) * ratio)
}

// rippleText applies one palette style per rune of text, using rippleLevel with
// index = i+phase so the wave travels across the string one character per phase
// tick. The palette holds curated theme styles (cold-to-warm); travel = len-1 so
// the cosine spans every entry exactly once.
//
// phase advances the wave; the caller passes m.spinnerPhase so the ripple and the
// braille spinner share a single animation clock.
//
// On an empty palette or empty text the input string is returned verbatim (no
// styling) — a safe-skip contract for callers.
func rippleText(text string, palette []lipgloss.Style, phase, waveLen int) string {
	if len(palette) == 0 || text == "" {
		return text
	}
	travel := len(palette) - 1
	if travel == 0 {
		return palette[0].Render(text)
	}

	var b strings.Builder
	for i, r := range text {
		level := rippleLevel(i+phase, travel, waveLen)
		if level < 0 {
			level = 0
		}
		if level > travel {
			level = travel
		}
		b.WriteString(palette[level].Render(string(r)))
	}
	return b.String()
}

// ripplePalette is the run-state colour ramp used by the working status line: the
// curated sonutTheme styles ordered cold (faint) at the cosine troughs to
// warm-bright (accent) at its peaks, giving a visible "the agent is thinking"
// temperature feel. Built lazily from the active theme so a /theme swap is
// reflected. No hex literal appears here — every entry is a named theme style
// (theme.go), satisfying the repo's no-hex-outside-theme rule.
func ripplePalette() []lipgloss.Style {
	// A dim→lime ramp blended from the brand accent — deliberately NOT the semantic
	// amber (permission) / green (success) hues. Flashing those every spinner tick
	// trains the eye to ignore the exact colours that signal a real permission
	// prompt or a success, so the decorative working line stays in the brand lime.
	// Falls back to a coarse named ramp if the theme has no parseable accent/faint.
	bright := sonutTheme.accent.GetForeground()
	dim := sonutTheme.faint.GetForeground()
	if bright == nil || dim == nil {
		return []lipgloss.Style{sonutTheme.faint, sonutTheme.muted, sonutTheme.accent}
	}
	blend := lipgloss.Blend1D(5, dim, bright)
	out := make([]lipgloss.Style, len(blend))
	for i, c := range blend {
		r, g, b, a := c.RGBA()
		out[i] = lipgloss.NewStyle().Foreground(color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)})
	}
	return out
}
