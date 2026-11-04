package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
)

// GoogleOAuthConfig holds Google OAuth configuration
type GoogleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scopes       []string
}

// PKCEChallenge represents PKCE challenge/verifier pair
type PKCEChallenge struct {
	Challenge string
	Verifier  string
	Method    string // always "S256" for SHA256
}

// GeneratePKCEChallenge generates a PKCE challenge/verifier pair
func GeneratePKCEChallenge() (*PKCEChallenge, error) {
	// Generate 32-byte random verifier
	verifier := make([]byte, 32)
	_, err := rand.Read(verifier)
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE verifier: %w", err)
	}

	verifierStr := base64.RawURLEncoding.EncodeToString(verifier)

	// Create SHA256 challenge from verifier
	hash := sha256.Sum256([]byte(verifierStr))
	challenge := base64.RawURLEncoding.EncodeToString(hash[:])

	return &PKCEChallenge{
		Challenge: challenge,
		Verifier:  verifierStr,
		Method:    "S256",
	}, nil
}

// BuildAuthorizationURL constructs the Google OAuth authorization URL
func (g *GoogleOAuthConfig) BuildAuthorizationURL(pkce *PKCEChallenge, state string) string {
	params := url.Values{}
	params.Set("client_id", g.ClientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", g.RedirectURI)
	params.Set("scope", strings.Join(g.Scopes, " "))
	params.Set("state", state)
	params.Set("code_challenge", pkce.Challenge)
	params.Set("code_challenge_method", pkce.Method)
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")

	return fmt.Sprintf("https://accounts.google.com/o/oauth2/v2/auth?%s", params.Encode())
}

// OAuthState represents the OAuth state machine
type OAuthState struct {
	State            string
	PKCEChallenge    *PKCEChallenge
	RedirectListener *RedirectListener
}

// RedirectListener listens for OAuth redirects on localhost
type RedirectListener struct {
	Port  int
	Code  chan string
	Error chan error
}

// GetCallbackURL returns the callback URL for the redirect listener
func (rl *RedirectListener) GetCallbackURL() string {
	return fmt.Sprintf("http://localhost:%d/auth/callback", rl.Port)
}

// GenerateRandomState generates a cryptographically random state parameter
func GenerateRandomState() (string, error) {
	state := make([]byte, 32)
	_, err := rand.Read(state)
	if err != nil {
		return "", fmt.Errorf("failed to generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(state), nil
}

// ValidateState validates the OAuth state parameter
func ValidateState(expectedState, actualState string) bool {
	return expectedState == actualState
}

// DefaultGoogleOAuthScopes returns the recommended OAuth scopes
func DefaultGoogleOAuthScopes() []string {
	return []string{
		"openid",
		"email",
		"profile",
	}
}

// GoogleOAuthFlowManager orchestrates the full OAuth flow
type GoogleOAuthFlowManager struct {
	config          *GoogleOAuthConfig
	supabaseURL     string
	supabaseAnonKey string
}

// NewGoogleOAuthFlowManager creates a new Google OAuth flow manager
func NewGoogleOAuthFlowManager(
	clientID, clientSecret string,
	supabaseURL, supabaseAnonKey string,
) *GoogleOAuthFlowManager {
	return &GoogleOAuthFlowManager{
		config: &GoogleOAuthConfig{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURI:  "http://localhost:54321/auth/v1/callback",
			Scopes:       DefaultGoogleOAuthScopes(),
		},
		supabaseURL:     supabaseURL,
		supabaseAnonKey: supabaseAnonKey,
	}
}

// InitiateFlow starts the OAuth flow
func (m *GoogleOAuthFlowManager) InitiateFlow() (*OAuthState, error) {
	// Generate PKCE challenge
	pkce, err := GeneratePKCEChallenge()
	if err != nil {
		return nil, err
	}

	// Generate state
	state, err := GenerateRandomState()
	if err != nil {
		return nil, err
	}

	return &OAuthState{
		State:         state,
		PKCEChallenge: pkce,
	}, nil
}

// GetAuthorizationURL returns the URL user should visit
func (m *GoogleOAuthFlowManager) GetAuthorizationURL(oauthState *OAuthState) string {
	return m.config.BuildAuthorizationURL(oauthState.PKCEChallenge, oauthState.State)
}

// GoogleAuthCode represents the authorization code from Google
type GoogleAuthCode struct {
	Code  string
	State string
}

// ExchangeCodeForSession exchanges the auth code for a Supabase session
// This would be implemented server-side in production
func (m *GoogleOAuthFlowManager) ExchangeCodeForSession(code, state, verifier string) (*Session, error) {
	// In production, this exchange happens on the backend
	// For CLI, we validate state and then let Supabase handle token exchange

	if state == "" {
		return nil, fmt.Errorf("missing state parameter")
	}

	if verifier == "" {
		return nil, fmt.Errorf("missing PKCE verifier")
	}

	// Note: The actual token exchange with Google happens via Supabase's OAuth provider
	// This is just a placeholder for the protocol
	return nil, fmt.Errorf("exchange not implemented in CLI (handled server-side)")
}
