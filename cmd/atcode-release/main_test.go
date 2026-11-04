package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseBuildArgsUsesEnvAndCliOverrides(t *testing.T) {
	env := func(key string) string {
		values := map[string]string{
			"ATCODE_BUILD_GOOS":   "linux",
			"ATCODE_BUILD_GOARCH": "arm64",
		}
		return values[key]
	}

	options, help, err := parseBuildArgs(nil, env)
	if err != nil {
		t.Fatalf("parseBuildArgs returned error: %v", err)
	}
	if help {
		t.Fatal("parseBuildArgs help = true, want false")
	}
	if options.GOOS != "linux" || options.GOARCH != "arm64" {
		t.Fatalf("env options = %#v", options)
	}

	options, help, err = parseBuildArgs([]string{"--goos=windows", "--goarch", "amd64", "--output", "dist/at.exe"}, emptyEnv)
	if err != nil {
		t.Fatalf("parseBuildArgs CLI returned error: %v", err)
	}
	if help {
		t.Fatal("parseBuildArgs CLI help = true, want false")
	}
	if options.GOOS != "windows" || options.GOARCH != "amd64" || options.Output != "dist/at.exe" {
		t.Fatalf("CLI options = %#v", options)
	}
}

func TestParseBuildArgsRejectsMissingValues(t *testing.T) {
	if _, _, err := parseBuildArgs([]string{"-o", "-h"}, emptyEnv); err == nil || !strings.Contains(err.Error(), "-o requires a value") {
		t.Fatalf("parseBuildArgs -o error = %v", err)
	}
	if _, _, err := parseBuildArgs([]string{"--goarch", "-h"}, emptyEnv); err == nil || !strings.Contains(err.Error(), "--goarch requires a value") {
		t.Fatalf("parseBuildArgs --goarch error = %v", err)
	}
	if _, _, err := parseBuildArgs([]string{"--goos="}, emptyEnv); err == nil || !strings.Contains(err.Error(), "--goos requires a value") {
		t.Fatalf("parseBuildArgs --goos= error = %v", err)
	}
}

func TestParseSmokeArgsAcceptsPathAlias(t *testing.T) {
	options, help, err := parseSmokeArgs([]string{"--binary=dist/at", "--goos", "linux", "--version", "0.1.0"})
	if err != nil {
		t.Fatalf("parseSmokeArgs returned error: %v", err)
	}
	if help {
		t.Fatal("parseSmokeArgs help = true, want false")
	}
	if options.BinaryPath != "dist/at" || options.GOOS != "linux" || options.Version != "0.1.0" {
		t.Fatalf("smoke options = %#v", options)
	}
}

func TestParsePackageArgsAcceptsEngineArtifact(t *testing.T) {
	options, help, err := parsePackageArgs([]string{"--engine", "dist/private-engine", "--version", "0.1.0"})
	if err != nil {
		t.Fatalf("parsePackageArgs returned error: %v", err)
	}
	if help {
		t.Fatal("parsePackageArgs help = true, want false")
	}
	if options.EngineArtifact != "dist/private-engine" || options.Version != "0.1.0" {
		t.Fatalf("package options = %#v", options)
	}
}

func TestBuildHelpIncludesEnvironmentOverrides(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"build", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run build --help code = %d stderr = %q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"atcode-release build", "--goos", "--goarch", "ATCODE_BUILD_GOOS"} {
		if !strings.Contains(output, want) {
			t.Fatalf("build help missing %q: %s", want, output)
		}
	}
}

func emptyEnv(string) string {
	return ""
}
