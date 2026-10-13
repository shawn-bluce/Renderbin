package handlers

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// AdminHandler serves the account-management page: the list of every account
// with its file counts, suspension, and password resets. Super admin only.
//
// This is the only place in the app that reads or writes another user's row.
// It deliberately grants no access to anyone's *files* -- there is still no
// super-admin exception on the file endpoints, so the counts below are the
// most the super admin can see of someone else's content.
type AdminHandler struct {
	queries *sqlcgen.Queries
	// conn is here only for Delete, which has to remove an account's files,
	// its sessions and the row itself as one unit. There are no foreign keys
	// to cascade for us (the schema declares none), so the transaction is what stops a
	// failure halfway through from leaving files owned by an id that no longer
	// exists -- rows nothing in the app can list, delete or serve.
	conn   *sql.DB
	logger *slog.Logger
}

func NewAdminHandler(queries *sqlcgen.Queries, conn *sql.DB, logger *slog.Logger) *AdminHandler {
	return &AdminHandler{queries: queries, conn: conn, logger: logger}
}

type adminUserResponse struct {
	ID           int64   `json:"id"`
	Username     string  `json:"username"`
	Nickname     string  `json:"nickname"`
	IsSuperAdmin bool    `json:"is_super_admin"`
	Disabled     bool    `json:"disabled"`
	DisabledAt   *string `json:"disabled_at"`
	CreatedAt    string  `json:"created_at"`
	FileCount    int64   `json:"file_count"`
	TrashedCount int64   `json:"trashed_count"`
	UsedBytes    int64   `json:"used_bytes"`
	QuotaBytes   int64   `json:"quota_bytes"`
}

// List returns every account with how many files it owns, at
// GET /api/admin/users. Password hashes and API keys are deliberately not part
// of the response: the page has no use for them, and the whole-database backup
// is already the one place that exposes them.
func (h *AdminHandler) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}

	rows, err := h.queries.ListUsersWithFileCounts(r.Context())
	if err != nil {
		serverError(w, h.logger, "list users", err)
		return
	}

	resp := make([]adminUserResponse, 0, len(rows))
	for _, u := range rows {
		item := adminUserResponse{
			ID:           u.ID,
			Username:     u.Username,
			Nickname:     u.Nickname,
			IsSuperAdmin: u.ID == SuperAdminID,
			Disabled:     u.DisabledAt.Valid,
			DisabledAt:   fmtNullTime(u.DisabledAt),
			CreatedAt:    u.CreatedAt.Format(timeLayout),
			FileCount:    u.FileCount,
			TrashedCount: u.TrashedCount,
			UsedBytes:    u.UsedBytes,
			QuotaBytes:   u.QuotaBytes,
		}
		resp = append(resp, item)
	}

	writeJSON(w, resp)
}

// targetUserID reads the {id} path parameter. A non-numeric id is a malformed
// request (400), not a missing user (404) -- it could never name a row.
func targetUserID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

type setUserStatusRequest struct {
	Disabled bool `json:"disabled"`
}

// SetStatus suspends or restores an account at
// PATCH /api/admin/users/{id}/status.
//
// A suspended account cannot log in, its existing sessions stop resolving, its
// MCP key stops working, and its files stop being served at /res/{slug}. The
// super admin cannot be suspended: it is the only account that can undo a
// suspension, so allowing it would be a one-way door out of the app with no
// path back that doesn't involve editing the database by hand.
func (h *AdminHandler) SetStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}
	id, ok := targetUserID(w, r)
	if !ok {
		return
	}

	var req setUserStatusRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	if req.Disabled && id == SuperAdminID {
		http.Error(w, "the super admin cannot be disabled", http.StatusForbidden)
		return
	}

	var err error
	if req.Disabled {
		_, err = h.queries.DisableUser(r.Context(), id)
	} else {
		_, err = h.queries.EnableUser(r.Context(), id)
	}
	if err != nil {
		queryError(w, h.logger, "set user status", err, "user_id", id)
		return
	}

	// CurrentUser already rejects a suspended user's sessions, so this is
	// belt-and-braces -- but leaving live rows for an account that can no
	// longer use them only creates a window if that check is ever relaxed.
	if req.Disabled {
		if err := h.queries.DeleteUserSessions(r.Context(), id); err != nil {
			h.logger.Warn("delete sessions of disabled user", "user_id", id, "error", err)
		}
	}
	h.logger.Info("user status changed", "user_id", id, "disabled", req.Disabled)

	// 204 rather than the updated row: the row alone can't carry the file
	// counts the list shows, and a response with those silently zeroed is worse
	// than none. The page flips its own state after a successful call, the same
	// convention the settings toggles already follow.
	w.WriteHeader(http.StatusNoContent)
}

type resetUserPasswordRequest struct {
	NewPassword string `json:"new_password"`
}

// ResetPassword sets another account's password at
// POST /api/admin/users/{id}/password, without knowing the old one -- that is
// the whole point, since there is no self-service recovery flow.
//
// Unlike a self-service change (which deliberately leaves sessions alone), this
// ends every session of the target account: an admin reset is what you reach
// for when an account is compromised or its owner is locked out, and both cases
// want whoever is currently signed in to be signed out.
//
// It refuses to target the caller. Changing your own password is what
// PATCH /api/user is for, and that path requires the current one -- so without
// this check, this endpoint was a way for a *session* to change the password it
// was authenticated with while knowing nothing about it. That is exactly the
// move a script running in the super admin's browser wants: one POST both takes
// the account and locks its owner out, with the CLI as the only way back.
func (h *AdminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	caller, ok := requireSuperAdmin(w, r)
	if !ok {
		return
	}
	id, ok := targetUserID(w, r)
	if !ok {
		return
	}
	if id == caller.ID {
		http.Error(w, "use profile settings to change your own password", http.StatusForbidden)
		return
	}

	var req resetUserPasswordRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	if errMsg := auth.ValidatePassword(req.NewPassword); errMsg != "" {
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		serverError(w, h.logger, "hash password", err)
		return
	}
	rows, err := h.queries.UpdateUserPassword(r.Context(), sqlcgen.UpdateUserPasswordParams{
		PasswordHash: hash,
		ID:           id,
	})
	if err != nil {
		serverError(w, h.logger, "reset user password", err, "user_id", id)
		return
	}
	if rows == 0 {
		notFound(w)
		return
	}
	if err := h.queries.DeleteUserSessions(r.Context(), id); err != nil {
		h.logger.Warn("delete sessions after password reset", "user_id", id, "error", err)
	}
	h.logger.Info("user password reset by super admin", "user_id", id)

	w.WriteHeader(http.StatusNoContent)
}

type createUserRequest struct {
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

// createUserResponse carries the generated password. It is the only time that
// value exists outside a bcrypt hash, so the page has to show it and the admin
// has to pass it on; there is no way to read it back afterwards, only to reset
// it into a new one.
type createUserResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Password string `json:"password"`
}

// Create adds an account at POST /api/admin/users, generating its password
// rather than taking one.
//
// It exists because adding a colleague used to mean turning on global
// self-registration, asking them to sign up, and turning it back off -- a
// window during which anyone who could reach the instance could create an
// account. The admin never chooses the password here: a password typed by one
// person for another tends to be weak, reused, or both, and the recipient can
// change it from their own profile page once they are in.
func (h *AdminHandler) Create(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}

	var req createUserRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}

	password, err := auth.NewGeneratedPassword()
	if err != nil {
		serverError(w, h.logger, "generate password", err)
		return
	}

	user, err := createUser(r.Context(), h.queries, registerRequest{
		Username: req.Username,
		Nickname: req.Nickname,
		Password: password,
	}, false)
	if err != nil {
		writeCreateUserError(w, h.logger, "create user by super admin", err)
		return
	}
	h.logger.Info("user created by super admin", "user_id", user.ID, "username", user.Username)

	writeJSONStatus(w, http.StatusCreated, createUserResponse{
		ID:       user.ID,
		Username: user.Username,
		Nickname: user.Nickname,
		Password: password,
	})
}

type deleteUserResponse struct {
	DeletedFiles int64 `json:"deleted_files"`
}

// Delete removes an account and everything it owns at
// DELETE /api/admin/users/{id}.
//
// This is the only irreversible operation an admin can perform on someone
// else's data, and it does not go through the trash: the account's files are
// hard-deleted along with it, because leaving them would leave rows owned by an
// id that no longer exists -- invisible to every listing, unservable at
// /res/{slug}, and impossible to remove through the app. Suspending is the
// reversible option and the right one for someone who might come back.
//
// Two accounts are refused: the super admin (the only account that can manage
// accounts, so deleting it is a one-way door out of the app) and the caller's
// own, which is the same door reached from the other side.
func (h *AdminHandler) Delete(w http.ResponseWriter, r *http.Request) {
	// requireSuperAdmin pins the caller to SuperAdminID, so refusing that id
	// below also covers "you cannot delete your own account".
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}
	id, ok := targetUserID(w, r)
	if !ok {
		return
	}
	if id == SuperAdminID {
		http.Error(w, "the super admin cannot be deleted", http.StatusForbidden)
		return
	}

	tx, err := h.conn.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, h.logger, "begin delete user", err, "user_id", id)
		return
	}
	defer func() { _ = tx.Rollback() }()
	q := h.queries.WithTx(tx)

	files, err := q.DeleteUserFiles(r.Context(), id)
	if err != nil {
		serverError(w, h.logger, "delete user files", err, "user_id", id)
		return
	}
	if err := q.DeleteUserSessions(r.Context(), id); err != nil {
		serverError(w, h.logger, "delete user sessions", err, "user_id", id)
		return
	}
	rows, err := q.DeleteUser(r.Context(), id)
	if err != nil {
		serverError(w, h.logger, "delete user", err, "user_id", id)
		return
	}
	// Checked before the commit, so a request naming a user that does not
	// exist rolls back rather than committing a no-op that reports 404.
	if rows == 0 {
		notFound(w)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, h.logger, "commit delete user", err, "user_id", id)
		return
	}
	h.logger.Info("user deleted by super admin", "user_id", id, "files", files)

	writeJSON(w, deleteUserResponse{DeletedFiles: files})
}

type setUserQuotaRequest struct {
	QuotaBytes int64 `json:"quota_bytes"`
}

// maxQuotaBytes is an upper bound on what the quota field can be set to. It is
// a typo guard, not a product limit: the point is that "100" followed by a
// slipped keyboard cannot silently become an exabyte.
const maxQuotaBytes = 1 << 40 // 1 TiB

// SetQuota changes how much an account may store, at
// PATCH /api/admin/users/{id}/quota. Lowering it below what the account already
// uses is allowed and blocks further uploads without touching stored files --
// deleting someone's data to satisfy a new limit is not a decision this
// endpoint gets to make.
func (h *AdminHandler) SetQuota(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}
	id, ok := targetUserID(w, r)
	if !ok {
		return
	}

	var req setUserQuotaRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}
	if req.QuotaBytes < 0 || req.QuotaBytes > maxQuotaBytes {
		http.Error(w, fmt.Sprintf("quota_bytes must be between 0 and %d", maxQuotaBytes), http.StatusBadRequest)
		return
	}

	_, err := h.queries.UpdateUserQuota(r.Context(), sqlcgen.UpdateUserQuotaParams{
		QuotaBytes: req.QuotaBytes,
		ID:         id,
	})
	if err != nil {
		queryError(w, h.logger, "set user quota", err, "user_id", id)
		return
	}
	h.logger.Info("user quota changed", "user_id", id, "quota_bytes", req.QuotaBytes)

	w.WriteHeader(http.StatusNoContent)
}
