package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"time"
)

// ErrFileChangedOnDisk is returned by CheckConflict when a file's current
// on-disk content no longer matches what a tool last read or wrote in this
// session — i.e. it was modified outside @code. The write tools surface it with
// guidance to re-read before retrying, so a stale overwrite cannot silently
// clobber an external edit.
var ErrFileChangedOnDisk = errors.New("the file changed on disk since you last read it")

// FileVersion is what a tool last observed for a file: an authoritative content
// hash plus cheap stat metadata kept for diagnostics.
type FileVersion struct {
	Hash  string
	Size  int64
	MTime time.Time
	// Read-range fingerprint for the qwen-style unchanged fast-path: a follow-up
	// read_file with the same (start, end, max_lines) can short-circuit when the
	// on-disk size/mtime still match, avoiding re-read + re-prefill of the same
	// dump (dominant cost on local hybrid models).
	LastReadStart    int
	LastReadEnd      int
	LastReadMaxLines int
	// LastReadCacheable is true when the last successful read_file may be
	// short-circuited (text file, completed OK). Writes clear this.
	LastReadCacheable bool
}

// FileTracker records, per absolute path, the version of each file a tool read
// or wrote within a single session. A later write compares the current on-disk
// content against this baseline to detect a modification made outside @code.
//
// Construct with NewFileTracker. A nil *FileTracker is a valid no-op — every
// method tolerates it — so a caller that has not wired the feature (tests, MCP)
// needs no nil guards.
type FileTracker struct {
	mu       sync.Mutex
	versions map[string]FileVersion
	// lastRecorded is the most recently read/written path — the file the model
	// is "looking at". Edit tools use it to recover a call that omitted path.
	lastRecorded string
	// rejectedWrites holds, per path, the content of a write_file call refused by
	// the already-exists guard, so the retry can reuse it (reuse_content: true)
	// instead of regenerating and resending the entire file.
	rejectedWrites map[string]string
}

type trackedDiskState struct {
	exists bool
	size   int64
	mtime  time.Time
}

func NewFileTracker() *FileTracker {
	return &FileTracker{versions: make(map[string]FileVersion)}
}

// StashRejectedWrite keeps the content of a refused write for absPath and
// reports whether it was stored (false on a nil tracker, where no reuse is
// possible and the caller should not advertise it).
func (tracker *FileTracker) StashRejectedWrite(absPath, content string) bool {
	if tracker == nil {
		return false
	}
	tracker.mu.Lock()
	if tracker.rejectedWrites == nil {
		tracker.rejectedWrites = make(map[string]string)
	}
	tracker.rejectedWrites[absPath] = content
	tracker.mu.Unlock()
	return true
}

// TakeRejectedWrite returns and removes the stashed content for absPath.
func (tracker *FileTracker) TakeRejectedWrite(absPath string) (string, bool) {
	if tracker == nil {
		return "", false
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	content, ok := tracker.rejectedWrites[absPath]
	if ok {
		delete(tracker.rejectedWrites, absPath)
	}
	return content, ok
}

// Record stores the version of absPath given its content and optional stat info.
// Write paths use Record; it clears the read-cacheable flag so a post-edit
// re-read always returns fresh content.
func (tracker *FileTracker) Record(absPath string, content []byte, info os.FileInfo) {
	if tracker == nil {
		return
	}
	version := FileVersion{Hash: HashContent(content)}
	if info != nil {
		version.Size = info.Size()
		version.MTime = info.ModTime()
	}
	tracker.mu.Lock()
	// Preserve nothing about prior reads — content just changed (or first touch).
	tracker.versions[absPath] = version
	tracker.lastRecorded = absPath
	tracker.mu.Unlock()
}

// RecordRead is Record plus the read-range fingerprint used by the unchanged
// short-circuit. Call from read_file after a successful emission.
func (tracker *FileTracker) RecordRead(absPath string, content []byte, info os.FileInfo, startLine, endLine, maxLines int) {
	if tracker == nil {
		return
	}
	version := FileVersion{
		Hash:              HashContent(content),
		LastReadStart:     startLine,
		LastReadEnd:       endLine,
		LastReadMaxLines:  maxLines,
		LastReadCacheable: true,
	}
	if info != nil {
		version.Size = info.Size()
		version.MTime = info.ModTime()
	}
	tracker.mu.Lock()
	tracker.versions[absPath] = version
	tracker.lastRecorded = absPath
	tracker.mu.Unlock()
}

// UnchangedSinceRead reports whether absPath still matches the size/mtime of
// the last cacheable read with the same range args — without reading the file
// body. info must come from a fresh Stat of absPath.
func (tracker *FileTracker) UnchangedSinceRead(absPath string, info os.FileInfo, startLine, endLine, maxLines int) bool {
	if tracker == nil || info == nil {
		return false
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	version, ok := tracker.versions[absPath]
	if !ok || !version.LastReadCacheable {
		return false
	}
	if version.LastReadStart != startLine || version.LastReadEnd != endLine || version.LastReadMaxLines != maxLines {
		return false
	}
	if version.Size != info.Size() || !version.MTime.Equal(info.ModTime()) {
		return false
	}
	return true
}

// LastRecordedPath returns the most recently recorded (read or written) path,
// or "" when nothing has been tracked yet.
func (tracker *FileTracker) LastRecordedPath() string {
	if tracker == nil {
		return ""
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.lastRecorded
}

// TrackedPaths returns every path recorded this session, in no particular order.
func (tracker *FileTracker) TrackedPaths() []string {
	if tracker == nil {
		return nil
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	paths := make([]string, 0, len(tracker.versions))
	for p := range tracker.versions {
		paths = append(paths, p)
	}
	return paths
}

// snapshotDiskState captures cheap metadata for tracked files immediately
// before an @code-owned shell command. RefreshChangedSince uses it after the
// command to rebaseline only files that command actually changed, preventing a
// formatter/build step from being misreported later as an external stale edit.
func (tracker *FileTracker) snapshotDiskState() map[string]trackedDiskState {
	if tracker == nil {
		return nil
	}
	states := make(map[string]trackedDiskState)
	for _, path := range tracker.TrackedPaths() {
		info, err := os.Stat(path)
		if err != nil {
			states[path] = trackedDiskState{}
			continue
		}
		states[path] = trackedDiskState{exists: true, size: info.Size(), mtime: info.ModTime()}
	}
	return states
}

// refreshChangedSince records the final content of tracked files modified by
// the shell command that followed snapshot. Unchanged files are not read.
// Deleted files are forgotten so a later recreation establishes a new baseline.
func (tracker *FileTracker) refreshChangedSince(before map[string]trackedDiskState) {
	if tracker == nil || len(before) == 0 {
		return
	}
	for path, previous := range before {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			if previous.exists {
				tracker.Forget(path)
			}
			continue
		}
		if err != nil {
			continue
		}
		if previous.exists && previous.size == info.Size() && previous.mtime.Equal(info.ModTime()) {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		tracker.Record(path, content, info)
	}
}

// Version returns the recorded version for absPath and whether one exists.
func (tracker *FileTracker) Version(absPath string) (FileVersion, bool) {
	if tracker == nil {
		return FileVersion{}, false
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	version, ok := tracker.versions[absPath]
	return version, ok
}

// Forget drops any recorded version for absPath, so the next read re-establishes
// the baseline. Used after a tool rewrites a file by a path other than its own
// (e.g. apply_patch) where the new content is not cheaply available to Record.
func (tracker *FileTracker) Forget(absPath string) {
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	delete(tracker.versions, absPath)
	if tracker.lastRecorded == absPath {
		tracker.lastRecorded = ""
	}
	tracker.mu.Unlock()
}

// CheckConflict reports ErrFileChangedOnDisk when absPath has a recorded baseline
// and currentContent (the bytes the caller just read from disk) no longer matches
// it. An untracked path returns nil: with no baseline there is nothing to
// conflict against, so a first-touch write is always allowed.
func (tracker *FileTracker) CheckConflict(absPath string, currentContent []byte) error {
	if tracker == nil {
		return nil
	}
	version, ok := tracker.Version(absPath)
	if !ok {
		return nil
	}
	if HashContent(currentContent) != version.Hash {
		return ErrFileChangedOnDisk
	}
	return nil
}

// HashContent returns the hex-encoded SHA-256 of content. Exported so the write
// tools and tests share one definition of a file's identity.
func HashContent(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// fileConflictMessage is the actionable error a write tool returns when a target
// changed on disk since it was last read, telling the model how to recover.
func fileConflictMessage(relativePath string) string {
	return "Error writing " + relativePath + ": " + ErrFileChangedOnDisk.Error() +
		" (it may have been edited outside @code). Re-read it with read_file, then re-apply your change so you do not overwrite the newer content."
}
