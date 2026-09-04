package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/shawn-bluce/renderbin/backend/internal/config"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// SetupHandler serves the first-run welcome flow: while the users table is
// empty every SPA route redirects to /welcome, which creates the first user
// (the super admin, id=1) and writes the initial configs.
type SetupHandler struct {
	queries *sqlcgen.Queries
	auth    *AuthHandler
	logger  *slog.Logger
	config  config.Runtime
}

func NewSetupHandler(queries *sqlcgen.Queries, authH *AuthHandler, logger *slog.Logger, cfg config.Runtime) *SetupHandler {
	return &SetupHandler{queries: queries, auth: authH, logger: logger, config: cfg}
}

type setupStatusResponse struct {
	NeedsSetup         bool   `json:"needs_setup"`
	AllowRegistration  bool   `json:"allow_registration"`
	MaxFileSizeBytes   int64  `json:"max_file_size_bytes"`
	PublicShareBaseURL string `json:"public_share_base_url"`
}

// Status is public: the SPA layout guard calls it on every load to decide
// whether to route to /welcome, and the login page uses allow_registration
// to decide whether to show the register link.
func (h *SetupHandler) Status(w http.ResponseWriter, r *http.Request) {
	count, err := h.queries.CountUsers(r.Context())
	if err != nil {
		h.logger.Error("count users", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, setupStatusResponse{
		NeedsSetup:         count == 0,
		AllowRegistration:  configBool(r, h.queries, ConfigAllowRegistration),
		MaxFileSizeBytes:   h.config.MaxFileSizeBytes,
		PublicShareBaseURL: h.config.PublicShareBaseURL,
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
	user, errMsg, err := createUser(r, h.queries, registerRequest{
		Username: req.Username,
		Nickname: req.Nickname,
		Password: req.Password,
	}, true)
	if errMsg != "" {
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}
	if errors.Is(err, errFirstUserExists) {
		http.Error(w, "already set up", http.StatusConflict)
		return
	}
	if err != nil {
		h.logger.Error("create first user", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	for key, value := range map[string]string{
		ConfigAllowRegistration: boolString(req.AllowRegistration),
		ConfigMCPEnabled:        boolString(req.MCPEnabled),
	} {
		if err := h.queries.SetConfig(r.Context(), sqlcgen.SetConfigParams{Key: key, Value: value}); err != nil {
			h.logger.Error("set config", "key", key, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	if err := h.auth.startSession(w, r, user.ID); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
