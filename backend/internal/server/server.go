// Package server assembles the chi router: API routes under /api and the
// embedded SvelteKit SPA (with client-side-routing fallback) for everything else.
package server

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/shawn-bluce/renderbin/backend/internal/config"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
	"github.com/shawn-bluce/renderbin/backend/internal/handlers"
	"github.com/shawn-bluce/renderbin/backend/internal/web"
)

func New(queries *sqlcgen.Queries, conn *sql.DB, logger *slog.Logger) http.Handler {
	return NewWithConfig(queries, conn, logger, config.Default())
}

func NewWithConfig(queries *sqlcgen.Queries, conn *sql.DB, logger *slog.Logger, cfg config.Runtime) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(slogRequestLogger(logger))

	authH := handlers.NewAuthHandler(queries, logger)
	setupH := handlers.NewSetupHandler(queries, authH, logger, cfg)
	settingsH := handlers.NewSettingsHandler(queries, logger)
	files := handlers.NewFilesHandler(queries, logger, cfg)
	backupH := handlers.NewBackupHandler(conn, logger)
	adminH := handlers.NewAdminHandler(queries, conn, logger)

	r.Route("/api", func(r chi.Router) {
		// Without these, an unmatched /api path fell through to the SPA
		// catch-all and answered 200 with index.html: fetch() got HTML where it
		// expected JSON and threw an opaque parse error, curl and any uptime
		// check saw a healthy 200 for a route that does not exist, and a typo in
		// a client looked like a working call. chi does not inherit the parent's
		// NotFound into a subrouter, so it has to be set here.
		r.NotFound(apiNotFound)
		r.MethodNotAllowed(apiMethodNotAllowed)

		r.Get("/health", handlers.Health)

		r.Get("/setup/status", setupH.Status)
		r.Post("/setup", setupH.Setup)

		r.Post("/auth/login", authH.Login)
		r.Post("/auth/register", authH.Register)
		r.Post("/auth/logout", authH.Logout)
		r.Get("/auth/me", authH.Me)

		r.Group(func(r chi.Router) {
			r.Use(requireAuth(queries))

			r.Get("/settings", settingsH.Get)
			r.Put("/settings", settingsH.Update)
			r.Patch("/user", settingsH.UpdateProfile)
			r.Get("/user/usage", settingsH.Usage)
			r.Post("/user/api-key", settingsH.EnsureAPIKey)
			r.Post("/user/api-key/reset", settingsH.ResetAPIKey)

			// Nothing static may be mounted at the /api/files/{slug} level.
			// chi matches a static segment before a parameter, so a route like
			// the former /api/files/search shadows any file whose custom slug
			// happens to be "search" -- that file's own endpoint silently
			// answers with the other handler's response. Search and the trash
			// bulk action therefore live at their own top-level paths.
			r.Get("/search", files.Search)
			r.Delete("/trash", files.EmptyTrash)

			r.Get("/files", files.List)
			r.Get("/files/{slug}", files.Get)
			r.Get("/files/{slug}/download", files.Download)
			r.Post("/files", files.Create)
			r.Patch("/files/{slug}", files.Update)
			r.Patch("/files/{slug}/name", files.Rename)
			r.Patch("/files/{slug}/visibility", files.SetVisibility)
			r.Patch("/files/{slug}/tags", files.SetTags)
			r.Patch("/files/{slug}/expiry", files.SetExpiry)
			r.Post("/files/{slug}/refresh-code", files.RefreshCode)
			r.Post("/files/{slug}/restore", files.Restore)
			r.Delete("/files/{slug}", files.Delete)
			r.Delete("/files/{slug}/permanent", files.HardDelete)

			r.Get("/backup", backupH.Download)
			r.Post("/backup/restore", backupH.Restore)

			// Account management. Every handler here re-checks super admin
			// itself rather than relying on a middleware on this subtree, so
			// mounting one of them elsewhere by mistake still fails closed.
			r.Get("/admin/users", adminH.List)
			r.Post("/admin/users", adminH.Create)
			r.Delete("/admin/users/{id}", adminH.Delete)
			r.Patch("/admin/users/{id}/status", adminH.SetStatus)
			r.Patch("/admin/users/{id}/quota", adminH.SetQuota)
			r.Post("/admin/users/{id}/password", adminH.ResetPassword)
		})
	})

	r.Get("/res/{slug}", files.Render)

	// MCP endpoint: its own Bearer-token (API key) auth + mcp_enabled gate,
	// deliberately outside /api and the session-cookie requireAuth.
	r.Handle("/mcp", handlers.NewMCPHandler(queries, logger, cfg))

	r.NotFound(spaHandler(web.FS()))

	return r
}

// requireAuth 401s any request without a valid credential — a session cookie
// or a Bearer API key (the same per-user key MCP uses, and under the same
// rules: dead while mcp_enabled is off or the account is suspended) — and
// resolves it to the user once so downstream handlers can read it from the
// request context instead of each repeating the lookup. Every file query is
// owner-scoped, so handlers need that identity anyway — resolving it here
// makes "behind requireAuth implies a known user" a property of the router
// rather than a convention each new handler has to remember.
//
// How the caller authenticated travels along in the context: super-admin
// endpoints accept only sessions (see handlers.requireSuperAdmin), so an API
// key stays a file-scope credential.
//
// The lookup and the context accessors live in internal/handlers so this
// middleware and the routes outside it can share them without an import cycle
// (this package already imports internal/handlers).
func requireAuth(queries *sqlcgen.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, viaAPIKey, ok := handlers.CurrentIdentity(r, queries)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, handlers.WithIdentity(r, user, viaAPIKey))
		})
	}
}

// apiNotFound and apiMethodNotAllowed answer in the content type an API client
// is already parsing, instead of letting the SPA catch-all reply with HTML.
func apiNotFound(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusNotFound, "no such endpoint")
}

func apiMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// securityHeaders sets the baseline every response should carry.
//
// It deliberately does NOT set a Content-Security-Policy: the SPA needs one
// shaped around its own assets, and user-uploaded documents need the opposite
// of one, so /res/{slug} sets its own sandbox policy in the render handler (see
// handlers.setUserContentHeaders) and nothing global would suit both.
//
// X-Frame-Options is set for everything here and removed again by
// handlers.setUserContentHeaders, so an owner can still embed their own
// published document while the app's own pages stay unframable (which is what
// stops a framed dashboard being clickjacked into a destructive action).
//
// The exemption follows the handler rather than a path prefix on purpose: an
// earlier version skipped anything under "/res/", but only /res/{slug} is a
// real route -- "/res/" and "/res/a/b" fall through to the SPA catch-all, so
// the app's own pages became framable at those URLs.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		// The access code for a shared file travels in the query string, so a
		// referrer leaving this origin would carry it to a third party.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func slogRequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger.Info("request", "method", r.Method, "path", r.URL.Path)
			next.ServeHTTP(w, r)
		})
	}
}

// spaHandler serves static assets from distFS, falling back to index.html
// for any path that isn't a real file so client-side routing works.
func spaHandler(distFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(distFS))
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(distFS, trimLeadingSlash(r.URL.Path)); err != nil {
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}
