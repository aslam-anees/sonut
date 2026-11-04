package llama

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEngineKeyIsPrivateStableAndSharedAcrossConcurrentStarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys", "engine-key")
	t.Setenv("ATCODE_ENGINE_KEY_FILE", path)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := PrepareEngineAuth(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	key, err := readEngineKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 64 {
		t.Fatal("expected random 256-bit hex key")
	}
	if err := PrepareEngineAuth(); err != nil {
		t.Fatal(err)
	}
	again, _ := readEngineKey(path)
	if again != key {
		t.Fatal("a subsequent launch rotated the shared key")
	}
	req := httptest.NewRequest("GET", "http://localhost/v1/models", nil)
	setAuth(req)
	if req.Header.Get("Authorization") != "Bearer "+key {
		t.Fatal("client did not use the shared key")
	}
	remote := httptest.NewRequest("GET", "https://example.com/v1/models", nil)
	setAuth(remote)
	if remote.Header.Get("Authorization") != "" {
		t.Fatal("local engine key must never be sent to a remote host")
	}
}

func TestEngineKeyRejectsInvalidPermissionsAndSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	t.Setenv("ATCODE_ENGINE_KEY_FILE", path)
	if err := PrepareEngineAuth(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := PrepareEngineAuth(); err == nil {
			t.Fatal("accepted a group/world-readable key")
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "key-link")
	if err := os.Symlink(path, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	t.Setenv("ATCODE_ENGINE_KEY_FILE", link)
	if err := PrepareEngineAuth(); err == nil {
		t.Fatal("accepted a key symlink")
	}
}

func TestMissingEngineKeyDoesNotSendBundledCredential(t *testing.T) {
	t.Setenv("ATCODE_ENGINE_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	req := httptest.NewRequest("GET", "http://localhost/v1/models", nil)
	setAuth(req)
	if req.Header.Get("Authorization") != "" {
		t.Fatal("sent a fallback credential")
	}
}

func TestServerStartupReturnsWhenChildExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	exited := make(chan error, 1)
	exited <- errors.New("unsupported runtime flag")
	if err := waitForServerProcess(ctx, 0, exited); err == nil || !strings.Contains(err.Error(), "unsupported runtime flag") {
		t.Fatalf("expected the process failure instead of an endless health wait, got %v", err)
	}
}
