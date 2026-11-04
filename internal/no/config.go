package no

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ConfigFileName is the repo-local config file, matching upstream
// no-mistakes' `.no-mistakes.yaml`. Only the guard-relevant subset of the
// upstream schema is honored here (see WIRING.md).
const ConfigFileName = ".no-mistakes.yaml"

// Config is the guard-relevant subset of no-mistakes' RepoConfig. Any field
// omitted from the file falls back to a permissive-but-sane default so a
// repo with no config at all still gets baseline protection without any
// setup.
type Config struct {
	// ProtectedBranches lists branches that must not receive direct commits
	// or force-pushes from the guarded git layer. Defaults to
	// ["main", "master"].
	ProtectedBranches []string `yaml:"protected_branches"`

	// BlockForcePushDataLoss, when true (the default), refuses a force-push
	// that would discard remote commits the caller never incorporated. This
	// mirrors upstream's resolveForcePushDecision safety net.
	BlockForcePushDataLoss *bool `yaml:"block_force_push_data_loss"`

	// AllowProtectedBranchCommits, when true, disables the protected-branch
	// guard entirely. Default false (guard enabled).
	AllowProtectedBranchCommits bool `yaml:"allow_protected_branch_commits"`
}

// DefaultConfig returns the permissive-but-sane defaults used when no
// .no-mistakes.yaml exists in the repo: protected-branch and force-push
// guards are both on, but nothing requires the user to opt in first.
func DefaultConfig() Config {
	t := true
	return Config{
		ProtectedBranches:      []string{"main", "master"},
		BlockForcePushDataLoss: &t,
	}
}

// blockForcePush reports the effective value of BlockForcePushDataLoss,
// defaulting to true when unset.
func (c Config) blockForcePush() bool {
	if c.BlockForcePushDataLoss == nil {
		return true
	}
	return *c.BlockForcePushDataLoss
}

// isProtectedBranch reports whether branch is in the configured protected
// list. Empty branch names never match.
func (c Config) isProtectedBranch(branch string) bool {
	if branch == "" || c.AllowProtectedBranchCommits {
		return false
	}
	for _, b := range c.ProtectedBranches {
		if b == branch {
			return true
		}
	}
	return false
}

// LoadConfig reads .no-mistakes.yaml from repoRoot. A missing file is not an
// error: it returns DefaultConfig(), keeping guards permissive-but-sane
// without requiring any setup. A present-but-malformed file is a real error
// so a broken config is never silently ignored.
func LoadConfig(repoRoot string) (Config, error) {
	path := filepath.Join(repoRoot, ConfigFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return Config{}, err
	}

	cfg := DefaultConfig()
	// Decode into a fresh struct so an explicit `protected_branches: []`
	// in the file can still override the default list, while an absent key
	// keeps the default via the zero-value merge below.
	var raw Config
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}
	if raw.ProtectedBranches != nil {
		cfg.ProtectedBranches = raw.ProtectedBranches
	}
	if raw.BlockForcePushDataLoss != nil {
		cfg.BlockForcePushDataLoss = raw.BlockForcePushDataLoss
	}
	cfg.AllowProtectedBranchCommits = raw.AllowProtectedBranchCommits
	return cfg, nil
}
