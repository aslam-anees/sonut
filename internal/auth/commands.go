package auth

import (
	"context"
	"fmt"
)

// LoginFlow handles the interactive login process
type LoginFlow struct {
	manager *AuthManager
}

// NewLoginFlow creates a new login flow
func NewLoginFlow(manager *AuthManager) *LoginFlow {
	return &LoginFlow{manager: manager}
}

// RunGoogleLogin initiates Google OAuth login
func (lf *LoginFlow) RunGoogleLogin(ctx context.Context) (*Session, error) {
	// Step 1: Initiate OAuth flow
	oauthState, err := lf.manager.OAuthFlowMgr.InitiateFlow()
	if err != nil {
		return nil, fmt.Errorf("failed to initiate OAuth flow: %w", err)
	}

	// Step 2: Get authorization URL
	authURL := lf.manager.OAuthFlowMgr.GetAuthorizationURL(oauthState)
	fmt.Println("\n🔐 @code Authentication")
	fmt.Println("\nOpening browser for Google sign-in...")
	fmt.Printf("\nIf the browser doesn't open, visit this URL:\n%s\n", authURL)

	// Step 3: Start local redirect listener
	// In production, this would listen for the OAuth callback
	// For now, we'll wait for manual token input or use a simplified flow

	fmt.Println("\n⏳ Waiting for authentication...")
	fmt.Println("(This is a placeholder for the full OAuth flow)")

	return nil, fmt.Errorf("OAuth flow not yet fully implemented - use magic link for now")
}

// RunMagicLinkLogin initiates magic link login
func (lf *LoginFlow) RunMagicLinkLogin(ctx context.Context, email string) error {
	// Step 1: Send magic link
	fmt.Printf("\n📧 Sending magic link to %s...\n", email)

	if err := lf.manager.SupabaseClient.SignInWithMagicLink(ctx, email); err != nil {
		return fmt.Errorf("failed to send magic link: %w", err)
	}

	fmt.Println("✅ Magic link sent!")
	fmt.Println("\nCheck your email for the authentication link.")
	fmt.Println("Once you click it, return here to complete the login.")

	// Step 2: Wait for user to verify (placeholder)
	fmt.Println("\n(Note: In the full implementation, you would enter the OTP or follow the email link)")

	return nil
}

// LoginCommand represents the /auth login command
func LoginCommand(ctx context.Context, manager *AuthManager, method string, email string) error {
	// Check if already logged in
	if manager.IsLoggedIn() {
		user := manager.GetCurrentUser()
		fmt.Printf("✅ Already logged in as %s\n", user.Email)
		return nil
	}

	flow := NewLoginFlow(manager)

	switch method {
	case "google":
		_, err := flow.RunGoogleLogin(ctx)
		return err
	case "magic":
		if email == "" {
			return fmt.Errorf("email required for magic link login")
		}
		return flow.RunMagicLinkLogin(ctx, email)
	default:
		return fmt.Errorf("unknown login method: %s (use 'google' or 'magic')", method)
	}
}

// LogoutCommand represents the /auth logout command
func LogoutCommand(ctx context.Context, manager *AuthManager) error {
	if !manager.IsLoggedIn() {
		fmt.Println("❌ Not logged in")
		return nil
	}

	if err := manager.Logout(); err != nil {
		return fmt.Errorf("logout failed: %w", err)
	}

	fmt.Println("✅ Logged out successfully")
	return nil
}

// StatusCommand represents the /auth status command
func StatusCommand(_ context.Context, manager *AuthManager) error {
	if !manager.IsLoggedIn() {
		fmt.Println("❌ Not logged in")
		fmt.Println("\nRun '/auth login' to authenticate")
		return nil
	}

	user := manager.GetCurrentUser()
	fmt.Println("✅ Logged in")
	fmt.Printf("   Email: %s\n", user.Email)
	fmt.Printf("   User ID: %s\n", user.ID)

	return nil
}

// VerifyOTPCommand handles OTP verification from magic link or TOTP
func VerifyOTPCommand(ctx context.Context, manager *AuthManager, email string, otp string) error {
	if email == "" {
		return fmt.Errorf("email required for OTP verification")
	}

	if otp == "" {
		return fmt.Errorf("OTP code required")
	}

	fmt.Println("🔐 Verifying OTP...")

	session, err := manager.SupabaseClient.VerifyOTP(ctx, email, otp)
	if err != nil {
		return fmt.Errorf("OTP verification failed: %w", err)
	}

	if err := manager.SaveSession(session); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	fmt.Println("✅ Authentication successful!")
	fmt.Printf("   Logged in as: %s\n", session.User.Email)

	return nil
}

// RefreshTokenCommand refreshes an expired session
func RefreshTokenCommand(ctx context.Context, manager *AuthManager) error {
	if !manager.IsLoggedIn() {
		return fmt.Errorf("not logged in")
	}

	session := manager.currentSession
	if session == nil || session.RefreshToken == "" {
		return fmt.Errorf("refresh token not available")
	}

	fmt.Println("🔄 Refreshing authentication token...")

	newSession, err := manager.SupabaseClient.RefreshSession(ctx, session.RefreshToken)
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}

	if err := manager.SaveSession(newSession); err != nil {
		return fmt.Errorf("failed to save refreshed session: %w", err)
	}

	fmt.Println("✅ Token refreshed successfully")
	return nil
}

// InitCommand initializes authentication configuration
func InitCommand(ctx context.Context, manager *AuthManager) error {
	fmt.Println("\n🔐 @code Authentication Setup")

	// Load any existing session
	existing, err := manager.LoadStoredSession()
	if err == nil && existing != nil {
		fmt.Printf("Found existing session for %s\n", existing.User.Email)
		fmt.Println("\nRun '/auth logout' to start fresh, or '/auth status' to view current session")
		return nil
	}

	fmt.Println("Setting up authentication for @code...")
	fmt.Println("\nAvailable methods:")
	fmt.Println("  1. Google OAuth (recommended)")
	fmt.Println("  2. Magic link (email-based)")
	fmt.Println("\nRun '/auth login google' or '/auth login magic <email>'")

	return nil
}
