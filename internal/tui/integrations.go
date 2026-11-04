package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Build-mode integrations: optional third-party credentials (Supabase, OAuth,
// hosting) the user can attach to a project so build mode can wire real
// backends/deploys. Everything is optional — an app must always build and
// preview without any of them; a configured secret just unlocks that lane.
//
// Secrets are stored per-project in .atcode/integrations.json (0600). The
// /build status card renders each one greyed (○) until configured (●), and
// /integrate <name> <secret> flips it.

// integrationDefs is the supported set, in display order. Free-tier friendly.
var integrationDefs = []struct {
	name string
	hint string
}{
	{"supabase", "Supabase anon/service key — database, auth, storage (free tier)"},
	{"oauth", "OAuth client secret — social sign-in for the built app"},
	{"hosting", "Hosting token (e.g. Netlify/Vercel) — deploy the built app"},
}

func integrationsPath(cwd string) string {
	return filepath.Join(cwd, ".atcode", "integrations.json")
}

// loadIntegrations returns the configured integration secrets for the
// workspace. Missing or unreadable file just means "none configured".
func loadIntegrations(cwd string) map[string]string {
	data, err := os.ReadFile(integrationsPath(cwd))
	if err != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	if json.Unmarshal(data, &out) != nil {
		return map[string]string{}
	}
	return out
}

// saveIntegration persists one secret (or removes it when secret is empty).
func saveIntegration(cwd, name, secret string) error {
	all := loadIntegrations(cwd)
	if secret == "" {
		delete(all, name)
	} else {
		all[name] = secret
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	path := integrationsPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// knownIntegration reports whether name is a supported integration.
func knownIntegration(name string) bool {
	for _, def := range integrationDefs {
		if def.name == name {
			return true
		}
	}
	return false
}

// integrationsSection renders the /build card section: green dot when a
// secret is stored, grey otherwise, with the command that configures it.
func integrationsSection(cwd string) string {
	configured := loadIntegrations(cwd)
	lines := []string{"Integrations (optional — the app builds and previews without them):"}
	for _, def := range integrationDefs {
		mark := "○"
		state := "/integrate " + def.name + " <secret> to enable"
		if strings.TrimSpace(configured[def.name]) != "" {
			mark = "●"
			state = "configured"
		}
		lines = append(lines, "  "+mark+" "+def.name+" — "+state)
	}
	return strings.Join(lines, "\n")
}

// configuredIntegrationNames lists integrations with a stored secret, sorted,
// for the build-mode system prompt (names only — never the secrets).
func configuredIntegrationNames(cwd string) []string {
	configured := loadIntegrations(cwd)
	names := make([]string, 0, len(configured))
	for name, secret := range configured {
		if strings.TrimSpace(secret) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// handleIntegrateCommand implements /integrate <name> [secret]:
//   - no args: show status of all integrations
//   - name + secret: store it (turns the /build button green)
//   - "<name> off": remove the stored secret
func (m model) handleIntegrateCommand(arg string) model {
	fields := strings.Fields(arg)
	card := func(text string) model {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem, text: text,
		})
		return m
	}
	if len(fields) == 0 {
		return card("Integrations\n" + integrationsSection(m.cwd) +
			"\nSecrets are stored in .atcode/integrations.json (0600) and never leave this machine.")
	}
	name := strings.ToLower(fields[0])
	if !knownIntegration(name) {
		names := make([]string, 0, len(integrationDefs))
		for _, def := range integrationDefs {
			names = append(names, def.name)
		}
		return card("Integrations\nstatus: warning\nUnknown integration " + name +
			". Supported: " + strings.Join(names, ", "))
	}
	if len(fields) < 2 {
		for _, def := range integrationDefs {
			if def.name == name {
				return card("Integrations\n" + def.name + ": " + def.hint +
					"\nUsage: /integrate " + name + " <secret>  (or \"/integrate " + name + " off\" to remove)")
			}
		}
	}
	secret := strings.Join(fields[1:], " ")
	if strings.EqualFold(secret, "off") {
		secret = ""
	}
	if err := saveIntegration(m.cwd, name, secret); err != nil {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendError, text: "integrations: " + err.Error(),
		})
		return m
	}
	if secret == "" {
		return card("Integrations\n" + name + ": removed\n" + integrationsSection(m.cwd))
	}
	return card("Integrations\n" + name + ": configured ●\n" + integrationsSection(m.cwd))
}
