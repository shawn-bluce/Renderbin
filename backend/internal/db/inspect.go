package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
)

// Plan describes what opening a database with this binary would do, without
// doing it: `server migrate --check` prints it before an upgrade, so the
// operator of a large instance knows how long the server will be down and
// whether the disk is big enough.
type Plan struct {
	// Pending are the migrations not yet applied, in the order they would run.
	Pending []string
	// MoveFiles and MoveBytes are the content 0009_extract_content would move
	// out of the database, when it is pending.
	MoveFiles int64
	MoveBytes int64
	// Need is the free space the move requires; Free is what the blob
	// directory's volume has (FreeKnown false when the platform cannot say).
	Need      uint64
	Free      uint64
	FreeKnown bool
}

// Inspect reports the Plan for the database at path, whose content would go
// to blobDir. It reads the database and changes nothing -- it does not create
// the blob directory, nor the database if there is none.
func Inspect(path, blobDir string) (Plan, error) {
	if _, err := os.Stat(path); err != nil {
		return Plan{}, fmt.Errorf("no database at %s: %w", path, err)
	}
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return Plan{}, err
	}
	defer conn.Close()

	var plan Plan
	plan.Free, plan.FreeKnown = blobstore.FreeBytesAt(blobDir)

	applied := map[string]bool{}
	if ok, err := tableExists(conn, "schema_migrations"); err != nil {
		return Plan{}, err
	} else if ok {
		rows, err := conn.Query(`SELECT filename FROM schema_migrations`)
		if err != nil {
			return Plan{}, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return Plan{}, err
			}
			applied[name] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Plan{}, err
		}
	}
	names, err := migrationNames()
	if err != nil {
		return Plan{}, err
	}
	for _, name := range names {
		if !applied[name] {
			plan.Pending = append(plan.Pending, name)
		}
	}

	if !applied["0009_extract_content"] {
		plan.MoveFiles, plan.MoveBytes, err = inlineContent(conn)
		if err != nil {
			return Plan{}, err
		}
		if plan.MoveFiles > 0 {
			plan.Need = spaceNeeded(plan.MoveBytes)
		}
	}
	return plan, nil
}

// inlineContent counts the content still stored inside the database, at
// whatever schema version it is.
func inlineContent(conn *sql.DB) (files, bytes int64, err error) {
	if ok, err := tableExists(conn, "files"); err != nil || !ok {
		return 0, 0, err
	}
	// content_size arrived in 0003; before that, measure the column itself.
	size := "length(CAST(html_content AS BLOB))"
	if ok, err := columnExists(conn, "files", "content_size"); err != nil {
		return 0, 0, err
	} else if ok {
		size = "content_size"
	}
	where := ""
	if ok, err := tableExists(conn, "blob_extraction"); err != nil {
		return 0, 0, err
	} else if ok {
		where = " WHERE id NOT IN (SELECT file_id FROM blob_extraction)"
	}
	err = conn.QueryRow(`SELECT COUNT(*), COALESCE(SUM(`+size+`), 0) FROM files`+where).Scan(&files, &bytes)
	return files, bytes, err
}

func tableExists(conn *sql.DB, name string) (bool, error) {
	var n int
	err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

func columnExists(conn *sql.DB, table, column string) (bool, error) {
	var n int
	err := conn.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n > 0, err
}
