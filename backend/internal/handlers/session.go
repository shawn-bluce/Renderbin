package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// SuperAdminID is the fixed id of the super admin: the first user ever
// created (through the welcome/setup flow) gets id=1.
const SuperAdminID = 1

// IsSuperAdmin reports whether the user is the super admin (id=1).
// Note this gates *capabilities* (global settings, backups), never access to
// files: file endpoints are owner-scoped for everyone, super admin included,
// matching what MCP has always done.
func IsSuperAdmin(u sqlcgen.User) bool {
	return u.ID == SuperAdminID
}

// Two ways to identify a caller, for the two kinds of route:
//
//   - Behind the requireAuth middleware, the user is already resolved and
//     stashed in the request context: use requireUser.
//   - Outside it (/res/{slug}, /api/auth/me), nothing has done that, so the
//     credential must be resolved on the spot: use CurrentUser.
//
// Both live in handlers rather than server to avoid an import cycle:
// internal/server already imports internal/handlers to build routes.

// CurrentUser resolves the request's credential — a session cookie or a
// Bearer API key — to its user row. Called by the requireAuth middleware and
// by the handlers registered outside it; handlers behind the middleware
// should call requireUser instead of paying for a second lookup.
func CurrentUser(r *http.Request, queries *sqlcgen.Queries) (sqlcgen.User, bool) {
	user, _, ok := CurrentIdentity(r, queries)
	return user, ok
}

// CurrentIdentity is CurrentUser plus how the caller authenticated. The
// session cookie is tried first; the Authorization header is only consulted
// when there is no valid session. viaAPIKey lets the super-admin surfaces
// refuse key-authenticated callers (see requireSuperAdmin).
func CurrentIdentity(r *http.Request, queries *sqlcgen.Queries) (user sqlcgen.User, viaAPIKey bool, ok bool) {
	if user, ok := sessionUser(r, queries); ok {
		return user, false, true
	}
	if user, ok := apiKeyUser(r, queries); ok {
		return user, true, true
	}
	return sqlcgen.User{}, false, false
}

func sessionUser(r *http.Request, queries *sqlcgen.Queries) (sqlcgen.User, bool) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return sqlcgen.User{}, false
	}
	sess, err := queries.GetValidSession(r.Context(), cookie.Value)
	if err != nil {
		return sqlcgen.User{}, false
	}
	user, err := queries.GetUserByID(r.Context(), sess.UserID)
	if err != nil {
		return sqlcgen.User{}, false
	}
	// A suspended account is not an identity. Checking here rather than at the
	// login door is what makes suspension take effect immediately: every
	// authenticated path (requireAuth, /api/auth/me, and the owner bypass in
	// /res/{slug}) resolves its user through this function, so a session issued
	// before the suspension stops working on its very next request.
	if user.DisabledAt.Valid {
		return sqlcgen.User{}, false
	}
	return user, true
}

// apiKeyUser resolves an "Authorization: Bearer rb_..." header to its user,
// mirroring MCP's verifyAPIKey rule for rule: the key only works while the
// mcp_enabled config is on (one switch kills every key everywhere — the
// settings page already refuses to issue keys while it is off), and a
// suspended account's key is as dead as its password.
func apiKeyUser(r *http.Request, queries *sqlcgen.Queries) (sqlcgen.User, bool) {
	token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !found || token == "" {
		return sqlcgen.User{}, false
	}
	if !configBool(r.Context(), queries, ConfigMCPEnabled) {
		return sqlcgen.User{}, false
	}
	user, err := userByAPIKey(r.Context(), queries, token)
	return user, err == nil
}

// userByAPIKey resolves an API key to its account, the one rule shared by the
// REST API and MCP so the two cannot drift: an unknown key and a suspended
// account's key are both sql.ErrNoRows (a suspended account's key is as dead as
// its password), and any other error is a real failure.
func userByAPIKey(ctx context.Context, queries *sqlcgen.Queries, token string) (sqlcgen.User, error) {
	user, err := queries.GetUserByAPIKey(ctx, sql.NullString{String: token, Valid: true})
	if err != nil {
		return sqlcgen.User{}, err
	}
	if user.DisabledAt.Valid {
		return sqlcgen.User{}, sql.ErrNoRows
	}
	return user, nil
}

// identityContextKey is unexported so nothing outside this package can plant an
// identity in the context; only WithIdentity can.
type identityContextKey struct{}

// identity is who a request is, and how they proved it: viaAPIKey lets
// requireSuperAdmin tell a session from a key.
type identity struct {
	user      sqlcgen.User
	viaAPIKey bool
}

// WithIdentity returns r carrying user (and how they authenticated) in its
// context. The requireAuth middleware in internal/server is the only intended
// caller.
func WithIdentity(r *http.Request, user sqlcgen.User, viaAPIKey bool) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), identityContextKey{}, identity{user, viaAPIKey}))
}

// isAPIKeyAuth reports whether the request authenticated with an API key
// instead of a session cookie.
func isAPIKeyAuth(r *http.Request) bool {
	id, _ := r.Context().Value(identityContextKey{}).(identity)
	return id.viaAPIKey
}

// requireUser is the standard preamble for every handler behind requireAuth:
//
//	user, ok := requireUser(w, r)
//	if !ok {
//		return
//	}
//
// Owner-scoped queries need the returned user.ID, so a handler that skips this
// won't compile. The 401 is unreachable in practice — requireAuth would have
// rejected the request first — but it fails closed if a route is ever mounted
// outside the middleware group by mistake.
func requireUser(w http.ResponseWriter, r *http.Request) (sqlcgen.User, bool) {
	id, ok := r.Context().Value(identityContextKey{}).(identity)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return sqlcgen.User{}, false
	}
	return id.user, true
}

// requireSuperAdmin is the preamble for every super-admin-only handler
// (global settings, backup, account management). 403 rather than 404: these
// paths are fixed and the SPA links to them, so only the caller's privilege is
// in question, not the endpoint's existence.
//
// It also refuses API-key authentication outright. The api_key column is
// stored in plaintext, which is acceptable exactly because a key has never
// been able to reach /api/backup — a leaked key that could download the whole
// database (every hash and every other key) would reopen the escalation path
// that made plaintext storage a recorded deferral rather than a bug. Keys are
// a file-scope credential; anything super-admin needs the session a browser
// login produces.
func requireSuperAdmin(w http.ResponseWriter, r *http.Request) (sqlcgen.User, bool) {
	user, ok := requireUser(w, r)
	if !ok {
		return sqlcgen.User{}, false
	}
	if !IsSuperAdmin(user) {
		http.Error(w, "super admin only", http.StatusForbidden)
		return sqlcgen.User{}, false
	}
	if isAPIKeyAuth(r) {
		http.Error(w, "super admin endpoints require a session, not an API key", http.StatusForbidden)
		return sqlcgen.User{}, false
	}
	return user, true
}
