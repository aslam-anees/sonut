// Package sec implements the agentless workspace security posture layer used
// by /sec. It inventories a repository with bounded, read-only filesystem and
// git metadata checks, correlates findings into attack paths, and exports a
// machine-readable report for the native TUI.
package sec

import "time"

const ReportVersion = 1

type ExposureState string

const (
	ExposureRuntimeObserved  ExposureState = "runtime-observed"
	ExposureConfiguredPublic ExposureState = "configured-public"
	ExposurePotential        ExposureState = "potential"
	ExposureUnknown          ExposureState = "unknown"
)

type RiskPathClass string

const (
	RiskPathInitialAccess       RiskPathClass = "initial-access"
	RiskPathLateralMovement     RiskPathClass = "lateral-movement"
	RiskPathPrivilegeEscalation RiskPathClass = "privilege-escalation"
	RiskPathDataAccess          RiskPathClass = "data-access"
	RiskPathSupplyChain         RiskPathClass = "supply-chain"
)

type GenerationMode string

const (
	GenerationDeterministic GenerationMode = "deterministic"
	GenerationAgentAssisted GenerationMode = "agent-assisted"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

type Finding struct {
	ID             string                 `json:"id"`
	RuleID         string                 `json:"rule_id"`
	Title          string                 `json:"title"`
	Description    string                 `json:"description"`
	Category       string                 `json:"category"`
	Severity       Severity               `json:"severity"`
	Priority       string                 `json:"priority,omitempty"` // P0–P3 (codex-security severity policy)
	RiskScore      int                    `json:"risk_score"`
	Path           string                 `json:"path,omitempty"`
	Line           int                    `json:"line,omitempty"`
	Evidence       string                 `json:"evidence,omitempty"`
	Confidence     string                 `json:"confidence"`
	EvidenceKind   string                 `json:"evidence_kind"`
	Remediation    string                 `json:"remediation"`
	Verification   string                 `json:"verification"`
	Factors        []string               `json:"factors,omitempty"`
	AttackPath     []string               `json:"attack_path,omitempty"`
	ExposureState  ExposureState          `json:"exposure_state,omitempty"`
	Service        string                 `json:"service,omitempty"`
	Owner          string                 `json:"owner,omitempty"`
	FixTarget      string                 `json:"fix_target,omitempty"`
	RepositoryRoot string                 `json:"repository_root,omitempty"`
	ThreatIntel    []ThreatIntelReference `json:"threat_intel,omitempty"`
	// Static assessment tuple (codex-security static-finding-assessment).
	Source  string `json:"source,omitempty"`
	Control string `json:"control,omitempty"`
	Sink    string `json:"sink,omitempty"`

	// secretFingerprint only deduplicates score penalties for the same value
	// observed in multiple files. It is deliberately absent from JSON.
	secretFingerprint string
}

type Node struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Kind        string            `json:"kind"`
	Path        string            `json:"path,omitempty"`
	Severity    Severity          `json:"severity,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
}

type Edge struct {
	From            string `json:"from"`
	To              string `json:"to"`
	Relation        string `json:"relation"`
	Verdict         string `json:"verdict,omitempty"`
	EvidenceID      string `json:"evidence_id,omitempty"`
	Confidence      string `json:"confidence,omitempty"`
	ObservationType string `json:"observation_type,omitempty"`
	EvidenceSource  string `json:"evidence_source,omitempty"`
}

type Asset struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Kind        string            `json:"kind"`
	Provider    string            `json:"provider,omitempty"`
	Format      string            `json:"format,omitempty"`
	Service     string            `json:"service,omitempty"`
	Repository  string            `json:"repository,omitempty"`
	SourcePath  string            `json:"source_path,omitempty"`
	SourceLine  int               `json:"source_line,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	EvidenceIDs []string          `json:"evidence_ids,omitempty"`
}

type Exposure struct {
	AssetID         string        `json:"asset_id"`
	State           ExposureState `json:"state"`
	Protocol        string        `json:"protocol,omitempty"`
	Ports           []int         `json:"ports,omitempty"`
	EntryPoint      string        `json:"entry_point,omitempty"`
	Authentication  string        `json:"authentication_observation,omitempty"`
	TLS             string        `json:"tls_observation,omitempty"`
	Confidence      string        `json:"confidence,omitempty"`
	EvidenceIDs     []string      `json:"evidence_ids,omitempty"`
	RuntimeEventIDs []string      `json:"runtime_event_ids,omitempty"`
}

// Route is a bounded, configuration-only view of a reverse-proxy or ingress
// route. It records the mapping and visible policy controls without claiming
// that the route is reachable from the public internet.
type Route struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Host           string        `json:"host,omitempty"`
	Path           string        `json:"path,omitempty"`
	Target         string        `json:"target,omitempty"`
	Protocol       string        `json:"protocol,omitempty"`
	Ports          []int         `json:"ports,omitempty"`
	Authentication string        `json:"authentication_observation,omitempty"`
	TLS            string        `json:"tls_observation,omitempty"`
	Middlewares    []string      `json:"middlewares,omitempty"`
	ExposureState  ExposureState `json:"exposure_state,omitempty"`
	Confidence     string        `json:"confidence,omitempty"`
	SourcePath     string        `json:"source_path,omitempty"`
	SourceLine     int           `json:"source_line,omitempty"`
	AssetID        string        `json:"asset_id,omitempty"`
	EvidenceIDs    []string      `json:"evidence_ids,omitempty"`
}

type RiskPathStep struct {
	NodeID          string `json:"node_id"`
	Relation        string `json:"relation"`
	EvidenceID      string `json:"evidence_id,omitempty"`
	Confidence      string `json:"confidence,omitempty"`
	ObservationType string `json:"observation_type,omitempty"`
}

type RiskPath struct {
	ID          string         `json:"id"`
	Class       RiskPathClass  `json:"class"`
	Title       string         `json:"title"`
	Steps       []RiskPathStep `json:"steps"`
	RiskScore   int            `json:"risk_score"`
	Confidence  string         `json:"confidence"`
	EvidenceIDs []string       `json:"evidence_ids,omitempty"`
	Owner       string         `json:"owner,omitempty"`
	Service     string         `json:"service,omitempty"`
	FixTarget   string         `json:"fix_target,omitempty"`
}

type FixPlan struct {
	FindingOrPathID string         `json:"finding_or_path_id"`
	TargetPath      string         `json:"target_path,omitempty"`
	TargetLine      int            `json:"target_line,omitempty"`
	RepositoryRoot  string         `json:"repository_root,omitempty"`
	Service         string         `json:"service,omitempty"`
	Owner           string         `json:"owner,omitempty"`
	Summary         string         `json:"summary"`
	Verification    string         `json:"verification"`
	GenerationMode  GenerationMode `json:"generation_mode"`
}

type ThreatIntelReference struct {
	Source                  string     `json:"source"`
	AdvisoryID              string     `json:"advisory_id,omitempty"`
	URL                     string     `json:"url,omitempty"`
	Package                 string     `json:"package,omitempty"`
	Ecosystem               string     `json:"ecosystem,omitempty"`
	ExactVersion            string     `json:"exact_version,omitempty"`
	PublishedAt             *time.Time `json:"published_at,omitempty"`
	ModifiedAt              *time.Time `json:"modified_at,omitempty"`
	VulnerableVersionStatus string     `json:"vulnerable_version_status,omitempty"`
	KnownExploitation       bool       `json:"known_exploitation,omitempty"`
	ExactMatch              bool       `json:"exact_match,omitempty"`
	Confidence              string     `json:"confidence,omitempty"`
}

type ThreatIntelStatus string

const (
	ThreatIntelDisabled    ThreatIntelStatus = "disabled"
	ThreatIntelCached      ThreatIntelStatus = "cached"
	ThreatIntelFresh       ThreatIntelStatus = "fresh"
	ThreatIntelUnavailable ThreatIntelStatus = "unavailable"
	ThreatIntelError       ThreatIntelStatus = "error"
)

type ThreatIntelSummary struct {
	Status       ThreatIntelStatus `json:"status"`
	Source       string            `json:"source,omitempty"`
	Matches      int               `json:"matches,omitempty"`
	ExactMatches int               `json:"exact_matches,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type Control struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description"`
	Evidence    string `json:"evidence,omitempty"`
}

type Coverage struct {
	FilesSeen           int      `json:"files_seen"`
	FilesScanned        int      `json:"files_scanned"`
	FilesSkipped        int      `json:"files_skipped"`
	BytesScanned        int64    `json:"bytes_scanned"`
	Truncated           bool     `json:"truncated"`
	MaxFiles            int      `json:"max_files"`
	MaxFileBytes        int64    `json:"max_file_bytes"`
	TrackedFiles        int      `json:"tracked_files"`
	DurationMillis      int64    `json:"duration_ms"`
	ReferenceSkipped    bool     `json:"reference_directories_skipped"`
	DiffFiles           int      `json:"diff_files,omitempty"`
	ScopePaths          []string `json:"scope_paths,omitempty"`
	StructuredFiles     int      `json:"structured_files,omitempty"`
	StructuredMalformed int      `json:"structured_malformed,omitempty"`
	StructuredNodes     int      `json:"structured_nodes,omitempty"`
	StructuredEdges     int      `json:"structured_edges,omitempty"`
}

type Summary struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}

// ScanMode selects the inventory scope. Diff mode (codex-security security-diff-scan)
// only examines Git-changed files for fast PR/working-tree review.
type ScanMode string

const (
	ScanModeFull ScanMode = "full"
	ScanModeDiff ScanMode = "diff"
)

type Report struct {
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generated_at"`
	Root        string    `json:"root"`
	Mode        ScanMode  `json:"mode,omitempty"`
	// Policy is a redacted excerpt of SECURITY.md guidance (untrusted data).
	Policy      string             `json:"policy,omitempty"`
	DiffBase    string             `json:"diff_base,omitempty"`
	Score       int                `json:"score"`
	Grade       string             `json:"grade"`
	Summary     Summary            `json:"summary"`
	Findings    []Finding          `json:"findings"`
	Nodes       []Node             `json:"nodes"`
	Edges       []Edge             `json:"edges"`
	Controls    []Control          `json:"controls"`
	Coverage    Coverage           `json:"coverage"`
	Assets      []Asset            `json:"assets,omitempty"`
	Exposures   []Exposure         `json:"exposures,omitempty"`
	Routes      []Route            `json:"routes,omitempty"`
	RiskPaths   []RiskPath         `json:"risk_paths,omitempty"`
	FixPlans    []FixPlan          `json:"fix_plans,omitempty"`
	ThreatIntel ThreatIntelSummary `json:"threat_intel,omitempty"`
}

type Options struct {
	MaxFiles     int
	MaxFileBytes int64
	Mode         ScanMode
	// DiffBase is the git revision for Diff mode (default HEAD = working tree vs HEAD).
	DiffBase string
}

func DefaultOptions() Options {
	return Options{
		MaxFiles:     12_000,
		MaxFileBytes: 512 * 1024,
		Mode:         ScanModeFull,
	}
}

func DiffOptions() Options {
	opts := DefaultOptions()
	opts.Mode = ScanModeDiff
	opts.DiffBase = "HEAD"
	opts.MaxFiles = 2_000
	return opts
}

func severityForScore(score int) Severity {
	switch {
	case score >= 90:
		return SeverityCritical
	case score >= 70:
		return SeverityHigh
	case score >= 45:
		return SeverityMedium
	case score >= 20:
		return SeverityLow
	default:
		return SeverityInfo
	}
}

func gradeForScore(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 55:
		return "D"
	default:
		return "F"
	}
}

// priorityForSeverity maps final severity to codex-security P0–P3 priorities.
func priorityForSeverity(severity Severity) string {
	switch severity {
	case SeverityCritical:
		return "P0"
	case SeverityHigh:
		return "P1"
	case SeverityMedium:
		return "P2"
	case SeverityLow, SeverityInfo:
		return "P3"
	default:
		return "P3"
	}
}
