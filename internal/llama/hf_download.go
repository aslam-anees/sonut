package llama

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	hfRepoID  = "anees-aslam-ai/atcode-v1.0"
	hfBaseURL = "https://huggingface.co"
)

// HFFile represents a file entry in a HuggingFace repository.
type HFFile struct {
	Filename string `json:"rfilename"`
	Size     int64  `json:"size"`
}

// ListHFModelFiles queries the HF API and returns all GGUF files in the sonut repo.
func ListHFModelFiles() ([]HFFile, error) {
	url := fmt.Sprintf("%s/api/models/%s", hfBaseURL, hfRepoID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	setModelAuthorization(req)
	req.Header.Set("User-Agent", "atcode/1.1")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to reach model repository: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("model access denied; set ATCODE_MODEL_TOKEN and retry")
		}
		return nil, fmt.Errorf("model repository returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Siblings []HFFile `json:"siblings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to parse repository info: %w", err)
	}

	var gguf []HFFile
	for _, f := range result.Siblings {
		if strings.HasSuffix(strings.ToLower(f.Filename), ".gguf") {
			gguf = append(gguf, f)
		}
	}

	// Sort: main model first, mmproj second
	sort.SliceStable(gguf, func(i, j int) bool {
		iMM := strings.Contains(strings.ToLower(gguf[i].Filename), "mmproj")
		jMM := strings.Contains(strings.ToLower(gguf[j].Filename), "mmproj")
		if iMM == jMM {
			return false
		}
		return !iMM // main model before mmproj
	})

	return gguf, nil
}

// DownloadHFFile downloads a single file from the HF repo with resumable support.
// onProgress is called with (bytesDownloaded, totalBytes) as download progresses.
func DownloadHFFile(filename, localPath string, onProgress func(downloaded, total int64)) error {
	url := fmt.Sprintf("%s/%s/resolve/main/%s", hfBaseURL, hfRepoID, filename)

	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return fmt.Errorf("failed to create model directory: %w", err)
	}

	tmpPath := localPath + ".part"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	setModelAuthorization(req)
	req.Header.Set("User-Agent", "atcode/1.1")

	// Attempt to resume a partial download
	var alreadyDownloaded int64
	if info, err := os.Stat(tmpPath); err == nil {
		alreadyDownloaded = info.Size()
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", alreadyDownloaded))
	}

	client := &http.Client{
		Timeout: 0,
		Transport: &http.Transport{
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("model access denied; set ATCODE_MODEL_TOKEN and retry")
		}
		return fmt.Errorf("model component download failed with HTTP %d", resp.StatusCode)
	}

	var f *os.File
	if alreadyDownloaded > 0 && resp.StatusCode == http.StatusPartialContent {
		f, err = os.OpenFile(tmpPath, os.O_WRONLY|os.O_APPEND, 0o600)
	} else {
		alreadyDownloaded = 0
		f, err = os.Create(tmpPath)
	}
	if err != nil {
		return fmt.Errorf("failed to open download file: %w", err)
	}
	defer f.Close()

	total := resp.ContentLength
	if total > 0 {
		total += alreadyDownloaded
	}

	buf := make([]byte, 64*1024)
	written := alreadyDownloaded

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := f.Write(buf[:n]); writeErr != nil {
				return fmt.Errorf("write error: %w", writeErr)
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("download interrupted: %w", readErr)
		}
	}
	f.Close()

	if err := os.Rename(tmpPath, localPath); err != nil {
		return fmt.Errorf("failed to finalize download: %w", err)
	}
	return nil
}

func setModelAuthorization(req *http.Request) {
	token := strings.TrimSpace(os.Getenv("ATCODE_MODEL_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("HF_TOKEN"))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// LocalModelFilename maps repository-side component names to stable @code
// names so implementation and supplier labels never become part of ~/.atcode
// or the startup UI.
func LocalModelFilename(remoteName string) string {
	lower := strings.ToLower(filepath.Base(remoteName))
	switch {
	case strings.Contains(lower, "mmproj"):
		return "atcode-vision.gguf"
	case isDraftModelFilename(lower):
		return "atcode-accelerator.gguf"
	default:
		return "atcode-model.gguf"
	}
}

// CheckModelsPresent scans ~/.atcode/models/ for downloaded GGUF model files.
// Returns (mainModelPath, mmProjPath, mainFound).
func CheckModelsPresent() (string, string, bool) {
	_ = MigrateLegacyModelNames()
	modelDir := GetModelDir()
	entries, err := os.ReadDir(modelDir)
	if err != nil {
		return "", "", false
	}

	// Current atcode/Bonsai models take priority, followed by older families.
	var mainPath, mmProjPath string
	rank := func(lower string) int {
		switch {
		case strings.Contains(lower, "atcode") ||
			strings.Contains(lower, "bonsai") ||
			(strings.Contains(lower, "dspark") && strings.Contains(lower, "27b")):
			return 5
		case strings.Contains(lower, "qwopus"):
			return 4
		case strings.HasPrefix(lower, "omnicoder-9b"):
			return 3
		case strings.Contains(lower, "omnicoder"):
			return 2
		case strings.HasPrefix(lower, "@0.1-"):
			return 1
		}
		return 0
	}
	mainRank, mmRank := -1, -1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".gguf") {
			continue
		}
		// Speculative-decoding draft models (e.g. Qwopus3.5-9B-Coder-DFlash) are
		// not standalone LLMs — never select one as main or mmproj. They are
		// picked up separately by FindDraftModel.
		if isDraftModelFilename(lower) {
			continue
		}
		r := rank(lower)
		if strings.Contains(lower, "mmproj") {
			if r > mmRank {
				mmProjPath = filepath.Join(modelDir, name)
				mmRank = r
			}
		} else {
			if r > mainRank {
				mainPath = filepath.Join(modelDir, name)
				mainRank = r
			}
		}
	}
	return mainPath, mmProjPath, mainPath != ""
}

// FindDraftModel scans the model directory for a speculative-decoding sidecar.
func FindDraftModel() string {
	_ = MigrateLegacyModelNames()
	modelDir := GetModelDir()
	entries, err := os.ReadDir(modelDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		if !strings.HasSuffix(lower, ".gguf") {
			continue
		}
		if isDraftModelFilename(lower) {
			return filepath.Join(modelDir, e.Name())
		}
	}
	return ""
}

func isDraftModelFilename(lower string) bool {
	if strings.HasPrefix(lower, "atcode-accelerator") {
		return true
	}
	if strings.Contains(lower, "dflash") || strings.Contains(lower, "draft") {
		return true
	}
	if !strings.Contains(lower, "dspark") {
		return false
	}
	// "dspark" is also part of Prism's full Bonsai-27B main-model name.
	// Only a bare/non-27B dspark file is a speculative sidecar.
	return !strings.Contains(lower, "atcode") &&
		!strings.Contains(lower, "bonsai") &&
		!strings.Contains(lower, "27b")
}

// EnsureAtcodeDir creates ~/.atcode and ~/.atcode/model/ if they do not exist.
// "model" (singular) is the canonical @code location; GetModelDir still
// falls back to the legacy plural directory when it holds GGUFs.
func EnsureAtcodeDir() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	atcodeDir := filepath.Join(home, ".atcode")
	for _, dir := range []string{atcodeDir, filepath.Join(atcodeDir, "model")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}
	if err := MigrateLegacyRuntimeNames(); err != nil {
		return fmt.Errorf("failed to migrate the atcode engine layout: %w", err)
	}
	if err := MigrateLegacyModelNames(); err != nil {
		return fmt.Errorf("failed to migrate atcode model names: %w", err)
	}
	return nil
}

// MigrateLegacyModelNames removes supplier/model-family labels from GGUF files
// stored under ~/.atcode. Files already carrying an @code name are untouched.
func MigrateLegacyModelNames() error {
	modelDir := GetModelDir()
	entries, err := os.ReadDir(modelDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		lower := strings.ToLower(entry.Name())
		if !strings.HasSuffix(lower, ".gguf") {
			continue
		}
		if strings.HasPrefix(lower, "atcode-") || strings.HasPrefix(lower, "@0.1-") {
			continue
		}
		if !strings.Contains(lower, "prism") &&
			!strings.Contains(lower, "bonsai") &&
			!strings.Contains(lower, "dspark") {
			continue
		}
		oldPath := filepath.Join(modelDir, entry.Name())
		newPath := filepath.Join(modelDir, LocalModelFilename(entry.Name()))
		if err := renameIfTargetMissing(oldPath, newPath); err != nil {
			return err
		}
	}
	return nil
}
