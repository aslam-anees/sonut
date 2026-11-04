package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// TokenStorage handles persistent token storage
type TokenStorage struct {
	configDir string
	authFile  string
}

// NewTokenStorage creates a new token storage manager
func NewTokenStorage(configDir string) *TokenStorage {
	return &TokenStorage{
		configDir: configDir,
		authFile:  filepath.Join(configDir, "auth.json"),
	}
}

// SaveSession persists a session to disk
func (ts *TokenStorage) SaveSession(session *Session) error {
	// Create config directory if it doesn't exist
	if err := os.MkdirAll(ts.configDir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Serialize session
	data := map[string]interface{}{
		"access_token":  session.AccessToken,
		"refresh_token": session.RefreshToken,
		"expires_in":    session.ExpiresIn,
		"expires_at":    session.ExpiresAt.Unix(),
		"user": map[string]interface{}{
			"id":    session.User.ID,
			"email": session.User.Email,
		},
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	// Write with restricted permissions
	if err := os.WriteFile(ts.authFile, jsonData, 0600); err != nil {
		return fmt.Errorf("failed to write auth file: %w", err)
	}

	return nil
}

// LoadSession loads a session from disk
func (ts *TokenStorage) LoadSession() (*Session, error) {
	data, err := os.ReadFile(ts.authFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No saved session
		}
		return nil, fmt.Errorf("failed to read auth file: %w", err)
	}

	var sessionData map[string]interface{}
	if err := json.Unmarshal(data, &sessionData); err != nil {
		return nil, fmt.Errorf("failed to parse auth file: %w", err)
	}

	// Reconstruct session
	session := &Session{
		AccessToken:  toString(sessionData["access_token"]),
		RefreshToken: toString(sessionData["refresh_token"]),
		ExpiresIn:    toInt(sessionData["expires_in"]),
	}

	// Restore expiry time
	if expiresAtUnix, ok := sessionData["expires_at"].(float64); ok {
		session.ExpiresAt = time.Unix(int64(expiresAtUnix), 0)
	}

	// Restore user info
	if userMap, ok := sessionData["user"].(map[string]interface{}); ok {
		session.User = User{
			ID:    toString(userMap["id"]),
			Email: toString(userMap["email"]),
		}
	}

	return session, nil
}

// DeleteSession removes the saved session
func (ts *TokenStorage) DeleteSession() error {
	if err := os.Remove(ts.authFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete auth file: %w", err)
	}
	return nil
}

// ProfileStorage handles user profile persistence
type ProfileStorage struct {
	configDir string
}

// NewProfileStorage creates a new profile storage manager
func NewProfileStorage(configDir string) *ProfileStorage {
	return &ProfileStorage{
		configDir: configDir,
	}
}

// SaveProfile persists user preferences
func (ps *ProfileStorage) SaveProfile(profile map[string]interface{}) error {
	profileFile := filepath.Join(ps.configDir, "profile.json")

	jsonData, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}

	if err := os.WriteFile(profileFile, jsonData, 0600); err != nil {
		return fmt.Errorf("failed to write profile file: %w", err)
	}

	return nil
}

// LoadProfile loads user preferences
func (ps *ProfileStorage) LoadProfile() (map[string]interface{}, error) {
	profileFile := filepath.Join(ps.configDir, "profile.json")

	data, err := os.ReadFile(profileFile)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]interface{}), nil
		}
		return nil, fmt.Errorf("failed to read profile file: %w", err)
	}

	var profile map[string]interface{}
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile file: %w", err)
	}

	return profile, nil
}

// Helper functions
func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	}
	return 0
}

// AuthManager combines all auth operations
type AuthManager struct {
	SupabaseClient *SupabaseClient
	tokenStorage   *TokenStorage
	profileStorage *ProfileStorage
	OAuthFlowMgr   *GoogleOAuthFlowManager
	currentSession *Session
}

// NewAuthManager creates a new auth manager
func NewAuthManager(
	supabaseURL, supabaseAnonKey string,
	googleClientID, googleClientSecret string,
	configDir string,
) *AuthManager {
	return &AuthManager{
		SupabaseClient: NewSupabaseClient(supabaseURL, supabaseAnonKey),
		tokenStorage:   NewTokenStorage(configDir),
		profileStorage: NewProfileStorage(configDir),
		OAuthFlowMgr: NewGoogleOAuthFlowManager(
			googleClientID,
			googleClientSecret,
			supabaseURL,
			supabaseAnonKey,
		),
	}
}

// LoadStoredSession loads a previously saved session
func (am *AuthManager) LoadStoredSession() (*Session, error) {
	session, err := am.tokenStorage.LoadSession()
	if err != nil {
		return nil, err
	}

	if session == nil {
		return nil, nil
	}

	am.currentSession = session
	return session, nil
}

// IsLoggedIn checks if a valid session exists
func (am *AuthManager) IsLoggedIn() bool {
	if am.currentSession == nil {
		return false
	}
	return !am.currentSession.IsExpired()
}

// GetCurrentUser returns the logged-in user
func (am *AuthManager) GetCurrentUser() *User {
	if am.currentSession == nil {
		return nil
	}
	return &am.currentSession.User
}

// Logout clears the current session
func (am *AuthManager) Logout() error {
	if am.currentSession != nil {
		// Could call SignOut on Supabase here if needed
	}
	am.currentSession = nil
	return am.tokenStorage.DeleteSession()
}

// SaveSession persists the current session
func (am *AuthManager) SaveSession(session *Session) error {
	am.currentSession = session
	return am.tokenStorage.SaveSession(session)
}
