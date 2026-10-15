package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestOpenRunsAllMigrations also covers re-opening: a second Open of the same
// file must not re-apply anything.
func TestOpenRunsAllMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	conn, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// schema_migrations should record exactly one row per embedded migration file.
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	want := len(entries)
	if want == 0 {
		t.Fatal("no migration files embedded")
	}

	var got int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&got); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if got != want {
		t.Errorf("applied migrations = %d, want %d", got, want)
	}

	// Every column the schema declares must exist and be queryable.
	if _, err := conn.Exec(
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('s', 'n', '<p>h</p>', 'code', 1)`,
	); err != nil {
		t.Fatalf("insert file: %v", err)
	}
	var (
		successCount, viewCount int64
		tags                    string
	)
	if err := conn.QueryRow(
		`SELECT success_count, view_count, tags FROM files WHERE slug = 's'`,
	).Scan(&successCount, &viewCount, &tags); err != nil {
		t.Fatalf("select added columns: %v", err)
	}

	// sessions must exist too, and also demands an owner.
	if _, err := conn.Exec(
		`INSERT INTO sessions (token, expires_at, user_id) VALUES ('t', CURRENT_TIMESTAMP, 1)`,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Re-opening the same file must not re-apply migrations or error.
	conn.Close()
	again, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()
	if err := again.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&got); err != nil {
		t.Fatalf("count after reopen: %v", err)
	}
	if got != want {
		t.Errorf("migration count changed on reopen: %d, want %d", got, want)
	}
}

// rawText reads a column as text, bypassing the driver's TIMESTAMP handling, so
// these tests can assert on what is actually stored rather than on what the
// driver hands back after parsing it.
func rawText(t *testing.T, conn *sql.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := conn.QueryRow(query, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return s
}

// TestTimestampsStoredAsComparableUTC pins the DSN's _time_format/_timezone
// pair. Without them the driver writes Go's time.Time.String() -- local wall
// clock plus a monotonic clock reading, e.g.
//
//	2026-09-09 09:40:21.365288 +0800 CST m=+2592020.551943710
//
// which shares no format with the CURRENT_TIMESTAMP columns next to it and made
// every SQL comparison between the two a lexicographic accident.
func TestTimestampsStoredAsComparableUTC(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// A fixed instant in a non-UTC zone: if the write path ignored the zone,
	// the stored text would be the local wall clock instead.
	zone := time.FixedZone("UTC+8", 8*60*60)
	want := time.Date(2026, 9, 9, 1, 40, 21, 0, time.UTC)
	if _, err := conn.Exec(
		`INSERT INTO sessions (token, expires_at, user_id) VALUES ('t', ?, 1)`, want.In(zone),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	stored := rawText(t, conn, `SELECT CAST(expires_at AS TEXT) FROM sessions WHERE token = 't'`)
	if _, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", stored); err != nil {
		t.Errorf("stored timestamp %q is not ISO-8601: %v", stored, err)
	}
	if want.Format("2006-01-02 15:04:05") != stored[:19] {
		t.Errorf("stored timestamp %q is not the UTC wall clock of %v", stored, want)
	}

	// It round-trips to the same instant through the driver...
	var got time.Time
	if err := conn.QueryRow(`SELECT expires_at FROM sessions WHERE token = 't'`).Scan(&got); err != nil {
		t.Fatalf("scan expires_at: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("round-tripped %v, want %v", got, want)
	}

	// ...and SQLite's own date functions can read it, which is what
	// GetValidSession's unixepoch() comparison relies on.
	if epoch := rawText(t, conn,
		`SELECT CAST(unixepoch(expires_at) AS TEXT) FROM sessions WHERE token = 't'`,
	); epoch != fmt.Sprint(want.Unix()) {
		t.Errorf("unixepoch(expires_at) = %s, want %d", epoch, want.Unix())
	}
}

// openRaw opens a database file with the production DSN but WITHOUT running
// migrations, so a test can stage the schema of an earlier release by hand.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_time_format=sqlite&_timezone=UTC")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestMigrationRewritesLegacyTimestamps exercises migration 0002's rewrite of
// the timestamps an earlier build stored in Go's own format, on a database that
// really is in the pre-0002 state.
func TestMigrationRewritesLegacyTimestamps(t *testing.T) {
	// Everything before 0002 is the first migration only: the state the rows
	// below were written in.
	conn := openRaw(t, filepath.Join(t.TempDir(), "app.db"))
	applyMigrationsBefore(t, conn, "0002")

	cases := []struct {
		name   string
		legacy string
		want   string // expected stored value after the migration
	}{
		{
			name:   "positive offset with fraction",
			legacy: "2026-08-11 09:40:58.357245 +0800 CST m=+86457.543907751",
			want:   "2026-08-11 01:40:58+00:00",
		},
		{
			// The ' -' discriminator has to survive the hyphens in the date.
			name:   "negative offset without fraction",
			legacy: "2026-08-11 09:40:58 -0500 EST m=+1.500000001",
			want:   "2026-08-11 14:40:58+00:00",
		},
		{
			name:   "already UTC in Go's format",
			legacy: "2026-08-11 09:40:58.1 +0000 UTC m=+2.5",
			want:   "2026-08-11 09:40:58+00:00",
		},
		{
			// Anything already in the new format must be left exactly alone.
			name:   "already migrated",
			legacy: "2026-08-11 09:40:58+00:00",
			want:   "2026-08-11 09:40:58+00:00",
		},
	}

	for i, c := range cases {
		slug := fmt.Sprintf("s%d", i)
		if _, err := conn.Exec(
			`INSERT INTO files (slug, name, html_content, access_code, user_id, expires_at)
			 VALUES (?, 'n', '<p>h</p>', 'code', 1, ?)`, slug, c.legacy,
		); err != nil {
			t.Fatalf("insert file %s: %v", slug, err)
		}
		if _, err := conn.Exec(
			`INSERT INTO sessions (token, expires_at, user_id) VALUES (?, ?, 1)`, slug, c.legacy,
		); err != nil {
			t.Fatalf("insert session %s: %v", slug, err)
		}
	}
	// A NULL expiry (the common case) must stay NULL rather than becoming a date.
	if _, err := conn.Exec(
		`INSERT INTO files (slug, name, html_content, access_code, user_id)
		 VALUES ('nolimit', 'n', '<p>h</p>', 'code', 1)`,
	); err != nil {
		t.Fatalf("insert file without expiry: %v", err)
	}

	if _, err := migrate(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for i, c := range cases {
		slug := fmt.Sprintf("s%d", i)
		t.Run(c.name, func(t *testing.T) {
			if got := rawText(t, conn,
				`SELECT CAST(expires_at AS TEXT) FROM files WHERE slug = ?`, slug,
			); got != c.want {
				t.Errorf("files.expires_at = %q, want %q", got, c.want)
			}
			if got := rawText(t, conn,
				`SELECT CAST(expires_at AS TEXT) FROM sessions WHERE token = ?`, slug,
			); got != c.want {
				t.Errorf("sessions.expires_at = %q, want %q", got, c.want)
			}
		})
	}

	var expires sql.NullTime
	if err := conn.QueryRow(`SELECT expires_at FROM files WHERE slug = 'nolimit'`).Scan(&expires); err != nil {
		t.Fatalf("scan null expiry: %v", err)
	}
	if expires.Valid {
		t.Errorf("a NULL expiry became %v", expires.Time)
	}
}

// applyMigrationsBefore applies every migration up to but excluding the named
// one, leaving a database in exactly the state the previous release left it.
func applyMigrationsBefore(t *testing.T, conn *sql.DB, exclude string) {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		filename TEXT PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name >= exclude {
			break
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := conn.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := conn.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, name); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
}

// TestMigrationBackfillsContentSize covers the upgrade path for migration 0003.
//
// content_size is what lets the listings stop reading html_content, and the
// quota check trust a SUM instead of measuring every document. Existing rows
// predate the column, so if the backfill were wrong every file already in the
// database would report zero bytes: the dashboard would show "0 B" for real
// files and the quota would consider a full account empty.
func TestMigrationBackfillsContentSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	legacy := openRaw(t, path)
	applyMigrationsBefore(t, legacy, "0003")

	rows := []struct {
		slug, content string
		wantBytes     int64
	}{
		{"ascii", "<p>hello</p>", 12},
		// Bytes, not characters: length() on TEXT would report 4 here and
		// undercount every non-ASCII document in the database.
		{"cjk", "汉字测试", 12},
		{"empty-ish", "x", 1},
	}
	for _, r := range rows {
		if _, err := legacy.Exec(
			`INSERT INTO files (slug, name, html_content, access_code, user_id)
			 VALUES (?, 'n', ?, 'code', 1)`, r.slug, r.content,
		); err != nil {
			t.Fatalf("insert %s: %v", r.slug, err)
		}
	}
	if _, err := legacy.Exec(
		`INSERT INTO users (username, nickname, password_hash) VALUES ('a', 'A', 'h')`,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	legacy.Close()

	// Reopening is the upgrade: db.Open runs whatever has not been recorded.
	conn, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	defer conn.Close()

	for _, r := range rows {
		var got int64
		if err := conn.QueryRow(
			`SELECT content_size FROM files WHERE slug = ?`, r.slug).Scan(&got); err != nil {
			t.Fatalf("read content_size for %s: %v", r.slug, err)
		}
		if got != r.wantBytes {
			t.Errorf("content_size(%s) = %d, want %d", r.slug, got, r.wantBytes)
		}
	}

	// The pre-existing account picks up the default quota rather than 0, which
	// would silently block every upload after an upgrade.
	var quota int64
	if err := conn.QueryRow(`SELECT quota_bytes FROM users WHERE username = 'a'`).Scan(&quota); err != nil {
		t.Fatalf("read quota_bytes: %v", err)
	}
	if quota != 100<<20 {
		t.Errorf("quota_bytes = %d after upgrade, want the 100MB default", quota)
	}
}

// TestContentSizeIsSelfCorrecting pins the triggers that keep files.content_size
// honest regardless of which binary wrote the row.
//
// The realistic way to get a wrong value is a downgrade: an older image runs
// happily against an upgraded database (sqlc expands SELECT * into explicit
// column names at generation time, so columns added later are simply ignored),
// but its INSERT does not name content_size and takes the DEFAULT 0. Rolling
// forward again would not repair those rows, because migration 0003 is already
// recorded and its backfill never runs twice -- leaving files that report zero
// bytes and count nothing against their owner's quota.
func TestContentSizeIsSelfCorrecting(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// Exactly what an older binary's INSERT looks like: no content_size.
	const content = "汉字内容" // 12 bytes, 4 characters
	if _, err := conn.Exec(
		`INSERT INTO files (slug, name, html_content, access_code, user_id)
		 VALUES ('legacy', 'n', ?, 'code', 1)`, content,
	); err != nil {
		t.Fatalf("insert without content_size: %v", err)
	}

	var size int64
	if err := conn.QueryRow(`SELECT content_size FROM files WHERE slug = 'legacy'`).Scan(&size); err != nil {
		t.Fatalf("read content_size: %v", err)
	}
	if size != int64(len(content)) {
		t.Errorf("content_size = %d after an insert that omitted it, want %d bytes", size, len(content))
	}

	// The same for an update that rewrites the content without the size.
	const longer = "a much longer body than before"
	if _, err := conn.Exec(
		`UPDATE files SET html_content = ? WHERE slug = 'legacy'`, longer,
	); err != nil {
		t.Fatalf("update without content_size: %v", err)
	}
	if err := conn.QueryRow(`SELECT content_size FROM files WHERE slug = 'legacy'`).Scan(&size); err != nil {
		t.Fatalf("read content_size: %v", err)
	}
	if size != int64(len(longer)) {
		t.Errorf("content_size = %d after an update that omitted it, want %d", size, len(longer))
	}

	// A counter bump must not be treated as a content change -- /res/{slug}
	// issues one on every request, and AFTER UPDATE OF html_content is what
	// keeps those off the trigger. Prove it by planting a wrong size (writing
	// content_size itself does not fire the trigger either): a bump must leave
	// it alone, and only a content write puts it right again.
	if _, err := conn.Exec(`UPDATE files SET content_size = 999 WHERE slug = 'legacy'`); err != nil {
		t.Fatalf("plant a wrong size: %v", err)
	}
	if _, err := conn.Exec(
		`UPDATE files SET success_count = success_count + 1 WHERE slug = 'legacy'`,
	); err != nil {
		t.Fatalf("bump counter: %v", err)
	}
	if err := conn.QueryRow(`SELECT content_size FROM files WHERE slug = 'legacy'`).Scan(&size); err != nil {
		t.Fatalf("read content_size: %v", err)
	}
	if size != 999 {
		t.Errorf("content_size = %d after a counter bump, want the planted 999: the content trigger fired", size)
	}
	if _, err := conn.Exec(`UPDATE files SET html_content = ? WHERE slug = 'legacy'`, longer); err != nil {
		t.Fatalf("rewrite content: %v", err)
	}
	if err := conn.QueryRow(`SELECT content_size FROM files WHERE slug = 'legacy'`).Scan(&size); err != nil {
		t.Fatalf("read content_size: %v", err)
	}
	if size != int64(len(longer)) {
		t.Errorf("content_size = %d after a content write, want %d", size, len(longer))
	}
}

// TestContentSizeCountsSiteArchive pins migration 0004's rebuilt triggers: for
// a site the size is html_content plus the stored zip, on insert and on update
// of either column. Restore inserts rows with their size already set, so a
// trigger that only knew html_content would zero every site's size.
func TestContentSizeCountsSiteArchive(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	sizeOf := func() int64 {
		t.Helper()
		var n int64
		if err := conn.QueryRow(`SELECT content_size FROM files WHERE slug = 'site'`).Scan(&n); err != nil {
			t.Fatalf("read content_size: %v", err)
		}
		return n
	}

	if _, err := conn.Exec(
		`INSERT INTO files (slug, name, html_content, access_code, user_id, kind, archive)
		 VALUES ('site', 'n', '', 'code', 1, 'site', ?)`, []byte("12345"),
	); err != nil {
		t.Fatalf("insert site: %v", err)
	}
	if got := sizeOf(); got != 5 {
		t.Errorf("content_size = %d for a 5-byte archive and no html, want 5", got)
	}

	if _, err := conn.Exec(`UPDATE files SET archive = ? WHERE slug = 'site'`, []byte("1234567890")); err != nil {
		t.Fatalf("replace archive: %v", err)
	}
	if got := sizeOf(); got != 10 {
		t.Errorf("content_size = %d after replacing the archive with 10 bytes, want 10", got)
	}

	// Both columns count: html bytes (not characters) plus archive bytes.
	if _, err := conn.Exec(`UPDATE files SET html_content = ? WHERE slug = 'site'`, "汉"); err != nil {
		t.Fatalf("set html: %v", err)
	}
	if got := sizeOf(); got != 13 {
		t.Errorf("content_size = %d for 3 html bytes + 10 archive bytes, want 13", got)
	}

	// A restore-style insert that already carries a size the triggers disagree
	// with is corrected rather than trusted.
	if _, err := conn.Exec(
		`INSERT INTO files (slug, name, html_content, access_code, user_id, kind, archive, content_size)
		 VALUES ('restored', 'n', '', 'code2', 1, 'site', ?, 0)`, []byte("abc"),
	); err != nil {
		t.Fatalf("insert restored site: %v", err)
	}
	var n int64
	if err := conn.QueryRow(`SELECT content_size FROM files WHERE slug = 'restored'`).Scan(&n); err != nil {
		t.Fatalf("read restored size: %v", err)
	}
	if n != 3 {
		t.Errorf("restored site content_size = %d, want 3", n)
	}
}

// TestMigrationMovesContentLast covers migration 0006, which rebuilds files
// with html_content and archive as its last two columns. A table rebuild is
// the one kind of migration that can lose data silently, so this checks that
// every column of every row survives, and that the id counter does too.
func TestMigrationMovesContentLast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	legacy := openRaw(t, path)
	applyMigrationsBefore(t, legacy, "0006")

	for _, stmt := range []string{
		`INSERT INTO users (username, nickname, password_hash) VALUES ('a', 'A', 'h')`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id, tags, is_public,
		                    expires_at, success_count, kind)
		 VALUES ('doc', 'Doc', '汉字 body', 'code1', 1, 'x,y', 1,
		         '2030-01-02 03:04:05+00:00', 7, 'markdown')`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id, kind, archive,
		                    deleted_at, max_views, expired_reason)
		 VALUES ('site', 'Site', '', 'code2', 1, 'site', x'504b0506',
		         '2026-01-01 00:00:00+00:00', 3, 'views')`,
		// Large enough that the rebuild's leftover free pages dominate the
		// file, which is what the compaction check below needs to see.
		`UPDATE files SET html_content = html_content || hex(zeroblob(200000)) WHERE slug = 'doc'`,
		// Issued and then hard-deleted: the counter must stay at 3, not fall
		// back to the highest surviving id.
		`INSERT INTO files (slug, name, html_content, access_code, user_id)
		 VALUES ('gone', 'Gone', 'x', 'code3', 1)`,
		`DELETE FROM files WHERE slug = 'gone'`,
	} {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("stage pre-0006 data: %v\n%s", err, stmt)
		}
	}

	// Named columns, so the comparison does not depend on the order the
	// migration is changing.
	const cols = `id, slug, name, html_content, is_public, access_code, created_at,
		updated_at, deleted_at, success_count, failure_count, tags, expires_at,
		max_views, view_count, kind, code_success_count, user_id, expired_at,
		expired_reason, content_size, archive`
	snapshot := func(conn *sql.DB) []string {
		t.Helper()
		rows, err := conn.Query(`SELECT ` + cols + ` FROM files ORDER BY id`)
		if err != nil {
			t.Fatalf("read files: %v", err)
		}
		defer rows.Close()
		names, _ := rows.Columns()
		var out []string
		for rows.Next() {
			vals := make([]any, len(names))
			ptrs := make([]any, len(names))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out = append(out, fmt.Sprintf("%v", vals))
		}
		return out
	}
	before := snapshot(legacy)
	legacy.Close()

	conn, err := Open(path)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	defer conn.Close()

	if after := snapshot(conn); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("rows changed by the rebuild:\nbefore %v\nafter  %v", before, after)
	}

	var last2 string
	if err := conn.QueryRow(
		`SELECT group_concat(name, ',') FROM (
		   SELECT name FROM pragma_table_info('files') ORDER BY cid DESC LIMIT 2)`,
	).Scan(&last2); err != nil {
		t.Fatalf("read column order: %v", err)
	}
	if last2 != "archive,html_content" {
		t.Errorf("last two columns = %s, want html_content then archive", last2)
	}

	if seq := rawText(t, conn, `SELECT seq FROM sqlite_sequence WHERE name = 'files'`); seq != "3" {
		t.Errorf("files sequence = %s after the rebuild, want 3 (the highest id ever issued)", seq)
	}
	if n := rawText(t, conn, `SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'files_new'`); n != "0" {
		t.Errorf("files_new still has %s sequence row(s)", n)
	}

	// The rebuild wrote a second copy before dropping the first; Open must
	// have given that space back rather than leave the file twice its size.
	if free := rawText(t, conn, `SELECT freelist_count FROM pragma_freelist_count`); free != "0" {
		t.Errorf("%s free pages after the upgrade, want 0 (compacted)", free)
	}

	// What the DROP took with it must be back.
	for _, name := range []string{
		"idx_files_owner_list",
		"files_content_size_after_insert",
		"files_content_size_after_update",
	} {
		if n := rawText(t, conn,
			`SELECT COUNT(*) FROM sqlite_master WHERE name = ? AND tbl_name = 'files'`, name,
		); n != "1" {
			t.Errorf("%s missing after the rebuild", name)
		}
	}
}

// TestReadsDoNotQueueBehindEachOther pins that the handle is a pool. With a
// single connection, a query still holding its connection -- a backup's
// VACUUM INTO, a large row being read -- made every other request wait for it.
func TestReadsDoNotQueueBehindEachOther(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// An open cursor keeps its connection until it is closed.
	held, err := conn.Query(`SELECT name FROM sqlite_master`)
	if err != nil {
		t.Fatalf("hold a reader: %v", err)
	}
	defer held.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM files`).Scan(&n); err != nil {
		t.Fatalf("second query while another holds a connection: %v", err)
	}
}

// TestConcurrentTransactionsWaitForTheWriteLock pins _txlock=immediate. A
// deferred transaction that reads and then writes upgrades its lock half-way,
// and when another connection has written in between, SQLite fails it
// immediately with SQLITE_BUSY instead of waiting -- busy_timeout does not
// apply to that case. update_site's read-merge-write transaction is exactly
// that shape.
func TestConcurrentTransactionsWaitForTheWriteLock(t *testing.T) {
	conn, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`INSERT INTO configs (key, value) VALUES ('n', '0')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const workers, rounds = 8, 25
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				errs <- func() error {
					tx, err := conn.Begin()
					if err != nil {
						return err
					}
					defer tx.Rollback()
					var v int
					if err := tx.QueryRow(`SELECT CAST(value AS INTEGER) FROM configs WHERE key = 'n'`).Scan(&v); err != nil {
						return err
					}
					if _, err := tx.Exec(`UPDATE configs SET value = ? WHERE key = 'n'`, fmt.Sprint(v+1)); err != nil {
						return err
					}
					return tx.Commit()
				}()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("read-then-write transaction failed under contention: %v", err)
		}
	}

	if got := rawText(t, conn, `SELECT value FROM configs WHERE key = 'n'`); got != fmt.Sprint(workers*rounds) {
		t.Errorf("counter = %s, want %d: a transaction lost an update", got, workers*rounds)
	}
}
