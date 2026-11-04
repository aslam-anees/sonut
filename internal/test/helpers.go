package test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHelper provides utilities for testing
type TestHelper struct {
	t       *testing.T
	tempDir string
	cleanup func()
}

// NewTestHelper creates a test helper
func NewTestHelper(t *testing.T) *TestHelper {
	tempDir, err := os.MkdirTemp("", "sonut-test-")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	return &TestHelper{
		t:       t,
		tempDir: tempDir,
		cleanup: func() {
			os.RemoveAll(tempDir)
		},
	}
}

// TempDir returns the temporary test directory
func (th *TestHelper) TempDir() string {
	return th.tempDir
}

// TempFile creates a temporary file
func (th *TestHelper) TempFile(name string, content string) string {
	path := filepath.Join(th.tempDir, name)

	// Create directories if needed
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		th.t.Fatalf("failed to create directory: %v", err)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		th.t.Fatalf("failed to write temp file: %v", err)
	}

	return path
}

// Cleanup removes all test artifacts
func (th *TestHelper) Cleanup() {
	if th.cleanup != nil {
		th.cleanup()
	}
}

// AssertEqual checks if two values are equal
func (th *TestHelper) AssertEqual(expected, actual interface{}, msg string) {
	if expected != actual {
		th.t.Errorf("%s: expected %v, got %v", msg, expected, actual)
	}
}

// AssertNotNil checks if a value is not nil
func (th *TestHelper) AssertNotNil(value interface{}, msg string) {
	if value == nil {
		th.t.Errorf("%s: expected non-nil value", msg)
	}
}

// AssertNil checks if a value is nil
func (th *TestHelper) AssertNil(value interface{}, msg string) {
	if value != nil {
		th.t.Errorf("%s: expected nil value, got %v", msg, value)
	}
}

// AssertNoError checks if there was no error
func (th *TestHelper) AssertNoError(err error, msg string) {
	if err != nil {
		th.t.Errorf("%s: %v", msg, err)
	}
}

// AssertError checks if there was an error
func (th *TestHelper) AssertError(err error, msg string) {
	if err == nil {
		th.t.Errorf("%s: expected error", msg)
	}
}

// MockTimer provides deterministic time for testing
type MockTimer struct {
	current time.Time
}

// NewMockTimer creates a mock timer
func NewMockTimer(t time.Time) *MockTimer {
	return &MockTimer{current: t}
}

// Now returns the current mocked time
func (mt *MockTimer) Now() time.Time {
	return mt.current
}

// Advance moves time forward
func (mt *MockTimer) Advance(d time.Duration) {
	mt.current = mt.current.Add(d)
}

// Set sets the time to a specific value
func (mt *MockTimer) Set(t time.Time) {
	mt.current = t
}

// ContextWithTimeout creates a context with timeout
func ContextWithTimeout(duration time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), duration)
}

// ExpectError creates an expected error message
func ExpectError(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

// TestData provides sample data for tests
type TestData struct {
	SampleEmail       string
	SamplePassword    string
	SampleAccessToken string
	SampleUserID      string
}

// NewTestData creates sample test data
func NewTestData() *TestData {
	return &TestData{
		SampleEmail:       "test@sonut.local",
		SamplePassword:    "test-password-123",
		SampleAccessToken: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		SampleUserID:      "test-user-123",
	}
}
