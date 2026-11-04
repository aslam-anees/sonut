package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SupabaseClient wraps the Supabase GoTrue API
type SupabaseClient struct {
	url        string
	anonKey    string
	redirectTo string // custom emailRedirectTo for magic links
	httpClient *http.Client
}

// NewSupabaseClient creates a new Supabase auth client
func NewSupabaseClient(url, anonKey string) *SupabaseClient {
	return &SupabaseClient{
		url:        url,
		anonKey:    anonKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewSupabaseClientWithRedirect creates a client that uses a custom redirect URL
// for magic links (points to the local loopback server).
func NewSupabaseClientWithRedirect(url, anonKey, redirectTo string) *SupabaseClient {
	c := NewSupabaseClient(url, anonKey)
	c.redirectTo = redirectTo
	return c
}

// SignInWithGoogle initiates Google OAuth sign-in
func (s *SupabaseClient) SignInWithGoogle(ctx context.Context) (*AuthResponse, error) {
	// This returns an OAuth URL that the user must visit
	// In practice, this is handled by a local redirect listener

	payload := map[string]interface{}{
		"provider": "google",
		"options": map[string]string{
			"redirectTo": "http://localhost:54321/auth/v1/callback",
		},
	}

	return s.signInWithProvider(ctx, payload)
}

// SignInWithMagicLink sends a magic link to the user's email
func (s *SupabaseClient) SignInWithMagicLink(ctx context.Context, email string) error {
	redirectTo := s.redirectTo
	if redirectTo == "" {
		redirectTo = "sonut://auth-callback"
	}
	payload := map[string]interface{}{
		"email": email,
		"options": map[string]interface{}{
			"emailRedirectTo": redirectTo,
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/auth/v1/otp", s.url)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.anonKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("magic link request failed: %s", string(data))
	}

	return nil
}

// VerifyOTP verifies a one-time password (from magic link or TOTP)
func (s *SupabaseClient) VerifyOTP(ctx context.Context, email, otp string) (*Session, error) {
	payload := map[string]interface{}{
		"email": email,
		"token": otp,
		"type":  "email",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/auth/v1/verify", s.url)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.anonKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OTP verification failed: %s", string(data))
	}

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}

	return &session, nil
}

// RefreshSession refreshes an expired access token
func (s *SupabaseClient) RefreshSession(ctx context.Context, refreshToken string) (*Session, error) {
	payload := map[string]interface{}{
		"refresh_token": refreshToken,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/auth/v1/token?grant_type=refresh_token", s.url)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.anonKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed: %s", string(data))
	}

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, err
	}

	return &session, nil
}

// SignOut logs out the current user
func (s *SupabaseClient) SignOut(ctx context.Context, accessToken string) error {
	url := fmt.Sprintf("%s/auth/v1/logout", s.url)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("apikey", s.anonKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sign out failed: %s", string(data))
	}

	return nil
}

// GetUser retrieves the current user profile
func (s *SupabaseClient) GetUser(ctx context.Context, accessToken string) (*User, error) {
	url := fmt.Sprintf("%s/auth/v1/user", s.url)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("apikey", s.anonKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get user failed: %s", string(data))
	}

	var user User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

// signInWithProvider is a helper for OAuth flows
func (s *SupabaseClient) signInWithProvider(ctx context.Context, payload map[string]interface{}) (*AuthResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/auth/v1/oauth/authorize", s.url)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.anonKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("provider sign-in failed: %s", string(data))
	}

	var authResp AuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&authResp); err != nil {
		return nil, err
	}

	return &authResp, nil
}

// Session represents an authenticated session
type Session struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	ExpiresIn    int       `json:"expires_in"`
	RefreshToken string    `json:"refresh_token"`
	User         User      `json:"user"`
	ExpiresAt    time.Time `json:"-"`
}

// IsExpired checks if the session has expired
func (s *Session) IsExpired() bool {
	if s == nil {
		return true
	}
	if s.ExpiresAt.IsZero() {
		// Calculate expiry from ExpiresIn
		s.ExpiresAt = time.Now().Add(time.Duration(s.ExpiresIn) * time.Second)
	}
	return time.Now().After(s.ExpiresAt)
}

// User represents an authenticated user
type User struct {
	ID           string                 `json:"id"`
	Email        string                 `json:"email"`
	Phone        string                 `json:"phone"`
	AppMetadata  map[string]interface{} `json:"app_metadata"`
	UserMetadata map[string]interface{} `json:"user_metadata"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// AuthResponse represents the OAuth authorization response
type AuthResponse struct {
	URL string `json:"url"`
}
