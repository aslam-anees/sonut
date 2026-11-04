package tools

import (
	"context"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/security"
)

// execNetworkBinaries are shell commands whose invocation is treated as an
// outbound network intent for the security layer. Detection is best-effort:
// it parses the command line for these binaries plus a destination, it does
// not sandbox or fully understand shell syntax.
var execNetworkBinaries = []string{"curl", "wget", "nc", "ncat", "netcat", "ssh", "scp", "sftp"}

var execURLPattern = regexp.MustCompile(`https?://[^\s'"]+`)

// execCommandWrappers are tokens that keep the NEXT field in command position
// (so `sudo curl …` still detects curl, while `echo curl …` does not).
var execCommandWrappers = map[string]bool{
	"sudo": true, "env": true, "nohup": true, "time": true, "xargs": true, "command": true,
}

// execCommandSeparators put the following field back into command position.
var execCommandSeparators = map[string]bool{";": true, "&&": true, "||": true, "|": true, "&": true}

// execEgressResolveGitRemote resolves a git remote name to its URL in dir,
// best-effort (empty on any failure). Overridable for tests.
var execEgressResolveGitRemote = func(dir, remote string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", remote)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// flagExecCommandEgress best-effort scans a shell command line for
// network-capable binaries and routes the destination through the security
// checkpoint. workdir (may be empty) is used to resolve git remote names.
// It returns a non-nil error only when the checkpoint blocks the destination;
// commands with no recognizable network binary or destination are left alone
// (exec_command's normal permission flow already gates arbitrary execution).
func flagExecCommandEgress(commandText, workdir string) error {
	return flagExecCommandEgressWithMetadata(commandText, workdir, security.OutboundMetadata{})
}

func flagExecCommandEgressWithMetadata(commandText, workdir string, metadata security.OutboundMetadata) error {
	binary := matchedExecNetworkBinary(commandText)
	if binary == "" {
		return nil
	}
	host, port := execEgressDestination(binary, commandText, workdir)
	if host == "" {
		return nil
	}
	_, err := security.Default().CheckOutboundWithMetadata("exec("+binary+")", host, port, "tcp", commandText, true, metadata)
	return err
}

// matchedExecNetworkBinary returns the network binary invoked by commandText,
// matching only tokens in COMMAND position (first token, after wrappers like
// sudo/env, and after shell separators) so that `echo curl …` is not flagged.
func matchedExecNetworkBinary(commandText string) string {
	fields := strings.Fields(commandText)
	atCommand := true
	for i, field := range fields {
		if execCommandSeparators[field] {
			atCommand = true
			continue
		}
		if !atCommand {
			continue
		}
		name := field
		if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
			name = name[idx+1:]
		}
		// env assignments (FOO=bar cmd …) keep the command position open.
		if strings.Contains(name, "=") {
			continue
		}
		if execCommandWrappers[name] {
			continue
		}
		atCommand = false
		for _, bin := range execNetworkBinaries {
			if name == bin {
				return bin
			}
		}
		// git is only network-relevant when its SUBCOMMAND is push (not when
		// the word "push" merely appears somewhere, e.g. in a commit message).
		if name == "git" && gitSubcommand(fields[i+1:]) == "push" {
			return "git"
		}
	}
	return ""
}

// gitSubcommand returns the first non-flag token after `git`.
func gitSubcommand(rest []string) string {
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if execCommandSeparators[tok] {
			return ""
		}
		if strings.HasPrefix(tok, "-") {
			// `git -c key=val push` / `git -C dir push`: skip the flag's value.
			if tok == "-c" || tok == "-C" {
				i++
			}
			continue
		}
		return tok
	}
	return ""
}

// execEgressDestination extracts the (host, port) a command talks to,
// best-effort per binary. Empty host means "no recognizable destination".
func execEgressDestination(binary, commandText, workdir string) (string, int) {
	// Explicit URL anywhere in the line wins, for every binary.
	if m := execURLPattern.FindString(commandText); m != "" {
		if host, port := hostPortFromURL(m); host != "" {
			return host, port
		}
	}

	fields := strings.Fields(commandText)
	args := argsAfterBinary(fields, binary)

	switch binary {
	case "curl", "wget":
		arg := strings.Trim(firstNonFlagArg(args, curlValueFlags), `"'`)
		if arg != "" && !strings.ContainsAny(arg, "$`") {
			// Schemeless URL (`curl example.com/api`): parse with an assumed
			// https scheme so the common no-scheme invocation is still checked.
			if host, port := hostPortFromURL("https://" + arg); host != "" {
				return host, port
			}
		}
	case "ssh", "scp", "sftp":
		return sshDestination(binary, args)
	case "git":
		remote := "origin"
		if idx := indexOfField(args, "push"); idx >= 0 {
			if r := firstNonFlagArg(args[idx+1:], nil); r != "" {
				remote = r
			}
		}
		if remoteURL := execEgressResolveGitRemote(workdir, remote); remoteURL != "" {
			return gitRemoteHostPort(remoteURL)
		}
	}

	// Positional "<binary> host port" fallback (the nc/ncat shape). Accepts
	// IPs, dotted names, and bare hostnames like `localhost`.
	for i := 0; i < len(args)-1; i++ {
		host := args[i]
		if !looksLikeHost(host) {
			continue
		}
		if port, err := strconv.Atoi(args[i+1]); err == nil && port > 0 && port < 65536 {
			return host, port
		}
	}
	return "", 0
}

// curlValueFlags are curl/wget flags that consume the next token, so their
// value is not mistaken for the destination.
var curlValueFlags = map[string]bool{
	"-X": true, "-H": true, "-d": true, "-o": true, "-u": true, "-T": true,
	"-F": true, "-A": true, "-e": true, "-b": true, "-c": true,
	"--request": true, "--header": true, "--data": true, "--data-raw": true,
	"--data-binary": true, "--output": true, "--user": true, "--upload-file": true,
	"--form": true, "--user-agent": true, "--referer": true, "--cookie": true,
	"--url": true, "--connect-timeout": true, "--max-time": true, "--retry": true,
}

// sshValueFlags are ssh/scp/sftp flags that consume the next token.
var sshValueFlags = map[string]bool{
	"-i": true, "-F": true, "-l": true, "-o": true, "-J": true, "-b": true,
	"-c": true, "-D": true, "-L": true, "-R": true, "-W": true, "-w": true,
	"-E": true, "-e": true, "-m": true, "-S": true, "-B": true,
}

// sshDestination extracts host/port from ssh/scp/sftp argument shapes:
// `user@host`, `host:path`, a bare hostname, honoring -p (ssh/sftp) and
// -P (scp) port flags. Default port 22.
func sshDestination(binary string, args []string) (string, int) {
	port := 22
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if execCommandSeparators[tok] {
			break
		}
		if strings.HasPrefix(tok, "-") {
			isPortFlag := (tok == "-p" && binary != "scp") || (tok == "-P" && binary != "ssh")
			if isPortFlag && i+1 < len(args) {
				if p, err := strconv.Atoi(args[i+1]); err == nil && p > 0 && p < 65536 {
					port = p
				}
				i++
				continue
			}
			if sshValueFlags[tok] {
				i++
			}
			continue
		}
		// For scp/sftp only an explicit remote spec (`user@host` or
		// `host:path`) is a destination — a bare token is a local file.
		// For ssh a bare hostname token is the destination.
		remoteMarked := strings.ContainsAny(tok, "@:")
		if binary != "ssh" && !remoteMarked {
			continue
		}
		host := tok
		if at := strings.LastIndexByte(host, '@'); at >= 0 {
			host = host[at+1:]
		}
		if colon := strings.IndexByte(host, ':'); colon >= 0 {
			host = host[:colon]
		}
		if looksLikeHost(host) {
			return host, port
		}
	}
	return "", 0
}

// gitRemoteHostPort parses both URL remotes (https://host/…, ssh://host/…)
// and scp-like remotes (git@host:org/repo.git).
func gitRemoteHostPort(remote string) (string, int) {
	if strings.Contains(remote, "://") {
		if parsed, err := url.Parse(remote); err == nil && parsed.Hostname() != "" {
			port := 0
			if p := parsed.Port(); p != "" {
				port, _ = strconv.Atoi(p)
			} else if strings.EqualFold(parsed.Scheme, "https") {
				port = 443
			} else if strings.EqualFold(parsed.Scheme, "http") {
				port = 80
			} else if strings.EqualFold(parsed.Scheme, "ssh") || strings.EqualFold(parsed.Scheme, "git") {
				port = 22
			}
			return parsed.Hostname(), port
		}
		return "", 0
	}
	host := remote
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if colon := strings.IndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	if looksLikeHost(host) {
		return host, 22
	}
	return "", 0
}

func hostPortFromURL(raw string) (string, int) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "", 0
	}
	port := 0
	if p := parsed.Port(); p != "" {
		port, _ = strconv.Atoi(p)
	} else if strings.EqualFold(parsed.Scheme, "https") {
		port = 443
	} else if strings.EqualFold(parsed.Scheme, "http") {
		port = 80
	}
	return parsed.Hostname(), port
}

// firstNonFlagArg returns the first token that is neither a flag nor the value
// of a flag in valueFlags, stopping at shell separators.
func firstNonFlagArg(args []string, valueFlags map[string]bool) string {
	for i := 0; i < len(args); i++ {
		tok := args[i]
		if execCommandSeparators[tok] {
			return ""
		}
		if strings.HasPrefix(tok, "-") {
			if valueFlags[tok] {
				i++
			}
			continue
		}
		return tok
	}
	return ""
}

// argsAfterBinary returns the fields following the command-position token that
// matched binary (basename match), or nil when not found.
func argsAfterBinary(fields []string, binary string) []string {
	for i, field := range fields {
		name := field
		if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
			name = name[idx+1:]
		}
		if name == binary {
			return fields[i+1:]
		}
	}
	return nil
}

func indexOfField(fields []string, want string) int {
	for i, f := range fields {
		if f == want {
			return i
		}
	}
	return -1
}

// looksLikeHost accepts IPs, dotted names, and bare hostnames (letters,
// digits, dashes) such as `localhost` — but not flags or option values.
func looksLikeHost(token string) bool {
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	if net.ParseIP(token) != nil {
		return true
	}
	for _, r := range token {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	// Pure numbers (e.g. a port or count argument) are not hostnames.
	if _, err := strconv.Atoi(token); err == nil {
		return false
	}
	return true
}
