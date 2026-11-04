package security

import (
	"os"
	"regexp"
	"strings"

	"github.com/aistatine/sonut/internal/secrets"
)

var (
	emailPattern = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
	// dotenvLinePattern matches high-value secret-shaped assignments only. A
	// shell command commonly begins with harmless environment setup such as
	// PATH=...; treating every uppercase assignment as a secret would make the
	// best-effort exec surface contradict explicit sandbox network approvals.
	dotenvLinePattern = regexp.MustCompile(`(?mi)^(?:[A-Z][A-Z0-9_]*_)?(PASSWORD|PASSWD|SECRET|TOKEN|API[_-]?KEY|ACCESS[_-]?KEY|AUTH(?:ORIZATION)?|PRIVATE[_-]?KEY|CREDENTIAL)[A-Z0-9_-]*=\S+`)
)

// classifyPayload scans an outbound payload (URL, request body, or command
// line) for secrets, high-value dotenv-style assignments, home-directory file
// paths, and email addresses, returning the resulting classification and a
// human-readable reason.
func classifyPayload(payload string) (Classification, string) {
	if payload == "" {
		return Clean, ""
	}
	if findings := secrets.Scan(payload); len(findings) > 0 {
		return Blocked, findings[0].Type + " detected"
	}
	if dotenvLinePattern.MatchString(payload) {
		return Blocked, "possible .env-style secret assignment detected"
	}

	reasons := make([]string, 0, 2)
	if emailPattern.MatchString(payload) {
		reasons = append(reasons, "email address")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.Contains(payload, home) {
		reasons = append(reasons, "home-directory file path")
	}
	if len(reasons) > 0 {
		return Sensitive, strings.Join(reasons, ", ") + " present"
	}
	return Clean, ""
}
