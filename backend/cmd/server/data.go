package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
)

// dataPaths resolves where the database and the blob store live. The blob
// directory defaults to a sibling of the database, so the one /data volume the
// container mounts holds both and nothing about a deployment has to change.
func dataPaths() (dbPath, blobDir string) {
	dbPath = envOr("DB_PATH", defaultDBPath)
	blobDir = envOr("BLOB_DIR", filepath.Join(filepath.Dir(dbPath), "blobs"))
	return dbPath, blobDir
}

// openData opens the database and its blob store, as the server and every
// subcommand that touches data do. Opening runs any pending migrations.
func openData() (*sql.DB, *blobstore.Store, error) {
	dbPath, blobDir := dataPaths()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, nil, fmt.Errorf("create db dir: %w", err)
	}
	store, err := blobstore.Open(blobDir)
	if err != nil {
		return nil, nil, err
	}
	conn, err := db.Open(dbPath, store)
	if err != nil {
		return nil, nil, fmt.Errorf("open database %s: %w", dbPath, err)
	}
	return conn, store, nil
}
