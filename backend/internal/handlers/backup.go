package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/backup"
	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
)

// restoreHeadroom is how much free space a restore wants beyond the size of
// the upload: the uploaded database is written to a temp file and then copied
// into the live one, on top of the blobs themselves.
const restoreHeadroom = 64 << 20

// BackupHandler serves downloadable backups and restores uploaded ones.
type BackupHandler struct {
	db     *sql.DB
	blobs  *blobstore.Store
	logger *slog.Logger
}

func NewBackupHandler(db *sql.DB, blobs *blobstore.Store, logger *slog.Logger) *BackupHandler {
	return &BackupHandler{db: db, blobs: blobs, logger: logger}
}

// Download streams a complete backup at GET /api/backup: a tar of a
// consistent database snapshot plus every blob it references (see
// backup.Write), written to the response as it is produced rather than
// assembled on disk first. Restoring one goes through POST /api/backup/restore,
// which copies its rows into the live database rather than swapping the file.
//
// For a large instance, `server backup` does the same from the command line
// without depending on a browser download surviving the whole transfer.
//
// Super-admin only, not merely signed-in: the backup is the whole instance,
// so it carries every user's files, password hash and MCP API key. 403 rather
// than 404 because this path is fixed and public (the SPA links to it) — only
// the caller's privilege is in question, not the endpoint's existence.
func (h *BackupHandler) Download(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireSuperAdmin(w, r); !ok {
		return
	}

	filename := fmt.Sprintf("renderbin-backup-%s.tar", backup.FilenameStamp(time.Now()))
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if err := backup.Write(r.Context(), h.db, h.blobs, w); err != nil {
		// Usually after the first byte, so the status is already sent; the
		// truncated tar is refused by restore, which checks every blob
		// arrived. Logged as an error either way: a backup that silently
		// fails is the worst kind.
		h.logger.Error("write backup", "error", err)
	}
}

type restoreResponse struct {
	Users int64 `json:"users"`
	Files int64 `json:"files"`
}

// Restore replaces the live data with an uploaded backup at
// POST /api/backup/restore. The body is the raw backup file (no multipart
// wrapper — there is exactly one field and streaming it is simpler on both
// sides): the .tar Download produces, or a .db from a version that kept
// content inside the database.
//
// Super-admin only, for the same reason as Download and then some: this reads
// every account out of the uploaded file and writes it over everyone's data.
//
// There is no fixed size cap. It used to be 256MB, which meant an instance
// that had grown past that could download a backup it could not restore.
// Instead the upload must declare its length, and that length must fit in the
// free space of the data volume, which is what a cap was protecting; the body
// is then held to the length it declared.
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

	if r.ContentLength <= 0 {
		if r.ContentLength == 0 {
			http.Error(w, "no file was uploaded", http.StatusBadRequest)
			return
		}
		http.Error(w, "the upload must declare its size (Content-Length)", http.StatusLengthRequired)
		return
	}
	if free, ok := h.blobs.FreeBytes(); ok && uint64(r.ContentLength)+restoreHeadroom > free {
		http.Error(w, fmt.Sprintf("not enough free disk space to restore this backup: it is %d bytes and %d are available",
			r.ContentLength, free), http.StatusInsufficientStorage)
		return
	}
	// The server's 60s ReadTimeout is for ordinary requests; a backup of a
	// few gigabytes takes longer than that to arrive. Only the super admin
	// gets this far, so lifting it here does not open a slow-client hole.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	body := http.MaxBytesReader(w, r.Body, r.ContentLength)

	stats, err := backup.Restore(r.Context(), h.db, h.blobs, body)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		http.Error(w, "the upload is larger than it declared", http.StatusBadRequest)
		return
	case errors.Is(err, backup.ErrNotSQLite),
		errors.Is(err, backup.ErrNoAccounts),
		errors.Is(err, backup.ErrSchemaMismatch),
		errors.Is(err, backup.ErrIncomplete),
		errors.Is(err, backup.ErrCorrupt),
		errors.Is(err, backup.ErrBareDatabase):
		// The uploader's mistake, and safe to describe: nothing was changed.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, db.ErrInsufficientSpace):
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
		return
	case err != nil:
		serverError(w, h.logger, "restore backup", err)
		return
	}
	h.logger.Info("backup restored", "by_user_id", user.ID, "users", stats.Users, "files", stats.Files)

	writeJSON(w, restoreResponse{Users: stats.Users, Files: stats.Files})
}
