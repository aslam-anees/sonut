package llama

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EngineAuthFile holds a per-user local-server key, never a published credential
// or a command-line secret. An explicit file override also isolates test runs.
func EngineAuthFile() string {
	if path := strings.TrimSpace(os.Getenv("ATCODE_ENGINE_KEY_FILE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".atcode", "engine-api-key")
}

// PrepareEngineAuth atomically creates the shared key before starting a server.
// Concurrent CLI/TUI launches use the winner's complete file. Invalid or
// excessively permissive existing files fail closed instead of being replaced.
func PrepareEngineAuth() error {
	path := EngineAuthFile()
	if path == "" {
		return errors.New("cannot resolve local engine key path")
	}
	if _, err := readEngineKey(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create local engine key directory: %w", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate local engine key: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".engine-key-*")
	if err != nil {
		return fmt.Errorf("create local engine key: %w", err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(hex.EncodeToString(secret) + "\n"); err != nil {
		_ = f.Close()
		return fmt.Errorf("write local engine key: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close local engine key: %w", err)
	}
	// A hard link publishes the complete, owner-only file without overwriting a
	// concurrent winner. Both paths are in the same directory/filesystem.
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("publish local engine key: %w", err)
	}
	_, err = readEngineKey(path)
	return err
}

func readEngineKey(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("local engine key must be a regular file, not a symlink")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("local engine key must have owner-only permissions (0600)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read local engine key: %w", err)
	}
	key := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("local engine key is invalid; expected 32 random bytes encoded as hex")
	}
	return key, nil
}

// setAuth never sends a built-in/shared password. Missing or invalid local key
// files result in an unauthenticated request, which the protected server rejects.
func setAuth(req *http.Request) {
	host := strings.TrimSuffix(strings.ToLower(req.URL.Hostname()), ".")
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return
	}
	if key, err := readEngineKey(EngineAuthFile()); err == nil {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}
