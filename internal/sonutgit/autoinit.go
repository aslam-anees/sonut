package sonutgit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// dangerousDirs are directories we refuse to git-init because doing so would
// track the user's entire home directory or system root.
func isDangerousDir(dir string) bool {
	home, _ := os.UserHomeDir()
	for _, d := range []string{home, "/", "/home", "/Users", "/root"} {
		if d != "" && filepath.Clean(dir) == filepath.Clean(d) {
			return true
		}
	}
	return false
}

// EnsureRepo makes sure dir is inside a git repository. If it isn't, it runs
// `git init` there and creates an initial empty commit so that diff operations
// (which require at least one commit) work immediately. The root of the newly
// created repo is returned.
//
// If dir is already a git repo the function is a no-op and returns its root.
func EnsureRepo(ctx context.Context, dir string) (root string, created bool, err error) {
	dir, err = resolveCwd(dir)
	if err != nil {
		return "", false, err
	}

	// Check if already a git repo.
	result, _ := defaultRunGit(ctx, dir, "rev-parse", "--show-toplevel")
	if result.ExitCode == 0 {
		root := filepath.Clean(strings.TrimSpace(result.Stdout))
		// Best-effort: ensure .atcode/ is excluded so deleted-file contents
		// never leak into git history for pre-existing repos.
		_ = ensureSonutExcluded(root)
		return root, false, nil
	}

	// Refuse to init a dangerous directory (home dir, fs root, etc.).
	if isDangerousDir(dir) {
		return "", false, fmt.Errorf("refusing to git init %s: too broad a directory; open a project subdirectory instead", dir)
	}

	// Not a git repo — initialise one.
	if err := runGitInit(ctx, dir); err != nil {
		return "", false, fmt.Errorf("git init: %w", err)
	}

	// Set minimal identity so git commit doesn't fail in CI / new environments.
	_ = setGitConfigIfMissing(ctx, dir, "user.email", "sonut@local")
	_ = setGitConfigIfMissing(ctx, dir, "user.name", "@code")

	// Ensure a .gitignore for the sonut working directory exists so internal
	// state isn't tracked.
	_ = writeGitignore(dir)

	// git commit requires at least one file; commit .gitignore or create an
	// empty sentinel if the directory is truly empty.
	if err := stageAndCommitInit(ctx, dir); err != nil {
		return "", false, fmt.Errorf("initial commit: %w", err)
	}

	return dir, true, nil
}

func runGitInit(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "git", "init", "-b", "main")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Older git versions don't support -b; retry without it.
		cmd2 := exec.CommandContext(ctx, "git", "init")
		cmd2.Dir = dir
		if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
			return fmt.Errorf("%s / %s", string(out), string(out2))
		}
	}
	return nil
}

func setGitConfigIfMissing(ctx context.Context, dir, key, value string) error {
	// Only set locally; don't override global user preferences.
	check := exec.CommandContext(ctx, "git", "config", "--local", key)
	check.Dir = dir
	if check.Run() == nil {
		return nil // already set
	}
	set := exec.CommandContext(ctx, "git", "config", "--local", key, value)
	set.Dir = dir
	return set.Run()
}

func writeGitignore(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	// Don't overwrite existing .gitignore.
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	content := "# @code internal state\n.atcode/\n"
	return os.WriteFile(path, []byte(content), 0644)
}

// ensureSonutExcluded appends .atcode/ to .git/info/exclude for repos that
// already have their own .gitignore. This keeps the trash bin out of history
// without touching user-managed ignore files.
func ensureSonutExcluded(repoRoot string) error {
	excludeDir := filepath.Join(repoRoot, ".git", "info")
	if err := os.MkdirAll(excludeDir, 0755); err != nil {
		return err
	}
	excludePath := filepath.Join(excludeDir, "exclude")
	data, _ := os.ReadFile(excludePath)
	if strings.Contains(string(data), ".atcode/") {
		return nil
	}
	f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n# @code internal state\n.atcode/\n")
	return err
}

func stageAndCommitInit(ctx context.Context, dir string) error {
	// Stage only .gitignore to avoid accidentally committing secrets (.env),
	// node_modules, build artifacts, or other sensitive files on first init.
	gitignorePath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		add := exec.CommandContext(ctx, "git", "add", ".gitignore")
		add.Dir = dir
		if out, err := add.CombinedOutput(); err != nil {
			return fmt.Errorf("git add .gitignore: %s", string(out))
		}
	}
	commit := exec.CommandContext(ctx, "git", "commit", "--allow-empty", "-m", "chore: sonut workspace initialised")
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		return fmt.Errorf("git commit: %s", string(out))
	}
	return nil
}
