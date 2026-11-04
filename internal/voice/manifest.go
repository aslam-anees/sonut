package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ManifestSchemaVersion = 1
	LightProfileVersion   = "light-1"
	MaxVoiceArtifactBytes = 4 * 1024 * 1024 * 1024
)

// DefaultInstallRoot is the per-user location for the voice pack. Keeping the
// path in this package makes the TUI, installer, and release diagnostics agree
// without consulting the repository or ref/ at runtime.
func DefaultInstallRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".atcode", "voice")
}

type Artifact struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	URL        string `json:"url"`
	TargetPath string `json:"targetPath"`
	Bytes      int64  `json:"bytes"`
	SHA256     string `json:"sha256"`
	License    string `json:"license"`
	Required   bool   `json:"required"`
}

type Manifest struct {
	SchemaVersion    int        `json:"schemaVersion"`
	Profile          string     `json:"profile"`
	ProfileVersion   string     `json:"profileVersion"`
	AudioCPPRevision string     `json:"audioCppRevision"`
	RequiredBytes    int64      `json:"requiredBytes"`
	Artifacts        []Artifact `json:"artifacts"`
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return fmt.Errorf("unsupported voice manifest schema %d", m.SchemaVersion)
	}
	if m.Profile != "light" || m.ProfileVersion == "" {
		return fmt.Errorf("voice manifest must describe the light profile")
	}
	if len(m.Artifacts) == 0 {
		return fmt.Errorf("voice manifest has no artifacts")
	}
	for _, artifact := range m.Artifacts {
		if strings.TrimSpace(artifact.Name) == "" || strings.TrimSpace(artifact.TargetPath) == "" {
			return fmt.Errorf("voice artifact has no name or target path")
		}
		if !strings.HasPrefix(artifact.URL, "https://") {
			return fmt.Errorf("voice artifact %q must use HTTPS", artifact.Name)
		}
		if artifact.Bytes <= 0 || artifact.Bytes > MaxVoiceArtifactBytes {
			return fmt.Errorf("voice artifact %q has invalid byte length", artifact.Name)
		}
		hash := strings.ToLower(strings.TrimSpace(artifact.SHA256))
		if len(hash) != sha256.Size*2 {
			return fmt.Errorf("voice artifact %q has invalid SHA-256", artifact.Name)
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return fmt.Errorf("voice artifact %q has invalid SHA-256: %w", artifact.Name, err)
		}
		if artifact.License == "" {
			return fmt.Errorf("voice artifact %q has no license", artifact.Name)
		}
	}
	if m.RequiredBytes <= 0 {
		return fmt.Errorf("voice manifest has no disk requirement")
	}
	return nil
}

// DefaultLightManifest is intentionally filled from the checked-in release
// manifest rather than probing a mutable model hub at runtime. Keep this
// function as the single code path used by /voice models and /voice setup.
func DefaultLightManifest() Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Profile:       "light", ProfileVersion: LightProfileVersion,
		AudioCPPRevision: "238ab6a9e321c17de8e120559f57efeedaeb1345",
		RequiredBytes:    1450000000,
		Artifacts: []Artifact{
			{Name: "qwen3-asr-0.6b-q8_0", Version: "Qwen3-ASR-0.6B", URL: "https://huggingface.co/audio-cpp/audio.cpp-gguf/resolve/09fe073ba154561f4474162e8bd4ab233a848eca/Qwen3-ASR-0.6B-GGUF/qwen3-asr-0.6b-q8_0.gguf", TargetPath: "models/qwen3-asr-0.6b-q8_0.gguf", Bytes: 1151272416, SHA256: "6c44ec2fb4cee513892d7863c1fcc3ea6b699ffa4d899b0ef4ab19956d9544f7", License: "Apache-2.0 (Qwen/Qwen3-ASR-0.6B model card)", Required: true},
			{Name: "supertonic-3-f16", Version: "Supertonic 3", URL: "https://huggingface.co/audio-cpp/audio.cpp-gguf/resolve/09fe073ba154561f4474162e8bd4ab233a848eca/Supertonic-3-GGUF/supertonic-3-f16.gguf", TargetPath: "models/supertonic-3-f16.gguf", Bytes: 255637890, SHA256: "bc9b5fa3b05015d3a0216005aa52484f4167c8830a65b6e66aeb84e23cc5d811", License: "OpenRAIL (Supertone/supertonic-3 model card)", Required: true},
			{Name: "silero-vad-16k", Version: "audio.cpp bundled Silero VAD", URL: "https://raw.githubusercontent.com/0xShug0/audio.cpp/238ab6a9e321c17de8e120559f57efeedaeb1345/assets/framework/models/silero_vad/silero_vad_16k.safetensors", TargetPath: "assets/framework/models/silero_vad/silero_vad_16k.safetensors", Bytes: 1239748, SHA256: "c59271c284ae9c8335d795d60e0bfdb71aaaceec578d9bd9ffc1b8153c319ea1", License: "MIT (Silero VAD)", Required: true},
		},
	}
}

type DownloadProgress struct {
	Artifact Artifact
	Received int64
	Total    int64
}

type InstallerOptions struct {
	Root     string
	Client   *http.Client
	Progress func(DownloadProgress)
	FreeDisk func(root string, required int64) error
}

func Install(ctx context.Context, manifest Manifest, options InstallerOptions) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if options.Root == "" {
		return fmt.Errorf("voice install root is required")
	}
	if options.Client == nil {
		options.Client = http.DefaultClient
	}
	if options.FreeDisk == nil {
		options.FreeDisk = freeDisk
	}
	if err := options.FreeDisk(options.Root, manifest.RequiredBytes); err != nil {
		return err
	}
	if err := os.MkdirAll(options.Root, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(options.Root, ".install.lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("voice installation is already running or locked: %w", err)
	}
	defer func() { _ = lock.Close(); _ = os.Remove(lockPath) }()
	for _, artifact := range manifest.Artifacts {
		if err := downloadArtifact(ctx, options, artifact); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(options.Root, "manifest.json.part")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(options.Root, "manifest.json")); err != nil {
		return err
	}
	return nil
}

func downloadArtifact(ctx context.Context, options InstallerOptions, artifact Artifact) error {
	target := filepath.Join(options.Root, filepath.Clean(artifact.TargetPath))
	root := filepath.Clean(options.Root) + string(os.PathSeparator)
	if !strings.HasPrefix(target, root) {
		return fmt.Errorf("voice artifact %q escapes install root", artifact.Name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if stat, err := os.Stat(target); err == nil && stat.Size() == artifact.Bytes {
		if verifyFile(target, artifact) == nil {
			return nil
		}
	}
	part := target + ".part"
	var received int64
	if stat, err := os.Stat(part); err == nil {
		received = stat.Size()
	}
	if received > artifact.Bytes {
		if err := os.Truncate(part, 0); err != nil {
			return err
		}
		received = 0
	}
	if received == artifact.Bytes && received > 0 {
		if err := verifyFile(part, artifact); err == nil {
			return promoteVerifiedFile(part, target)
		}
		if err := os.Truncate(part, 0); err != nil {
			return err
		}
		received = 0
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	if received > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", received))
	}
	response, err := options.Client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifact.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download %s: HTTP %s", artifact.Name, response.Status)
	}
	if received > 0 && response.StatusCode == http.StatusOK {
		if err := os.Truncate(part, 0); err != nil {
			return err
		}
		received = 0
	}
	if received > 0 && response.StatusCode == http.StatusPartialContent {
		wantRange := fmt.Sprintf("bytes %d-", received)
		if !strings.HasPrefix(strings.TrimSpace(response.Header.Get("Content-Range")), wantRange) {
			return fmt.Errorf("download %s returned an invalid resume range", artifact.Name)
		}
	}
	if response.ContentLength >= 0 && received+response.ContentLength > artifact.Bytes {
		return fmt.Errorf("download %s exceeds expected size", artifact.Name)
	}
	file, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	buffer := make([]byte, 128*1024)
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			received += int64(read)
			if received > artifact.Bytes {
				return fmt.Errorf("download %s exceeds expected size", artifact.Name)
			}
			if _, err := file.Write(buffer[:read]); err != nil {
				return err
			}
			if options.Progress != nil {
				options.Progress(DownloadProgress{Artifact: artifact, Received: received, Total: artifact.Bytes})
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("download %s: %w", artifact.Name, readErr)
		}
	}
	if received != artifact.Bytes {
		return fmt.Errorf("download %s has %d bytes; expected %d", artifact.Name, received, artifact.Bytes)
	}
	if err := verifyFile(part, artifact); err != nil {
		// A completed but corrupt part must not poison the next resumable retry.
		// Interrupted/network-error parts are intentionally retained above so a
		// later invocation can continue from the last verified byte boundary.
		_ = os.Remove(part)
		return err
	}
	return promoteVerifiedFile(part, target)
}

func promoteVerifiedFile(part, target string) error {
	if runtime.GOOS != "windows" {
		return os.Rename(part, target)
	}
	// Windows does not replace an existing file with os.Rename. Keep a rollback
	// name while promoting the verified part so a failed promotion can recover
	// the previous artifact rather than leaving a corrupt or missing model.
	backup := target + ".old"
	_ = os.Remove(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(part, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func verifyFile(path string, artifact Artifact) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, MaxVoiceArtifactBytes+1)); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, artifact.SHA256) {
		return fmt.Errorf("SHA-256 mismatch for %s: got %s", artifact.Name, actual)
	}
	return nil
}
