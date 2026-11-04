package main

import (
	"os"
	"strings"

	"github.com/aistatine/sonut/internal/cli"
)

func main() {
	mirrorCanonicalEnvironment()
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}

// mirrorCanonicalEnvironment lets the rebranded ATCODE_* surface drive older
// internal readers while SONUT_* remains a compatibility fallback. Canonical
// values always win and both names are inherited by child agents.
func mirrorCanonicalEnvironment() {
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(key, "ATCODE_") {
			continue
		}
		legacyKey := "SONUT_" + strings.TrimPrefix(key, "ATCODE_")
		_ = os.Setenv(legacyKey, value)
	}
}
