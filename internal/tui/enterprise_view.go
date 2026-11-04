package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aistatine/sonut/internal/security"
)

// enterpriseText renders the ENTERPRISE panel: session egress counters from
// the security guard, the size of the persisted on-disk audit trail, and the
// hardening capabilities available from internal/enterprise. It is a
// read-only status view — nothing here mutates guard or layer state.
func (m model) enterpriseText() string {
	guard := security.Default()
	counters := guard.Summary()

	return renderCommandCardTranscript(commandCard{
		Title: "Enterprise Security",
		Summary: []string{
			fmt.Sprintf("%d requests out", counters.RequestsOut),
			fmt.Sprintf("%d hosts", counters.Hosts),
			fmt.Sprintf("%d blocked", counters.Blocked),
		},
		Sections: []commandCardSection{
			{
				Title: "Audit trail (persisted)",
				Lines: auditTrailLines(filepath.Join(m.cwd, ".atcode", "audit")),
			},
			{
				Title: "Capabilities",
				Lines: []string{
					"audit — every egress event mirrored to the enterprise audit trail",
					"authz — role-based permission checks (deny by default)",
					"redaction — sensitive headers stripped from logged requests",
					"ssrf-guard — outbound URL validation blocks internal/link-local targets",
					"ratelimit — sliding/fixed-window request limiting",
					"secretbox — AES-GCM encryption for data at rest",
					"token — signed JWT session tokens",
				},
			},
			{
				Title: "Tip",
				Lines: []string{"use /sec for the unified posture, live egress, outbound surfaces, controls, and advisor workspace"},
			},
		},
	})
}

// auditTrailLines summarizes the daily JSONL audit files under dir, newest
// first, capped to the most recent seven days.
func auditTrailLines(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return []string{"(no persisted audit files yet — they appear in .atcode/audit as egress happens)"}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > 7 {
		names = names[:7]
	}
	lines := make([]string, 0, len(names))
	for _, name := range names {
		day := strings.TrimSuffix(name, ".jsonl")
		count := 0
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			count = strings.Count(string(data), "\n")
		}
		lines = append(lines, fmt.Sprintf("%s — %d event(s)", day, count))
	}
	if len(lines) == 0 {
		return []string{"(no persisted audit files yet — they appear in .atcode/audit as egress happens)"}
	}
	return lines
}
