package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"runtime"
	"sort"

	_ "modernc.org/sqlite"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens the SQLite database at path, enables WAL mode, and applies
// any pending migrations from internal/db/migrations in filename order.
//
// store is where file content lives (see internal/blobstore). The database
// only names blobs, so it is meaningless without the store that goes with it,
// and one migration (0009_extract_content) writes into it: an older database
// still holding its content inline has that content moved out on open.
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
//
// The handle is a pool of several connections, so reads run side by side:
// in WAL mode a reader never waits for a writer or for another reader. It used
// to be a single connection, which made every request queue behind whatever
// the one before it was doing -- a backup's VACUUM INTO of the whole database
// held up every login and every share link for as long as it ran.
//
// Writers are still one at a time, but SQLite serializes them, not the pool:
// a write waits up to busy_timeout for the lock. _txlock=immediate is what
// makes that safe with more than one connection. A default (deferred)
// transaction that reads first and writes later has to upgrade its lock
// mid-way, and if another connection wrote in between SQLite fails it at once
// with SQLITE_BUSY rather than waiting. BEGIN IMMEDIATE takes the write lock
// up front and waits for it like any other write.
func Open(path string, store *blobstore.Store) (*sql.DB, error) {
	if store == nil {
		return nil, errors.New("open database: no blob store")
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_txlock=immediate&_time_format=sqlite&_timezone=UTC", path)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conns := max(4, runtime.NumCPU())
	conn.SetMaxOpenConns(conns)
	// As many idle as open: a connection closed between requests is a file
	// reopen plus a cold page cache on the next one.
	conn.SetMaxIdleConns(conns)

	// backup.Restore opens untrusted uploads through here, so a failure must not
	// leave the handle (and its file descriptor) behind.
	if _, err := conn.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	applied, err := migrate(conn, store)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if applied > 0 {
		if err := compactIfMostlyFree(conn); err != nil {
			conn.Close()
			return nil, fmt.Errorf("compact after migrating: %w", err)
		}
	}

	return conn, nil
}

// compactIfMostlyFree runs VACUUM when over a quarter of the file is free
// pages. A migration that rebuilds a table (0006 rebuilt files, the bulk of
// the data) writes the new copy before dropping the old one, so the file ends
// up about twice its size with half of it free. SQLite reuses free pages but
// never hands them back to the filesystem, and on a small VPS a database
// that doubled overnight is how the disk fills. VACUUM cannot run inside a
// transaction, which is why this is here rather than in the migration.
//
// It only runs on a start that applied migrations, so an ordinary restart
// never pays for it -- emptying the trash is not meant to trigger a rewrite.
//
// VACUUM's cost follows the live data, not the file: after 0010 dropped the
// content columns of a 10GB database, it copies only the metadata left.
func compactIfMostlyFree(conn *sql.DB) error {
	var free, total int64
	if err := conn.QueryRow(`SELECT freelist_count FROM pragma_freelist_count`).Scan(&free); err != nil {
		return err
	}
	if err := conn.QueryRow(`SELECT page_count FROM pragma_page_count`).Scan(&total); err != nil {
		return err
	}
	if free*4 <= total {
		return nil
	}
	_, err := conn.Exec(`VACUUM`)
	return err
}

// goMigrations are the steps SQL cannot express, run in name order among the
// SQL files and recorded in schema_migrations the same way. Unlike a SQL file,
// a Go step is not wrapped in one transaction: it manages its own, and must be
// safe to re-run after it was interrupted, since it is only recorded once it
// has finished.
var goMigrations = map[string]func(*sql.DB, *blobstore.Store) error{
	"0009_extract_content": extractContent,
}

// migrationNames lists every migration, SQL files and Go steps, in the order
// they apply.
func migrationNames() ([]string, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries)+len(goMigrations))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	for name := range goMigrations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// migrate applies pending migrations and reports how many it applied.
func migrate(conn *sql.DB, store *blobstore.Store) (int, error) {
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		filename TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		return 0, err
	}

	names, err := migrationNames()
	if err != nil {
		return 0, err
	}

	applied := 0
	for _, name := range names {
		var done int
		if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE filename = ?`, name).Scan(&done); err != nil {
			return 0, err
		}
		if done > 0 {
			continue
		}

		if step, ok := goMigrations[name]; ok {
			if err := step(conn, store); err != nil {
				return 0, fmt.Errorf("apply %s: %w", name, err)
			}
			if _, err := conn.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, name); err != nil {
				return 0, err
			}
			applied++
			continue
		}

		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return 0, err
		}
		tx, err := conn.Begin()
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(string(sqlBytes)); err != nil {
			tx.Rollback()
			return 0, fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, name); err != nil {
			tx.Rollback()
			return 0, err
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		applied++
	}

	return applied, nil
}
