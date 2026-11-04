package tools

import (
	"strings"

	sonutSandbox "github.com/aistatine/sonut/internal/sandbox"
)

var sandboxDeniedKeywords = []string{
	"operation not permitted",
	"permission denied",
	"read-only file system",
	"seccomp",
	"sandbox",
	"landlock",
	"failed to write file",
}

var sandboxNetworkDeniedKeywords = []string{
	"cannot open a network socket",
	"cannot open netlink socket",
	"uv_interface_addresses",
	"listen eperm",
	"getaddrinfo eai_again",
	"network is unreachable",
}

func markLikelySandboxDenial(meta map[string]string, plan sonutSandbox.CommandPlan, exitCode int, sections ...string) {
	kind, keyword := sandboxDenialKind(plan, exitCode, sections...)
	if meta == nil || kind == "" {
		return
	}
	meta[SandboxLikelyDeniedMeta] = "true"
	meta[SandboxDenialKindMeta] = kind
	meta[SandboxDenialReasonMeta] = "sandbox blocked command execution"
	if keyword != "" {
		meta[SandboxDenialKeywordMeta] = keyword
	}
}

func likelySandboxDenied(plan sonutSandbox.CommandPlan, exitCode int, sections ...string) bool {
	kind, _ := sandboxDenialKind(plan, exitCode, sections...)
	return kind != ""
}

func sandboxDenialKind(plan sonutSandbox.CommandPlan, exitCode int, sections ...string) (string, string) {
	if !plan.Wrapped {
		return "", ""
	}
	if networkDeniedBySandbox(plan) {
		if keyword := sandboxNetworkDenialKeyword(sections...); keyword != "" {
			return SandboxDenialKindNetwork, keyword
		}
	}
	if exitCode == 0 {
		return "", ""
	}
	if sandboxDenialKeyword(sections...) != "" {
		if networkDeniedBySandbox(plan) {
			if keyword := sandboxNetworkDenialKeyword(sections...); keyword != "" {
				return SandboxDenialKindNetwork, keyword
			}
		}
		return SandboxDenialKindSandbox, sandboxDenialKeyword(sections...)
	}
	if plan.TargetBackend == sonutSandbox.BackendLinuxBwrap && exitCode == 128+31 {
		return SandboxDenialKindSandbox, "seccomp"
	}
	return "", ""
}

func networkDeniedBySandbox(plan sonutSandbox.CommandPlan) bool {
	return plan.PermissionProfile.Network.Mode == sonutSandbox.NetworkDeny || plan.Policy.Network == sonutSandbox.NetworkDeny
}

func sandboxNetworkDenialKeyword(sections ...string) string {
	for _, section := range sections {
		lower := strings.ToLower(section)
		for _, keyword := range sandboxNetworkDeniedKeywords {
			if strings.Contains(lower, keyword) {
				return keyword
			}
		}
	}
	return ""
}

func sandboxDenialKeyword(sections ...string) string {
	for _, section := range sections {
		lower := strings.ToLower(section)
		for _, keyword := range sandboxDeniedKeywords {
			if strings.Contains(lower, keyword) {
				return keyword
			}
		}
	}
	return ""
}
