package sonutgit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureRepo_RefuseHomedir(t *testing.T) {
	home, _ := os.UserHomeDir()
	_, _, err := EnsureRepo(context.Background(), home)
	if err == nil {
		t.Fatal("EnsureRepo should refuse the home directory")
	}
}

func TestEnsureRepo_RefuseRoot(t *testing.T) {
	_, _, err := EnsureRepo(context.Background(), "/")
	if err == nil {
		t.Fatal("EnsureRepo should refuse the filesystem root")
	}
}

func TestEnsureRepo_CreatesRepo(t *testing.T) {
	dir := t.TempDir()
	root, created, err := EnsureRepo(context.Background(), dir)
	if err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	if !created {
		t.Error("expected created=true for a new dir")
	}
	if root != dir {
		t.Errorf("root = %q, want %q", root, dir)
	}
	// .gitignore must exist and contain .atcode/
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatal("no .gitignore created")
	}
	if !strings.Contains(string(data), ".atcode/") {
		t.Error(".gitignore does not contain .atcode/")
	}
}

func TestEnsureRepo_NoSecretInInitialCommit(t *testing.T) {
	dir := t.TempDir()
	// Write a secret that git add -A would have staged.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureRepo(context.Background(), dir); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	// The .env must not appear in the initial commit.
	result, _ := defaultRunGit(context.Background(), dir, "show", "--stat", "HEAD")
	if strings.Contains(result.Stdout, ".env") {
		t.Error(".env was committed in the initial commit — secrets leaked into git history")
	}
}

func TestEnsureRepo_IdempotentOnExistingRepo(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureRepo(context.Background(), dir); err != nil {
		t.Fatalf("first EnsureRepo: %v", err)
	}
	_, created, err := EnsureRepo(context.Background(), dir)
	if err != nil {
		t.Fatalf("second EnsureRepo: %v", err)
	}
	if created {
		t.Error("expected created=false on existing repo")
	}
}

func TestEnsureSonutExcluded(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureRepo(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// Call again (pre-existing repo path) to trigger ensureSonutExcluded.
	if _, _, err := EnsureRepo(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	exclude := filepath.Join(dir, ".git", "info", "exclude")
	data, err := os.ReadFile(exclude)
	if err != nil {
		t.Fatal("exclude file missing")
	}
	if !strings.Contains(string(data), ".atcode/") {
		t.Error(".git/info/exclude does not contain .atcode/")
	}
}
