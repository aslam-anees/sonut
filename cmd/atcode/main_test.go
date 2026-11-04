package main

import (
	"os"
	"testing"
)

func TestMirrorCanonicalEnvironmentPrefersAtcode(t *testing.T) {
	t.Setenv("ATCODE_PROVIDER", "canonical")
	t.Setenv("SONUT_PROVIDER", "legacy")

	mirrorCanonicalEnvironment()

	if got := os.Getenv("SONUT_PROVIDER"); got != "canonical" {
		t.Fatalf("SONUT_PROVIDER = %q, want canonical mirror", got)
	}
}
