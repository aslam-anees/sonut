package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/auth"
)

func saveTestSession(t *testing.T, session *auth.Session) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	storage := auth.NewTokenStorage(filepath.Join(home, ".atcode"))
	if err := storage.SaveSession(session); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitStoresAuthenticatedUserFeedback(t *testing.T) {
	var got submission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/user_feedback" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if gotAuth := r.Header.Get("Authorization"); gotAuth != "Bearer user-access-token" {
			t.Errorf("Authorization = %q", gotAuth)
		}
		if gotKey := r.Header.Get("apikey"); gotKey != "anon-key" {
			t.Errorf("apikey = %q", gotKey)
		}
		if gotPrefer := r.Header.Get("Prefer"); gotPrefer != "return=minimal" {
			t.Errorf("Prefer = %q", gotPrefer)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	t.Setenv("SUPABASE_URL", server.URL)
	t.Setenv("SUPABASE_ANON_KEY", "anon-key")
	saveTestSession(t, &auth.Session{
		AccessToken:  "user-access-token",
		RefreshToken: "refresh-token",
		ExpiresIn:    3600,
		ExpiresAt:    time.Now().Add(time.Hour),
		User:         auth.User{ID: "00000000-0000-0000-0000-000000000001"},
	})

	oldVersion := Version
	Version = "1.2.3"
	t.Cleanup(func() { Version = oldVersion })

	if err := Submit(context.Background(), "  The command palette is excellent.  "); err != nil {
		t.Fatal(err)
	}
	if got.Message != "The command palette is excellent." {
		t.Errorf("message = %q", got.Message)
	}
	if got.AppVersion != "1.2.3" || got.Platform == "" || got.Arch == "" {
		t.Errorf("metadata = %#v", got)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "user_id") {
		t.Fatalf("client payload must not set user_id: %s", raw)
	}
}

func TestSubmitRequiresSignedInUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := Submit(context.Background(), "Please add a compact mode.")
	if !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("error = %v, want ErrNotSignedIn", err)
	}
}

func TestSubmitRejectsOversizedFeedbackBeforeNetwork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := Submit(context.Background(), strings.Repeat("a", MaxMessageLength+1))
	if err == nil || !strings.Contains(err.Error(), "4000") {
		t.Fatalf("error = %v, want length error", err)
	}
}

func TestSubmitReportsSupabaseFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "table unavailable", http.StatusBadRequest)
	}))
	defer server.Close()

	t.Setenv("SUPABASE_URL", server.URL)
	t.Setenv("SUPABASE_ANON_KEY", "anon-key")
	saveTestSession(t, &auth.Session{
		AccessToken: "user-access-token",
		ExpiresIn:   3600,
		ExpiresAt:   time.Now().Add(time.Hour),
	})

	err := Submit(context.Background(), "Something went wrong.")
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v, want Supabase status", err)
	}
}
