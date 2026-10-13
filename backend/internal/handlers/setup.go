package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// SetupHandler serves the first-run welcome flow: while the users table is
// empty every SPA route redirects to /welcome, which creates the first user
// (the super admin, id=1) and writes the initial configs.
type SetupHandler struct {
	queries *sqlcgen.Queries
	auth    *AuthHandler
	logger  *slog.Logger
}

func NewSetupHandler(queries *sqlcgen.Queries, authH *AuthHandler, logger *slog.Logger) *SetupHandler {
	return &SetupHandler{queries: queries, auth: authH, logger: logger}
}

type setupStatusResponse struct {
	NeedsSetup        bool `json:"needs_setup"`
	AllowRegistration bool `json:"allow_registration"`
}

// Status is public: the SPA layout guard calls it on every load to decide
// whether to route to /welcome, and the login page uses allow_registration
// to decide whether to show the register link.
func (h *SetupHandler) Status(w http.ResponseWriter, r *http.Request) {
	count, err := h.queries.CountUsers(r.Context())
	if err != nil {
		serverError(w, h.logger, "count users", err)
		return
	}
	writeJSON(w, setupStatusResponse{
		NeedsSetup:        count == 0,
		AllowRegistration: configBool(r.Context(), h.queries, ConfigAllowRegistration),
	})
}

type setupRequest struct {
	Username          string `json:"username"`
	Nickname          string `json:"nickname"`
	Password          string `json:"password"`
	AllowRegistration bool   `json:"allow_registration"`
	MCPEnabled        bool   `json:"mcp_enabled"`
}

// Setup creates the super admin and the initial configs, then logs the new
// admin in. Only valid while no user exists; afterwards it always 409s.
func (h *SetupHandler) Setup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}

	// No count-then-create here: CreateFirstUser only inserts while the users
	// table is empty, so the "is this the first run" decision happens inside
	// the write instead of ~80ms of bcrypt before it. Concurrent requests used
	// to all pass a CountUsers check and create an account each.
	user, err := createUser(r.Context(), h.queries, registerRequest{
		Username: req.Username,
		Nickname: req.Nickname,
		Password: req.Password,
	}, true)
	if errors.Is(err, errFirstUserExists) {
		http.Error(w, "already set up", http.StatusConflict)
		return
	}
	if err != nil {
		writeCreateUserError(w, h.logger, "create first user", err)
		return
	}

	if err := setBoolConfigs(r.Context(), h.queries, map[string]bool{
		ConfigAllowRegistration: req.AllowRegistration,
		ConfigMCPEnabled:        req.MCPEnabled,
	}); err != nil {
		serverError(w, h.logger, "set config", err)
		return
	}

	if err := h.auth.startSession(w, r, user.ID); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
