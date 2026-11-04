package tools

import (
	"strings"
	"testing"
)

// A trailing-& backgrounded process dies the moment the bash tool returns
// (the process group is killed), which surfaced as "no output" + exit -1 when
// a model ran `python3 -m http.server 8080 &`. The command must be blocked
// pre-execution with a redirect to exec_command's persistent sessions.
func TestDetectBashBackgroundIssue(t *testing.T) {
	blocked := []string{
		"python3 -m http.server 8080 &",
		"cd /tmp/games && python3 -m http.server 8080 &",
		"nohup npm run dev",
		"npm run dev & disown",
	}
	for _, command := range blocked {
		issue := detectBashBackgroundIssue(command)
		if issue == nil {
			t.Errorf("%q must be flagged as a background process", command)
			continue
		}
		if !strings.Contains(issue.Suggestion, "exec_command") {
			t.Errorf("%q suggestion must redirect to exec_command, got %q", command, issue.Suggestion)
		}
	}
	allowed := []string{
		"python3 -m http.server 8080",
		"mkdir -p a && touch a/b", // && is not backgrounding
		"go test ./... && echo ok",
		"grep -r 'foo&' src", // & not at end of command
	}
	for _, command := range allowed {
		if issue := detectBashBackgroundIssue(command); issue != nil {
			t.Errorf("%q must not be flagged, got %+v", command, issue)
		}
	}
}
