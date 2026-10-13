package handlers

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
)

// serverError logs err under op (plus any extra key/value pairs, which come
// before the error in the record) and answers 500 "internal error". The text
// is deliberately all the client ever sees: the cause belongs in the log.
func serverError(w http.ResponseWriter, logger *slog.Logger, op string, err error, args ...any) {
	logger.Error(op, append(args[:len(args):len(args)], "error", err)...)
	internalError(w)
}

// internalError answers 500 for a failure that has already been logged.
func internalError(w http.ResponseWriter) {
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// notFound answers the one 404 every owner-scoped miss uses. A missing slug, a
// foreign one and a trashed one must be indistinguishable (see CLAUDE.md), so
// there is exactly one body for them.
func notFound(w http.ResponseWriter) {
	http.Error(w, "not found", http.StatusNotFound)
}

// queryError reports a failed single-row lookup or write: no row is 404, any
// other error is logged and answered as serverError. Ownership lives in the
// SQL, so "no row" is the whole story for a slug that is missing or foreign.
func queryError(w http.ResponseWriter, logger *slog.Logger, op string, err error, args ...any) {
	if errors.Is(err, sql.ErrNoRows) {
		notFound(w)
		return
	}
	serverError(w, logger, op, err, args...)
}
