// Package selfupdate keeps the installed "@" binary and the local @0.1 models
// in sync with the latest release. Checks are best-effort with short timeouts:
// a dead network can never block startup.
//
// Binary channel: the at-code repository publishes install/VERSION plus
// prebuilt binaries under bin/at-<os>-<arch>. When VERSION is newer than the
// running build, the startup screen offers a one-key update that downloads the
// matching binary and swaps it in place (the "@", "at" and "sonut" names all
// resolve to the same file).
//
// Model channel: the HuggingFace repo (anees-aslam-ai/atcode-v1.0) is the source of
// truth. When it lists GGUF files that are not on disk, the old models are
// removed first and the new ones downloaded through the regular startup
// download screen (largest = main model first, as returned by ListHFModelFiles).
package selfupdate

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/llama"
	"github.com/aistatine/sonut/internal/update"
)

// CurrentVersion is stamped by the CLI entrypoint (same channel as insights).
var CurrentVersion = "dev"

const (
	atCodeRawBase = "https://raw.githubusercontent.com/aslam-anees/at-code/src"
	checkTimeout  = 6 * time.Second
)

// BinaryUpdate describes an available @ binary update.
type BinaryUpdate struct {
	Current string
	Latest  string
	URL     string
}

// ModelUpdate describes a pending model refresh.
type ModelUpdate struct {
	// Missing are HF files not present locally (to download).
	Missing []llama.HFFile
	// Stale are local GGUF paths no longer listed on HF (to delete first).
	Stale []string
}

// CheckBinary fetches install/VERSION from the at-code repo and compares it to
// the running version. Returns nil when up to date, unreachable, or on a dev build
// where the comparison would always fire spuriously offline.
func CheckBinary() *BinaryUpdate {
	current := strings.TrimSpace(CurrentVersion)
	if current == "" {
		return nil
	}
	client := &http.Client{Timeout: checkTimeout}
	resp, err := client.Get(atCodeRawBase + "/install/VERSION")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return nil
	}
	latest := strings.TrimSpace(string(raw))
	cmp, err := update.CompareSemver(latest, current)
	if err != nil || cmp <= 0 {
		return nil
	}
	name := fmt.Sprintf("at-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return &BinaryUpdate{
		Current: current,
		Latest:  latest,
		URL:     atCodeRawBase + "/bin/" + name,
	}
}

// ApplyBinary downloads the new binary and atomically replaces the running
// executable. The "@"/"at"/"sonut" commands are symlinks to (or copies of) the
// same file, so replacing the resolved target updates all of them.
func ApplyBinary(u *BinaryUpdate) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot locate current binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	tmp := exe + ".update"
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(u.URL)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download interrupted: %w", err)
	}
	// Sanity: a real @code binary is megabytes; anything tiny is an error page.
	if n < 1<<20 {
		os.Remove(tmp)
		return fmt.Errorf("downloaded file too small (%d bytes) — refusing to install", n)
	}

	if runtime.GOOS == "windows" {
		// Windows cannot replace a running exe; park the old one instead.
		old := exe + ".old"
		os.Remove(old)
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("could not stage update: %w", err)
		}
		if err := os.Rename(tmp, exe); err != nil {
			_ = os.Rename(old, exe) // roll back
			return fmt.Errorf("could not install update: %w", err)
		}
		return nil
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("could not install update: %w", err)
	}
	return nil
}

// CheckModels compares the HF repo listing to the local model directory.
// Returns nil when in sync or the repo is unreachable.
func CheckModels() *ModelUpdate {
	// A user-provided local model (e.g. OmniCoder-9B dropped into ~/.atcode/model)
	// won't appear in the HF repo listing; without this guard it would be marked
	// stale and deleted, then replaced by a multi-gigabyte re-download.
	if mainPath, _, ok := llama.CheckModelsPresent(); ok {
		lower := strings.ToLower(filepath.Base(mainPath))
		if strings.Contains(lower, "omnicoder") || strings.Contains(lower, "qwopus") {
			return nil
		}
	}
	files, err := llama.ListHFModelFiles()
	if err != nil || len(files) == 0 {
		return nil
	}
	modelDir := llama.GetModelDir()
	entries, _ := os.ReadDir(modelDir)
	local := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
			local[e.Name()] = true
		}
	}
	remote := map[string]bool{}
	var missing []llama.HFFile
	for _, f := range files {
		localName := llama.LocalModelFilename(f.Filename)
		remote[localName] = true
		if !local[localName] {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var stale []string
	for name := range local {
		if !remote[name] {
			stale = append(stale, filepath.Join(modelDir, name))
		}
	}
	return &ModelUpdate{Missing: missing, Stale: stale}
}

// RemoveStale deletes the outdated model files (frees disk before the new
// download starts). Partial .part files for stale names are removed too.
func (mu *ModelUpdate) RemoveStale() {
	if mu == nil {
		return
	}
	for _, p := range mu.Stale {
		_ = os.Remove(p)
		_ = os.Remove(p + ".part")
	}
}
