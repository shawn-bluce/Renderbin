package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/backup"
)

// maxRestoreBytes caps an uploaded snapshot. The body is streamed to a temp
// file rather than buffered, so this bounds disk rather than memory — but an
// unbounded upload is still a way to fill the volume the database lives on.
const maxRestoreBytes = 256 << 20

// BackupHandler serves a downloadable snapshot of the SQLite database.
type BackupHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewBackupHandler(db *sql.DB, logger *slog.Logger) *BackupHandler {
	return &BackupHandler{db: db, logger: logger}
}

// Download streams a consistent SQLite snapshot at GET /api/backup. The
// snapshot is a standalone .db file (produced via VACUUM INTO into a temp
// dir). Restoring one goes through POST /api/backup/restore, which copies its
// rows into the live database rather than swapping the file.
//
// Super-admin only, not merely signed-in: the snapshot is the whole database,
// so it carries every user's files, password hash and MCP API key. 403 rather
// than 404 because this path is fixed and public (the SPA links to it) — only
// the caller's privilege is in question, not the endpoint's existence.
func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}

	dir, err := os.MkdirTemp("", "renderbin-backup-")
	if err != nil {
		serverError(w, h.logger, "backup temp dir", err)
		return
	}
	defer os.RemoveAll(dir)

	dest := filepath.Join(dir, "backup.db")
	if err := backup.Snapshot(r.Context(), h.db, dest); err != nil {
		serverError(w, h.logger, "backup snapshot", err)
		return
	}

	f, err := os.Open(dest)
	if err != nil {
		serverError(w, h.logger, "open backup snapshot", err)
		return
	}
	defer f.Close()

	filename := fmt.Sprintf("renderbin-backup-%s.db", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if stat, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	}

	if _, err := io.Copy(w, f); err != nil {
		// Headers (200) are already sent, so we can't change the status now;
		// just log the truncated transfer.
		h.logger.Warn("stream backup", "error", err)
	}
}

type restoreResponse struct {
	Users int64 `json:"users"`
	Files int64 `json:"files"`
}

// Restore replaces the live database with an uploaded snapshot at
// POST /api/backup/restore. The body is the raw .db file (no multipart wrapper
// — there is exactly one field and streaming it straight to disk is simpler on
// both sides).
//
// Super-admin only, for the same reason as Download and then some: this reads
// every account out of the uploaded file and writes it over everyone's data.
//
// The restore is atomic — see backup.Restore — so a rejected or failed upload
// leaves the current data untouched. On success the caller's own session is
// most likely gone, since the snapshot's sessions table replaced the live one;
// the response is still 200 with what was restored, and the client is expected
// to reload and re-authenticate.
func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	user, ok := requireSuperAdmin(w, r)
	if !ok {
		return
	}

	dir, err := os.MkdirTemp("", "renderbin-restore-")
	if err != nil {
		serverError(w, h.logger, "restore temp dir", err)
		return
	}
	defer os.RemoveAll(dir)

	// Streamed to disk rather than held in memory: a database is exactly the
	// kind of upload that doesn't fit comfortably in a request buffer.
	uploaded := filepath.Join(dir, "uploaded.db")
	f, err := os.Create(uploaded)
	if err != nil {
		serverError(w, h.logger, "create restore temp file", err)
		return
	}
	written, copyErr := io.Copy(f, http.MaxBytesReader(w, r.Body, maxRestoreBytes))
	closeErr := f.Close()
	if copyErr != nil {
		// MaxBytesReader's error surfaces here; either way the upload is unusable.
		http.Error(w, "could not read the uploaded file (max 256MB)", http.StatusRequestEntityTooLarge)
		return
	}
	if closeErr != nil {
		serverError(w, h.logger, "write restore temp file", closeErr)
		return
	}
	if written == 0 {
		http.Error(w, "no file was uploaded", http.StatusBadRequest)
		return
	}

	stats, err := backup.Restore(r.Context(), h.db, uploaded)
	if errors.Is(err, backup.ErrNotSQLite) ||
		errors.Is(err, backup.ErrNoAccounts) ||
		errors.Is(err, backup.ErrSchemaMismatch) {
		// The uploader's mistake, and safe to describe: nothing was changed.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		serverError(w, h.logger, "restore snapshot", err)
		return
	}
	h.logger.Info("database restored", "by_user_id", user.ID, "users", stats.Users, "files", stats.Files)

	writeJSON(w, restoreResponse{Users: stats.Users, Files: stats.Files})
}
