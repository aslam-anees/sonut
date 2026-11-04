package provideroauth

import (
	"context"
	"time"

	"github.com/aistatine/sonut/internal/oauth"
)

type Config struct{}

func New() *Config {
	return &Config{}
}

type OpenRouterOptions struct {
	Out         interface{}
	HTTPClient  interface{}
	BaseURL     string
	OpenBrowser func(string) error
	Timeout     time.Duration
}

func OpenRouterLogin(ctx context.Context, opts OpenRouterOptions) (string, error) {
	return "", nil
}

type ChatGPTOptions struct {
	Env         map[string]string
	HTTPClient  interface{}
	Out         interface{}
	OpenBrowser func(string) error
	Timeout     time.Duration
}

func ChatGPTLogin(ctx context.Context, opts ChatGPTOptions) (oauth.Token, error) {
	return oauth.Token{}, nil
}
