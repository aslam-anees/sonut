package voice

import "strings"

type ComposerSnapshot struct {
	Text   string
	Cursor int
}

// ReplaceableTranscript keeps the text before and after the insertion point
// stable while an ASR stream revises its partial result.
type ReplaceableTranscript struct {
	base    ComposerSnapshot
	current ComposerSnapshot
	start   int
	end     int
	active  bool
	partial string
}

func NewReplaceableTranscript(base ComposerSnapshot) *ReplaceableTranscript {
	base.Cursor = clampRuneOffset(base.Text, base.Cursor)
	return &ReplaceableTranscript{base: base, current: base, start: base.Cursor, end: base.Cursor, active: true}
}

func (t *ReplaceableTranscript) Update(partial string) ComposerSnapshot {
	if !t.active {
		return t.base
	}
	t.partial = partial
	text := replaceRunes(t.current.Text, t.start, t.end, partial)
	t.end = t.start + len([]rune(partial))
	t.current = ComposerSnapshot{Text: text, Cursor: t.end}
	return t.current
}

func (t *ReplaceableTranscript) Commit(final string) ComposerSnapshot {
	if !t.active {
		return t.base
	}
	t.active = false
	t.partial = final
	text := replaceRunes(t.current.Text, t.start, t.end, final)
	end := t.start + len([]rune(final))
	t.current = ComposerSnapshot{Text: text, Cursor: end}
	return t.current
}

func (t *ReplaceableTranscript) Cancel() ComposerSnapshot {
	if !t.active {
		return t.current
	}
	t.active = false
	return t.base
}

func (t *ReplaceableTranscript) Active() bool    { return t.active }
func (t *ReplaceableTranscript) Partial() string { return t.partial }

func replaceRunes(text string, start, end int, replacement string) string {
	runes := []rune(text)
	start = clampInt(start, 0, len(runes))
	end = clampInt(end, start, len(runes))
	result := make([]rune, 0, len(runes)+len([]rune(replacement)))
	result = append(result, runes[:start]...)
	result = append(result, []rune(replacement)...)
	result = append(result, runes[end:]...)
	return string(result)
}

func clampRuneOffset(text string, offset int) int { return clampInt(offset, 0, len([]rune(text))) }
func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// NormalizeTranscript is intentionally conservative. The final transcript is
// exact for routing; this only normalizes the empty/whitespace edge case used
// by the composer and confirmation logic.
func NormalizeTranscript(text string) string { return strings.TrimSpace(text) }
