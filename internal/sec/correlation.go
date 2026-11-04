package sec

import (
	"fmt"
	"sort"
	"strings"
)

// correlate turns isolated scanner signals into the "toxic combinations" that
// deserve operator attention. Like a network policy verdict, a path is only
// emitted when every required signal is present; absence of evidence does not
// become a guessed edge.
func correlate(report *Report, signals map[string]*fileSignals) {
	for _, signal := range signals {
		if signal.testLike {
			continue
		}
		switch {
		case signal.exposed && signal.secret:
			finding := Finding{
				RuleID:      "toxic.public-secret",
				Title:       "Public path and credential material meet in one resource",
				Description: "The same file contains an external exposure signal and credential material. This combination creates a plausible direct secret-compromise path.",
				Category:    "attack-path", RiskScore: 98, Severity: SeverityCritical,
				Path:        signal.path,
				Remediation: "Remove the public path first, rotate the credential, then replace embedded credentials with a scoped workload identity.",
				Factors:     []string{"internet exposed", "credential material", "same deployable resource"},
				AttackPath:  []string{"internet", signal.nodeID, "sensitive-data"}, ExposureState: ExposureConfiguredPublic,
				Confidence: "high", EvidenceKind: "correlated-workspace-signals",
			}
			addFinding(report, signal, finding)
		case signal.exposed && signal.privileged:
			finding := Finding{
				RuleID:      "toxic.public-privilege",
				Title:       "Configured public ingress meets elevated host access",
				Description: "A configured public ingress boundary and elevated runtime privilege occur in the same resource. This is an evidence-backed configuration path, not proof of public reachability or exploitation.",
				Category:    "attack-path", RiskScore: 96, Severity: SeverityCritical,
				Path:        signal.path,
				Remediation: "Restrict ingress and remove host-level privilege. Treat both changes as one remediation because either signal amplifies the other.",
				Factors:     []string{"internet exposed", "elevated identity", "same deployable resource"},
				AttackPath:  []string{"internet", signal.nodeID, "runtime-identity"}, ExposureState: ExposureConfiguredPublic,
				Confidence: "high", EvidenceKind: "correlated-workspace-signals",
			}
			addFinding(report, signal, finding)
		case signal.workflow && signal.unpinned && signal.privileged:
			finding := Finding{
				RuleID:      "toxic.ci-supply-chain",
				Title:       "Mutable CI dependency can reach a write-capable identity",
				Description: "A mutable action reference and broad workflow permission appear together. If the action changes maliciously, the workflow token provides an immediate write path.",
				Category:    "attack-path", RiskScore: 94, Severity: SeverityCritical,
				Path:        signal.path,
				Remediation: "Pin the action to a reviewed commit SHA and reduce the workflow token to read-only before re-enabling privileged jobs.",
				Factors:     []string{"mutable dependency", "write-capable identity", "CI execution path"},
				AttackPath:  []string{"supply-chain", signal.nodeID, "ci-identity"},
				Confidence:  "high", EvidenceKind: "correlated-workspace-signals",
			}
			addFinding(report, signal, finding)
		}
	}
	linkFindingsToAssets(report)
	buildGraph(report)
	buildRiskPaths(report)
}

func buildGraph(report *Report) {
	nodes := map[string]Node{}
	for _, node := range report.Nodes {
		nodes[node.ID] = node
	}
	if _, ok := nodes["workspace"]; !ok {
		nodes["workspace"] = Node{ID: "workspace", Label: "workspace", Kind: "workspace"}
	}
	edges := map[string]Edge{}
	for _, edge := range report.Edges {
		edges[edge.From+"\x00"+edge.To+"\x00"+edge.Relation] = edge
	}
	addNode := func(node Node) {
		if _, exists := nodes[node.ID]; !exists {
			nodes[node.ID] = node
		}
	}
	addEdge := func(edge Edge) {
		key := edge.From + "\x00" + edge.To + "\x00" + edge.Relation
		edges[key] = edge
	}

	for index := range report.Findings {
		finding := &report.Findings[index]
		fileID := "file:" + finding.Path
		findingNodeID := "finding:" + finding.ID
		addNode(Node{
			ID: findingNodeID, Label: finding.Title, Kind: "finding", Path: finding.Path,
			Severity: finding.Severity,
			Labels:   map[string]string{"category": finding.Category, "score": fmt.Sprint(finding.RiskScore)},
		})
		evidenceID := "finding:" + finding.ID
		if finding.Path != "" {
			addNode(Node{
				ID: fileID, Label: finding.Path, Kind: "resource", Path: finding.Path,
				Labels: map[string]string{"scope": "workspace"},
			})
			addEdge(Edge{From: findingNodeID, To: fileID, Relation: "affects", Verdict: "observed", EvidenceID: evidenceID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind, EvidenceSource: finding.Path})
			addEdge(Edge{From: fileID, To: "workspace", Relation: "belongs to", Verdict: "observed", EvidenceID: evidenceID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind, EvidenceSource: finding.Path})
		}
		switch finding.Category {
		case "exposure":
			if finding.RiskScore >= 45 {
				addNode(Node{ID: "internet", Label: "Internet", Kind: "exposure", Labels: map[string]string{"trust": "untrusted"}})
				addEdge(Edge{From: "internet", To: findingNodeID, Relation: "configured ingress", Verdict: "configured", EvidenceID: evidenceID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind, EvidenceSource: finding.Path})
				if len(finding.AttackPath) == 0 {
					finding.AttackPath = []string{"internet", findingNodeID, fileID}
				}
			}
		case "secrets", "data":
			addNode(Node{ID: "sensitive-data", Label: "Sensitive data", Kind: "data", Labels: map[string]string{"value": "high"}})
			if fileID != "file:" {
				addEdge(Edge{From: fileID, To: "sensitive-data", Relation: "contains", Verdict: "observed", EvidenceID: evidenceID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind, EvidenceSource: finding.Path})
			}
		case "identity", "ai-control":
			identityID, label := "runtime-identity", "Runtime identity"
			if strings.HasPrefix(finding.RuleID, "ci.") {
				identityID, label = "ci-identity", "CI workflow identity"
			}
			addNode(Node{ID: identityID, Label: label, Kind: "identity", Labels: map[string]string{"privilege": "elevated"}})
			if fileID != "file:" {
				addEdge(Edge{From: fileID, To: identityID, Relation: "runs as", Verdict: "observed", EvidenceID: evidenceID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind, EvidenceSource: finding.Path})
			}
		case "supply-chain":
			addNode(Node{ID: "supply-chain", Label: "Software supply chain", Kind: "exposure", Labels: map[string]string{"trust": "third-party"}})
			addEdge(Edge{From: "supply-chain", To: findingNodeID, Relation: "can influence", Verdict: "configured", EvidenceID: evidenceID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind, EvidenceSource: finding.Path})
		case "attack-path":
			for i := 0; i+1 < len(finding.AttackPath); i++ {
				from, to := finding.AttackPath[i], finding.AttackPath[i+1]
				switch from {
				case "internet":
					addNode(Node{ID: from, Label: "Internet", Kind: "exposure", Labels: map[string]string{"trust": "untrusted"}})
				case "supply-chain":
					addNode(Node{ID: from, Label: "Software supply chain", Kind: "exposure", Labels: map[string]string{"trust": "third-party"}})
				}
				switch to {
				case "sensitive-data":
					addNode(Node{ID: to, Label: "Sensitive data", Kind: "data", Labels: map[string]string{"value": "high"}})
				case "runtime-identity":
					addNode(Node{ID: to, Label: "Runtime identity", Kind: "identity", Labels: map[string]string{"privilege": "elevated"}})
				case "ci-identity":
					addNode(Node{ID: to, Label: "CI workflow identity", Kind: "identity", Labels: map[string]string{"privilege": "write"}})
				default:
					if strings.HasPrefix(to, "file:") {
						addNode(Node{ID: to, Label: strings.TrimPrefix(to, "file:"), Kind: "resource", Path: strings.TrimPrefix(to, "file:")})
					}
				}
				if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
					continue
				}
				addEdge(Edge{From: from, To: to, Relation: "attack path", Verdict: "correlated", EvidenceID: evidenceID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind, EvidenceSource: finding.Path})
			}
		}
	}

	report.Nodes = make([]Node, 0, len(nodes))
	for _, node := range nodes {
		report.Nodes = append(report.Nodes, node)
	}
	sort.Slice(report.Nodes, func(i, j int) bool { return report.Nodes[i].ID < report.Nodes[j].ID })
	report.Edges = make([]Edge, 0, len(edges))
	for _, edge := range edges {
		report.Edges = append(report.Edges, edge)
	}
	sort.Slice(report.Edges, func(i, j int) bool {
		if report.Edges[i].From != report.Edges[j].From {
			return report.Edges[i].From < report.Edges[j].From
		}
		if report.Edges[i].To != report.Edges[j].To {
			return report.Edges[i].To < report.Edges[j].To
		}
		return report.Edges[i].Relation < report.Edges[j].Relation
	})
}

func linkFindingsToAssets(report *Report) {
	for index := range report.Findings {
		finding := &report.Findings[index]
		for _, asset := range report.Assets {
			if asset.SourcePath == finding.Path {
				if finding.Service == "" {
					finding.Service = asset.Service
				}
				if finding.Owner == "" && asset.Owner != "" {
					finding.Owner = asset.Owner
				}
				if finding.ExposureState == "" {
					for _, exposure := range report.Exposures {
						if exposure.AssetID == asset.ID {
							finding.ExposureState = exposure.State
							break
						}
					}
				}
				break
			}
		}
	}
}

func buildRiskPaths(report *Report) {
	report.RiskPaths = nil
	for _, finding := range report.Findings {
		if len(finding.AttackPath) < 2 || finding.Category != "attack-path" {
			continue
		}
		pathClass := RiskPathDataAccess
		switch {
		case strings.Contains(finding.RuleID, "ci-supply"), strings.Contains(finding.RuleID, "supply"):
			pathClass = RiskPathSupplyChain
		case strings.Contains(finding.RuleID, "public-privilege"):
			pathClass = RiskPathPrivilegeEscalation
		case strings.Contains(finding.RuleID, "public-secret"):
			pathClass = RiskPathInitialAccess
		}
		steps := make([]RiskPathStep, 0, len(finding.AttackPath)-1)
		for index := 0; index+1 < len(finding.AttackPath); index++ {
			from, to := strings.TrimSpace(finding.AttackPath[index]), strings.TrimSpace(finding.AttackPath[index+1])
			if from == "" || to == "" {
				continue
			}
			steps = append(steps, RiskPathStep{NodeID: to, Relation: "attack path", EvidenceID: "finding:" + finding.ID, Confidence: finding.Confidence, ObservationType: finding.EvidenceKind})
		}
		if len(steps) == 0 {
			continue
		}
		report.RiskPaths = append(report.RiskPaths, RiskPath{ID: "path:" + finding.ID, Class: pathClass, Title: finding.Title, Steps: steps, RiskScore: finding.RiskScore, Confidence: finding.Confidence, EvidenceIDs: []string{"finding:" + finding.ID}, Owner: finding.Owner, Service: finding.Service, FixTarget: finding.FixTarget})
	}
	sort.Slice(report.RiskPaths, func(i, j int) bool { return report.RiskPaths[i].ID < report.RiskPaths[j].ID })
}

func detectControls(paths map[string]bool) []Control {
	hasPrefix := func(prefix string) (bool, string) {
		for path := range paths {
			if strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix)) {
				return true, path
			}
		}
		return false, ""
	}
	hasOne := func(candidates ...string) (bool, string) {
		for _, candidate := range candidates {
			for path := range paths {
				if strings.EqualFold(path, candidate) {
					return true, path
				}
			}
		}
		return false, ""
	}
	control := func(id, name, description string, present bool, evidence string) Control {
		status := "missing"
		if present {
			status = "present"
		}
		return Control{ID: id, Name: name, Status: status, Description: description, Evidence: evidence}
	}

	securityPolicy, securityEvidence := hasOne("SECURITY.md", ".github/SECURITY.md")
	dependencyUpdates, dependencyEvidence := hasOne(".github/dependabot.yml", ".github/dependabot.yaml", "renovate.json", ".renovaterc")
	owners, ownersEvidence := hasOne("CODEOWNERS", ".github/CODEOWNERS", "docs/CODEOWNERS")
	lockfile, lockEvidence := hasOne("go.sum", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "Cargo.lock", "Gemfile.lock", "poetry.lock")
	ci, ciEvidence := hasPrefix(".github/workflows/")
	ignore, ignoreEvidence := hasOne(".gitignore")

	return []Control{
		control("policy.security", "Vulnerability reporting policy", "Gives researchers and operators a safe, documented reporting path.", securityPolicy, securityEvidence),
		control("supply.dependency-updates", "Automated dependency updates", "Keeps direct dependencies and pinned workflow actions current.", dependencyUpdates, dependencyEvidence),
		control("ownership.codeowners", "Security ownership mapping", "Routes changes to accountable reviewers through CODEOWNERS.", owners, ownersEvidence),
		control("supply.lockfile", "Reproducible dependency lock", "Makes the resolved software supply chain reviewable and repeatable.", lockfile, lockEvidence),
		control("pipeline.ci", "Continuous verification", "Runs automated checks when the code changes.", ci, ciEvidence),
		control("secrets.ignore", "Repository ignore boundary", "Helps keep local secrets and generated security artifacts out of commits.", ignore, ignoreEvidence),
	}
}

func finalize(report *Report) {
	sort.SliceStable(report.Findings, func(i, j int) bool {
		if report.Findings[i].RiskScore != report.Findings[j].RiskScore {
			return report.Findings[i].RiskScore > report.Findings[j].RiskScore
		}
		if report.Findings[i].Path != report.Findings[j].Path {
			return report.Findings[i].Path < report.Findings[j].Path
		}
		if report.Findings[i].Line != report.Findings[j].Line {
			return report.Findings[i].Line < report.Findings[j].Line
		}
		return report.Findings[i].RuleID < report.Findings[j].RuleID
	})
	for index := range report.Findings {
		finding := &report.Findings[index]
		if finding.Priority == "" {
			finding.Priority = priorityForSeverity(finding.Severity)
		}
		// codex-security severity policy: test/docs-only and low-confidence
		// findings must not keep critical without correlated evidence.
		if finding.Category != "attack-path" && finding.RiskScore >= 90 &&
			(strings.Contains(strings.ToLower(finding.Path), "test") ||
				strings.Contains(strings.ToLower(finding.Path), "fixture") ||
				strings.Contains(strings.ToLower(finding.Path), "example")) {
			finding.RiskScore = clamp(finding.RiskScore-30, 10, 100)
			finding.Severity = severityForScore(finding.RiskScore)
			finding.Priority = priorityForSeverity(finding.Severity)
			finding.Factors = compactStrings(append(finding.Factors, "severity tempered: test/example path"))
		}
		switch finding.Severity {
		case SeverityCritical:
			report.Summary.Critical++
		case SeverityHigh:
			report.Summary.High++
		case SeverityMedium:
			report.Summary.Medium++
		case SeverityLow:
			report.Summary.Low++
		default:
			report.Summary.Info++
		}
	}

	penalty := 0.0
	seenSecrets := map[string]bool{}
	for index, finding := range report.Findings {
		if index >= 12 {
			break
		}
		if finding.secretFingerprint != "" {
			if seenSecrets[finding.secretFingerprint] {
				continue
			}
			seenSecrets[finding.secretFingerprint] = true
		}
		weight := 0.0
		switch finding.Severity {
		case SeverityCritical:
			weight = 20
		case SeverityHigh:
			weight = 11
		case SeverityMedium:
			weight = 5
		case SeverityLow:
			weight = 1.5
		case SeverityInfo:
			weight = 0.25
		}
		penalty += weight / (1 + float64(index)*0.16)
	}
	missingControls := 0
	for _, control := range report.Controls {
		if control.Status == "missing" {
			missingControls++
		}
	}
	penalty += float64(missingControls) * 1.5
	report.Score = clamp(100-int(penalty+0.5), 0, 100)
	report.Grade = gradeForScore(report.Score)
}
