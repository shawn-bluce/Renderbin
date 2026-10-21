package auth

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewSessionToken(t *testing.T) {
	tok, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("session token length = %d, want 64", len(tok))
	}
	if _, err := hex.DecodeString(tok); err != nil {
		t.Errorf("session token is not valid hex: %v", err)
	}

	other, err := NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken (second): %v", err)
	}
	if tok == other {
		t.Error("expected two session tokens to differ")
	}
}

func TestNewAccessCode(t *testing.T) {
	code, err := NewAccessCode()
	if err != nil {
		t.Fatalf("NewAccessCode: %v", err)
	}
	// 128 bits: a code is the secret half of a share link, and /res does not
	// throttle wrong guesses.
	if len(code) != 22 {
		t.Errorf("access code length = %d, want 22", len(code))
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(code); err != nil {
		t.Errorf("access code is not valid URL-safe base64: %v", err)
	} else if len(decoded) != 16 {
		t.Errorf("access code decodes to %d bytes, want 16 (128 bits)", len(decoded))
	}

	other, err := NewAccessCode()
	if err != nil {
		t.Fatalf("NewAccessCode (second): %v", err)
	}
	if code == other {
		t.Error("expected two access codes to differ")
	}
}

func TestNewAPIKey(t *testing.T) {
	key, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey: %v", err)
	}
	if !strings.HasPrefix(key, APIKeyPrefix) {
		t.Errorf("api key %q missing %q prefix", key, APIKeyPrefix)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(key, APIKeyPrefix)); err != nil {
		t.Errorf("api key body is not valid hex: %v", err)
	}
	if want := len(APIKeyPrefix) + 48; len(key) != want {
		t.Errorf("api key length = %d, want %d", len(key), want)
	}

	other, err := NewAPIKey()
	if err != nil {
		t.Fatalf("NewAPIKey (second): %v", err)
	}
	if key == other {
		t.Error("expected two api keys to differ")
	}
}
