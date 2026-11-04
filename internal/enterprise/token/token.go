// Portions derived from DEEIX-Chat, Copyright 2026 DEEIX-AI, Apache-2.0.
// Modified for @code; see WIRING.md and third_party/licenses/DEEIX-Chat-LICENSE.txt.
// Package token issues and verifies HMAC-signed JWT session tokens.
// Vendored from DEEIX-Chat/backend's internal/pkg/token package.
package token

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the JWT payload used for session tokens.
type Claims struct {
	UserID    uint   `json:"user_id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	SessionID string `json:"session_id"`
	TokenType string `json:"token_type"`
	jwt.RegisteredClaims
}

// Generate issues an access token.
func Generate(secret string, userID uint, username string, role string, ttl time.Duration) (string, error) {
	return GenerateWithClaims(secret, userID, username, role, "", "", "access", ttl)
}

// GenerateWithClaims issues a token carrying session context and an explicit
// token type (e.g. "access", "refresh").
func GenerateWithClaims(
	secret string,
	userID uint,
	username string,
	role string,
	sessionID string,
	tokenID string,
	tokenType string,
	ttl time.Duration,
) (string, error) {
	claims := Claims{
		UserID:    userID,
		Username:  username,
		Role:      role,
		SessionID: sessionID,
		TokenType: tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ID:        tokenID,
		},
	}

	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(secret))
}

// Parse verifies and decodes an access token.
func Parse(secret string, tokenString string) (*Claims, error) {
	tokenObj, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := tokenObj.Claims.(*Claims)
	if !ok || !tokenObj.Valid {
		return nil, errors.New("invalid token")
	}

	return claims, nil
}
