// Package providercatalog is the curated, dependency-free catalog of provider
// descriptors the onboarding surfaces (TUI wizard, CLI setup, sonutcommands
// snapshots) present to the user. It deliberately imports nothing from
// internal/config so config can consume it without an import cycle; the base
// URL constants here must stay in sync with internal/config.
package providercatalog

import (
	"fmt"
	"sort"
	"strings"
)

type Transport string
type APIFormat string

const (
	APIFormatOpenAIChatCompletions APIFormat = "openai-chat-completions"
)

const (
	TransportOpenAI              Transport = "openai"
	TransportAnthropic           Transport = "anthropic"
	TransportAnthropicCompatible Transport = "anthropic-compatible"
	TransportGoogle              Transport = "google"
	TransportOpenAICompatible    Transport = "openai-compatible"
)

type Descriptor struct {
	ID                  string
	DefaultBaseURL      string
	DefaultModel        string
	AuthEnvVars         []string
	Transport           Transport
	Name                string
	RequiresAuth        bool
	Local               bool
	Recommended         bool
	OAuth               bool
	OAuthMintsKey       bool
	OAuthDeviceFlow     bool
	SupportedAPIFormats []APIFormat
}

// catalog is the source of truth, keyed by canonical id. @code is local-first,
// so the bundled llama.cpp runtime is the recommended default; hosted
// providers follow for users who bring their own keys.
var catalog = map[string]Descriptor{
	"llamacpp": {
		ID:                  "llamacpp",
		Name:                "llama.cpp (local)",
		Transport:           TransportOpenAICompatible,
		DefaultBaseURL:      "http://127.0.0.1:8080/v1",
		Local:               true,
		Recommended:         true,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
	},
	"ollama": {
		ID:                  "ollama",
		Name:                "Ollama (local)",
		Transport:           TransportOpenAICompatible,
		DefaultBaseURL:      "http://127.0.0.1:11434/v1",
		Local:               true,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
	},
	"openai": {
		ID:             "openai",
		Name:           "OpenAI",
		Transport:      TransportOpenAI,
		DefaultBaseURL: "https://api.openai.com/v1",
		DefaultModel:   "gpt-4.1",
		AuthEnvVars:    []string{"OPENAI_API_KEY"},
		RequiresAuth:   true,
	},
	"anthropic": {
		ID:             "anthropic",
		Name:           "Anthropic",
		Transport:      TransportAnthropic,
		DefaultBaseURL: "https://api.anthropic.com",
		DefaultModel:   "claude-sonnet-4.5",
		AuthEnvVars:    []string{"ANTHROPIC_API_KEY"},
		RequiresAuth:   true,
	},
	"google": {
		ID:             "google",
		Name:           "Google Gemini",
		Transport:      TransportGoogle,
		DefaultBaseURL: "https://generativelanguage.googleapis.com",
		AuthEnvVars:    []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		RequiresAuth:   true,
	},
	"groq": {
		ID:                  "groq",
		Name:                "Groq",
		Transport:           TransportOpenAICompatible,
		DefaultBaseURL:      "https://api.groq.com/openai/v1",
		AuthEnvVars:         []string{"GROQ_API_KEY"},
		RequiresAuth:        true,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
	},
	"mistral": {
		ID:                  "mistral",
		Name:                "Mistral",
		Transport:           TransportOpenAICompatible,
		DefaultBaseURL:      "https://api.mistral.ai/v1",
		AuthEnvVars:         []string{"MISTRAL_API_KEY"},
		RequiresAuth:        true,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
	},
	"openrouter": {
		ID:                  "openrouter",
		Name:                "OpenRouter",
		Transport:           TransportOpenAICompatible,
		DefaultBaseURL:      "https://openrouter.ai/api/v1",
		AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
		RequiresAuth:        true,
		OAuth:               true,
		OAuthMintsKey:       true,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
	},
	"lmstudio": {
		ID:                  "lmstudio",
		Name:                "LM Studio (local)",
		Transport:           TransportOpenAICompatible,
		DefaultBaseURL:      "http://127.0.0.1:1234/v1",
		Local:               true,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
	},
	"bedrock": {
		ID:           "bedrock",
		Name:         "Amazon Bedrock",
		Transport:    TransportOpenAICompatible,
		AuthEnvVars:  []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"},
		RequiresAuth: true,
	},
	"custom-openai-compatible": {
		ID:                  "custom-openai-compatible",
		Name:                "Custom (OpenAI-compatible)",
		Transport:           TransportOpenAICompatible,
		SupportedAPIFormats: []APIFormat{APIFormatOpenAIChatCompletions},
	},
	"custom-anthropic-compatible": {
		ID:        "custom-anthropic-compatible",
		Name:      "Custom (Anthropic-compatible)",
		Transport: TransportAnthropicCompatible,
	},
}

// runtimeUnsupported lists catalog-only providers the Go runtime cannot drive
// yet, with the user-facing reason.
var runtimeUnsupported = map[string]string{
	"bedrock": "Amazon Bedrock is catalog-only until the native adapter lands.",
}

// NormalizeID canonicalizes a catalog id: trims, lowercases, and resolves
// common aliases so config files and CLI flags are forgiving.
func NormalizeID(id string) string {
	normalized := strings.ToLower(strings.TrimSpace(id))
	switch normalized {
	case "llama.cpp", "llama-cpp", "local":
		return "llamacpp"
	case "gemini":
		return "google"
	case "claude":
		return "anthropic"
	}
	return normalized
}

func Get(id string) (Descriptor, bool) {
	descriptor, ok := catalog[NormalizeID(id)]
	if !ok {
		return Descriptor{}, false
	}
	return cloneDescriptor(descriptor), true
}

func Require(id string) (Descriptor, error) {
	descriptor, ok := Get(id)
	if !ok {
		return Descriptor{}, fmt.Errorf("unknown provider %q in catalog", id)
	}
	return descriptor, nil
}

func RuntimeSupported(d Descriptor) bool {
	_, unsupported := runtimeUnsupported[d.ID]
	return !unsupported
}

func RuntimeUnsupportedReason(d Descriptor) string {
	return runtimeUnsupported[d.ID]
}

// All returns every descriptor sorted by id (stable for listings; callers that
// want the recommended provider first re-sort themselves).
func All() []Descriptor {
	descriptors := make([]Descriptor, 0, len(catalog))
	for _, descriptor := range catalog {
		descriptors = append(descriptors, cloneDescriptor(descriptor))
	}
	sort.Slice(descriptors, func(left int, right int) bool {
		return descriptors[left].ID < descriptors[right].ID
	})
	return descriptors
}

func OAuthProviders() []Descriptor {
	descriptors := []Descriptor{}
	for _, descriptor := range All() {
		if descriptor.OAuth {
			descriptors = append(descriptors, descriptor)
		}
	}
	return descriptors
}

func cloneDescriptor(descriptor Descriptor) Descriptor {
	descriptor.AuthEnvVars = append([]string{}, descriptor.AuthEnvVars...)
	descriptor.SupportedAPIFormats = append([]APIFormat{}, descriptor.SupportedAPIFormats...)
	return descriptor
}
