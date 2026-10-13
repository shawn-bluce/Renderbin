package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "correct-horse-battery-staple" {
		t.Error("hash must not be the plaintext password")
	}

	if !VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Error("expected correct password to verify")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Error("expected wrong password to fail verification")
	}
	if VerifyPassword("", "anything") {
		t.Error("expected empty hash to never verify")
	}
}
