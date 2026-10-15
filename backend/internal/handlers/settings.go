package handlers

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// SettingsHandler serves the settings page: global configs (readable by any
// logged-in user, writable only by the super admin), the current user's
// profile (nickname/password), and the per-user MCP API key.
type SettingsHandler struct {
	queries *sqlcgen.Queries
	logger  *slog.Logger
}

func NewSettingsHandler(queries *sqlcgen.Queries, logger *slog.Logger) *SettingsHandler {
	return &SettingsHandler{queries: queries, logger: logger}
}

type settingsResponse struct {
	AllowRegistration   bool `json:"allow_registration"`
	MCPEnabled          bool `json:"mcp_enabled"`
	UploadDefaultPublic bool `json:"upload_default_public"`
	QRWidget            bool `json:"qr_widget"`
}

func (h *SettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, settingsResponse{
		AllowRegistration:   configBool(r.Context(), h.queries, ConfigAllowRegistration),
		MCPEnabled:          configBool(r.Context(), h.queries, ConfigMCPEnabled),
		UploadDefaultPublic: configBool(r.Context(), h.queries, ConfigUploadDefaultPublic),
		QRWidget:            configBool(r.Context(), h.queries, ConfigQRWidget),
	})
}

type updateSettingsRequest struct {
	AllowRegistration   *bool `json:"allow_registration"`
	MCPEnabled          *bool `json:"mcp_enabled"`
	UploadDefaultPublic *bool `json:"upload_default_public"`
	QRWidget            *bool `json:"qr_widget"`
}

// Update writes global configs; super admin only. Fields are optional so the
// frontend can toggle one switch without resending the other.
func (h *SettingsHandler) Update(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}

	var req updateSettingsRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}

	updates := map[string]bool{}
	for key, value := range map[string]*bool{
		ConfigAllowRegistration:   req.AllowRegistration,
		ConfigMCPEnabled:          req.MCPEnabled,
		ConfigUploadDefaultPublic: req.UploadDefaultPublic,
		ConfigQRWidget:            req.QRWidget,
	} {
		if value != nil {
			updates[key] = *value
		}
	}
	if err := setBoolConfigs(r.Context(), h.queries, updates); err != nil {
		serverError(w, h.logger, "set config", err)
		return
	}

	h.Get(w, r)
}

type updateProfileRequest struct {
	Nickname        *string `json:"nickname"`
	CurrentPassword string  `json:"current_password"`
	NewPassword     string  `json:"new_password"`
}

// UpdateProfile lets the logged-in user change their own nickname and/or
// password. A password change requires the current password.
func (h *SettingsHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}

	var req updateProfileRequest
	if !decodeSmallJSON(w, r, &req) {
		return
	}

	if req.Nickname != nil {
		nickname, errMsg := validateNickname(*req.Nickname)
		if errMsg != "" {
			http.Error(w, errMsg, http.StatusBadRequest)
			return
		}
		if _, err := h.queries.UpdateUserNickname(r.Context(), sqlcgen.UpdateUserNicknameParams{
			Nickname: nickname,
			ID:       user.ID,
		}); err != nil {
			serverError(w, h.logger, "update nickname", err)
			return
		}
	}

	if req.NewPassword != "" {
		if !auth.VerifyPassword(user.PasswordHash, req.CurrentPassword) {
			http.Error(w, "current password is incorrect", http.StatusForbidden)
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
		// The affected-row count is ignored on purpose: requireAuth resolved
		// this user a moment ago, so "no such row" isn't a case here. The
		// privileged reset in admin.go is the caller that needs it.
		if _, err := h.queries.UpdateUserPassword(r.Context(), sqlcgen.UpdateUserPasswordParams{
			PasswordHash: hash,
			ID:           user.ID,
		}); err != nil {
			serverError(w, h.logger, "update password", err)
			return
		}
	}

	w.WriteHeader(http.StatusNoContent)
}

type usageResponse struct {
	UsedBytes  int64 `json:"used_bytes"`
	QuotaBytes int64 `json:"quota_bytes"`
}

// Usage reports the caller's stored bytes and their limit at
// GET /api/user/usage, so the dashboard can show a quota before an upload
// fails rather than only after.
//
// It is a separate call rather than a field on /api/auth/me because the layout
// guard hits /api/auth/me on every navigation, and this one runs an aggregate.
// The sum reads the indexed content_size column, not the documents themselves,
// and counts trashed files too -- matching what the upload check enforces, so
// the number on screen is the number that will be applied.
func (h *SettingsHandler) Usage(w http.ResponseWriter, r *http.Request) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	used, err := h.queries.SumUserContentSize(r.Context(), user.ID)
	if err != nil {
		serverError(w, h.logger, "sum user content size", err, "user_id", user.ID)
		return
	}
	writeJSON(w, usageResponse{UsedBytes: used, QuotaBytes: user.QuotaBytes})
}

type apiKeyResponse struct {
	APIKey string `json:"api_key"`
}

// EnsureAPIKey returns the current user's MCP API key, creating one if the
// user doesn't have one yet. Only available while MCP is enabled.
func (h *SettingsHandler) EnsureAPIKey(w http.ResponseWriter, r *http.Request) {
	h.serveAPIKey(w, r, false)
}

// ResetAPIKey replaces the current user's MCP API key with a fresh one.
func (h *SettingsHandler) ResetAPIKey(w http.ResponseWriter, r *http.Request) {
	h.serveAPIKey(w, r, true)
}

func (h *SettingsHandler) serveAPIKey(w http.ResponseWriter, r *http.Request, reset bool) {
	user, ok := requireUser(w, r)
	if !ok {
		return
	}
	if !configBool(r.Context(), h.queries, ConfigMCPEnabled) {
		http.Error(w, "MCP is disabled", http.StatusConflict)
		return
	}

	key := user.ApiKey.String
	if reset || !user.ApiKey.Valid || key == "" {
		newKey, err := auth.NewAPIKey()
		if err != nil {
			serverError(w, h.logger, "generate api key", err)
			return
		}
		if err := h.queries.SetUserAPIKey(r.Context(), sqlcgen.SetUserAPIKeyParams{
			ApiKey: sql.NullString{String: newKey, Valid: true},
			ID:     user.ID,
		}); err != nil {
			serverError(w, h.logger, "set api key", err)
			return
		}
		key = newKey
	}

	writeJSON(w, apiKeyResponse{APIKey: key})
}
