// Package knowledge implements @code's local knowledge graph: a directory of
// markdown notes linked Obsidian-style with [[wikilinks]] and #tags. Notes
// live under the per-user @code home so they can later be synced/shared, and
// /knowledge renders them as an interactive force-directed graph.
package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Note is a single markdown note in the knowledge base.
type Note struct {
	Name     string // slug (filename without .md)
	Title    string
	Path     string
	Content  string
	Links    []string // wikilink target slugs
	Tags     []string
	Modified time.Time

	// Structured metadata is additive. Older notes commonly have no
	// frontmatter, so all fields are optional and the original markdown remains
	// the source of truth.
	Kind         string
	Aliases      []string
	Created      time.Time
	Updated      time.Time
	SourceURL    string
	SourceDomain string
	CapturedAt   time.Time
	ContentHash  string
	Provenance   string
	Contested    bool
	Stale        bool

	// FrontmatterRaw is retained verbatim so an editor can change a note while
	// preserving fields this version of @code does not understand.
	FrontmatterRaw string
}

// NoteDraft is the deliberately small write API used by the native editor.
// Existing frontmatter is supplied by LoadNotes and is preserved verbatim.
type NoteDraft struct {
	Path         string
	Name         string
	Title        string
	Tags         []string
	Body         string
	SourceURL    string
	SourceDomain string
	CapturedAt   time.Time
	SearchQuery  string
	Kind         string
	Frontmatter  string
}

var (
	wikilinkRe = regexp.MustCompile(`\[\[([^\[\]|]+)(?:\|[^\[\]]*)?\]\]`)
	tagRe      = regexp.MustCompile(`(?:^|\s)#([\p{L}\p{N}_-]+)`)
)

// Dir returns the knowledge base directory (~/.atcode/knowledge), honoring
// the SONUT_KNOWLEDGE_DIR override.
func Dir() string {
	if dir := os.Getenv("SONUT_KNOWLEDGE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./knowledge"
	}
	return filepath.Join(home, ".atcode", "knowledge")
}

// EnsureDir creates the knowledge directory and seeds a welcome note the
// first time so the graph is never empty.
func EnsureDir() (string, error) {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			return dir, nil
		}
	}
	seed := `# Welcome to @code Knowledge

This is your local knowledge base. Every ` + "`.md`" + ` file in this folder is a node;
link notes together with [[wikilinks]] and group them with #tags.

- Add notes with ` + "`/knowledge add <title> :: <content>`" + `
- View the graph with ` + "`/knowledge`" + `
- Files live here, plain markdown — edit them with any editor. #sonut

See also: [[how-linking-works]]
`
	seed2 := `# How linking works

Write ` + "`[[note-name]]`" + ` anywhere in a note to draw an edge to that note.
Links to notes that don't exist yet show up as ghost nodes — write them later. #sonut
`
	if err := os.WriteFile(filepath.Join(dir, "welcome.md"), []byte(seed), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "how-linking-works.md"), []byte(seed2), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// Slugify converts a title into a filesystem/link-safe slug.
func Slugify(title string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteRune('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// AddNote writes a new note (or appends to an existing one with the same
// title) and returns its path.
func AddNote(title, content string) (string, error) {
	dir, err := EnsureDir()
	if err != nil {
		return "", err
	}
	slug := Slugify(title)
	if slug == "" {
		return "", fmt.Errorf("empty note title")
	}
	path := filepath.Join(dir, slug+".md")
	if _, err := os.Stat(path); err == nil {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := fmt.Fprintf(f, "\n%s\n", content); err != nil {
			return "", err
		}
		return path, nil
	}
	body := fmt.Sprintf("# %s\n\n%s\n", strings.TrimSpace(title), content)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// SaveNote creates or replaces a note. It is safe to call with a draft from an
// older note: unknown YAML keys are retained through FrontmatterRaw.
func SaveNote(draft NoteDraft) (string, error) {
	dir, err := EnsureDir()
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(draft.Title)
	slug := strings.TrimSpace(draft.Name)
	if slug == "" {
		slug = Slugify(title)
	}
	if slug == "" {
		return "", fmt.Errorf("empty note title")
	}
	if filepath.Base(slug) != slug {
		return "", fmt.Errorf("invalid note name")
	}
	path := draft.Path
	if path == "" {
		path = filepath.Join(dir, slug+".md")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	if !pathWithinDir(dir, path) {
		return "", fmt.Errorf("note path escapes knowledge directory")
	}
	if title == "" {
		title = slug
	}
	body := strings.TrimSpace(draft.Body)
	for _, tag := range draft.Tags {
		tag = strings.TrimPrefix(strings.TrimSpace(tag), "#")
		if tag != "" && !strings.Contains(body, "#"+tag) {
			if body != "" {
				body += "\n\n"
			}
			body += "#" + tag
		}
	}
	frontmatter := strings.TrimSpace(draft.Frontmatter)
	if frontmatter == "" && draft.SourceURL != "" {
		created := time.Now().UTC()
		captured := draft.CapturedAt
		if captured.IsZero() {
			captured = created
		}
		kind := draft.Kind
		if kind == "" {
			kind = "note"
		}
		frontmatter = renderFrontmatter(kind, created, created, draft.SourceURL, captured, draft.SearchQuery, draft.Tags, body)
	}
	if frontmatter != "" {
		updates := map[string]string{
			"updated_at":   time.Now().UTC().Format(time.RFC3339Nano),
			"content_hash": ContentHash(body),
		}
		if len(draft.Tags) > 0 {
			quoted := make([]string, 0, len(draft.Tags))
			for _, tag := range draft.Tags {
				quoted = append(quoted, "\""+strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(tag), "#"), "\"", "")+"\"")
			}
			updates["tags"] = "[" + strings.Join(quoted, ", ") + "]"
		}
		if strings.TrimSpace(draft.SourceURL) != "" {
			updates["source_url"] = strings.TrimSpace(draft.SourceURL)
		}
		if strings.TrimSpace(draft.SourceDomain) != "" {
			updates["source_domain"] = strings.TrimSpace(draft.SourceDomain)
		}
		frontmatter = updateFrontmatter(frontmatter, updates)
	}
	if frontmatter != "" {
		frontmatter = strings.TrimSpace(frontmatter)
	}
	var b strings.Builder
	if frontmatter != "" {
		b.WriteString("---\n")
		b.WriteString(strings.Trim(frontmatter, "-\n"))
		b.WriteString("\n---\n\n")
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// MoveToTrash performs a recoverable delete inside the knowledge directory.
// It intentionally does not use the workspace trash: SONUT_KNOWLEDGE_DIR may
// be outside the workspace and should still have the same safe-delete rule.
func MoveToTrash(path string) (string, error) {
	dir, err := EnsureDir()
	if err != nil {
		return "", err
	}
	if !pathWithinDir(dir, path) {
		return "", fmt.Errorf("refusing to trash a path outside the knowledge directory")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("refusing to trash a directory")
	}
	slot := time.Now().UTC().Format("2006-01-02T15-04-05.000000000")
	destDir := filepath.Join(dir, ".trash", slot)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(destDir, filepath.Base(path))
	if err := os.Rename(path, dest); err != nil {
		return "", fmt.Errorf("move note to recoverable trash: %w", err)
	}
	return dest, nil
}

// LoadNotes parses every markdown note in the knowledge directory.
func LoadNotes() ([]Note, error) {
	dir, err := EnsureDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var notes []Note
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, _ := e.Info()
		content := string(raw)
		frontmatter, fields, markdown := parseFrontmatter(content)
		n := Note{
			Name:           strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())),
			Path:           path,
			Content:        content,
			FrontmatterRaw: frontmatter,
		}
		if info != nil {
			n.Modified = info.ModTime()
		}
		n.Title = firstHeading(markdown)
		if n.Title == "" {
			n.Title = n.Name
		}
		for _, m := range wikilinkRe.FindAllStringSubmatch(content, -1) {
			n.Links = append(n.Links, Slugify(m[1]))
		}
		for _, m := range tagRe.FindAllStringSubmatch(content, -1) {
			n.Tags = append(n.Tags, strings.ToLower(m[1]))
		}
		n.Kind = firstField(fields, "type", "kind")
		n.SourceURL = firstField(fields, "source_url", "source")
		n.SourceDomain = firstField(fields, "source_domain", "domain")
		n.Provenance = firstField(fields, "provenance")
		n.Aliases = parseListField(fields["aliases"])
		n.Contested = parseBoolField(fields["contested"])
		n.Stale = parseBoolField(fields["stale"])
		n.Created = parseTimeField(fields, "created_at", "created")
		n.Updated = parseTimeField(fields, "updated_at", "modified")
		n.CapturedAt = parseTimeField(fields, "captured_at")
		n.ContentHash = firstField(fields, "content_hash")
		for _, tag := range parseListField(fields["tags"]) {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag == "" || containsString(n.Tags, tag) {
				continue
			}
			n.Tags = append(n.Tags, tag)
		}
		if n.ContentHash == "" {
			n.ContentHash = ContentHash(content)
		}
		if n.Created.IsZero() {
			n.Created = n.Modified
		}
		if n.Updated.IsZero() {
			n.Updated = n.Modified
		}
		if n.Kind == "" {
			n.Kind = "note"
		}
		notes = append(notes, n)
	}
	return notes, nil
}

// ContentHash is stable across machines and is used for explicit source
// provenance and safe derived-index invalidation.
func ContentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

func pathWithinDir(dir, path string) bool {
	dir, _ = filepath.Abs(dir)
	path, _ = filepath.Abs(path)
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parseFrontmatter(content string) (string, map[string]string, string) {
	fields := map[string]string{}
	if !strings.HasPrefix(content, "---") {
		return "", fields, content
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", fields, content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", fields, content
	}
	raw := strings.Join(lines[1:end], "\n")
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "\"'"))
		if key != "" {
			fields[key] = value
		}
	}
	return raw, fields, strings.Join(lines[end+1:], "\n")
}

func firstField(fields map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(fields[key]); value != "" {
			return value
		}
	}
	return ""
}

func parseTimeField(fields map[string]string, keys ...string) time.Time {
	value := firstField(fields, keys...)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func parseBoolField(value string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.Trim(value, "\"'"))) {
	case "true", "yes", "1":
		return true
	default:
		return false
	}
}

func parseListField(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	parts := strings.Split(value, ",")
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(strings.Trim(part, "\"'"))
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func renderFrontmatter(kind string, created, updated time.Time, sourceURL string, captured time.Time, query string, tags []string, body string) string {
	lines := []string{
		"type: " + kind,
		"created_at: " + created.UTC().Format(time.RFC3339Nano),
		"updated_at: " + updated.UTC().Format(time.RFC3339Nano),
		"source_url: " + sourceURL,
		"source_domain: " + sourceDomain(sourceURL),
		"captured_at: " + captured.UTC().Format(time.RFC3339Nano),
		"content_hash: " + ContentHash(body),
	}
	if query != "" {
		lines = append(lines, "search_query: "+query)
	}
	if len(tags) > 0 {
		quoted := make([]string, 0, len(tags))
		for _, tag := range tags {
			quoted = append(quoted, "\""+strings.ReplaceAll(tag, "\"", "")+"\"")
		}
		lines = append(lines, "tags: ["+strings.Join(quoted, ", ")+"]")
	}
	return strings.Join(lines, "\n")
}

func sourceDomain(sourceURL string) string {
	value := strings.TrimSpace(sourceURL)
	if value == "" {
		return ""
	}
	for _, prefix := range []string{"https://", "http://"} {
		value = strings.TrimPrefix(value, prefix)
	}
	if slash := strings.IndexByte(value, '/'); slash >= 0 {
		value = value[:slash]
	}
	return strings.TrimSpace(value)
}

func updateFrontmatter(raw string, updates map[string]string) string {
	lines := strings.Split(raw, "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		key, _, ok := strings.Cut(line, ":")
		key = strings.ToLower(strings.TrimSpace(key))
		if !ok || key == "" {
			continue
		}
		if value, exists := updates[key]; exists {
			lines[i] = key + ": " + value
			seen[key] = true
		}
	}
	for key, value := range updates {
		if !seen[key] {
			lines = append(lines, key+": "+value)
		}
	}
	return strings.Join(lines, "\n")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstHeading(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#!") {
			return strings.TrimSpace(strings.TrimLeft(line, "# "))
		}
	}
	return ""
}
