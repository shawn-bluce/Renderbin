package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// Config keys stored in the configs table. Boolean values are the strings
// "true"/"false"; a missing key reads as false.
const (
	ConfigAllowRegistration = "allow_registration"
	ConfigMCPEnabled        = "mcp_enabled"
	// ConfigUploadDefaultPublic makes files created over HTTP start public.
	// Missing (the default) means private — the fail-closed direction for a
	// setting that decides whether a link works before its owner ever looked
	// at it. MCP uploads ignore it: those tools promise "starts private" in
	// their descriptions, and publish_file is the agent's explicit consent step.
	ConfigUploadDefaultPublic = "upload_default_public"
	// ConfigQRWidget puts the "open on your phone" QR button on every page
	// served at /res (see qr.go). On by default, which migration 0007 seeds,
	// since a missing key reads as false.
	ConfigQRWidget = "qr_widget"
)

// configBool reads a boolean config; missing keys and read errors are false.
func configBool(ctx context.Context, queries *sqlcgen.Queries, key string) bool {
	v, err := queries.GetConfig(ctx, key)
	return err == nil && v == "true"
}

// setBoolConfigs writes boolean configs in one pass (order is unspecified).
// Not atomic: a failure partway leaves the earlier keys written, which is fine
// for independent toggles.
func setBoolConfigs(ctx context.Context, queries *sqlcgen.Queries, values map[string]bool) error {
	for key, value := range values {
		if err := queries.SetConfig(ctx, sqlcgen.SetConfigParams{Key: key, Value: strconv.FormatBool(value)}); err != nil {
			return fmt.Errorf("set config %q: %w", key, err)
		}
	}
	return nil
}

// usernamePattern keeps usernames short and unambiguous; nicknames are free-form.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type AuthHandler struct {
	queries *sqlcgen.Queries
	logger  *slog.Logger
}

func NewAuthHandler(queries *sqlcgen.Queries, logger *slog.Logger) *AuthHandler {
	return &AuthHandler{queries: queries, logger: logger}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}

	user, err := h.queries.GetUserByUsername(r.Context(), req.Username)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend the same bcrypt cost as a real check so response timing
		// doesn't reveal whether the username exists.
		auth.BurnPasswordCheck(req.Password)
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	if err != nil {
		serverError(w, h.logger, "get user by username", err)
		return
	}
	if !auth.VerifyPassword(user.PasswordHash, req.Password) {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	// Reported only *after* the password checks out, so this stays a message
	// for the account's owner rather than an oracle telling an attacker which
	// usernames exist -- the same reason BurnPasswordCheck exists above.
	if user.DisabledAt.Valid {
		http.Error(w, "account is disabled", http.StatusForbidden)
		return
	}

	if err := h.startSession(w, r, user.ID); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type registerRequest struct {
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Password string `json:"password"`
}

// Register creates a new account when registration is enabled, then logs the
// new user in. The very first user is created through Setup instead.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	count, err := h.queries.CountUsers(r.Context())
	if err != nil {
		serverError(w, h.logger, "count users", err)
		return
	}
	if count == 0 {
		http.Error(w, "setup required", http.StatusConflict)
		return
	}
	if !configBool(r.Context(), h.queries, ConfigAllowRegistration) {
		http.Error(w, "registration is disabled", http.StatusForbidden)
		return
	}

	var req registerRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}

	user, err := createUser(r.Context(), h.queries, req, false)
	if err != nil {
		writeCreateUserError(w, h.logger, "create user", err)
		return
	}

	if err := h.startSession(w, r, user.ID); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// maxNicknameRunes bounds a nickname. Counted in runes, not bytes: the byte
// form of the same rule rejected a 22-character Chinese nickname while telling
// the user the limit was 64 characters.
const maxNicknameRunes = 64

// validateNickname trims and checks a nickname, returning a client-facing
// message when it is unusable.
func validateNickname(nickname string) (string, string) {
	nickname = strings.TrimSpace(nickname)
	if nickname == "" || utf8.RuneCountInString(nickname) > maxNicknameRunes {
		return "", fmt.Sprintf("nickname must be 1-%d characters", maxNicknameRunes)
	}
	return nickname, ""
}

// errFirstUserExists is createUser's signal that the atomic first-run insert
// matched nothing because some other request already created the super admin.
var errFirstUserExists = errors.New("a user already exists")

// userInputError is a createUser failure that is the caller's to fix: msg is
// client-facing and status the HTTP code it maps to.
type userInputError struct {
	status int
	msg    string
}

func (e *userInputError) Error() string { return e.msg }

func badUserInput(msg string) error { return &userInputError{http.StatusBadRequest, msg} }

// errUsernameTaken is the one input failure that is a 409 rather than a 400.
var errUsernameTaken = &userInputError{http.StatusConflict, "username already taken"}

// writeCreateUserError answers a failed createUser: the caller's mistakes as
// their own status and message, anything else as a logged 500.
func writeCreateUserError(w http.ResponseWriter, logger *slog.Logger, op string, err error) {
	var input *userInputError
	if errors.As(err, &input) {
		http.Error(w, input.msg, input.status)
		return
	}
	serverError(w, logger, op, err)
}

// createUser validates a registration payload and inserts the user. A
// *userInputError means the payload was unusable (or the username taken);
// anything else is an unexpected failure.
//
// first selects the atomic first-run insert (CreateFirstUser, which only
// applies while the users table is empty) over the ordinary one. Setup cannot
// use a count-then-insert here: the bcrypt hash above sits inside that window
// and made the check useless under concurrency.
func createUser(ctx context.Context, queries *sqlcgen.Queries, req registerRequest, first bool) (sqlcgen.User, error) {
	req.Username = strings.TrimSpace(req.Username)
	if !usernamePattern.MatchString(req.Username) {
		return sqlcgen.User{}, badUserInput("username must be 1-64 chars of letters, digits, '.', '_' or '-'")
	}
	nickname, errMsg := validateNickname(req.Nickname)
	if errMsg != "" {
		return sqlcgen.User{}, badUserInput(errMsg)
	}
	if errMsg := auth.ValidatePassword(req.Password); errMsg != "" {
		return sqlcgen.User{}, badUserInput(errMsg)
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		return sqlcgen.User{}, err
	}

	var user sqlcgen.User
	if first {
		user, err = queries.CreateFirstUser(ctx, sqlcgen.CreateFirstUserParams{
			Username:     req.Username,
			Nickname:     nickname,
			PasswordHash: hash,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return sqlcgen.User{}, errFirstUserExists
		}
	} else {
		user, err = queries.CreateUser(ctx, sqlcgen.CreateUserParams{
			Username:     req.Username,
			Nickname:     nickname,
			PasswordHash: hash,
		})
	}
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return sqlcgen.User{}, errUsernameTaken
		}
		return sqlcgen.User{}, err
	}
	return user, nil
}

// startSession issues a session token for the user, persists it, and sets the
// session cookie. Expired sessions are swept opportunistically on each login.
func (h *AuthHandler) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := auth.NewSessionToken()
	if err != nil {
		h.logger.Error("generate session token", "error", err)
		return err
	}

	expiresAt := time.Now().Add(auth.SessionTTL)
	if err := h.queries.CreateSession(r.Context(), sqlcgen.CreateSessionParams{
		Token:     token,
		UserID:    userID,
		ExpiresAt: expiresAt,
	}); err != nil {
		h.logger.Error("create session", "error", err)
		return err
	}

	auth.SetSessionCookie(w, token, expiresAt, requestIsSecure(r))

	if err := h.queries.DeleteExpiredSessions(r.Context()); err != nil {
		h.logger.Warn("delete expired sessions", "error", err)
	}
	return nil
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := h.queries.DeleteSession(r.Context(), cookie.Value); err != nil {
			h.logger.Warn("delete session", "error", err)
		}
	}
	auth.ClearSessionCookie(w, requestIsSecure(r))
	w.WriteHeader(http.StatusNoContent)
}

type meResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	IsAdmin  bool   `json:"is_admin"`
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user, ok := CurrentUser(r, h.queries)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, meResponse{
		ID:       user.ID,
		Username: user.Username,
		Nickname: user.Nickname,
		IsAdmin:  IsSuperAdmin(user),
	})
}
