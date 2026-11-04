package sec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const OutputDir = ".atcode/sec"

type Artifacts struct {
	Directory string
	Report    string
	Markdown  string
	SARIF     string
}

// WriteArtifacts atomically writes the machine-readable report consumed by
// automation and the native /sec TUI. It is user-readable only because even
// redacted security metadata (paths and control state) should not be globally
// exposed. Also writes report.md and findings.sarif (codex-security export style).
func WriteArtifacts(report Report) (Artifacts, error) {
	dir := filepath.Join(report.Root, filepath.FromSlash(OutputDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Artifacts{}, fmt.Errorf("create security output directory: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return Artifacts{}, fmt.Errorf("encode security report: %w", err)
	}
	artifacts := Artifacts{
		Directory: dir,
		Report:    filepath.Join(dir, "report.json"),
		Markdown:  filepath.Join(dir, "report.md"),
		SARIF:     filepath.Join(dir, "findings.sarif"),
	}
	if err := atomicWrite(artifacts.Report, append(data, '\n'), 0o600); err != nil {
		return Artifacts{}, err
	}
	if err := atomicWrite(artifacts.Markdown, []byte(RenderMarkdown(report)), 0o600); err != nil {
		return Artifacts{}, fmt.Errorf("write markdown report: %w", err)
	}
	// SARIF is best-effort: projection errors must not invalidate the canonical JSON.
	if sarifData, sarifErr := RenderSARIF(report); sarifErr == nil {
		_ = atomicWrite(artifacts.SARIF, append(sarifData, '\n'), 0o600)
	}
	// Remove the exact legacy artifact from pre-native /sec releases. The
	// security UI now lives entirely in the TUI and should not leave a stale
	// browser surface behind.
	legacyDashboard := filepath.Join(dir, "dashboard.html")
	if err := os.Remove(legacyDashboard); err != nil && !os.IsNotExist(err) {
		return Artifacts{}, fmt.Errorf("remove legacy security dashboard: %w", err)
	}
	return artifacts, nil
}

// RenderMarkdown builds a human-readable summary for report.md.
func RenderMarkdown(report Report) string {
	var b strings.Builder
	mode := string(report.Mode)
	if mode == "" {
		mode = string(ScanModeFull)
	}
	fmt.Fprintf(&b, "# Security posture report\n\n")
	fmt.Fprintf(&b, "- **Generated:** %s\n", report.GeneratedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Root:** `%s`\n", report.Root)
	fmt.Fprintf(&b, "- **Mode:** %s\n", mode)
	if report.DiffBase != "" && report.Mode == ScanModeDiff {
		fmt.Fprintf(&b, "- **Diff base:** `%s`\n", report.DiffBase)
	}
	fmt.Fprintf(&b, "- **Score:** %d (%s)\n", report.Score, report.Grade)
	fmt.Fprintf(&b, "- **Summary:** critical=%d high=%d medium=%d low=%d info=%d\n",
		report.Summary.Critical, report.Summary.High, report.Summary.Medium, report.Summary.Low, report.Summary.Info)
	fmt.Fprintf(&b, "- **Coverage:** scanned=%d skipped=%d truncated=%v duration_ms=%d\n\n",
		report.Coverage.FilesScanned, report.Coverage.FilesSkipped, report.Coverage.Truncated, report.Coverage.DurationMillis)
	fmt.Fprintf(&b, "- **Threat intel:** %s", report.ThreatIntel.Status)
	if report.ThreatIntel.Source != "" {
		fmt.Fprintf(&b, " (%s)", report.ThreatIntel.Source)
	}
	fmt.Fprintf(&b, " · matches=%d exact=%d\n\n", report.ThreatIntel.Matches, report.ThreatIntel.ExactMatches)

	if len(report.Assets) > 0 {
		b.WriteString("## Assets and exposure\n\n")
		for _, asset := range report.Assets {
			fmt.Fprintf(&b, "- **%s** `%s` · %s", asset.Name, asset.ID, asset.Kind)
			if asset.Owner != "" {
				fmt.Fprintf(&b, " · owner=%s", asset.Owner)
			}
			b.WriteString("\n")
			for _, exposure := range report.Exposures {
				if exposure.AssetID == asset.ID {
					fmt.Fprintf(&b, "  - exposure=%s confidence=%s entry=%s\n", exposure.State, exposure.Confidence, exposure.EntryPoint)
				}
			}
		}
		b.WriteString("\n")
	}
	if len(report.Routes) > 0 {
		b.WriteString("## Configured proxy and ingress routes\n\n")
		for _, route := range report.Routes {
			entry := firstNonEmpty(route.Host+route.Path, "local route")
			target := firstNonEmpty(route.Target, "target unknown")
			fmt.Fprintf(&b, "- **%s** `%s` → `%s` · exposure=%s · confidence=%s\n", entry, route.Name, target, route.ExposureState, route.Confidence)
			fmt.Fprintf(&b, "  - protocol=%s tls=%s authentication=%s", firstNonEmpty(route.Protocol, "unknown"), firstNonEmpty(route.TLS, "unknown"), firstNonEmpty(route.Authentication, "unknown"))
			if len(route.Middlewares) > 0 {
				fmt.Fprintf(&b, " · middlewares=%s", strings.Join(route.Middlewares, ","))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n_Static route configuration does not prove public reachability._\n\n")
	}

	if report.Policy != "" {
		b.WriteString("## Repository SECURITY.md (untrusted policy data)\n\n")
		b.WriteString("```\n")
		// Cap policy in markdown projection.
		policy := report.Policy
		if len(policy) > 3000 {
			policy = policy[:3000] + "\n…[truncated]"
		}
		b.WriteString(policy)
		b.WriteString("\n```\n\n")
	}

	b.WriteString("## Prioritized findings\n\n")
	if len(report.Findings) == 0 {
		b.WriteString("_No findings in this scan scope._\n\n")
	}
	limit := len(report.Findings)
	if limit > 50 {
		limit = 50
	}
	for i, finding := range report.Findings[:limit] {
		fmt.Fprintf(&b, "### %d. [%s] %s — %s\n\n", i+1, finding.Priority, finding.Severity, finding.Title)
		fmt.Fprintf(&b, "- **ID:** `%s` · **Rule:** `%s` · **Score:** %d\n", finding.ID, finding.RuleID, finding.RiskScore)
		if finding.Path != "" {
			loc := finding.Path
			if finding.Line > 0 {
				loc = fmt.Sprintf("%s:%d", finding.Path, finding.Line)
			}
			fmt.Fprintf(&b, "- **Location:** `%s`\n", loc)
		}
		fmt.Fprintf(&b, "- **Description:** %s\n", finding.Description)
		if finding.Evidence != "" {
			fmt.Fprintf(&b, "- **Evidence:** `%s`\n", strings.ReplaceAll(finding.Evidence, "`", "'"))
		}
		if len(finding.AttackPath) > 0 {
			fmt.Fprintf(&b, "- **Attack path:** %s\n", strings.Join(finding.AttackPath, " → "))
		}
		if finding.ExposureState != "" || finding.Owner != "" || finding.Service != "" || finding.FixTarget != "" {
			fmt.Fprintf(&b, "- **Routing:** exposure=%s service=%q owner=%q fix_target=%q\n", finding.ExposureState, finding.Service, finding.Owner, finding.FixTarget)
		}
		if finding.Source != "" || finding.Sink != "" {
			fmt.Fprintf(&b, "- **Static tuple:** source=%q control=%q sink=%q\n", finding.Source, finding.Control, finding.Sink)
		}
		fmt.Fprintf(&b, "- **Remediation:** %s\n", finding.Remediation)
		fmt.Fprintf(&b, "- **Verify:** %s\n\n", finding.Verification)
	}
	if len(report.RiskPaths) > 0 {
		b.WriteString("## Evidence-backed risk paths\n\n")
		for _, path := range report.RiskPaths {
			fmt.Fprintf(&b, "- **%s** `%s` · score=%d · confidence=%s · owner=%s\n", path.Title, path.Class, path.RiskScore, path.Confidence, firstNonEmpty(path.Owner, "unowned"))
			steps := make([]string, 0, len(path.Steps))
			for _, step := range path.Steps {
				steps = append(steps, step.NodeID)
			}
			fmt.Fprintf(&b, "  - steps: %s\n", strings.Join(steps, " → "))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Controls\n\n")
	for _, control := range report.Controls {
		fmt.Fprintf(&b, "- **%s** (%s): %s", control.Name, control.Status, control.Description)
		if control.Evidence != "" {
			fmt.Fprintf(&b, " — `%s`", control.Evidence)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n---\n_Agentless local scan. No secret values are stored. Does not claim live exploitability._\n")
	return b.String()
}

// RenderSARIF emits SARIF 2.1.0 for GitHub code scanning style consumers.
// Deterministic projection only; canonical truth remains report.json.
func RenderSARIF(report Report) ([]byte, error) {
	type sarifMsg struct {
		Text string `json:"text"`
	}
	type sarifRegion struct {
		StartLine int `json:"startLine,omitempty"`
	}
	type sarifArtifactLoc struct {
		URI string `json:"uri"`
	}
	type sarifPhysical struct {
		ArtifactLocation sarifArtifactLoc `json:"artifactLocation"`
		Region           *sarifRegion     `json:"region,omitempty"`
	}
	type sarifLocation struct {
		PhysicalLocation sarifPhysical `json:"physicalLocation"`
	}
	type sarifRule struct {
		ID               string   `json:"id"`
		Name             string   `json:"name,omitempty"`
		ShortDescription sarifMsg `json:"shortDescription"`
		FullDescription  sarifMsg `json:"fullDescription,omitempty"`
		Help             sarifMsg `json:"help,omitempty"`
		DefaultConfig    struct {
			Level string `json:"level"`
		} `json:"defaultConfiguration"`
	}
	type sarifResult struct {
		RuleID     string          `json:"ruleId"`
		Level      string          `json:"level"`
		Message    sarifMsg        `json:"message"`
		Locations  []sarifLocation `json:"locations,omitempty"`
		Properties map[string]any  `json:"properties,omitempty"`
	}
	type sarifDriver struct {
		Name           string      `json:"name"`
		InformationURI string      `json:"informationUri,omitempty"`
		Version        string      `json:"version"`
		Rules          []sarifRule `json:"rules"`
	}
	type sarifTool struct {
		Driver sarifDriver `json:"driver"`
	}
	type sarifRun struct {
		Tool    sarifTool     `json:"tool"`
		Results []sarifResult `json:"results"`
	}
	type sarifLog struct {
		Schema  string     `json:"$schema"`
		Version string     `json:"version"`
		Runs    []sarifRun `json:"runs"`
	}

	rulesByID := map[string]sarifRule{}
	results := make([]sarifResult, 0, len(report.Findings))
	for _, finding := range report.Findings {
		level := sarifLevel(finding.Severity)
		if _, ok := rulesByID[finding.RuleID]; !ok {
			rule := sarifRule{
				ID:               finding.RuleID,
				Name:             finding.RuleID,
				ShortDescription: sarifMsg{Text: finding.Title},
				FullDescription:  sarifMsg{Text: finding.Description},
				Help:             sarifMsg{Text: finding.Remediation},
			}
			rule.DefaultConfig.Level = level
			rulesByID[finding.RuleID] = rule
		}
		var locs []sarifLocation
		if finding.Path != "" {
			uri := strings.TrimPrefix(filepath.ToSlash(finding.Path), "./")
			pl := sarifPhysical{ArtifactLocation: sarifArtifactLoc{URI: uri}}
			if finding.Line > 0 {
				pl.Region = &sarifRegion{StartLine: finding.Line}
			}
			locs = append(locs, sarifLocation{PhysicalLocation: pl})
		}
		results = append(results, sarifResult{
			RuleID:    finding.RuleID,
			Level:     level,
			Message:   sarifMsg{Text: finding.Title + ": " + finding.Description},
			Locations: locs,
			Properties: map[string]any{
				"atcode/v1": map[string]any{
					"id":             finding.ID,
					"priority":       finding.Priority,
					"risk_score":     finding.RiskScore,
					"category":       finding.Category,
					"exposure_state": finding.ExposureState,
					"owner":          finding.Owner,
					"service":        finding.Service,
					"fix_target":     finding.FixTarget,
					"threat_intel":   finding.ThreatIntel,
				},
			},
		})
	}
	rules := make([]sarifRule, 0, len(rulesByID))
	for _, rule := range rulesByID {
		rules = append(rules, rule)
	}
	// Stable rule order for deterministic SARIF.
	for i := 0; i < len(rules); i++ {
		for j := i + 1; j < len(rules); j++ {
			if rules[j].ID < rules[i].ID {
				rules[i], rules[j] = rules[j], rules[i]
			}
		}
	}
	doc := sarifLog{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:    "atcode-sec",
				Version: fmt.Sprintf("%d", ReportVersion),
				Rules:   rules,
			}},
			Results: results,
		}},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sarifLevel(severity Severity) string {
	switch severity {
	case SeverityCritical, SeverityHigh:
		return "error"
	case SeverityMedium:
		return "warning"
	default:
		return "note"
	}
}

func ReadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Report{}, fmt.Errorf("read security report: %w", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return Report{}, fmt.Errorf("decode security report: %w", err)
	}
	if report.Version != ReportVersion {
		return Report{}, fmt.Errorf("unsupported security report version %d", report.Version)
	}
	return report, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".sec-*")
	if err != nil {
		return fmt.Errorf("create temporary artifact: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return fmt.Errorf("secure artifact permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write security artifact: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync security artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close security artifact: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish security artifact: %w", err)
	}
	return nil
}
