package gram

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// memoriesFile is the name of the JSON file that holds all memories under a
// FileStore's root directory.
const memoriesFile = "memories.json"

// FileStore is the default, dependency-free Store implementation. It
// persists memories as a single JSON document under a root directory,
// guarded by an in-process mutex and rewritten atomically on every mutation
// so data survives process restarts.
type FileStore struct {
	mu       sync.Mutex
	rootDir  string
	path     string
	memories map[string]Memory
	now      func() time.Time
	closed   bool
}

// DefaultRoot returns the default root directory for gram's on-disk data:
// ~/.atcode/memory. os.UserHomeDir is only consulted as a fallback default —
// callers that want a different location should pass rootDir explicitly to
// NewFileStore instead of relying on this function.
func DefaultRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = "."
	}
	return filepath.Join(home, ".atcode", "memory")
}

// NewFileStore opens (creating if necessary) a file-backed Store rooted at
// rootDir. Passing an empty rootDir falls back to DefaultRoot().
func NewFileStore(rootDir string) (*FileStore, error) {
	if strings.TrimSpace(rootDir) == "" {
		rootDir = DefaultRoot()
	}
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		return nil, fmt.Errorf("gram: create root dir: %w", err)
	}
	fs := &FileStore{
		rootDir:  rootDir,
		path:     filepath.Join(rootDir, memoriesFile),
		memories: map[string]Memory{},
		now:      time.Now,
	}
	if err := fs.load(); err != nil {
		return nil, err
	}
	return fs, nil
}

func (fs *FileStore) load() error {
	data, err := os.ReadFile(fs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("gram: read store: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var list []Memory
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("gram: decode store: %w", err)
	}
	for _, m := range list {
		fs.memories[m.ID] = m
	}
	return nil
}

// saveLocked writes the full memory set to disk atomically (write to a temp
// file, then rename). Caller must hold fs.mu.
func (fs *FileStore) saveLocked() error {
	list := make([]Memory, 0, len(fs.memories))
	for _, m := range fs.memories {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("gram: encode store: %w", err)
	}

	tmp, err := os.CreateTemp(fs.rootDir, "memories-*.tmp")
	if err != nil {
		return fmt.Errorf("gram: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("gram: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("gram: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, fs.path); err != nil {
		return fmt.Errorf("gram: rename temp file: %w", err)
	}
	return nil
}

func newMemoryID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("gram: generate id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Save implements Store.
func (fs *FileStore) Save(input SaveInput) (Memory, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed {
		return Memory{}, fmt.Errorf("gram: store is closed")
	}

	now := fs.now()
	id := strings.TrimSpace(input.ID)
	existing, ok := Memory{}, false
	if id != "" {
		existing, ok = fs.memories[id]
	}
	if id == "" {
		generated, err := newMemoryID()
		if err != nil {
			return Memory{}, err
		}
		id = generated
	}

	m := Memory{
		ID:        id,
		Project:   strings.TrimSpace(input.Project),
		Title:     strings.TrimSpace(input.Title),
		Body:      input.Body,
		Tags:      append([]string(nil), input.Tags...),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if ok {
		m.CreatedAt = existing.CreatedAt
	}

	fs.memories[id] = m
	if err := fs.saveLocked(); err != nil {
		return Memory{}, err
	}
	return m, nil
}

// Recall implements Store.
func (fs *FileStore) Recall(query Query) ([]Memory, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed {
		return nil, fmt.Errorf("gram: store is closed")
	}

	text := strings.ToLower(strings.TrimSpace(query.Text))
	project := strings.TrimSpace(query.Project)

	var results []Memory
	for _, m := range fs.memories {
		if project != "" && m.Project != project {
			continue
		}
		if text != "" && !memoryMatches(m, text) {
			continue
		}
		results = append(results, m)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	if query.Limit > 0 && len(results) > query.Limit {
		results = results[:query.Limit]
	}
	return results, nil
}

func memoryMatches(m Memory, lowerText string) bool {
	if strings.Contains(strings.ToLower(m.Title), lowerText) {
		return true
	}
	if strings.Contains(strings.ToLower(m.Body), lowerText) {
		return true
	}
	for _, tag := range m.Tags {
		if strings.Contains(strings.ToLower(tag), lowerText) {
			return true
		}
	}
	return false
}

// Get implements Store.
func (fs *FileStore) Get(id string) (Memory, bool, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed {
		return Memory{}, false, fmt.Errorf("gram: store is closed")
	}
	m, ok := fs.memories[strings.TrimSpace(id)]
	return m, ok, nil
}

// Delete implements Store.
func (fs *FileStore) Delete(id string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.closed {
		return fmt.Errorf("gram: store is closed")
	}
	id = strings.TrimSpace(id)
	if _, ok := fs.memories[id]; !ok {
		return nil
	}
	delete(fs.memories, id)
	return fs.saveLocked()
}

// Close implements Store. FileStore holds no long-lived OS handles between
// calls (each mutation opens/renames a temp file), so Close simply marks the
// store unusable to catch programming errors; it always returns nil.
func (fs *FileStore) Close() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.closed = true
	return nil
}

var _ Store = (*FileStore)(nil)
