package handlers

import (
	"net/http"

	"github.com/shawn-bluce/renderbin/backend/internal/buildinfo"
)

// Health is the unauthenticated liveness probe at GET /api/health, also used
// by the container's HEALTHCHECK. It reports the build version so a running
// instance can be identified without shell access.
func Health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"status":  "ok",
		"version": buildinfo.Version,
	})
}
