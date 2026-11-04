package voice

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	markdownFenceRE = regexp.MustCompile("(?s)```.*?```")
	markdownLinkRE  = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	urlRE           = regexp.MustCompile(`https?://[^\s)]+`)
	longPathRE      = regexp.MustCompile(`(?:/|[A-Za-z]:\\)[^\s]+`)
	hashRE          = regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)
	formatRE        = regexp.MustCompile(`[*_~#>]`)
)

// CleanForSpeech removes structures that are useful on screen but harmful in
// a spoken response. Fenced code is dropped completely; inline code is kept
// as words when it is short enough to be useful.
func CleanForSpeech(text string) string {
	text = markdownFenceRE.ReplaceAllString(text, " ")
	text = markdownLinkRE.ReplaceAllString(text, "$1")
	text = urlRE.ReplaceAllString(text, " ")
	text = longPathRE.ReplaceAllString(text, " ")
	text = hashRE.ReplaceAllString(text, " ")
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.Contains(trimmed, "|") {
			continue
		}
		kept = append(kept, line)
	}
	text = strings.Join(kept, " ")
	text = strings.ReplaceAll(text, "`", "")
	text = formatRE.ReplaceAllString(text, "")
	text = strings.Join(strings.Fields(text), " ")
	return strings.TrimSpace(text)
}

// SentenceChunker emits only complete clauses. It retains an incomplete tail
// so streamed deltas cannot cause duplicate or unstable speech.
type SentenceChunker struct {
	buffer      string
	maxRunes    int
	inCodeFence bool
	inURL       bool
}

func NewSentenceChunker() *SentenceChunker { return &SentenceChunker{maxRunes: 320} }

func (c *SentenceChunker) Push(delta string) []string {
	if delta == "" {
		return nil
	}
	// Keep fenced-code state across provider deltas. Cleaning each delta
	// independently would speak the first code line before the closing fence
	// arrived, and splitting a URL across deltas could expose a fragment.
	for delta != "" {
		if c.inCodeFence {
			closeIndex := strings.Index(delta, "```")
			if closeIndex < 0 {
				return c.take(false)
			}
			delta = delta[closeIndex+3:]
			c.inCodeFence = false
			continue
		}
		openIndex := strings.Index(delta, "```")
		if openIndex < 0 {
			c.appendProse(delta)
			break
		}
		c.appendProse(delta[:openIndex])
		delta = delta[openIndex+3:]
		if closeIndex := strings.Index(delta, "```"); closeIndex >= 0 {
			delta = delta[closeIndex+3:]
			continue
		}
		c.inCodeFence = true
	}
	return c.take(false)
}

func (c *SentenceChunker) Flush() []string {
	c.inCodeFence = false
	c.inURL = false
	return c.take(true)
}

func (c *SentenceChunker) Reset() {
	c.buffer = ""
	c.inCodeFence = false
	c.inURL = false
}

func (c *SentenceChunker) appendProse(delta string) {
	for delta != "" {
		if c.inURL {
			if space := strings.IndexFunc(delta, unicode.IsSpace); space >= 0 {
				c.buffer += " "
				delta = delta[space+1:]
				c.inURL = false
				continue
			}
			return
		}
		urlIndex := strings.Index(delta, "https://")
		if httpIndex := strings.Index(delta, "http://"); httpIndex >= 0 && (urlIndex < 0 || httpIndex < urlIndex) {
			urlIndex = httpIndex
		}
		if urlIndex < 0 {
			c.buffer += delta
			return
		}
		c.buffer += delta[:urlIndex]
		delta = delta[urlIndex:]
		if space := strings.IndexFunc(delta, unicode.IsSpace); space >= 0 {
			delta = delta[space+1:]
			continue
		}
		c.inURL = true
		return
	}
}

func (c *SentenceChunker) take(force bool) []string {
	clean := CleanForSpeech(c.buffer)
	if clean == "" {
		c.buffer = ""
		return nil
	}
	var chunks []string
	start := 0
	runes := []rune(clean)
	for i := 0; i < len(runes); i++ {
		if !isSpeechBoundary(runes, i) {
			continue
		}
		part := strings.TrimSpace(string(runes[start : i+1]))
		if part == "" {
			start = i + 1
			continue
		}
		chunks = append(chunks, part)
		start = i + 1
	}
	remaining := strings.TrimSpace(string(runes[start:]))
	if force && remaining != "" {
		chunks = append(chunks, remaining)
		remaining = ""
	} else if len([]rune(remaining)) >= c.maxRunes {
		if split := strings.LastIndexAny(remaining, ",;: "); split > 0 {
			chunks = append(chunks, strings.TrimSpace(remaining[:split+1]))
			remaining = strings.TrimSpace(remaining[split+1:])
		}
	}
	c.buffer = remaining
	return chunks
}

func isSpeechBoundary(runes []rune, index int) bool {
	r := runes[index]
	if r != '.' && r != '?' && r != '!' && r != '。' && r != '？' && r != '！' && r != '\n' {
		return false
	}
	if r == '.' {
		if index > 0 && index+1 < len(runes) && unicode.IsDigit(runes[index-1]) && unicode.IsDigit(runes[index+1]) {
			return false
		}
		if index > 0 {
			wordStart := index - 1
			for wordStart >= 0 && unicode.IsLetter(runes[wordStart]) {
				wordStart--
			}
			word := strings.ToLower(string(runes[wordStart+1 : index+1]))
			switch word {
			case "mr.", "mrs.", "ms.", "dr.", "e.g.", "i.e.", "etc.":
				return false
			}
		}
		if index+1 < len(runes) && !unicode.IsSpace(runes[index+1]) {
			return false
		}
	}
	return true
}
