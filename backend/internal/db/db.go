package db

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens the SQLite database at path, enables WAL mode, and applies
// any pending migrations from internal/db/migrations in filename order.
//
// The two time DSN parameters are load-bearing, not tuning. The driver's
// default write format is Go's time.Time.String() -- local wall clock with the
// monotonic clock reading appended ("... +0800 CST m=+2592020.55") -- which
// nothing else in the database speaks, since CURRENT_TIMESTAMP writes
// 'YYYY-MM-DD HH:MM:SS' in UTC. _time_format=sqlite switches writes to
// ISO-8601, and _timezone=UTC makes both directions UTC: written times are
// converted before formatting, and a stored value without an offset (every
// CURRENT_TIMESTAMP column) is read back as UTC rather than as local time.
// Together they make every timestamp column one comparable format. Migration
// 0002 rewrote the rows an earlier build had already stored the old way.
//
// LoadLocation("UTC") is resolved by the standard library itself, so this adds
// no tzdata dependency to the binary.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_time_format=sqlite&_timezone=UTC", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1)

	// backup.Restore opens untrusted uploads through here, so a failure must not
	// leave the handle (and its file descriptor) behind.
	if _, err := conn.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	if err := migrate(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return conn, nil
}

func migrate(conn *sql.DB) error {
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		filename TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		return err
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}

		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	return nil
}
