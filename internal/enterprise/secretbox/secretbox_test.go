package secretbox

import "testing"

func TestEncryptDecryptRoundTrip(t *testing.T) {
	encrypted, err := EncryptString("test-secret", "sk-super-secret-api-key")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if encrypted == "sk-super-secret-api-key" {
		t.Fatalf("ciphertext must not equal plaintext")
	}
	decrypted, err := DecryptString("test-secret", encrypted)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if decrypted != "sk-super-secret-api-key" {
		t.Fatalf("got %q, want original plaintext", decrypted)
	}
}

func TestDecryptWithWrongSecretFails(t *testing.T) {
	encrypted, err := EncryptString("correct-secret", "top-secret")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := DecryptString("wrong-secret", encrypted); err == nil {
		t.Fatalf("expected decryption with wrong secret to fail")
	}
}

func TestEmptyPlaintextRoundTrips(t *testing.T) {
	encrypted, err := EncryptString("secret", "")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if encrypted != "" {
		t.Fatalf("expected empty ciphertext for empty plaintext, got %q", encrypted)
	}
}
