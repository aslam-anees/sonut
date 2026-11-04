package provideronboarding

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/providercatalog"
	"github.com/aistatine/sonut/internal/providerhealth"
	"github.com/aistatine/sonut/internal/providermodeldiscovery"
)

type LocalDetectOptions struct {
	HTTPClient *http.Client
	Timeout    time.Duration
	UserAgent  string
	Candidates []LocalRuntimeCandidate
}

type LocalRuntimeCandidate struct {
	CatalogID string
	Name      string
	BaseURL   string
}

type DetectedLocalRuntime struct {
	CatalogID string
	Name      string
	BaseURL   string
	Models    []string
}

func (d DetectedLocalRuntime) SetupAction() Action {
	command := "at providers add " + strings.TrimSpace(d.CatalogID)
	if baseURL := strings.TrimSpace(d.BaseURL); baseURL != "" {
		command += " --base-url " + baseURL
	}
	if len(d.Models) > 0 && strings.TrimSpace(d.Models[0]) != "" {
		command += " --model " + d.Models[0]
	}
	return Action{
		Label:   "Adopt runtime",
		Command: command,
		Detail:  "Use the detected local runtime without an API key.",
	}
}

type Action struct {
	Label   string
	Command string
	Detail  string
}

type ProviderState struct {
	Profile config.ProviderProfile
	Active  bool
}

func (s ProviderState) Actions() []Action {
	profile := s.Profile
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		name = strings.TrimSpace(profile.CatalogID)
	}
	if name == "" {
		name = strings.TrimSpace(profile.Provider)
	}
	if name == "" {
		name = "provider"
	}
	actions := make([]Action, 0, 3)
	if !s.Active {
		actions = append(actions, Action{Label: "Use provider", Command: UseCommand(name)})
	}
	if needsCredential(profile) {
		env := strings.TrimSpace(profile.APIKeyEnv)
		if env == "" {
			env = "the provider API key environment variable"
		}
		actions = append(actions, Action{
			Label:  "Set API key",
			Detail: "Set " + env + " in your shell.",
		})
	}
	actions = append(actions, Action{Label: "Check provider", Command: CheckCommand(name, true)})
	return actions
}

type SetupError struct {
	Class string
	err   error
}

func (e *SetupError) Error() string {
	if e == nil {
		return ""
	}
	if e.err == nil || strings.TrimSpace(e.err.Error()) == "" {
		return e.Class
	}
	return e.Class + ": " + e.err.Error()
}

func (e *SetupError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func DetectLocalRuntimes(ctx context.Context, opts LocalDetectOptions) []DetectedLocalRuntime {
	if ctx == nil {
		ctx = context.Background()
	}
	candidates := opts.Candidates
	if len(candidates) == 0 {
		candidates = defaultCandidates()
	}
	type detected struct {
		index   int
		runtime DetectedLocalRuntime
	}
	results := make(chan detected, len(candidates))
	var group sync.WaitGroup
	for index, candidate := range candidates {
		candidate := candidate
		group.Add(1)
		go func(index int) {
			defer group.Done()
			descriptor, ok := providercatalog.Get(candidate.CatalogID)
			if !ok {
				return
			}
			models, err := providermodeldiscovery.DiscoverCatalog(ctx, descriptor, config.ProviderProfile{
				CatalogID:    descriptor.ID,
				ProviderKind: config.ProviderKind(descriptor.Transport),
				BaseURL:      candidate.BaseURL,
			}, providermodeldiscovery.Options{
				HTTPClient: opts.HTTPClient,
				Timeout:    localProbeTimeout(opts.Timeout),
				UserAgent:  opts.UserAgent,
			})
			if err != nil {
				return
			}
			modelIDs := make([]string, 0, len(models))
			for _, model := range models {
				if modelID := strings.TrimSpace(model.ID); modelID != "" {
					modelIDs = append(modelIDs, modelID)
				}
			}
			name := strings.TrimSpace(candidate.Name)
			if name == "" {
				name = descriptor.Name
			}
			results <- detected{index: index, runtime: DetectedLocalRuntime{
				CatalogID: descriptor.ID,
				Name:      name,
				BaseURL:   candidate.BaseURL,
				Models:    modelIDs,
			}}
		}(index)
	}
	group.Wait()
	close(results)
	ordered := make([]detected, 0, len(results))
	for result := range results {
		ordered = append(ordered, result)
	}
	// Candidates are stable, but sort by index explicitly so completion order
	// cannot make CLI/JSON output flicker across runs.
	for left := 1; left < len(ordered); left++ {
		for right := left; right > 0 && ordered[right].index < ordered[right-1].index; right-- {
			ordered[right], ordered[right-1] = ordered[right-1], ordered[right]
		}
	}
	runtimes := make([]DetectedLocalRuntime, 0, len(ordered))
	for _, result := range ordered {
		runtimes = append(runtimes, result.runtime)
	}
	return runtimes
}

func UseCommand(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "at providers use <name>"
	}
	return "at providers use " + name
}

func CheckCommand(name string, connectivity bool) string {
	name = strings.TrimSpace(name)
	command := "at providers check"
	if name != "" {
		command += " " + name
	}
	if connectivity {
		command += " --connectivity"
	}
	return command
}

func ClassifySetupProbe(result providerhealth.Result) (*SetupError, bool) {
	if result.Status == providerhealth.StatusPass {
		return nil, false
	}
	check := result.PrimaryCheck()
	class := "provider unreachable"
	if check != nil {
		switch check.Category {
		case providerhealth.CategoryAuth:
			class = "authentication failed"
		case providerhealth.CategoryRateLimit:
			class = "provider rate limited"
		}
	}
	if result.Status == providerhealth.StatusWarn && check == nil {
		class = "provider warning"
	}
	message := "provider health check did not pass"
	if check != nil && strings.TrimSpace(check.Message) != "" {
		message = strings.TrimSpace(check.Message)
	}
	return &SetupError{Class: class, err: fmt.Errorf("%s", message)}, true
}

func defaultCandidates() []LocalRuntimeCandidate {
	return []LocalRuntimeCandidate{
		{CatalogID: "ollama", Name: "Ollama (local)", BaseURL: "http://127.0.0.1:11434/v1"},
		{CatalogID: "lmstudio", Name: "LM Studio (local)", BaseURL: "http://127.0.0.1:1234/v1"},
		{CatalogID: "llamacpp", Name: "llama.cpp (local)", BaseURL: "http://127.0.0.1:8080/v1"},
	}
}

func localProbeTimeout(timeout time.Duration) time.Duration {
	if timeout > 0 {
		return timeout
	}
	return 900 * time.Millisecond
}

func needsCredential(profile config.ProviderProfile) bool {
	if profile.HasConfiguredCredential() {
		return false
	}
	if descriptor, ok := providercatalog.Get(profile.CatalogID); ok {
		return descriptor.RequiresAuth && !descriptor.Local
	}
	kind := strings.ToLower(strings.TrimSpace(string(profile.ProviderKind)))
	return kind == string(config.ProviderKindOpenAI) || kind == string(config.ProviderKindAnthropic) || kind == string(config.ProviderKindGoogle)
}
