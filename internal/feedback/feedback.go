// Package feedback submits explicit user feedback to Supabase.
//
// Feedback is sent only when the user deliberately runs /feedback with a
// message. The signed-in user's access token is used for the request, and the
// database stamps user_id with auth.uid() under row-level security.
package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aistatine/sonut/internal/auth"
	"github.com/aistatine/sonut/internal/config"
)

const (
	MaxMessageLength = 4000
	requestTimeout   = 10 * time.Second
)

var (
	ErrNotSignedIn    = errors.New("feedback requires a signed-in user")
	ErrSessionExpired = errors.New("the signed-in session has expired")

	// Version is set by the CLI at startup so feedback rows identify the build.
	Version = "dev"
)

type submission struct {
	Message    string `json:"message"`
	Platform   string `json:"platform"`
	Arch       string `json:"arch"`
	AppVersion string `json:"app_version"`
}

// Submit stores one deliberate feedback message in Supabase. user_id is
// intentionally omitted from the payload: the database default auth.uid()
// derives ownership from the verified bearer token.
func Submit(ctx context.Context, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("feedback message is empty")
	}
	if utf8.RuneCountInString(message) > MaxMessageLength {
		return fmt.Errorf("feedback exceeds %d characters", MaxMessageLength)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ErrNotSignedIn
	}
	tokenStorage := auth.NewTokenStorage(filepath.Join(home, ".atcode"))
	session, err := tokenStorage.LoadSession()
	if err != nil || session == nil || strings.TrimSpace(session.AccessToken) == "" {
		return ErrNotSignedIn
	}

	cfg := config.Load()
	if strings.TrimSpace(cfg.SupabaseURL) == "" || strings.TrimSpace(cfg.SupabaseKey) == "" {
		return errors.New("Supabase is not configured")
	}

	if session.IsExpired() {
		if strings.TrimSpace(session.RefreshToken) == "" {
			return ErrSessionExpired
		}
		refreshCtx, cancel := context.WithTimeout(nonNilContext(ctx), requestTimeout)
		refreshed, refreshErr := auth.NewSupabaseClient(cfg.SupabaseURL, cfg.SupabaseKey).
			RefreshSession(refreshCtx, session.RefreshToken)
		cancel()
		if refreshErr != nil || refreshed == nil || strings.TrimSpace(refreshed.AccessToken) == "" {
			return ErrSessionExpired
		}
		if refreshed.ExpiresAt.IsZero() && refreshed.ExpiresIn > 0 {
			refreshed.ExpiresAt = time.Now().Add(time.Duration(refreshed.ExpiresIn) * time.Second)
		}
		if refreshed.User.ID == "" {
			refreshed.User = session.User
		}
		if err := tokenStorage.SaveSession(refreshed); err != nil {
			return fmt.Errorf("save refreshed session: %w", err)
		}
		session = refreshed
	}

	payload, err := json.Marshal(submission{
		Message:    message,
		Platform:   runtime.GOOS,
		Arch:       runtime.GOARCH,
		AppVersion: Version,
	})
	if err != nil {
		return fmt.Errorf("encode feedback: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(nonNilContext(ctx), requestTimeout)
	defer cancel()
	endpoint := strings.TrimRight(cfg.SupabaseURL, "/") + "/rest/v1/user_feedback"
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create feedback request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", cfg.SupabaseKey)
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	req.Header.Set("Prefer", "return=minimal")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("submit feedback: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Supabase rejected feedback (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
