package tools

import "testing"

func TestExecEgressDestination_ParsesCurlURL(t *testing.T) {
	host, port := execEgressDestination("curl", "curl -s https://example.com:8443/api", "")
	if host != "example.com" || port != 8443 {
		t.Fatalf("got host=%q port=%d", host, port)
	}
}

func TestExecEgressDestination_Shapes(t *testing.T) {
	cases := []struct {
		name    string
		binary  string
		command string
		host    string
		port    int
	}{
		{"schemeless curl", "curl", "curl example.com", "example.com", 443},
		{"schemeless curl with path and flags", "curl", "curl -X POST api.internal.io/v1", "api.internal.io", 443},
		{"curl env var has no destination", "curl", `curl "$URL"`, "", 0},
		{"ssh user@ip", "ssh", "ssh user@10.0.0.5", "10.0.0.5", 22},
		{"ssh with port flag", "ssh", "ssh -p 2222 host.example.com", "host.example.com", 2222},
		{"scp remote path", "scp", "scp f.txt host.example.com:/tmp", "host.example.com", 22},
		{"nc bare hostname", "nc", "nc localhost 8080", "localhost", 8080},
		{"nc raw ip", "nc", "nc 203.0.113.5 8080", "203.0.113.5", 8080},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port := execEgressDestination(tc.binary, tc.command, "")
			if host != tc.host || port != tc.port {
				t.Fatalf("got host=%q port=%d, want host=%q port=%d", host, port, tc.host, tc.port)
			}
		})
	}
}

func TestExecEgressDestination_GitPushResolvesRemote(t *testing.T) {
	orig := execEgressResolveGitRemote
	defer func() { execEgressResolveGitRemote = orig }()
	execEgressResolveGitRemote = func(dir, remote string) string {
		if remote != "origin" {
			t.Fatalf("expected remote origin, got %q", remote)
		}
		return "git@github.com:acme/repo.git"
	}
	host, port := execEgressDestination("git", "git push origin main", "/tmp")
	if host != "github.com" || port != 22 {
		t.Fatalf("got host=%q port=%d", host, port)
	}
}

func TestExecEgressDestination_GitPushHTTPSRemote(t *testing.T) {
	orig := execEgressResolveGitRemote
	defer func() { execEgressResolveGitRemote = orig }()
	execEgressResolveGitRemote = func(dir, remote string) string {
		return "https://github.com/acme/repo.git"
	}
	host, port := execEgressDestination("git", "git push", "/tmp")
	if host != "github.com" || port != 443 {
		t.Fatalf("got host=%q port=%d", host, port)
	}
}

func TestMatchedExecNetworkBinary_CommandPositionOnly(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{"ls -la", ""},
		{"curl https://example.com", "curl"},
		{"sudo curl example.com", "curl"},
		{"FOO=bar curl example.com", "curl"},
		{"echo curl https://x.com", ""},                            // curl is an argument, not the command
		{"grep fetch notes.txt && wget files.example.com", "wget"}, // command after separator
		{"git push origin main", "git"},
		{"git status", ""},
		{`git commit -m "do not push yet"`, ""}, // "push" in the message is not the subcommand
		{"git -C /tmp push origin main", "git"},
	}
	for _, tc := range cases {
		if got := matchedExecNetworkBinary(tc.command); got != tc.want {
			t.Fatalf("matchedExecNetworkBinary(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestFlagExecCommandEgress_BlocksRawIPNonStandardPort(t *testing.T) {
	err := flagExecCommandEgress("nc 203.0.113.5 8080", "")
	if err == nil {
		t.Fatalf("expected raw IP non-standard port to be blocked")
	}
}

func TestFlagExecCommandEgress_AllowsPlainCommand(t *testing.T) {
	if err := flagExecCommandEgress("ls -la", ""); err != nil {
		t.Fatalf("unexpected block for plain command: %v", err)
	}
}
