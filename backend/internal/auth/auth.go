// Package auth holds pure credential/token/cookie mechanics with no
// database dependency. User storage, session storage, and HTTP wiring live
// in internal/handlers and internal/server.
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const SessionCookieName = "rb_session"

// APIKeyPrefix marks a string as one of our MCP API keys, making it
// recognizable in configs and logs.
const APIKeyPrefix = "rb_"

// SessionTTL is how long a login session (and its cookie) stays valid.
const SessionTTL = 30 * 24 * time.Hour

// MinPasswordLen is the one password policy this app has. It lives here rather
// than in handlers because three unrelated callers need the same number: the
// HTTP registration/profile handlers, the super admin's reset endpoint, and the
// reset-password CLI subcommand, which has no HTTP layer to borrow it from.
const MinPasswordLen = 6

// MaxPasswordBytes is bcrypt's own limit, not a policy: GenerateFromPassword
// refuses anything longer, and nothing validated against it, so the error
// surfaced as HTTP 500 "internal error".
//
// It bites non-Latin passphrases first and hardest. The limit is in *bytes*,
// and a Chinese character is three of them, so a perfectly ordinary 25-character
// passphrase is 75 bytes -- which meant a Chinese-speaking operator could be
// stopped, with no explanation, on the very first screen of the app.
const MaxPasswordBytes = 72

// ValidatePassword returns a client-facing message describing why password is
// unusable, or "" when it is fine. Every path that sets a password (setup,
// registration, the profile page, the admin reset, the CLI) goes through this
// so the rules and their wording are stated once.
func ValidatePassword(password string) string {
	if len(password) < MinPasswordLen {
		return fmt.Sprintf("password must be at least %d characters", MinPasswordLen)
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Sprintf("password must be at most %d bytes (about %d Chinese characters)",
			MaxPasswordBytes, MaxPasswordBytes/3)
	}
	return ""
}

// HashPassword bcrypt-hashes a password for storage in users.password_hash.
// Callers should run ValidatePassword first; this rejects the same inputs, but
// as an opaque error rather than something worth showing a user.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// VerifyPassword reports whether password matches the stored bcrypt hash.
func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is a bcrypt hash of an unguessable throwaway value. Login runs a
// bcrypt compare against it when the username doesn't exist, so "unknown
// user" and "wrong password" take the same time and usernames can't be
// enumerated through response timing.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("rb-no-such-user"), bcrypt.DefaultCost)

// BurnPasswordCheck spends one bcrypt verification's worth of CPU and reports
// nothing. Used to equalize login timing for nonexistent usernames.
func BurnPasswordCheck(password string) {
	_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
}

// NewSessionToken returns a 64-character hex string (32 random bytes).
func NewSessionToken() (string, error) {
	return randomHex(32)
}

// NewShortToken returns an 8-character string: the first 6 characters of a
// random hex string, base64-encoded. That is 24 bits of entropy — a deliberate
// choice trading strength for short shareable URLs; the admin can set a longer
// custom code per file when it matters, and slug generation retries on UNIQUE
// conflicts to compensate. RawURLEncoding keeps the result URL-query-safe and
// padding-free (StdEncoding's '+', '/', '=' would need escaping in ?code= and
// violate the slug charset). Used for both file slugs and access codes.
func NewShortToken() (string, error) {
	h, err := randomHex(16)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString([]byte(h[:6])), nil
}

// NewAccessCode returns a fresh default access code for a file.
func NewAccessCode() (string, error) {
	return NewShortToken()
}

// generatedPasswordAlphabet omits the characters people misread when copying a
// password out of a screen and into a login form: 0/O, 1/l/I, and the symbols
// a shell or a chat client would mangle. 16 characters from this 54-symbol set
// is a little over 92 bits, which is plenty for a credential the admin is
// expected to hand over and the recipient is expected to change.
const generatedPasswordAlphabet = "abcdefghijkmnopqrstuvwxyzACDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratedPasswordLen is how many characters NewGeneratedPassword produces.
const GeneratedPasswordLen = 16

// NewGeneratedPassword returns a random password for an account the super
// admin creates on someone's behalf. It is shown once, at creation; nothing
// stores it in plaintext, so the admin has to pass it on there and then.
func NewGeneratedPassword() (string, error) {
	buf := make([]byte, GeneratedPasswordLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, GeneratedPasswordLen)
	for i, b := range buf {
		// The alphabet's length does not divide 256, so this is very slightly
		// biased. For a 92-bit secret that bias is not worth a rejection loop.
		out[i] = generatedPasswordAlphabet[int(b)%len(generatedPasswordAlphabet)]
	}
	return string(out), nil
}

// NewAPIKey returns a per-user MCP API key: APIKeyPrefix plus 48 hex
// characters (24 random bytes).
func NewAPIKey() (string, error) {
	h, err := randomHex(24)
	if err != nil {
		return "", err
	}
	return APIKeyPrefix + h, nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// The Secure flag is a caller-supplied property of the request, not a
// constant, because a browser silently *discards* a Secure cookie that arrives
// over plain HTTP from anything but localhost. Hardcoding it to true made
// self-hosting over http://<lan-ip> look like a broken password: the login
// request succeeded, the cookie was dropped, and the next request bounced back
// to the login page with nothing to show for it. Callers pass
// handlers.requestIsSecure(r), which is true for real TLS and for
// X-Forwarded-Proto: https from a terminating proxy. Setting it from the
// request cannot weaken an HTTPS deployment (the flag is on wherever the
// scheme is https) and the unset direction is only reached where the transport
// is already plaintext and the cookie would otherwise not exist at all.
func SetSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie must mirror SetSessionCookie's attributes: browsers key a
// cookie by name/domain/path, and a Secure mismatch is not part of that key,
// but sending an unset-Secure deletion over HTTPS is still the same cookie --
// so passing the request's own scheme keeps the two calls symmetric.
func ClearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}
