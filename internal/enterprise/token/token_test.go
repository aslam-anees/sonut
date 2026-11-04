package token

import (
	"testing"
	"time"
)

func TestGenerateAndParseRoundTrip(t *testing.T) {
	signed, err := Generate("hmac-secret", 42, "alice", "admin", time.Hour)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	claims, err := Parse("hmac-secret", signed)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.UserID != 42 || claims.Username != "alice" || claims.Role != "admin" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if claims.TokenType != "access" {
		t.Fatalf("expected access token type, got %q", claims.TokenType)
	}
}

func TestParseRejectsWrongSecret(t *testing.T) {
	signed, err := Generate("hmac-secret", 1, "bob", "member", time.Hour)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := Parse("wrong-secret", signed); err == nil {
		t.Fatalf("expected parse with wrong secret to fail")
	}
}

func TestParseRejectsExpiredToken(t *testing.T) {
	signed, err := Generate("hmac-secret", 1, "bob", "member", -time.Hour)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := Parse("hmac-secret", signed); err == nil {
		t.Fatalf("expected parse of expired token to fail")
	}
}
