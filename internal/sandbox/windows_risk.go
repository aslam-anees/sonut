package sandbox

import (
	"regexp"
	"strings"
)

// windowsCommandRisk is a small, fail-closed supplement for commands that are
// executed by cmd.exe or PowerShell. The POSIX AST parser remains the primary
// analyzer; this path only recognizes high-signal command forms that the POSIX
// grammar cannot parse. It deliberately does not attempt to interpret general
// PowerShell, which keeps the hot path cheap and avoids pretending to prove
// semantics it cannot prove.
type windowsCommandRisk struct {
	destructive    bool
	network        bool
	pipedInstaller bool
}

var windowsPipedInstallerPattern = regexp.MustCompile(`(?i)^\s*(?:invoke-webrequest|iwr|invoke-restmethod|irm|curl|wget)\b[^;\r\n|]*\|\s*(?:invoke-expression|iex|powershell|pwsh)\b`)

// classifyWindowsCommand recognizes the Windows forms covered by the agentic
// safety rules: recursive deletion and remote content piped to an interpreter.
// It only follows explicit cmd /c and PowerShell -Command launchers; quoted
// documentation such as echo "Remove-Item -Recurse" is not treated as code.
func classifyWindowsCommand(command string) windowsCommandRisk {
	var result windowsCommandRisk
	for _, segment := range windowsCommandSegments(command) {
		classifyWindowsSegment(segment, &result, 0)
	}
	return result
}

func classifyWindowsSegment(segment string, result *windowsCommandRisk, depth int) {
	segment = strings.TrimSpace(segment)
	if segment == "" || depth > 2 {
		return
	}
	if windowsPipedInstallerPattern.MatchString(segment) {
		result.pipedInstaller = true
	}
	tokens := strings.Fields(segment)
	if len(tokens) == 0 {
		return
	}
	program := windowsProgramName(tokens[0])
	switch program {
	case "cmd", "cmd.exe":
		if payload := windowsLauncherPayload(tokens, "/c", "/k"); payload != "" {
			classifyWindowsSegment(payload, result, depth+1)
		}
		return
	case "powershell", "pwsh", "powershell.exe", "pwsh.exe":
		if hasWindowsFlag(tokens, "-encodedcommand", "-enc") {
			// Encoded PowerShell is not safely inspectable. Mark it as network-risky
			// so NetworkDeny cannot silently auto-allow an opaque payload.
			result.network = true
			return
		}
		if payload := windowsLauncherPayload(tokens, "-command", "-c"); payload != "" {
			classifyWindowsSegment(payload, result, depth+1)
		}
		return
	case "remove-item", "ri":
		if hasWindowsFlag(tokens, "-recurse", "-r") {
			result.destructive = true
		}
	case "rmdir", "rd":
		if hasWindowsFlag(tokens, "/s") {
			result.destructive = true
		}
	case "del", "erase":
		if hasWindowsFlag(tokens, "/s") {
			result.destructive = true
		}
	case "format":
		// Formatting a volume is unambiguously destructive even when the command
		// uses a drive letter rather than a POSIX device path.
		result.destructive = true
	case "invoke-webrequest", "iwr", "invoke-restmethod", "irm", "start-bitstransfer", "curl", "wget":
		result.network = true
	}
}

func windowsProgramName(token string) string {
	token = strings.Trim(token, "\"'")
	if index := strings.LastIndexAny(token, `/\\`); index >= 0 {
		token = token[index+1:]
	}
	return strings.ToLower(token)
}

func hasWindowsFlag(tokens []string, want ...string) bool {
	for _, token := range tokens[1:] {
		token = strings.ToLower(strings.Trim(token, "\"'"))
		for _, flag := range want {
			flag = strings.ToLower(flag)
			if token == flag || strings.HasPrefix(token, flag+":") || strings.HasPrefix(token, flag+"=") {
				return true
			}
		}
	}
	return false
}

func windowsLauncherPayload(tokens []string, flags ...string) string {
	for index, token := range tokens[1:] {
		lower := strings.ToLower(strings.Trim(token, "\"'"))
		for _, flag := range flags {
			if lower == strings.ToLower(flag) && index+2 <= len(tokens) {
				// PowerShell/cmd commonly wrap the -Command or /c payload in
				// quotes. Remove only the wrapper quotes here so the nested
				// classifier sees `Invoke-WebRequest … | iex` at the beginning
				// of the payload and cannot miss a piped installer.
				return strings.Trim(strings.Join(tokens[index+2:], " "), " \t\r\n\"'")
			}
		}
	}
	return ""
}

func windowsCommandSegments(command string) []string {
	var segments []string
	start := 0
	for index := 0; index < len(command); index++ {
		separator := 0
		switch command[index] {
		case ';', '\r', '\n':
			separator = 1
		case '&', '|':
			if index+1 < len(command) && command[index+1] == command[index] {
				separator = 2
			}
		}
		if separator == 0 {
			continue
		}
		segments = append(segments, command[start:index])
		index += separator - 1
		start = index + 1
	}
	segments = append(segments, command[start:])
	return segments
}
