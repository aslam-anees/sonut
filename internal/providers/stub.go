package providers

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/modelregistry"
)

type Provider interface{}

type Options struct{}

type RuntimeMetadata struct {
	ProviderKind config.ProviderKind
	APIModel     string
}

func New(profile interface{}, opts Options) (Provider, error) {
	return nil, nil
}

// ResolveRuntimeMetadata resolves the effective provider kind and API model
// for a profile without constructing a provider or touching the network. The
// configured model is resolved through the curated model registry so aliases
// (e.g. "sonnet-4.5") map to their real API model id and provider kind; a
// model unknown to the registry falls back to the profile's own values.
// Returning empty metadata with a nil error would make consumers (context
// report, config snapshots) silently blank out provider fields, so this
// always answers with the best-known values.
func ResolveRuntimeMetadata(profile config.ProviderProfile, opts Options) (RuntimeMetadata, error) {
	// A malformed base URL means no provider could ever be built from this
	// profile; surface it as a resolution error (callers render a generic
	// warning and must never echo the raw URL — it can embed credentials).
	if baseURL := strings.TrimSpace(profile.BaseURL); baseURL != "" {
		if _, err := url.Parse(baseURL); err != nil {
			return RuntimeMetadata{}, fmt.Errorf("invalid provider base URL")
		}
	}
	kind := profile.ProviderKind
	apiModel := strings.TrimSpace(profile.Model)
	if apiModel != "" {
		if registry, err := modelregistry.DefaultRegistry(); err == nil {
			if entry, err := registry.Require(apiModel); err == nil {
				if strings.TrimSpace(entry.APIModel) != "" {
					apiModel = entry.APIModel
				}
				if kind == "" {
					kind = config.ProviderKind(entry.Provider)
				}
			}
		}
	}
	return RuntimeMetadata{
		ProviderKind: kind,
		APIModel:     apiModel,
	}, nil
}
