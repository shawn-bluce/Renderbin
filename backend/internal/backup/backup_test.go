package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shawn-bluce/renderbin/backend/internal/backup"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
)

func TestSnapshotProducesRestorableCopy(t *testing.T) {
	// Open a migrated source DB and insert a row.
	src := openDB(t)
	if _, err := src.Exec(
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('s', 'n', '<p>hi</p>', 'code', 1)`,
	); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// Snapshot to a fresh path.
	dest := snapshotOf(t, src)

	// The snapshot must be a standalone, openable DB containing the row.
	restored, err := sql.Open("sqlite", "file:"+dest)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer restored.Close()

	var (
		content string
		count   int
	)
	if err := restored.QueryRow(`SELECT html_content FROM files WHERE slug = 's'`).Scan(&content); err != nil {
		t.Fatalf("query snapshot: %v", err)
	}
	if content != "<p>hi</p>" {
		t.Errorf("snapshot content = %q, want %q", content, "<p>hi</p>")
	}
	// The schema (all migrations) is copied too.
	if err := restored.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations in snapshot: %v", err)
	}
	if count == 0 {
		t.Error("snapshot is missing schema_migrations rows")
	}
}

// openDB opens a fresh migrated database that is closed when the test ends.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// snapshotOf writes a snapshot of conn to a fresh path and returns the path.
func snapshotOf(t *testing.T, conn *sql.DB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snap.db")
	if err := backup.Snapshot(context.Background(), conn, path); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return path
}

// seedUser gives a database the one account Restore insists a snapshot has.
func seedUser(t *testing.T, conn *sql.DB, username string) {
	t.Helper()
	if _, err := conn.Exec(
		`INSERT INTO users (username, nickname, password_hash) VALUES (?, ?, 'hash')`, username, username,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// seed opens a migrated database and fills it with one account and the given
// file slugs, so a restore can be checked for having replaced real content.
func seed(t *testing.T, username string, slugs ...string) *sql.DB {
	t.Helper()
	conn := openDB(t)
	seedUser(t, conn, username)

	for _, slug := range slugs {
		if _, err := conn.Exec(
			`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES (?, ?, '<p>x</p>', 'code', 1)`,
			slug, slug,
		); err != nil {
			t.Fatalf("seed file %q: %v", slug, err)
		}
	}
	return conn
}

func slugsIn(t *testing.T, conn *sql.DB) []string {
	t.Helper()
	rows, err := conn.Query(`SELECT slug FROM files ORDER BY slug`)
	if err != nil {
		t.Fatalf("query slugs: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan slug: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func TestRestoreReplacesLiveContents(t *testing.T) {
	// A snapshot of one database, restored over a different one.
	source := seed(t, "from-snapshot", "kept-a", "kept-b")
	snapshot := snapshotOf(t, source)

	live := seed(t, "live-user", "gone-1", "gone-2", "gone-3")

	stats, err := backup.Restore(context.Background(), live, snapshot)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if stats.Users != 1 || stats.Files != 2 {
		t.Errorf("stats = %+v, want 1 user / 2 files", stats)
	}

	// The live handle is the same *sql.DB the caller had before: the point of
	// copying rather than swapping the file is that no reopen is needed.
	if got := slugsIn(t, live); len(got) != 2 || got[0] != "kept-a" || got[1] != "kept-b" {
		t.Errorf("files after restore = %v, want the snapshot's two", got)
	}
	var username string
	if err := live.QueryRow(`SELECT username FROM users`).Scan(&username); err != nil {
		t.Fatalf("query user: %v", err)
	}
	if username != "from-snapshot" {
		t.Errorf("user after restore = %q, want the snapshot's", username)
	}
}

// The id counter has to come across with the rows. Restoring a bigger database
// over a smaller one otherwise leaves the live sequence behind the restored
// data, and the next insert collides with an id that already exists.
func TestRestoreCarriesTheIDSequence(t *testing.T) {
	source := seed(t, "big", "s1", "s2", "s3", "s4", "s5")
	snapshot := snapshotOf(t, source)

	live := seed(t, "small", "only-one")
	if _, err := backup.Restore(context.Background(), live, snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if _, err := live.Exec(
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('new', 'new', '<p>n</p>', 'c', 1)`,
	); err != nil {
		t.Fatalf("insert after restore: %v", err)
	}
	var id int64
	if err := live.QueryRow(`SELECT id FROM files WHERE slug = 'new'`).Scan(&id); err != nil {
		t.Fatalf("query new id: %v", err)
	}
	if id <= 5 {
		t.Errorf("new file id = %d, want > 5 (the restored maximum)", id)
	}
}

// An older backup must still restore: Restore migrates the snapshot up to the
// current schema first, which is the whole reason it opens it through db.Open.
func TestRestoreMigratesAnOlderSnapshot(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.db")
	conn, err := sql.Open("sqlite", "file:"+old+"?_time_format=sqlite&_timezone=UTC")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn.SetMaxOpenConns(1)

	// Hand-build a pre-0002 database: the initial migration only.
	entries, err := os.ReadDir(filepath.Join("..", "db", "migrations"))
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	first := entries[0].Name()
	body, err := os.ReadFile(filepath.Join("..", "db", "migrations", first))
	if err != nil {
		t.Fatalf("read %s: %v", first, err)
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (filename TEXT PRIMARY KEY, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		string(body),
		`INSERT INTO users (username, nickname, password_hash) VALUES ('legacy', 'Legacy', 'hash')`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('legacy-file', 'n', '<p>old</p>', 'c', 1)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("build old db: %v", err)
		}
	}
	if _, err := conn.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, first); err != nil {
		t.Fatalf("record migration: %v", err)
	}
	conn.Close()

	live := seed(t, "current", "will-go")
	if _, err := backup.Restore(context.Background(), live, old); err != nil {
		t.Fatalf("Restore of an older snapshot: %v", err)
	}
	if got := slugsIn(t, live); len(got) != 1 || got[0] != "legacy-file" {
		t.Errorf("files after restore = %v, want the old snapshot's", got)
	}
	// The columns 0002 adds must exist and be queryable afterwards.
	var reason string
	if err := live.QueryRow(`SELECT expired_reason FROM files WHERE slug = 'legacy-file'`).Scan(&reason); err != nil {
		t.Fatalf("query a post-0002 column: %v", err)
	}
}

func TestRestoreRejectsBadInput(t *testing.T) {
	dir := t.TempDir()

	notADB := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notADB, []byte("just some text, definitely not a database"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// A structurally valid database of ours, but with no accounts — restoring it
	// would leave an instance nobody can sign in to, with the old data gone.
	emptyPath := filepath.Join(dir, "empty.db")
	empty, err := db.Open(emptyPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	empty.Close()

	cases := []struct {
		name string
		path string
		want error
	}{
		{"not a database", notADB, backup.ErrNotSQLite},
		{"no accounts", emptyPath, backup.ErrNoAccounts},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			live := seed(t, "live-user", "untouched-1", "untouched-2")

			_, err := backup.Restore(context.Background(), live, c.path)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			// Rejection must leave the live data alone — that is what lets the
			// handler report the problem instead of a lost database.
			if got := slugsIn(t, live); len(got) != 2 {
				t.Errorf("live files = %v, want both still there", got)
			}
		})
	}
}

// A restore is all-or-nothing. Attaching a snapshot whose schema can't be
// copied must roll back, not leave the tables it already emptied empty.
func TestRestoreIsAtomic(t *testing.T) {
	// Build a database of ours, then break one table so the copy fails midway.
	brokenPath := filepath.Join(t.TempDir(), "broken.db")
	broken, err := db.Open(brokenPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	seedUser(t, broken, "someone")
	// Drop a column the live schema still has, so INSERT ... SELECT on that
	// table fails after earlier tables have already been cleared.
	if _, err := broken.Exec(`ALTER TABLE files DROP COLUMN tags`); err != nil {
		t.Fatalf("break schema: %v", err)
	}
	broken.Close()

	live := seed(t, "live-user", "keep-1", "keep-2")
	_, err = backup.Restore(context.Background(), live, brokenPath)
	if !errors.Is(err, backup.ErrSchemaMismatch) {
		t.Fatalf("err = %v, want ErrSchemaMismatch", err)
	}
	// Named, not a bare "no such column" from inside the copy.
	if !strings.Contains(err.Error(), "files.tags") {
		t.Errorf("error %q should name the missing column", err)
	}
	if got := slugsIn(t, live); len(got) != 2 {
		t.Errorf("live files = %v, want both — a failed restore must roll back", got)
	}
	var users int
	if err := live.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("users = %d, want the original 1", users)
	}
}

// Two restores in a row on the same *sql.DB: the attachment from the first must
// not linger and collide with the second.
func TestRestoreTwiceInARow(t *testing.T) {
	source := seed(t, "snap-user", "a")
	snapshot := snapshotOf(t, source)

	live := seed(t, "live-user", "b")
	for i := range 2 {
		if _, err := backup.Restore(context.Background(), live, snapshot); err != nil {
			t.Fatalf("Restore %d: %v", i+1, err)
		}
	}
	if got := slugsIn(t, live); len(got) != 1 || got[0] != "a" {
		t.Errorf("files = %v, want the snapshot's", got)
	}
}

func TestSnapshotFailsIfDestinationExists(t *testing.T) {
	src := openDB(t)

	// VACUUM INTO refuses to overwrite an existing file.
	dest := filepath.Join(t.TempDir(), "exists.db")
	if err := os.WriteFile(dest, []byte("not a db"), 0o644); err != nil {
		t.Fatalf("pre-create dest: %v", err)
	}
	if err := backup.Snapshot(context.Background(), src, dest); err == nil {
		t.Error("expected Snapshot to fail when the destination already exists")
	}
}

// TestRestoreKeepsTheLiveMigrationLedger pins that schema_migrations is not
// copied out of the snapshot.
//
// That table records which migration files *this* database has applied, which
// is a property of the binary that owns the file, not of the data being
// restored. Copying it let a snapshot taken on a newer build tell an older one
// that a migration it never ran was already applied -- and db.Open skips
// anything already recorded, so that migration would be skipped forever,
// leaving every query that touches the new column failing at runtime. It is a
// realistic sequence: pull a new image, roll back to the old tag, restore.
func TestRestoreKeepsTheLiveMigrationLedger(t *testing.T) {
	live := openDB(t)

	snapSrc := openDB(t)
	// Restore refuses a snapshot with no accounts, so give it one.
	seedUser(t, snapSrc, "admin")
	// Stand in for a snapshot taken by a future build: it claims a migration
	// this binary has never heard of.
	if _, err := snapSrc.Exec(
		`INSERT INTO schema_migrations (filename) VALUES ('9999_from_the_future.sql')`,
	); err != nil {
		t.Fatalf("seed future migration: %v", err)
	}
	snapPath := snapshotOf(t, snapSrc)

	if _, err := backup.Restore(context.Background(), live, snapPath); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	var future int
	if err := live.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE filename = '9999_from_the_future.sql'`,
	).Scan(&future); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if future != 0 {
		t.Error("the restore copied a migration record this binary never applied; " +
			"db.Open will now skip that migration forever")
	}

	// The live ledger is intact, so re-opening applies nothing and finds
	// everything it expects.
	var applied int
	if err := live.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied == 0 {
		t.Error("the restore emptied the live migration ledger")
	}
}
