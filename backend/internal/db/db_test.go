package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
)

// TestOpenRunsAllMigrations also covers re-opening: a second Open of the same
// file must not re-apply anything.
func TestOpenRunsAllMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")
	store := testStore(t)
	conn, err := Open(path, store)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer conn.Close()

	// schema_migrations should record exactly one row per migration: every
	// embedded file plus every Go step.
	names, err := migrationNames()
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	want := len(names)
	if want == 0 {
		t.Fatal("no migrations")
	}

	var got int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&got); err != nil {
		t.Fatalf("count schema_migrations: %v", err)
	}
	if got != want {
		t.Errorf("applied migrations = %d, want %d", got, want)
	}

	// Every column the schema declares must exist and be queryable.
	if _, err := conn.Exec(`INSERT INTO blobs (id, size, sha256) VALUES ('b', 8, 'x')`); err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	if _, err := conn.Exec(
		`INSERT INTO files (slug, name, blob_id, content_size, access_code, user_id) VALUES ('s', 'n', 'b', 8, 'code', 1)`,
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
	again, err := Open(path, store)
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
	conn := openTest(t)

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

	if _, err := migrate(conn, testStore(t)); err != nil {
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
	conn, err := Open(path, testStore(t))
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

// insertBlobRow records a blob of the given size directly, for tests of the
// files triggers that need no bytes on disk.
func insertBlobRow(t *testing.T, conn *sql.DB, id string, size int) {
	t.Helper()
	if _, err := conn.Exec(`INSERT INTO blobs (id, size, sha256, orphaned_at) VALUES (?, ?, 'x', CURRENT_TIMESTAMP)`, id, size); err != nil {
		t.Fatalf("insert blob %s: %v", id, err)
	}
}

// TestContentSizeMustMatchBlob pins 0010's check triggers, the replacement for
// the old guarantee that content_size was computed from the content in the
// same statement. A row whose size disagrees with its blob, or whose blob has
// no record, is refused outright -- including the inserts a backup restore
// does, so a tampered snapshot cannot plant a size the quota would trust.
func TestContentSizeMustMatchBlob(t *testing.T) {
	conn := openTest(t)
	insertBlobRow(t, conn, "five", 5)
	insertBlobRow(t, conn, "ten", 10)

	insert := func(slug, blob string, size int) error {
		_, err := conn.Exec(`INSERT INTO files (slug, name, blob_id, content_size, access_code, user_id)
			VALUES (?, 'n', ?, ?, 'code', 1)`, slug, blob, size)
		return err
	}
	if err := insert("wrong-size", "five", 4); err == nil {
		t.Error("a row claiming 4 bytes of a 5-byte blob was accepted")
	}
	if err := insert("no-blob", "missing", 0); err == nil {
		t.Error("a row naming a blob with no record was accepted")
	}
	if err := insert("ok", "five", 5); err != nil {
		t.Fatalf("a correct row was refused: %v", err)
	}

	if _, err := conn.Exec(`UPDATE files SET blob_id = 'ten' WHERE slug = 'ok'`); err == nil {
		t.Error("swapping the blob without its size was accepted")
	}
	if _, err := conn.Exec(`UPDATE files SET content_size = 999 WHERE slug = 'ok'`); err == nil {
		t.Error("planting a size by hand was accepted")
	}
	if _, err := conn.Exec(`UPDATE files SET blob_id = 'ten', content_size = 10 WHERE slug = 'ok'`); err != nil {
		t.Errorf("a consistent swap was refused: %v", err)
	}
	// /res bumps a counter on every request; that must not trip the check.
	if _, err := conn.Exec(`UPDATE files SET success_count = success_count + 1 WHERE slug = 'ok'`); err != nil {
		t.Errorf("a counter bump was refused: %v", err)
	}
	// A blob belongs to one row.
	if err := insert("shared", "ten", 10); err == nil {
		t.Error("a second row sharing a blob was accepted")
	}
}

// TestBlobReferenceTracking pins the triggers the collector relies on: a blob
// is orphaned exactly when the last row lets go of it, and only then.
func TestBlobReferenceTracking(t *testing.T) {
	conn := openTest(t)
	orphaned := func(id string) bool {
		t.Helper()
		var n int
		if err := conn.QueryRow(`SELECT orphaned_at IS NOT NULL FROM blobs WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("read blob %s: %v", id, err)
		}
		return n == 1
	}

	insertBlobRow(t, conn, "v1", 2)
	insertBlobRow(t, conn, "v2", 3)
	if !orphaned("v1") {
		t.Fatal("a new blob should start orphaned until a row adopts it")
	}
	if _, err := conn.Exec(`INSERT INTO files (slug, name, blob_id, content_size, access_code, user_id)
		VALUES ('f', 'n', 'v1', 2, 'code', 1)`); err != nil {
		t.Fatal(err)
	}
	if orphaned("v1") {
		t.Error("an inserted row did not adopt its blob")
	}

	// An edit swaps blobs: the new one is adopted, the old one let go.
	if _, err := conn.Exec(`UPDATE files SET blob_id = 'v2', content_size = 3 WHERE slug = 'f'`); err != nil {
		t.Fatal(err)
	}
	if orphaned("v2") || !orphaned("v1") {
		t.Errorf("after an edit: v1 orphaned=%v (want true), v2 orphaned=%v (want false)", orphaned("v1"), orphaned("v2"))
	}

	// The trash keeps its content: soft delete is an UPDATE of deleted_at.
	if _, err := conn.Exec(`UPDATE files SET deleted_at = CURRENT_TIMESTAMP WHERE slug = 'f'`); err != nil {
		t.Fatal(err)
	}
	if orphaned("v2") {
		t.Error("moving a file to the trash orphaned its content")
	}

	if _, err := conn.Exec(`DELETE FROM files WHERE slug = 'f'`); err != nil {
		t.Fatal(err)
	}
	if !orphaned("v2") {
		t.Error("deleting the row did not orphan its blob")
	}
}

// stageLegacyContent builds a database in the state the last release before
// blob storage left it, holding the given rows, and returns its path.
func stageLegacyContent(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	legacy := openRaw(t, path)
	applyMigrationsBefore(t, legacy, "0008")
	for _, stmt := range stmts {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("stage legacy data: %v\n%s", err, stmt)
		}
	}
	legacy.Close()
	return path
}

// TestMigrationMovesContentToBlobs covers 0008-0010 end to end: the content of
// every row leaves the database for the blob store, byte for byte, and
// nothing else about any row changes. A table rebuild is the one kind of
// migration that can lose data silently, so every column is compared.
func TestMigrationMovesContentToBlobs(t *testing.T) {
	big := strings.Repeat("0", 400000)
	path := stageLegacyContent(t,
		`INSERT INTO users (username, nickname, password_hash) VALUES ('a', 'A', 'h')`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id, tags, is_public,
		                    expires_at, success_count, kind)
		 VALUES ('doc', 'Doc', '汉字 body', 'code1', 1, 'x,y', 1,
		         '2030-01-02 03:04:05+00:00', 7, 'markdown')`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id, kind, archive,
		                    deleted_at, max_views, expired_reason)
		 VALUES ('site', 'Site', '', 'code2', 1, 'site', x'504b0506',
		         '2026-01-01 00:00:00+00:00', 3, 'views')`,
		// Large enough that the dropped content dominates the file, which is
		// what the compaction check below needs to see.
		`INSERT INTO files (slug, name, html_content, access_code, user_id)
		 VALUES ('big', 'Big', '`+big+`', 'code4', 1)`,
		// Issued and then hard-deleted: the counter must stay at 4, not fall
		// back to the highest surviving id.
		`INSERT INTO files (slug, name, html_content, access_code, user_id)
		 VALUES ('gone', 'Gone', 'x', 'code3', 1)`,
		`DELETE FROM files WHERE slug = 'gone'`,
	)
	want := map[string][]byte{"doc": []byte("汉字 body"), "site": {0x50, 0x4b, 0x05, 0x06}, "big": []byte(big)}

	const cols = `id, slug, name, is_public, access_code, created_at,
		updated_at, deleted_at, success_count, failure_count, tags, expires_at,
		max_views, view_count, kind, code_success_count, user_id, expired_at,
		expired_reason, content_size`
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
	before := snapshot(openRaw(t, path))

	store := testStore(t)
	conn, err := Open(path, store)
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	defer conn.Close()

	if after := snapshot(conn); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Errorf("rows changed by the migration:\nbefore %v\nafter  %v", before, after)
	}

	for slug, content := range want {
		var blobID string
		var size int64
		var orphaned bool
		if err := conn.QueryRow(`SELECT f.blob_id, b.size, b.orphaned_at IS NOT NULL FROM files f
			JOIN blobs b ON b.id = f.blob_id WHERE f.slug = ?`, slug).Scan(&blobID, &size, &orphaned); err != nil {
			t.Fatalf("%s: read its blob: %v", slug, err)
		}
		got, err := store.Read(blobID)
		if err != nil {
			t.Fatalf("%s: read blob from disk: %v", slug, err)
		}
		if !bytes.Equal(got, content) || size != int64(len(content)) {
			t.Errorf("%s: blob holds %d bytes (recorded %d), want its %d bytes of content", slug, len(got), size, len(content))
		}
		if orphaned {
			t.Errorf("%s: its blob is marked orphaned, so the collector would delete it", slug)
		}
	}
	if n := rawText(t, conn, `SELECT COUNT(*) FROM blobs`); n != "3" {
		t.Errorf("%s blob records, want exactly one per file", n)
	}

	for _, gone := range []string{"html_content", "archive"} {
		if n := rawText(t, conn, `SELECT COUNT(*) FROM pragma_table_info('files') WHERE name = ?`, gone); n != "0" {
			t.Errorf("files.%s still exists", gone)
		}
	}
	if n := rawText(t, conn, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'blob_extraction'`); n != "0" {
		t.Error("blob_extraction was not dropped")
	}
	if seq := rawText(t, conn, `SELECT seq FROM sqlite_sequence WHERE name = 'files'`); seq != "4" {
		t.Errorf("files sequence = %s after the rebuild, want 4 (the highest id ever issued)", seq)
	}
	if n := rawText(t, conn, `SELECT COUNT(*) FROM sqlite_sequence WHERE name = 'files_new'`); n != "0" {
		t.Errorf("files_new still has %s sequence row(s)", n)
	}
	// The database held the content until the rebuild dropped it; Open must
	// have given that space back.
	if free := rawText(t, conn, `SELECT freelist_count FROM pragma_freelist_count`); free != "0" {
		t.Errorf("%s free pages after the upgrade, want 0 (compacted)", free)
	}
	for _, name := range []string{
		"idx_files_owner_list",
		"files_blob_check_insert",
		"files_blob_check_update",
		"files_blob_adopt",
		"files_blob_swap",
		"files_blob_release",
	} {
		if n := rawText(t, conn,
			`SELECT COUNT(*) FROM sqlite_master WHERE name = ? AND tbl_name = 'files'`, name,
		); n != "1" {
			t.Errorf("%s missing after the rebuild", name)
		}
	}
	for _, name := range []string{"files_content_size_after_insert", "files_content_size_after_update"} {
		if n := rawText(t, conn, `SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name); n != "0" {
			t.Errorf("%s survived; it measures columns that no longer exist", name)
		}
	}
}

// TestExtractionResumes interrupts the extraction after its first batch, with
// a blob written but never recorded as a crash would leave it, and checks that
// the next start finishes the job without redoing the first batch.
func TestExtractionResumes(t *testing.T) {
	path := stageLegacyContent(t,
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('a', 'A', 'first', 'c1', 1)`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('b', 'B', 'second', 'c2', 1)`,
	)
	store := testStore(t)

	// The first start gets as far as one batch of one file.
	defer func(old int64) { extractBatchBytes = old }(extractBatchBytes)
	extractBatchBytes = 1
	partial := openRaw(t, path)
	applyMigrationsFrom(t, partial, "0008", "0009")
	if res, err := extractBatch(partial, store); err != nil || res.files != 1 {
		t.Fatalf("first batch = %+v, %v; want one file", res, err)
	}
	if _, err := store.Put([]byte("written but never recorded")); err != nil {
		t.Fatal(err)
	}
	partial.Close()

	conn, err := Open(path, store)
	if err != nil {
		t.Fatalf("Open (resume): %v", err)
	}
	defer conn.Close()

	for slug, content := range map[string]string{"a": "first", "b": "second"} {
		var id string
		if err := conn.QueryRow(`SELECT blob_id FROM files WHERE slug = ?`, slug).Scan(&id); err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		if got, err := store.Read(id); err != nil || string(got) != content {
			t.Errorf("%s: blob = %q, %v; want %q", slug, got, err, content)
		}
	}
	if n := rawText(t, conn, `SELECT COUNT(*) FROM blobs`); n != "2" {
		t.Errorf("%s blob records after resuming, want 2: the first batch was redone", n)
	}
}

// applyMigrationsFrom applies the SQL migrations from first (inclusive) up to
// but excluding stop.
func applyMigrationsFrom(t *testing.T, conn *sql.DB, first, stop string) {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name < first || name >= stop {
			continue
		}
		body, _ := migrationsFS.ReadFile("migrations/" + name)
		if _, err := conn.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := conn.Exec(`INSERT INTO schema_migrations (filename) VALUES (?)`, name); err != nil {
			t.Fatal(err)
		}
	}
}

// TestExtractionRefusesWithoutSpace pins the upgrade's disk check: rather than
// fill the disk halfway through, it refuses to start and changes nothing.
func TestExtractionRefusesWithoutSpace(t *testing.T) {
	path := stageLegacyContent(t,
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('a', 'A', 'content', 'c1', 1)`,
	)
	defer func(old func(*blobstore.Store) (uint64, bool)) { diskFree = old }(diskFree)
	diskFree = func(*blobstore.Store) (uint64, bool) { return 1024, true }

	if _, err := Open(path, testStore(t)); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("Open with a full disk = %v, want ErrInsufficientSpace", err)
	}
	raw := openRaw(t, path)
	if got := rawText(t, raw, `SELECT html_content FROM files WHERE slug = 'a'`); got != "content" {
		t.Errorf("content = %q after the refused upgrade, want it untouched", got)
	}
	if n := rawText(t, raw, `SELECT COUNT(*) FROM schema_migrations WHERE filename >= '0009'`); n != "0" {
		t.Errorf("%s migration(s) from 0009 on recorded after the refusal", n)
	}
}

// A row whose content_size disagrees with its content is refused rather than
// guessed at: the size is what the quota counts.
func TestExtractionRefusesInconsistentRows(t *testing.T) {
	path := stageLegacyContent(t,
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('a', 'A', 'content', 'c1', 1)`,
		// Writing content_size alone does not fire 0006's triggers.
		`UPDATE files SET content_size = 999 WHERE slug = 'a'`,
	)
	_, err := Open(path, testStore(t))
	if err == nil || !strings.Contains(err.Error(), "content_size says 999") {
		t.Fatalf("Open = %v, want a refusal naming the size mismatch", err)
	}
}

// TestInspectChangesNothing pins `server migrate --check`: it reports the
// pending work against a legacy database without doing any of it.
func TestInspectChangesNothing(t *testing.T) {
	path := stageLegacyContent(t,
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('a', 'A', '12345', 'c1', 1)`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id) VALUES ('b', 'B', '汉字', 'c2', 1)`,
	)
	blobDir := filepath.Join(t.TempDir(), "not-yet", "blobs")

	plan, err := Inspect(path, blobDir)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	// Everything from 0008 on, SQL files and Go steps alike.
	all, _ := migrationNames()
	var want []string
	for _, name := range all {
		if name >= "0008" {
			want = append(want, name)
		}
	}
	if fmt.Sprint(plan.Pending) != fmt.Sprint(want) {
		t.Errorf("Pending = %v, want %v", plan.Pending, want)
	}
	if plan.MoveFiles != 2 || plan.MoveBytes != 11 || plan.Need < 11 {
		t.Errorf("plan = %+v, want 2 files, 11 bytes", plan)
	}
	raw := openRaw(t, path)
	if n := rawText(t, raw, `SELECT COUNT(*) FROM schema_migrations WHERE filename >= '0008'`); n != "0" {
		t.Errorf("Inspect applied %s migration(s)", n)
	}
	raw.Close()
	if _, err := os.Stat(blobDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Inspect created the blob directory: %v", err)
	}

	store, err := blobstore.Open(blobDir)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := Open(path, store)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if plan, err = Inspect(path, blobDir); err != nil || len(plan.Pending) != 0 || plan.MoveFiles != 0 {
		t.Errorf("Inspect after upgrading = %+v, %v; want nothing pending", plan, err)
	}
}

// TestReadsDoNotQueueBehindEachOther pins that the handle is a pool. With a
// single connection, a query still holding its connection -- a backup's
// VACUUM INTO, a large row being read -- made every other request wait for it.
func TestReadsDoNotQueueBehindEachOther(t *testing.T) {
	conn := openTest(t)

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
	conn := openTest(t)
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

// TestMigrationFillsSearchMeta covers 0011/0012 on an upgrade: every file
// that already exists gets the head fields the default search looks at,
// extracted from content the upgrade just moved to the blob store.
func TestMigrationFillsSearchMeta(t *testing.T) {
	path := stageLegacyContent(t,
		`INSERT INTO files (slug, name, html_content, access_code, user_id, kind)
		 VALUES ('h', 'h', '<head><title>Old Report</title><meta name="description" content="from before"></head>', 'c1', 1, 'html')`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id, kind)
		 VALUES ('m', 'm', '# Markdown Title', 'c2', 1, 'markdown')`,
		`INSERT INTO files (slug, name, html_content, access_code, user_id, kind)
		 VALUES ('t', 't', '<title>not html</title>', 'c3', 1, 'txt')`,
	)
	conn, err := Open(path, testStore(t))
	if err != nil {
		t.Fatalf("Open (upgrade): %v", err)
	}
	defer conn.Close()

	for slug, want := range map[string]string{"h": "Old Report\nfrom before", "m": "Markdown Title", "t": ""} {
		var meta sql.NullString
		if err := conn.QueryRow(`SELECT blobs.search_meta FROM files JOIN blobs ON blobs.id = files.blob_id
			WHERE files.slug = ?`, slug).Scan(&meta); err != nil {
			t.Fatal(err)
		}
		if !meta.Valid || meta.String != want {
			t.Errorf("%s: search_meta = %v, want %q", slug, meta, want)
		}
	}
}
