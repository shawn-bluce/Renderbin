package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/shawn-bluce/renderbin/backend/internal/backup"
	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// instance is one server's data: a database and the blob store beside it.
type instance struct {
	conn  *sql.DB
	store *blobstore.Store
}

func newInstance(t *testing.T) instance {
	t.Helper()
	dir := t.TempDir()
	store, err := blobstore.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatalf("blobstore.Open: %v", err)
	}
	conn, err := db.Open(filepath.Join(dir, "app.db"), store)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return instance{conn: conn, store: store}
}

// seed creates an instance with one account and a file per slug, each
// holding "<p>slug</p>".
func seed(t *testing.T, username string, slugs ...string) instance {
	t.Helper()
	in := newInstance(t)
	in.addUser(t, username)
	for _, slug := range slugs {
		in.addFile(t, slug, []byte("<p>"+slug+"</p>"))
	}
	return in
}

func (in instance) addUser(t *testing.T, username string) {
	t.Helper()
	if _, err := in.conn.Exec(
		`INSERT INTO users (username, nickname, password_hash) VALUES (?, ?, 'hash')`, username, username,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// addFile stores content the way the handlers do: blob first, then the row.
func (in instance) addFile(t *testing.T, slug string, content []byte) sqlcgen.File {
	t.Helper()
	ctx := context.Background()
	q := sqlcgen.New(in.conn)
	b, err := in.store.Put(content)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := q.InsertBlob(ctx, sqlcgen.InsertBlobParams{ID: b.ID, Size: b.Size, Sha256: b.SHA256}); err != nil {
		t.Fatalf("InsertBlob: %v", err)
	}
	f, err := q.CreateFile(ctx, sqlcgen.CreateFileParams{
		Slug: slug, Name: slug, BlobID: b.ID, Kind: "html", AccessCode: "code", UserID: 1,
	})
	if err != nil {
		t.Fatalf("CreateFile(%s): %v", slug, err)
	}
	return f
}

// contentOf reads a file's content back through its row, as the handlers do.
func (in instance) contentOf(t *testing.T, slug string) string {
	t.Helper()
	var id string
	if err := in.conn.QueryRow(`SELECT blob_id FROM files WHERE slug = ?`, slug).Scan(&id); err != nil {
		t.Fatalf("blob of %s: %v", slug, err)
	}
	data, err := in.store.Read(id)
	if err != nil {
		t.Fatalf("read %s: %v", slug, err)
	}
	return string(data)
}

// blobFiles lists the blobs on disk.
func (in instance) blobFiles(t *testing.T) []string {
	t.Helper()
	var ids []string
	if err := in.store.Walk(func(id string, _ fs.FileInfo) error {
		ids = append(ids, id)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(ids)
	return ids
}

func backupOf(t *testing.T, in instance) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := backup.Write(context.Background(), in.conn, in.store, &buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

func restore(in instance, data []byte) (backup.Stats, error) {
	return backup.Restore(context.Background(), in.conn, in.store, bytes.NewReader(data))
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

// tarEntries lists a backup's entry names.
func tarEntries(t *testing.T, data []byte) []string {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(data))
	var names []string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("read backup: %v", err)
		}
		names = append(names, hdr.Name)
	}
}

type entry struct {
	name string
	data []byte
}

func tarOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// readEntries splits a backup back into its entries, for tests that tamper.
func readEntries(t *testing.T, data []byte) []entry {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(data))
	var out []entry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, entry{hdr.Name, body})
	}
}

func TestBackupRoundTrip(t *testing.T) {
	source := seed(t, "from-snapshot", "kept-a", "kept-b")
	binary := []byte{0x00, 0xff, 0x50, 0x4b, 0x03, 0x04}
	source.addFile(t, "binary", binary)
	// An edit leaves the old blob behind, orphaned: garbage, not data.
	edited := source.addFile(t, "edited", []byte("v1"))
	b, _ := source.store.Put([]byte("v2"))
	q := sqlcgen.New(source.conn)
	if err := q.InsertBlob(context.Background(), sqlcgen.InsertBlobParams{ID: b.ID, Size: b.Size, Sha256: b.SHA256}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.UpdateFile(context.Background(), sqlcgen.UpdateFileParams{
		Name: "edited", NewSlug: "edited", BlobID: b.ID, AccessCode: "code", Slug: "edited", UserID: 1,
	}); err != nil {
		t.Fatal(err)
	}

	data := backupOf(t, source)
	names := tarEntries(t, data)
	if len(names) != 5 || names[0] != "renderbin.db" {
		t.Fatalf("backup entries = %v, want renderbin.db then the 4 files' blobs", names)
	}
	for _, n := range names {
		if n == "blobs/"+edited.BlobID {
			t.Errorf("the backup carries %s, the orphaned old version", n)
		}
	}

	live := seed(t, "live-user", "gone-1", "gone-2", "gone-3")
	liveBlobs := live.blobFiles(t)
	stats, err := restore(live, data)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if stats.Users != 1 || stats.Files != 4 {
		t.Errorf("stats = %+v, want 1 user / 4 files", stats)
	}

	// The live handle is the same *sql.DB the caller had before: the point of
	// copying rather than swapping the file is that no reopen is needed.
	if got := slugsIn(t, live.conn); strings.Join(got, ",") != "binary,edited,kept-a,kept-b" {
		t.Errorf("files after restore = %v, want the snapshot's", got)
	}
	for slug, want := range map[string]string{"kept-a": "<p>kept-a</p>", "binary": string(binary), "edited": "v2"} {
		if got := live.contentOf(t, slug); got != want {
			t.Errorf("%s after restore = %q, want %q", slug, got, want)
		}
	}
	var username string
	if err := live.conn.QueryRow(`SELECT username FROM users`).Scan(&username); err != nil {
		t.Fatalf("query user: %v", err)
	}
	if username != "from-snapshot" {
		t.Errorf("user after restore = %q, want the snapshot's", username)
	}

	// The data the restore replaced is garbage now: on disk until the
	// collector's sweep, but no longer recorded.
	var recorded int
	for _, id := range liveBlobs {
		var n int
		if err := live.conn.QueryRow(`SELECT COUNT(*) FROM blobs WHERE id = ?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		recorded += n
	}
	if recorded != 0 {
		t.Errorf("%d of the replaced data's blobs are still recorded", recorded)
	}
}

// Restoring a server's own backup onto itself is the ordinary rollback, and
// the case where reusing the backup's blob IDs would collide with the live
// store: every restored blob gets a new ID instead.
func TestRestoreOwnBackup(t *testing.T) {
	in := seed(t, "admin", "a", "b")
	before := in.blobFiles(t)
	data := backupOf(t, in)
	in.addFile(t, "after-backup", []byte("lost by the rollback"))

	if _, err := restore(in, data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := slugsIn(t, in.conn); strings.Join(got, ",") != "a,b" {
		t.Errorf("files = %v, want the backup's", got)
	}
	if got := in.contentOf(t, "a"); got != "<p>a</p>" {
		t.Errorf("a = %q", got)
	}
	rows, err := in.conn.Query(`SELECT blob_id FROM files`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		for _, old := range before {
			if id == old {
				t.Errorf("restored file reuses blob %s instead of a fresh copy", id)
			}
		}
	}
}

// The id counter has to come across with the rows. Restoring a bigger database
// over a smaller one otherwise leaves the live sequence behind the restored
// data, and the next insert collides with an id that already exists.
func TestRestoreCarriesTheIDSequence(t *testing.T) {
	data := backupOf(t, seed(t, "big", "s1", "s2", "s3", "s4", "s5"))

	live := seed(t, "small", "only-one")
	if _, err := restore(live, data); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	f := live.addFile(t, "new", []byte("<p>n</p>"))
	if f.ID <= 5 {
		t.Errorf("new file id = %d, want > 5 (the restored maximum)", f.ID)
	}
}

// A .db downloaded from a version that kept content inside the database must
// still restore. Restore migrates it up to the current schema first -- the
// whole reason it opens the snapshot through db.Open -- and that migration is
// what moves its content out, into the live store.
func TestRestoreMigratesAnOlderSnapshot(t *testing.T) {
	old := filepath.Join(t.TempDir(), "old.db")
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
	data, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}

	live := seed(t, "current", "will-go")
	if _, err := restore(live, data); err != nil {
		t.Fatalf("Restore of an older snapshot: %v", err)
	}
	if got := slugsIn(t, live.conn); len(got) != 1 || got[0] != "legacy-file" {
		t.Errorf("files after restore = %v, want the old snapshot's", got)
	}
	if got := live.contentOf(t, "legacy-file"); got != "<p>old</p>" {
		t.Errorf("content after restore = %q, want the old snapshot's", got)
	}
	// The columns 0002 adds must exist and be queryable afterwards.
	var reason string
	if err := live.conn.QueryRow(`SELECT expired_reason FROM files WHERE slug = 'legacy-file'`).Scan(&reason); err != nil {
		t.Fatalf("query a post-0002 column: %v", err)
	}
}

func TestRestoreRejectsBadInput(t *testing.T) {
	good := backupOf(t, seed(t, "snap-user", "a", "b"))
	goodEntries := readEntries(t, good)

	// A database of ours with no accounts: restoring it would leave an
	// instance nobody can sign in to, with the old data gone.
	empty := backupOf(t, newInstance(t))

	// The database half of a backup on its own names blobs it does not carry.
	bare := goodEntries[0].data

	truncated := tarOf(t, goodEntries[:2]...)

	tampered := append([]entry(nil), goodEntries...)
	flipped := append([]byte(nil), tampered[1].data...)
	flipped[0] ^= 0xff
	tampered[1] = entry{tampered[1].name, flipped}

	extra := tarOf(t, append(append([]entry(nil), goodEntries...), entry{"../../etc/passwd", []byte("x")})...)
	notFirst := tarOf(t, goodEntries[1], goodEntries[0], goodEntries[2])

	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"not a backup", []byte("just some text, definitely not a database"), backup.ErrNotSQLite},
		{"no accounts", empty, backup.ErrNoAccounts},
		{"bare database", bare, backup.ErrBareDatabase},
		{"cut short", truncated, backup.ErrIncomplete},
		// Inside the database entry: its header promised more than arrived.
		{"cut mid-file", good[:600], backup.ErrIncomplete},
		{"tampered blob", tarOf(t, tampered...), backup.ErrCorrupt},
		{"unexpected entry", extra, backup.ErrCorrupt},
		{"database not first", notFirst, backup.ErrNotSQLite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			live := seed(t, "live-user", "untouched-1", "untouched-2")
			blobsBefore := live.blobFiles(t)

			_, err := restore(live, c.data)
			if !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
			// Rejection must leave the live data alone — that is what lets the
			// handler report the problem instead of a lost database.
			if got := slugsIn(t, live.conn); len(got) != 2 {
				t.Errorf("live files = %v, want both still there", got)
			}
			if got := live.contentOf(t, "untouched-1"); got != "<p>untouched-1</p>" {
				t.Errorf("live content = %q after a rejected restore", got)
			}
			// ...and take back whatever blobs it stored before it gave up.
			if after := live.blobFiles(t); strings.Join(after, ",") != strings.Join(blobsBefore, ",") {
				t.Errorf("blob store went from %d to %d entries after a rejected restore", len(blobsBefore), len(after))
			}
		})
	}
}

// A restore is all-or-nothing. A snapshot whose schema can't be copied must
// roll back, not leave the tables it already emptied empty.
func TestRestoreIsAtomic(t *testing.T) {
	broken := seed(t, "someone")
	// Drop a column the live schema still has, so INSERT ... SELECT on that
	// table fails after earlier tables have already been cleared.
	if _, err := broken.conn.Exec(`ALTER TABLE files DROP COLUMN tags`); err != nil {
		t.Fatalf("break schema: %v", err)
	}
	data := backupOf(t, broken)

	live := seed(t, "live-user", "keep-1", "keep-2")
	_, err := restore(live, data)
	if !errors.Is(err, backup.ErrSchemaMismatch) {
		t.Fatalf("err = %v, want ErrSchemaMismatch", err)
	}
	// Named, not a bare "no such column" from inside the copy.
	if !strings.Contains(err.Error(), "files.tags") {
		t.Errorf("error %q should name the missing column", err)
	}
	if got := slugsIn(t, live.conn); len(got) != 2 {
		t.Errorf("live files = %v, want both — a failed restore must roll back", got)
	}
	var users int
	if err := live.conn.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Errorf("users = %d, want the original 1", users)
	}
}

// Two restores in a row on the same *sql.DB: the attachment from the first must
// not linger and collide with the second.
func TestRestoreTwiceInARow(t *testing.T) {
	data := backupOf(t, seed(t, "snap-user", "a"))

	live := seed(t, "live-user", "b")
	for i := range 2 {
		if _, err := restore(live, data); err != nil {
			t.Fatalf("Restore %d: %v", i+1, err)
		}
	}
	if got := slugsIn(t, live.conn); len(got) != 1 || got[0] != "a" {
		t.Errorf("files = %v, want the snapshot's", got)
	}
	if got := live.contentOf(t, "a"); got != "<p>a</p>" {
		t.Errorf("a = %q", got)
	}
}

// A backup whose snapshot names a blob that is not on disk is refused at the
// source: the result would not restore.
func TestBackupFailsWhenContentIsMissing(t *testing.T) {
	in := seed(t, "admin", "a")
	f := in.addFile(t, "lost", []byte("bytes the disk lost"))
	if err := in.store.Remove(f.BlobID); err != nil {
		t.Fatal(err)
	}
	err := backup.Write(context.Background(), in.conn, in.store, io.Discard)
	if err == nil || !strings.Contains(err.Error(), f.BlobID) {
		t.Fatalf("Write = %v, want an error naming the missing blob", err)
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
	live := newInstance(t)

	snapSrc := seed(t, "admin")
	// Stand in for a snapshot taken by a future build: it claims a migration
	// this binary has never heard of.
	if _, err := snapSrc.conn.Exec(
		`INSERT INTO schema_migrations (filename) VALUES ('9999_from_the_future.sql')`,
	); err != nil {
		t.Fatalf("seed future migration: %v", err)
	}

	if _, err := restore(live, backupOf(t, snapSrc)); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	var future int
	if err := live.conn.QueryRow(
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
	if err := live.conn.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied == 0 {
		t.Error("the restore emptied the live migration ledger")
	}
}
