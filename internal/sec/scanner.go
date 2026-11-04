package sec

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aistatine/sonut/internal/secrets"
)

type rule struct {
	id          string
	title       string
	description string
	category    string
	baseScore   int
	pattern     *regexp.Regexp
	applies     func(string) bool
	remediation string
	factor      string
}

type fileSignals struct {
	path       string
	nodeID     string
	testLike   bool
	exposed    bool
	privileged bool
	secret     bool
	workflow   bool
	unpinned   bool
	findings   []int
}

var (
	sensitiveAssignment = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|authorization)\b(\s*[:=]\s*)([^\s,;}\]]+)`)
	dotenvAssignment    = regexp.MustCompile(`(?m)^[A-Z][A-Z0-9_]{2,}=(.+)$`)
)

var postureRules = []rule{
	{
		id: "network.public-cidr", title: "Ingress is configured for a public CIDR",
		description: "A network policy is configured with a public CIDR. This is configuration evidence of a public ingress boundary, not proof that an endpoint is reachable from the public internet.",
		category:    "exposure", baseScore: 78,
		pattern:     regexp.MustCompile(`(?i)(0\.0\.0\.0/0|::/0)`),
		applies:     configLike,
		remediation: "Restrict the source range to the smallest trusted CIDR and place public endpoints behind authenticated ingress.",
		factor:      "internet exposed",
	},
	{
		id: "network.wildcard-cors", title: "Wildcard cross-origin access is enabled",
		description: "A wildcard CORS policy can let untrusted sites read responses when it is combined with permissive authentication or credential handling.",
		category:    "exposure", baseScore: 58,
		pattern:     regexp.MustCompile(`(?i)(access-control-allow-origin.{0,18}\*|allow[_-]?origins?.{0,18}[\["']\*|cors.{0,24}origin.{0,12}\*)`),
		applies:     sourceOrConfig,
		remediation: "Replace the wildcard with an explicit allowlist of trusted origins and keep credentialed requests disabled unless required.",
		factor:      "browser reachable",
	},
	{
		id: "network.public-bind", title: "Service is configured to bind on every interface",
		description: "Binding to 0.0.0.0 or :: configures a wildcard listener. Reachability still depends on the host, network and runtime controls.",
		category:    "exposure", baseScore: 48,
		pattern:     regexp.MustCompile(`(?i)(listen|bind|host|address)[^\n]{0,30}(0\.0\.0\.0|\[::\])`),
		applies:     sourceOrConfig,
		remediation: "Bind local-only services to 127.0.0.1 and document the firewall or authenticated proxy protecting intentionally public listeners.",
		factor:      "network reachable",
	},
	{
		id: "cloud.public-storage", title: "Storage may be publicly readable",
		description: "The configuration contains a public storage ACL or disables a public-access block.",
		category:    "data", baseScore: 82,
		pattern:     regexp.MustCompile(`(?i)(public-read|public_read|block_public_acls\s*[:=]\s*false|blockpublicacls\s*:\s*false|allusers)`),
		applies:     configLike,
		remediation: "Enable the provider's public-access block, remove public ACLs, and grant access through a scoped workload identity.",
		factor:      "sensitive data exposed",
	},
	{
		id: "runtime.privileged-container", title: "Container runs with host-level privilege",
		description: "Privileged containers can bypass normal isolation and often provide a direct path to the host.",
		category:    "identity", baseScore: 82,
		pattern:     regexp.MustCompile(`(?i)\bprivileged\s*:\s*true\b`),
		applies:     containerConfig,
		remediation: "Remove privileged mode, drop all capabilities, add back only the minimum required capabilities, and enable a read-only root filesystem.",
		factor:      "high privilege",
	},
	{
		id: "runtime.host-network", title: "Workload shares the host network",
		description: "Host networking removes a major isolation boundary and can expose host-local services to the workload.",
		category:    "identity", baseScore: 72,
		pattern:     regexp.MustCompile(`(?i)\bhostnetwork\s*:\s*true\b|\bnetwork_mode\s*:\s*["']?host`),
		applies:     containerConfig,
		remediation: "Use an isolated network namespace and declare only the ports and network policies the workload needs.",
		factor:      "host access",
	},
	{
		id: "runtime.docker-socket", title: "Container can control the Docker daemon",
		description: "Mounting the Docker socket gives the container effective root control of the host.",
		category:    "identity", baseScore: 90,
		pattern:     regexp.MustCompile(`(?i)/var/run/docker\.sock`),
		applies:     containerConfig,
		remediation: "Remove the Docker socket mount. If orchestration is required, use a narrowly scoped broker with an explicit allowlist.",
		factor:      "host control",
	},
	{
		id: "runtime.root-user", title: "Runtime is explicitly configured as root",
		description: "A process running as root increases blast radius if the service or a dependency is compromised.",
		category:    "identity", baseScore: 48,
		pattern:     regexp.MustCompile(`(?im)^\s*(user\s+root|runAsUser\s*:\s*0|user\s*:\s*["']?0(?::0)?["']?)\s*$`),
		applies:     containerConfig,
		remediation: "Create a dedicated non-root user, set runAsNonRoot, and make writable paths explicit.",
		factor:      "elevated identity",
	},
	{
		id: "ci.write-all", title: "CI workflow has repository-wide write permission",
		description: "A compromised action or injected workflow step could modify repository contents, releases, packages, or tokens.",
		category:    "identity", baseScore: 78,
		pattern:     regexp.MustCompile(`(?im)^\s*permissions\s*:\s*write-all\s*$`),
		applies:     workflowFile,
		remediation: "Set top-level permissions to read-all (or {}) and grant narrowly scoped write permissions only to the job that needs them.",
		factor:      "write-capable identity",
	},
	{
		id: "ci.unpinned-action", title: "CI action is pinned to a mutable branch",
		description: "A branch or floating tag can change after review, turning a trusted workflow dependency into a supply-chain path.",
		category:    "supply-chain", baseScore: 56,
		pattern:     regexp.MustCompile(`(?im)^\s*uses\s*:\s*[^@\s]+@(main|master|latest)\s*$`),
		applies:     workflowFile,
		remediation: "Pin third-party actions to a full commit SHA and use a dependency updater to keep the pin current.",
		factor:      "mutable dependency",
	},
	{
		id: "ci.pull-request-target", title: "Privileged pull-request workflow requires review",
		description: "pull_request_target runs in the base repository context and can expose write tokens or secrets if untrusted code is checked out or executed.",
		category:    "supply-chain", baseScore: 67,
		pattern:     regexp.MustCompile(`(?im)^\s*pull_request_target\s*:`),
		applies:     workflowFile,
		remediation: "Avoid executing fork-controlled code in pull_request_target. Split untrusted build work from a minimal privileged follow-up workflow.",
		factor:      "untrusted contribution path",
	},
	{
		id: "transport.tls-verification-disabled", title: "TLS certificate verification is disabled",
		description: "Disabling verification lets an on-path attacker impersonate the intended service and read or modify protected traffic.",
		category:    "exposure", baseScore: 76,
		pattern:     regexp.MustCompile(`(?i)(insecureSkipVerify\s*[:=]\s*true|rejectUnauthorized\s*:\s*false|(tls[_-]?verify|verify[_-]?(ssl|tls|cert(?:ificate)?))\s*[:=]\s*false|curl\s+[^\n]*\s-k(?:\s|$))`),
		applies:     sourceOrConfig,
		remediation: "Restore certificate verification and configure the correct CA bundle or service identity.",
		factor:      "untrusted network",
	},
	{
		id: "filesystem.world-writable", title: "World-writable permissions are configured",
		description: "Mode 0777 lets any local identity modify the target and can create a code-execution or tampering path.",
		category:    "identity", baseScore: 58,
		pattern:     regexp.MustCompile(`(?i)(chmod\s+(?:-R\s+)?0?777\b|mode\s*[:=]\s*["']?0?777)`),
		applies:     sourceOrConfig,
		remediation: "Use the narrowest owner/group permissions required, typically 0755 for directories and 0644 for files.",
		factor:      "untrusted local writer",
	},
	{
		id: "agent.sandbox-disabled", title: "Agent sandbox appears disabled",
		description: "Disabling the execution sandbox lets model-directed commands affect the host outside the intended workspace boundary.",
		category:    "ai-control", baseScore: 86,
		pattern:     regexp.MustCompile(`(?i)("?sandbox"?\s*[:=]\s*(false|"disabled"|"danger-full-access")|sandbox_mode\s*[:=]\s*danger-full-access)`),
		applies:     agentConfig,
		remediation: "Enable workspace-scoped sandboxing and grant additional write roots explicitly only when a task needs them.",
		factor:      "agent can reach host",
	},
	{
		id: "agent.wildcard-tools", title: "Agent tool policy allows every tool",
		description: "A wildcard tool grant removes least-privilege boundaries and expands the impact of prompt injection or a compromised MCP server.",
		category:    "ai-control", baseScore: 74,
		pattern:     regexp.MustCompile(`(?i)(allowed[_-]?tools|tools)\s*[:=]\s*\[\s*["']\*["']\s*\]`),
		applies:     agentConfig,
		remediation: "Replace wildcard grants with the smallest named tool set and require confirmation for network or destructive operations.",
		factor:      "unbounded tool access",
	},
	{
		id: "agent.runtime-bypass-flags", title: "Agent runtime guardrails can be bypassed",
		description: "A script or configuration invokes an AI coding agent with a flag that disables sandbox or permission controls.",
		category:    "ai-control", baseScore: 84,
		pattern:     regexp.MustCompile(`(?i)(--dangerously-skip-permissions|--dangerously-bypass-approvals-and-sandbox|--sandbox[=\s]+danger-full-access|--no-sandbox|--yolo\b)`),
		applies:     sourceOrConfig,
		remediation: "Remove the bypass flag, enable workspace-scoped sandboxing, and require explicit approval for destructive or external actions.",
		factor:      "runtime guardrails bypassed",
	},
	{
		id: "execution.download-pipe-shell", title: "Downloaded content is executed without verification",
		description: "A network download is piped directly into a shell. A compromised host, redirect, or artifact can become immediate code execution.",
		category:    "supply-chain", baseScore: 80,
		pattern:     regexp.MustCompile(`(?i)\b(curl|wget)\b[^\n|]{0,260}\|\s*(sudo\s+)?(ba)?sh\b`),
		applies:     sourceOrConfig,
		remediation: "Download to a temporary file, verify a pinned digest or signature, inspect the artifact, then execute it with the least privilege required.",
		factor:      "unverified network payload",
	},
	{
		id: "cloud.metadata-credential-access", title: "Code reaches a cloud metadata credential endpoint",
		description: "The workspace references a link-local instance metadata endpoint that can return workload identity credentials when network isolation is weak.",
		category:    "identity", baseScore: 70,
		pattern:     regexp.MustCompile(`(?i)169\.254\.169\.254(?:/latest/meta-data/iam|/metadata/identity|[^\s"'<>]{0,100})`),
		applies:     sourceOrConfig,
		remediation: "Use the provider SDK with metadata hardening enabled, require IMDSv2 or the provider equivalent, and block metadata access from untrusted workloads.",
		factor:      "workload credential endpoint",
	},
	// --- codex-security inspired static sinks (high signal config/code patterns) ---
	{
		id: "code.unsafe-yaml-load", title: "Unsafe YAML load can execute constructors",
		description: "yaml.load without a SafeLoader can instantiate arbitrary Python objects from untrusted input (RCE class).",
		category:    "execution", baseScore: 84,
		pattern:     regexp.MustCompile(`(?i)\byaml\.load\s*\(`),
		applies:     sourceOrConfig,
		remediation: "Use yaml.safe_load or pass Loader=yaml.SafeLoader. Never load untrusted YAML with the default FullLoader/unsafe path.",
		factor:      "unsafe deserialization sink",
	},
	{
		id: "code.pickle-loads", title: "Pickle deserialization can execute code",
		description: "pickle.loads/load of untrusted bytes is a classic remote code execution sink.",
		category:    "execution", baseScore: 88,
		pattern:     regexp.MustCompile(`(?i)\bpickle\.(loads?|Unpickler)\s*\(`),
		applies:     sourceOrConfig,
		remediation: "Do not unpickle untrusted data. Prefer JSON or a signed, versioned format with an allowlisted schema.",
		factor:      "code-execution sink",
	},
	{
		id: "code.shell-true", title: "Subprocess runs through a shell",
		description: "shell=True (or equivalent) passes command strings through a shell, enabling injection when any part is attacker-influenced.",
		category:    "execution", baseScore: 72,
		pattern:     regexp.MustCompile(`(?i)(shell\s*=\s*True|subprocess\.(call|run|Popen)\s*\([^\n]{0,120}shell\s*=\s*True)`),
		applies:     sourceOrConfig,
		remediation: "Pass argv lists with shell=False and never interpolate untrusted strings into a shell command line.",
		factor:      "command injection surface",
	},
	{
		id: "code.eval-exec", title: "Dynamic code evaluation is present",
		description: "eval/exec (or Function constructor) of dynamic strings is an RCE sink when any input is attacker-controlled.",
		category:    "execution", baseScore: 76,
		pattern:     regexp.MustCompile(`(?i)(?m)^\s*(?:[^/\n]*\b)(eval|exec)\s*\(|\bnew\s+Function\s*\(`),
		applies:     sourceOrConfig,
		remediation: "Remove eval/exec of dynamic content. Use structured parsers or an explicit allowlisted DSL.",
		factor:      "dynamic code sink",
	},
	{
		id: "web.dangerous-innerhtml", title: "Unsanitized HTML injection sink",
		description: "dangerouslySetInnerHTML / innerHTML assignment is a common XSS sink when content includes untrusted data.",
		category:    "exposure", baseScore: 68,
		pattern:     regexp.MustCompile(`(?i)(dangerouslySetInnerHTML|innerHTML\s*=)`),
		applies:     sourceOrConfig,
		remediation: "Sanitize or avoid raw HTML. Prefer text content or a vetted sanitizer with a strict allowlist.",
		factor:      "XSS sink",
	},
	{
		id: "auth.jwt-none-alg", title: "JWT algorithm none or empty secret risk",
		description: "Accepting the none algorithm or an empty HMAC secret lets attackers forge tokens.",
		category:    "identity", baseScore: 90,
		pattern:     regexp.MustCompile(`(?i)(algorithms?\s*[:=]\s*\[[^\]]*(['\"]none['\"])|jwt\.(decode|verify)[^\n]{0,80}none|secret\s*[:=]\s*['\"]['\"])`),
		applies:     sourceOrConfig,
		remediation: "Require explicit strong algorithms (e.g. RS256/ES256/HS256) and non-empty secrets; reject alg=none.",
		factor:      "token forgery",
	},
	{
		id: "crypto.weak-hash-password", title: "Weak hash used in a password-like context",
		description: "MD5 or SHA-1 near password/credential handling is not suitable for password storage or integrity of security tokens.",
		category:    "identity", baseScore: 62,
		pattern:     regexp.MustCompile(`(?i)(password|passwd|credential).{0,40}(md5|sha1)\b|(md5|sha1).{0,40}(password|passwd|credential)`),
		applies:     sourceOrConfig,
		remediation: "Use a memory-hard password KDF (argon2id, scrypt, or bcrypt) with a unique salt per credential.",
		factor:      "weak credential protection",
	},
	{
		id: "runtime.debug-enabled", title: "Debug mode appears enabled in configuration",
		description: "Framework debug flags expose stack traces, interactive consoles, or verbose errors that aid attackers.",
		category:    "exposure", baseScore: 58,
		pattern:     regexp.MustCompile(`(?i)((DEBUG|FLASK_DEBUG|APP_DEBUG)\s*[:=]\s*(True|true|1|yes|on)\b|debug\s*[:=]\s*true\b)`),
		applies:     configLike,
		remediation: "Disable debug in non-local environments and serve generic error pages in production.",
		factor:      "verbose error surface",
	},
	{
		id: "transport.cleartext-http-secret", title: "Secret or credential sent over cleartext HTTP",
		description: "A configuration pairs an HTTP (non-TLS) URL with credential-bearing parameters.",
		category:    "exposure", baseScore: 74,
		pattern:     regexp.MustCompile(`(?i)http://[^\s"'<>]+(api[_-]?key|token|password|secret)=`),
		applies:     sourceOrConfig,
		remediation: "Use HTTPS endpoints and load secrets from a secret store rather than URL query parameters.",
		factor:      "cleartext credential transport",
	},
	{
		id: "ci.script-injection", title: "CI step may expand untrusted pull-request input",
		description: "Workflow expressions that interpolate pull_request title/body/head into run: scripts are a known script-injection class.",
		category:    "supply-chain", baseScore: 80,
		pattern:     regexp.MustCompile(`(?i)run:\s*[^\n]*\$\{\{\s*(github\.event\.pull_request\.(title|body|head\.(ref|label))|github\.head_ref)`),
		applies:     workflowFile,
		remediation: "Pass untrusted values via env: blocks and expand `$ENV` inside the script, never inline `${{ }}` into shell.",
		factor:      "untrusted CI input expansion",
	},
}

// Scan performs a bounded, local-only posture scan. It never executes project
// code, contacts a registry, or writes inside the workspace.
func Scan(root string, options Options) (Report, error) {
	started := time.Now()
	if options.MaxFiles <= 0 {
		options.MaxFiles = DefaultOptions().MaxFiles
	}
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = DefaultOptions().MaxFileBytes
	}
	if options.Mode == "" {
		options.Mode = ScanModeFull
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Report{}, fmt.Errorf("resolve scan root: %w", err)
	}
	info, err := os.Stat(absoluteRoot)
	if err != nil {
		return Report{}, fmt.Errorf("inspect scan root: %w", err)
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("scan root is not a directory: %s", absoluteRoot)
	}

	tracked := gitTrackedFiles(absoluteRoot)
	report := Report{
		Version:     ReportVersion,
		GeneratedAt: time.Now().UTC(),
		Root:        absoluteRoot,
		Mode:        options.Mode,
		DiffBase:    options.DiffBase,
		Policy:      loadSecurityPolicy(absoluteRoot),
		Findings:    []Finding{},
		Nodes: []Node{{
			ID: "workspace", Label: filepath.Base(absoluteRoot), Kind: "workspace",
			Labels: map[string]string{"scope": "local", "scanner": "agentless", "mode": string(options.Mode)},
		}},
		Edges:       []Edge{},
		ThreatIntel: ThreatIntelSummary{Status: ThreatIntelDisabled},
		Coverage: Coverage{
			MaxFiles: options.MaxFiles, MaxFileBytes: options.MaxFileBytes,
			TrackedFiles: len(tracked), ReferenceSkipped: true,
		},
	}

	signals := map[string]*fileSignals{}
	pathsSeen := map[string]bool{}
	structuredFiles := map[string][]byte{}

	if options.Mode == ScanModeDiff {
		if err := scanDiffScope(&report, signals, pathsSeen, structuredFiles, absoluteRoot, tracked, options); err != nil {
			return Report{}, err
		}
	} else {
		walkErr := filepath.WalkDir(absoluteRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				report.Coverage.FilesSkipped++
				return nil
			}
			rel, err := filepath.Rel(absoluteRoot, path)
			if err != nil {
				report.Coverage.FilesSkipped++
				return nil
			}
			rel = filepath.ToSlash(rel)
			if entry.IsDir() {
				if rel != "." && ignoredDirectory(rel, entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			report.Coverage.FilesSeen++
			pathsSeen[rel] = true
			if report.Coverage.FilesScanned >= options.MaxFiles {
				report.Coverage.Truncated = true
				report.Coverage.FilesSkipped++
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || !eligibleFile(rel) {
				report.Coverage.FilesSkipped++
				return nil
			}
			fileInfo, err := entry.Info()
			if err != nil || fileInfo.Size() > options.MaxFileBytes {
				report.Coverage.FilesSkipped++
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
				report.Coverage.FilesSkipped++
				return nil
			}
			report.Coverage.FilesScanned++
			report.Coverage.BytesScanned += int64(len(data))
			structuredFiles[rel] = append([]byte(nil), data...)
			scanFile(&report, signals, rel, data, fileInfo, tracked[rel])
			return nil
		})
		if walkErr != nil {
			return Report{}, fmt.Errorf("walk workspace: %w", walkErr)
		}
	}

	collectStructured(&report, structuredFiles)
	correlate(&report, signals)
	if enabled := securityConfigEnabled(absoluteRoot); enabled {
		// Threat intel is explicitly opt-in and failure-tolerant. The base local
		// scan above is always complete even when the provider is unavailable.
		applyThreatIntel(&report, structuredFiles)
	}
	applyOwnership(&report)
	// Full-tree control detection needs path inventory; for diff mode also probe
	// well-known control files even if they were not in the diff.
	if options.Mode == ScanModeDiff {
		for _, controlPath := range []string{
			"SECURITY.md", ".github/SECURITY.md", ".gitignore",
			".github/dependabot.yml", ".github/dependabot.yaml", "renovate.json",
			"CODEOWNERS", ".github/CODEOWNERS", "go.sum", "package-lock.json",
			"pnpm-lock.yaml", "yarn.lock", "Cargo.lock",
		} {
			if _, err := os.Stat(filepath.Join(absoluteRoot, filepath.FromSlash(controlPath))); err == nil {
				pathsSeen[controlPath] = true
			}
		}
		// Mark workflow presence if any workflow file exists.
		_ = filepath.WalkDir(filepath.Join(absoluteRoot, ".github", "workflows"), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry == nil || entry.IsDir() {
				return nil
			}
			if rel, relErr := filepath.Rel(absoluteRoot, path); relErr == nil {
				pathsSeen[filepath.ToSlash(rel)] = true
			}
			return nil
		})
	}
	report.Controls = detectControls(pathsSeen)
	finalize(&report)
	report.Coverage.DurationMillis = time.Since(started).Milliseconds()
	return report, nil
}

// scanDiffScope reviews only Git-changed files (working tree + index vs DiffBase).
// Mirrors codex-security security-diff-scan discovery without executing project code.
func scanDiffScope(report *Report, signals map[string]*fileSignals, pathsSeen map[string]bool, structuredFiles map[string][]byte, root string, tracked map[string]bool, options Options) error {
	base := strings.TrimSpace(options.DiffBase)
	if base == "" {
		base = "HEAD"
	}
	report.DiffBase = base
	changed := gitChangedFiles(root, base)
	report.Coverage.DiffFiles = len(changed)
	if len(changed) > 40 {
		report.Coverage.ScopePaths = append([]string(nil), changed[:40]...)
	} else {
		report.Coverage.ScopePaths = append([]string(nil), changed...)
	}
	if len(changed) == 0 {
		return nil
	}
	for _, rel := range changed {
		report.Coverage.FilesSeen++
		pathsSeen[rel] = true
		if report.Coverage.FilesScanned >= options.MaxFiles {
			report.Coverage.Truncated = true
			report.Coverage.FilesSkipped++
			continue
		}
		if !eligibleFile(rel) {
			report.Coverage.FilesSkipped++
			continue
		}
		full := filepath.Join(root, filepath.FromSlash(rel))
		fileInfo, err := os.Stat(full)
		if err != nil || fileInfo.IsDir() || fileInfo.Size() > options.MaxFileBytes {
			report.Coverage.FilesSkipped++
			continue
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			report.Coverage.FilesSkipped++
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			report.Coverage.FilesSkipped++
			continue
		}
		report.Coverage.FilesScanned++
		report.Coverage.BytesScanned += int64(len(data))
		structuredFiles[rel] = append([]byte(nil), data...)
		scanFile(report, signals, rel, data, fileInfo, tracked[rel])
	}
	return nil
}

func gitChangedFiles(root, base string) []string {
	seen := map[string]bool{}
	collect := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		data, err := cmd.Output()
		if err != nil {
			return
		}
		// NUL-delimited Git output is required here: strings.Fields or newline
		// splitting corrupts valid paths containing spaces, tabs, or newlines and
		// can make a diff scan inspect a different file than the one changed.
		for _, rawPath := range bytes.Split(data, []byte{0}) {
			if len(rawPath) == 0 {
				continue
			}
			path := string(rawPath)
			path = filepath.ToSlash(path)
			if path != "" && !strings.HasPrefix(path, ".git/") {
				seen[path] = true
			}
		}
	}
	// Working tree + index vs base; untracked source-like files.
	collect("diff", "--name-only", "-z", base)
	collect("diff", "--name-only", "--cached", "-z", base)
	collect("ls-files", "--others", "--exclude-standard", "-z")
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// loadSecurityPolicy returns a bounded SECURITY.md excerpt (codex-security
// security-guidance). Content is untrusted analysis data, never instructions.
func loadSecurityPolicy(root string) string {
	const maxPolicyBytes = 8 * 1024
	for _, rel := range []string{"SECURITY.md", ".github/SECURITY.md", "docs/SECURITY.md"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || len(data) == 0 {
			continue
		}
		if !utf8.Valid(data) {
			continue
		}
		text := strings.TrimSpace(string(data))
		if len(text) > maxPolicyBytes {
			text = text[:maxPolicyBytes] + "\n…[policy truncated]"
		}
		return text
	}
	return ""
}

func scanFile(report *Report, signals map[string]*fileSignals, path string, data []byte, info fs.FileInfo, tracked bool) {
	content := string(data)
	testLike := isTestLike(path)
	signal := &fileSignals{path: path, nodeID: "file:" + path, workflow: workflowFile(path), testLike: testLike}
	signals[path] = signal
	// The scanner contains literal detection signatures by design. Treating its
	// own rule table as a vulnerable resource would create guaranteed noise on
	// every repository that vendors /sec.
	if strings.HasPrefix(filepath.ToSlash(path), "internal/sec/") {
		return
	}

	for _, candidate := range postureRules {
		if candidate.applies != nil && !candidate.applies(path) {
			continue
		}
		loc := candidate.pattern.FindStringIndex(content)
		if loc == nil {
			continue
		}
		score := candidate.baseScore
		factors := []string{candidate.factor}
		if testLike {
			score -= 46
			factors = append(factors, "test or example context")
		}
		line := lineAt(content, loc[0])
		if isCommentEvidence(evidenceAt(content, line)) {
			continue
		}
		if tracked {
			score += 4
			factors = append(factors, "committed configuration")
		}
		if score < 20 {
			continue
		}
		finding := Finding{
			RuleID: candidate.id, Title: candidate.title, Description: candidate.description,
			Category: candidate.category, RiskScore: clamp(score, 5, 100),
			Path: path, Line: line, Evidence: evidenceAt(content, line),
			Remediation: candidate.remediation, Factors: compactStrings(factors),
		}
		finding.Severity = severityForScore(finding.RiskScore)
		addFinding(report, signal, finding)
		switch candidate.category {
		case "exposure":
			signal.exposed = true
		case "identity":
			signal.privileged = true
		}
		if candidate.id == "ci.unpinned-action" {
			signal.unpinned = true
		}
	}

	secretFindings := secrets.Scan(content)
	// Well-shaped fake credentials are deliberately common in scanner,
	// redaction, OAuth, and transport tests. Without an online validity check
	// there is no honest way to distinguish them from live values, so the
	// default posture view excludes test-only files instead of flooding the
	// operator. Production/config/example files remain in scope.
	if testLike {
		secretFindings = nil
	}
	if len(secretFindings) > 0 {
		for _, secret := range secretFindings {
			offset := strings.Index(content, secret.Match)
			line := lineAt(content, offset)
			score := 86
			factors := []string{"credential material detected"}
			lineText := ""
			lines := strings.Split(content, "\n")
			if line > 0 && line <= len(lines) {
				start := max(0, line-3)
				lineText = strings.ToLower(strings.Join(lines[start:line], "\n"))
			}
			if secret.Type == "jwt" && strings.Contains(lineText, "anon") {
				score = 48
				factors = append(factors, "public client token; validate row-level policy")
			}
			if tracked {
				score += 10
				factors = append(factors, "tracked by git")
			}
			templatePlaceholder := isTemplateSensitivePath(path) &&
				(secret.Type == "jwt" && strings.Contains(lineText, "anon") || looksPlaceholder(secret.Match))
			if testLike || templatePlaceholder {
				score -= 70
				factors = append(factors, "test or placeholder context")
			}
			if score < 20 {
				continue
			}
			finding := Finding{
				RuleID:      "secret." + secret.Type,
				Title:       "Credential material detected",
				Description: "A high-confidence " + strings.ReplaceAll(secret.Type, "_", " ") + " pattern is present. The report intentionally never stores the value.",
				Category:    "secrets", RiskScore: clamp(score, 10, 100), Path: path, Line: line,
				Evidence:    "[REDACTED:" + secret.Type + "]",
				Remediation: "Revoke or rotate the credential, remove it from source and history, then load it from the platform secret store at runtime.",
				Factors:     compactStrings(factors),
			}
			fingerprint := sha256.Sum256([]byte(secret.Match))
			finding.secretFingerprint = fmt.Sprintf("%x", fingerprint[:])
			finding.Severity = severityForScore(finding.RiskScore)
			addFinding(report, signal, finding)
			signal.secret = true
		}
	}

	if sensitivePath(path) {
		if tracked && !isTemplateSensitivePath(path) {
			finding := Finding{
				RuleID:      "secret.sensitive-file-tracked",
				Title:       "Sensitive file is tracked by git",
				Description: "The filename indicates credentials or private key material and the file is present in the repository index.",
				Category:    "secrets", RiskScore: 88, Severity: SeverityHigh, Path: path,
				Remediation: "Remove the file from git history, rotate any contained credentials, and add the path pattern to .gitignore.",
				Factors:     []string{"credential-bearing filename", "tracked by git"},
			}
			addFinding(report, signal, finding)
			signal.secret = true
		}
		if info.Mode().Perm()&0o077 != 0 && !isTemplateSensitivePath(path) {
			finding := Finding{
				RuleID:      "filesystem.sensitive-readable",
				Title:       "Sensitive file is readable by other local users",
				Description: "Group or world permissions can expose local credential material to another account on the machine.",
				Category:    "secrets", RiskScore: 61, Severity: SeverityMedium, Path: path,
				Evidence:    fmt.Sprintf("mode %04o", info.Mode().Perm()),
				Remediation: "Set the file mode to 0600 and ensure its parent directory is not group/world writable.",
				Factors:     []string{"credential-bearing filename", "broad local read access"},
			}
			addFinding(report, signal, finding)
			signal.secret = true
		}
	}

	if dotenvAssignment.MatchString(content) && sensitivePath(path) && !isTemplateSensitivePath(path) {
		signal.secret = true
	}
}

func addFinding(report *Report, signal *fileSignals, finding Finding) {
	finding.ID = findingID(finding.RuleID, finding.Path, finding.Line)
	if finding.Confidence == "" {
		finding.Confidence = "high"
	}
	if finding.EvidenceKind == "" {
		finding.EvidenceKind = "workspace-file"
	}
	if finding.Verification == "" {
		finding.Verification = verificationForFinding(finding)
	}
	if finding.Priority == "" {
		finding.Priority = priorityForSeverity(finding.Severity)
	}
	// Static assessment tuple defaults (codex-security): path is the sink
	// location; source/control filled when rule category implies them.
	if finding.Sink == "" && finding.Path != "" {
		finding.Sink = finding.Path
		if finding.Line > 0 {
			finding.Sink = fmt.Sprintf("%s:%d", finding.Path, finding.Line)
		}
	}
	if finding.Source == "" {
		switch finding.Category {
		case "exposure", "execution":
			finding.Source = "untrusted input or external trigger (static; verify reachability)"
		case "supply-chain":
			finding.Source = "third-party dependency or CI input"
		case "secrets", "data":
			finding.Source = "credential material at rest"
		case "identity", "ai-control":
			finding.Source = "elevated identity or agent tool grant"
		case "attack-path":
			finding.Source = "correlated multi-signal path"
		}
	}
	if finding.Control == "" {
		finding.Control = "missing or weak control implied by rule; see remediation"
	}
	if finding.ExposureState == "" {
		switch finding.RuleID {
		case "network.public-cidr", "network.public-bind":
			finding.ExposureState = ExposureConfiguredPublic
		case "network.wildcard-cors", "transport.tls-verification-disabled", "transport.cleartext-http-secret":
			finding.ExposureState = ExposurePotential
		}
	}
	signal.findings = append(signal.findings, len(report.Findings))
	report.Findings = append(report.Findings, finding)
}

func verificationForFinding(finding Finding) string {
	switch finding.Category {
	case "attack-path":
		return "Run /sec rescan and confirm this path is absent; verify every linked exposure and privilege factor was removed."
	case "secrets":
		return "Confirm the credential is revoked, search repository history for the fingerprint/type, then run /sec rescan."
	case "exposure":
		return "Run /sec rescan, then test the service from an untrusted network boundary to confirm access is denied."
	case "identity", "ai-control":
		return "Run /sec rescan and inspect the effective runtime policy to confirm least privilege is enforced."
	case "supply-chain":
		return "Run /sec rescan and verify the dependency or downloaded artifact is pinned to a reviewed immutable digest."
	default:
		return "Run /sec rescan after the change and confirm the finding no longer appears."
	}
}

func findingID(ruleID, path string, line int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", ruleID, path, line)))
	return fmt.Sprintf("%x", sum[:8])
}

func lineAt(content string, offset int) int {
	if offset <= 0 {
		return 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	return 1 + strings.Count(content[:offset], "\n")
}

func evidenceAt(content string, line int) string {
	lines := strings.Split(content, "\n")
	if line <= 0 || line > len(lines) {
		return ""
	}
	value := strings.TrimSpace(lines[line-1])
	value, _ = secrets.Redact(value)
	value = sensitiveAssignment.ReplaceAllString(value, `$1$2[REDACTED]`)
	if len(value) > 220 {
		value = value[:220] + "…"
	}
	return value
}

func isCommentEvidence(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "//") || strings.HasPrefix(value, "#") ||
		strings.HasPrefix(value, "/*") || strings.HasPrefix(value, "*") ||
		strings.HasPrefix(value, "--")
}

func looksPlaceholder(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "example") || strings.Contains(lower, "placeholder") ||
		strings.Contains(lower, "xxxxxxxx") || strings.Contains(lower, "test")
}

func gitTrackedFiles(root string) map[string]bool {
	out := map[string]bool{}
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	data, err := cmd.Output()
	if err != nil {
		return out
	}
	for _, item := range bytes.Split(data, []byte{0}) {
		if len(item) > 0 {
			out[filepath.ToSlash(string(item))] = true
		}
	}
	return out
}

func ignoredDirectory(rel, name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "coverage", ".next",
		".cache", "target", "__pycache__", "graphify-out", "ref":
		return true
	}
	return rel == ".atcode/sec" || rel == ".atcode/audit" || strings.HasPrefix(rel, ".git/")
}

func eligibleFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "dockerfile", "containerfile", "go.mod", "go.sum", "package.json", "package-lock.json",
		"pnpm-lock.yaml", "yarn.lock", "bun.lockb", "gemfile", "gemfile.lock",
		"requirements.txt", "poetry.lock", "pyproject.toml", "cargo.toml", "cargo.lock",
		"security.md", ".gitignore", "config.json":
		return true
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs", ".rs", ".java",
		".cs", ".rb", ".php", ".sh", ".bash", ".zsh", ".ps1", ".yaml", ".yml", ".json",
		".toml", ".tf", ".hcl", ".env", ".ini", ".conf", ".properties", ".xml", ".sql",
		".pem", ".key", ".crt", ".p12":
		return true
	default:
		return strings.HasPrefix(base, ".env.")
	}
}

func configLike(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	base := strings.ToLower(filepath.Base(path))
	return base == "dockerfile" || base == "containerfile" ||
		ext == ".yaml" || ext == ".yml" || ext == ".json" || ext == ".toml" ||
		ext == ".tf" || ext == ".hcl" || ext == ".ini" || ext == ".conf" ||
		ext == ".properties"
}

func sourceOrConfig(path string) bool { return eligibleFile(path) }

func containerConfig(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	return base == "dockerfile" || base == "containerfile" ||
		strings.Contains(lower, "docker-compose") || strings.Contains(lower, "compose.") ||
		strings.Contains(lower, "k8s") || strings.Contains(lower, "kubernetes") ||
		strings.Contains(lower, "helm") || strings.Contains(lower, "deployment") ||
		strings.Contains(lower, "pod.") || strings.Contains(lower, "daemonset")
}

func workflowFile(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	return strings.HasPrefix(lower, ".github/workflows/") &&
		(strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml"))
}

func agentConfig(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	// Scan predecessor config paths as sensitive even though @code never creates
	// them. Existing workspaces may still contain credentials there.
	return lower == ".atcode/config.json" || lower == ".zero/config.json" ||
		lower == ".sonut/config.json" || lower == "config.json" ||
		strings.HasSuffix(lower, "/mcp.json")
}

func isTestLike(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	return strings.Contains(lower, "/testdata/") || strings.Contains(lower, "/fixtures/") ||
		strings.Contains(lower, "/examples/") || strings.HasPrefix(lower, "docs/") ||
		strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.js") || strings.HasSuffix(base, "_test.py")
}

func sensitivePath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == "id_rsa" ||
		base == "id_ed25519" || base == "credentials" || base == "credentials.json" {
		return true
	}
	ext := strings.ToLower(filepath.Ext(base))
	return ext == ".pem" || ext == ".key" || ext == ".p12" || ext == ".pfx"
}

func isTemplateSensitivePath(path string) bool {
	lower := strings.ToLower(path)
	return strings.Contains(lower, "example") || strings.Contains(lower, "sample") ||
		strings.Contains(lower, "template") || strings.HasSuffix(lower, ".dist")
}

func clamp(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func compactStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
