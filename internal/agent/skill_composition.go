package agent

// Multi-skill composition reminder (ported from MiMo-Code's multi-skill
// workflow orchestration design).
//
// When the user explicitly references two or more skills via /skill-name
// tokens in the same message, inject a short reminder AFTER the user
// message (message-layer, not system prompt) so prefix cache stays stable
// and follow-through stays high. Single-skill references do not inject.

import (
	"regexp"
	"strings"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

// skillSlashPattern matches explicit /skill-name references (alphanumeric,
// dots, dashes, underscores — same charset as skill package names).
var skillSlashPattern = regexp.MustCompile(`(?:^|[\s(,;])(/[A-Za-z][A-Za-z0-9._-]{1,63})\b`)

// knownSlashCommands are atcode slash commands that must NOT be treated as
// skill names when scanning for multi-skill composition.
var knownSlashCommands = map[string]bool{
	"/build": true, "/integrate": true, "/share": true, "/plan": true,
	"/ps": true, "/stop": true, "/tools": true, "/skills": true,
	"/context": true, "/sec": true, "/image": true, "/add-dir": true,
	"/clear": true, "/search": true, "/find": true, "/hide": true,
	"/knowledge": true, "/kb": true, "/enterprise": true, "/ent": true,
	"/graph": true, "/mcp": true, "/resume": true, "/sessions": true,
	"/retitle": true, "/spec": true, "/init": true, "/compact": true,
	"/transcript": true, "/rewind": true, "/effort": true, "/btw": true,
	"/handoff": true, "/style": true, "/selfcorrect": true, "/sc": true,
	"/turns": true, "/help": true, "/doctor": true, "/health": true,
	"/config": true, "/debug": true, "/theme": true, "/exit": true,
	"/quit": true, "/commit": true, "/trash": true, "/review": true,
	"/memory": true, "/export": true, "/vim": true, "/tasks": true,
	"/git": true, "/signout": true, "/logout": true, "/nonstop": true,
	"/history": true, "/cd": true, "/browser": true, "/hw": true,
	"/hardware": true, "/feedback": true, "/mode": true, "/permissions": true,
	"/skill": true, // the skill tool name itself
}

// ExtractSkillReferences returns unique skill-like /name tokens from prompt,
// excluding known @code slash commands.
func ExtractSkillReferences(prompt string) []string {
	matches := skillSlashPattern.FindAllStringSubmatch(prompt, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		name := strings.ToLower(m[1])
		if knownSlashCommands[name] {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, strings.TrimPrefix(m[1], "/"))
	}
	return out
}

// skillCompositionReminder is the harness message injected when ≥2 skills
// are explicitly referenced. Keep it compact for local-model prefill cost.
func skillCompositionReminder(skillNames []string) string {
	return `<skill_composition_reminder>
The user has explicitly referenced multiple skills: ` + strings.Join(skillNames, ", ") + `.
Before starting work, complete a short orchestration plan:
1. Load each referenced skill via the skill tool (read full SKILL.md, not just the name).
2. Classify composition: pipeline (A→B) / parallel / constraint overlay.
3. If pipeline: define intermediate artifact format and path.
4. If skills conflict on format/style/process: declare which wins on which dimension.
5. Output a concise workflow (phase → skill → artifact), then execute it.
Keep planning proportional: simple combos need only two or three sentences.
</skill_composition_reminder>`
}

// maybeInjectSkillComposition appends a composition reminder after the latest
// user message when the prompt references ≥2 skills. Returns messages unchanged
// for 0–1 skill references (no orchestration problem).
func maybeInjectSkillComposition(messages []sonutruntime.Message, prompt string) []sonutruntime.Message {
	names := ExtractSkillReferences(prompt)
	if len(names) < 2 {
		return messages
	}
	reminder := skillCompositionReminder(names)
	// Append as a user-role system-reminder-style message (close to the query,
	// does not mutate the system prompt prefix).
	return append(messages, sonutruntime.Message{
		Role:    sonutruntime.MessageRoleUser,
		Content: reminder,
	})
}
