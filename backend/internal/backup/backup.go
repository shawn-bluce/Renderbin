// Package backup produces complete, restorable backups of a live instance,
// and restores one back over it.
//
// A backup is a tar stream: first renderbin.db, a VACUUM INTO snapshot of the
// database (consistent, fully checkpointed, safe to take without stopping the
// server), then blobs/<id> for every blob a file in that snapshot uses. The
// database names content and the blob store holds it (see internal/blobstore),
// so neither half is a backup on its own.
//
// Consistency between the two halves comes from the collector's grace period
// rather than from a lock: a blob the snapshot references was in use when the
// snapshot was taken, so even if it is replaced or deleted a moment later it
// stays on disk for the grace period (24h), which is the longest a backup may
// take. That works across processes, which is why `server backup` can run
// beside a live server.
package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
)

// Names inside the tar.
const (
	dbEntry    = "renderbin.db"
	blobPrefix = "blobs/"
)

// Rejections that are the uploader's fault rather than the server's, so callers
// can answer 400 instead of 500. Nothing has been changed when one is returned.
var (
	ErrNotSQLite      = errors.New("the uploaded file is not a renderbin backup")
	ErrNoAccounts     = errors.New("the snapshot contains no accounts, so restoring it would lock everyone out")
	ErrSchemaMismatch = errors.New("the snapshot's schema does not match this version of the app")
	ErrIncomplete     = errors.New("the backup is incomplete")
	ErrCorrupt        = errors.New("the backup is corrupt")
	// A bare database from after content moved out of it names blobs it does
	// not carry; only the .tar this package writes has them.
	ErrBareDatabase = errors.New("this .db file is only the database half of a backup; restore the .tar backup it came from")
)

// sqliteMagic is the 16-byte header every SQLite database file starts with.
const sqliteMagic = "SQLite format 3\x00"

// Stats reports what a restored snapshot contained.
type Stats struct {
	Users int64
	Files int64
}

// snapshot writes a consistent copy of the database to destPath using VACUUM
// INTO. destPath must not already exist (SQLite refuses to overwrite it).
func snapshot(ctx context.Context, database *sql.DB, destPath string) error {
	// VACUUM INTO takes a string literal, not a bound parameter, so the path
	// is interpolated. destPath is server-controlled (a temp path), never user
	// input; single quotes are still escaped defensively.
	escaped := strings.ReplaceAll(destPath, "'", "''")
	if _, err := database.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return fmt.Errorf("vacuum into %s: %w", destPath, err)
	}
	return nil
}

// openRaw opens a snapshot without migrating it: Write's own snapshot is
// already this binary's schema.
func openRaw(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_time_format=sqlite&_timezone=UTC")
}

type blobRef struct {
	id     string
	size   int64
	sha256 string
}

// referencedBlobs lists the blobs the snapshot's files use.
func referencedBlobs(ctx context.Context, conn *sql.DB) ([]blobRef, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT blobs.id, blobs.size, blobs.sha256 FROM blobs
		JOIN files ON files.blob_id = blobs.id
		ORDER BY blobs.id`)
	if err != nil {
		return nil, fmt.Errorf("list blobs: %w", err)
	}
	defer rows.Close()
	var refs []blobRef
	for rows.Next() {
		var r blobRef
		if err := rows.Scan(&r.id, &r.size, &r.sha256); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// Write streams a complete backup to w. It writes nothing to the live
// database; its temporary snapshot lives in the store's temp directory, on the
// data volume.
//
// An error after the first byte leaves w holding a truncated tar, which
// Restore refuses (it checks that every referenced blob arrived). Callers that
// stream to a client cannot change their status by then, and should log it.
func Write(ctx context.Context, live *sql.DB, store *blobstore.Store, w io.Writer) error {
	dir, err := os.MkdirTemp(store.TempDir(), "backup-")
	if err != nil {
		return fmt.Errorf("backup temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, dbEntry)
	if err := snapshot(ctx, live, path); err != nil {
		return err
	}
	refs, err := pruneSnapshot(ctx, path)
	if err != nil {
		return err
	}

	tw := tar.NewWriter(w)
	if err := addFile(tw, dbEntry, path); err != nil {
		return err
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := addBlob(tw, store, ref); err != nil {
			return err
		}
	}
	return tw.Close()
}

// pruneSnapshot drops the snapshot's records of blobs no file uses -- they
// are garbage awaiting the collector, and their bytes are not in the backup --
// and returns the blobs that remain.
func pruneSnapshot(ctx context.Context, path string) ([]blobRef, error) {
	conn, err := openRaw(path)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx,
		`DELETE FROM blobs WHERE NOT EXISTS (SELECT 1 FROM files WHERE files.blob_id = blobs.id)`); err != nil {
		return nil, fmt.Errorf("prune snapshot: %w", err)
	}
	refs, err := referencedBlobs(ctx, conn)
	if err != nil {
		return nil, err
	}
	return refs, conn.Close()
}

func addFile(tw *tar.Writer, name, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o644, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func addBlob(tw *tar.Writer, store *blobstore.Store, ref blobRef) error {
	f, err := store.Open(ref.id)
	if err != nil {
		// A blob a file used when the snapshot was taken is gone: the backup
		// took longer than the collector's grace period, or the store lost
		// data. Either way the result would not restore.
		return fmt.Errorf("back up blob %s: %w", ref.id, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != ref.size {
		return fmt.Errorf("back up blob %s: it is %d bytes on disk but recorded as %d", ref.id, info.Size(), ref.size)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: blobPrefix + ref.id, Mode: 0o644, Size: ref.size, ModTime: info.ModTime(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, ref.size)
	return err
}

// Restore replaces the live data with a backup read from r: the tar Write
// produces, or a bare .db downloaded from a version that still kept content
// inside the database. Either way the live database only changes in the last
// step, a single transaction, so a rejected or failed restore leaves it
// exactly as it was.
//
// It copies row data into the *existing* database rather than swapping the file
// underneath the process. Swapping would mean closing the live *sql.DB and
// handing a new one to every handler that captured a *sqlcgen.Queries at
// construction — a wide change with a window where the database is simply gone.
// Copying instead means the connection every handler already holds sees the
// restored rows on its next query: no reopen, no restart, nothing to coordinate.
//
// The snapshot is first opened through db.Open, which migrates it up to the
// current schema — that is what makes a backup taken by an older build
// restorable by a newer one. For a pre-blob .db, that migration is what moves
// its content out into the store.
//
// Every blob that arrives is stored under a *new* ID, and the snapshot is
// rewritten to match before anything is copied. So a restore never writes over
// or reuses a blob the live store already has: restoring this server's own
// backup cannot collide with the data it replaces, and cannot adopt a blob the
// collector is about to remove. Blobs stored by a restore that then fails are
// removed again; the live data's blobs become garbage only once it succeeds.
//
// Faithfulness is the point: the sessions table is replaced too, so whoever
// triggered the restore is signed out unless their session happens to be in the
// snapshot. Callers should expect to send the user back through login.
func Restore(ctx context.Context, live *sql.DB, store *blobstore.Store, r io.Reader) (stats Stats, err error) {
	dir, err := os.MkdirTemp(store.TempDir(), "restore-")
	if err != nil {
		return Stats{}, fmt.Errorf("restore temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	// Blobs this restore stored, to remove again if it fails.
	var created []string
	defer func() {
		if err != nil {
			for _, id := range created {
				_ = store.Remove(id)
			}
		}
	}()

	br := bufio.NewReaderSize(r, 4096)
	head, _ := br.Peek(512)
	path := filepath.Join(dir, dbEntry)
	switch {
	case bytes.HasPrefix(head, []byte(sqliteMagic)):
		stats, created, err = prepareLegacy(ctx, store, br, path)
	case len(head) >= 262 && string(head[257:262]) == "ustar":
		stats, created, err = prepareTar(ctx, store, br, path)
	default:
		err = ErrNotSQLite
	}
	if err != nil {
		return Stats{}, err
	}

	if err := copyInto(ctx, live, path); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// prepareLegacy readies a .db from before content moved out of the database:
// opening it through db.Open runs the extraction migration, which writes its
// content into the live store under new IDs.
func prepareLegacy(ctx context.Context, store *blobstore.Store, r io.Reader, path string) (Stats, []string, error) {
	if err := writeTemp(path, r); err != nil {
		return Stats{}, nil, err
	}
	bare, err := hasTable(path, "blobs")
	if err != nil {
		return Stats{}, nil, fmt.Errorf("%w: %v", ErrNotSQLite, err)
	}
	if bare {
		return Stats{}, nil, ErrBareDatabase
	}

	snap, err := db.Open(path, store)
	if err != nil {
		if errors.Is(err, db.ErrInsufficientSpace) {
			return Stats{}, nil, err
		}
		return Stats{}, nil, fmt.Errorf("%w: %v", ErrNotSQLite, err)
	}
	defer snap.Close()
	// Everything in its blobs table was stored just now, by the migration.
	created, err := blobIDs(ctx, snap)
	if err != nil {
		return Stats{}, nil, err
	}
	stats, err := snapshotStats(ctx, snap)
	if err != nil {
		return Stats{}, created, err
	}
	// Close before the live connection attaches it, so its WAL is
	// checkpointed into the file the other connection is about to read.
	return stats, created, snap.Close()
}

// prepareTar readies a backup Write produced: the database, then its blobs,
// each checked against the size and checksum the database records for it and
// stored under a new ID.
func prepareTar(ctx context.Context, store *blobstore.Store, r io.Reader, path string) (Stats, []string, error) {
	tr := tar.NewReader(r)
	hdr, err := tr.Next()
	if err != nil {
		return Stats{}, nil, fmt.Errorf("%w: %v", ErrNotSQLite, err)
	}
	if hdr.Name != dbEntry || hdr.Typeflag != tar.TypeReg {
		return Stats{}, nil, fmt.Errorf("%w: it does not start with %s", ErrNotSQLite, dbEntry)
	}
	if err := writeTemp(path, tr); err != nil {
		return Stats{}, nil, cutShort(err)
	}
	if err := checkSQLiteHeader(path); err != nil {
		return Stats{}, nil, err
	}

	snap, err := db.Open(path, store)
	if err != nil {
		return Stats{}, nil, fmt.Errorf("%w: %v", ErrNotSQLite, err)
	}
	defer snap.Close()
	stats, err := snapshotStats(ctx, snap)
	if err != nil {
		return Stats{}, nil, err
	}
	refs, err := referencedBlobs(ctx, snap)
	if err != nil {
		return Stats{}, nil, err
	}
	want := make(map[string]blobRef, len(refs))
	for _, ref := range refs {
		want[ref.id] = ref
	}

	var created []string
	renamed := make(map[string]string, len(refs)) // backup's id -> new id
	for {
		if err := ctx.Err(); err != nil {
			return Stats{}, created, err
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if cut := cutShort(err); cut != err {
				return Stats{}, created, cut
			}
			return Stats{}, created, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		id, ok := strings.CutPrefix(hdr.Name, blobPrefix)
		ref, wanted := want[id]
		if !ok || !wanted || hdr.Typeflag != tar.TypeReg {
			return Stats{}, created, fmt.Errorf("%w: unexpected entry %q", ErrCorrupt, hdr.Name)
		}
		if _, dup := renamed[id]; dup {
			return Stats{}, created, fmt.Errorf("%w: %s appears twice", ErrCorrupt, hdr.Name)
		}
		if hdr.Size != ref.size {
			return Stats{}, created, fmt.Errorf("%w: %s is %d bytes, the database says %d", ErrCorrupt, hdr.Name, hdr.Size, ref.size)
		}
		b, err := store.PutReader(io.LimitReader(tr, ref.size))
		if err != nil {
			return Stats{}, created, cutShort(fmt.Errorf("store %s: %w", hdr.Name, err))
		}
		created = append(created, b.ID)
		if b.Size != ref.size || b.SHA256 != ref.sha256 {
			return Stats{}, created, fmt.Errorf("%w: %s does not match its checksum", ErrCorrupt, hdr.Name)
		}
		renamed[id] = b.ID
	}
	if missing := len(want) - len(renamed); missing > 0 {
		return Stats{}, created, fmt.Errorf("%w: %d of its %d files' content is missing (was the download cut short?)",
			ErrIncomplete, missing, len(want))
	}

	if err := renameBlobs(ctx, snap, renamed); err != nil {
		return Stats{}, created, err
	}
	// A backup from before blobs carried search fields was migrated by
	// db.Open before its blobs had arrived, so those fields could not be
	// computed then; they can now.
	if err := db.FillSearchMeta(snap, store); err != nil {
		return Stats{}, created, fmt.Errorf("fill search fields: %w", err)
	}
	return stats, created, snap.Close()
}

// renameBlobs points the snapshot at the new IDs its blobs were stored under,
// and drops its records of blobs the backup did not carry (garbage that was
// awaiting the collector when it was taken).
func renameBlobs(ctx context.Context, snap *sql.DB, renamed map[string]string) error {
	// One connection: the mapping is a temp table, which only exists on the
	// connection that created it.
	conn, err := snap.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmts := []string{
		`CREATE TEMP TABLE blob_rename (old TEXT PRIMARY KEY, new TEXT NOT NULL)`,
		`DELETE FROM blobs WHERE NOT EXISTS (SELECT 1 FROM files WHERE files.blob_id = blobs.id)`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("rename blobs: %w", err)
		}
	}
	for old, id := range renamed {
		if _, err := tx.ExecContext(ctx, `INSERT INTO blob_rename (old, new) VALUES (?, ?)`, old, id); err != nil {
			return fmt.Errorf("rename blobs: %w", err)
		}
	}
	// blobs first: files' check trigger looks the new ID up in blobs.
	for _, q := range []string{
		`UPDATE blobs SET id = (SELECT new FROM blob_rename WHERE old = blobs.id)`,
		`UPDATE files SET blob_id = (SELECT new FROM blob_rename WHERE old = files.blob_id)`,
		`DROP TABLE blob_rename`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("rename blobs: %w", err)
		}
	}
	return tx.Commit()
}

// cutShort reports a tar that ends in the middle of an entry as ErrIncomplete:
// a download that was cut off is the uploader's to fix, not a server error.
func cutShort(err error) error {
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: it ends in the middle of a file (was the download cut short?)", ErrIncomplete)
	}
	return err
}

func writeTemp(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create restore temp file: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return fmt.Errorf("read the uploaded backup: %w", err)
	}
	return f.Close()
}

// hasTable reports whether the database at path has a table of that name,
// without migrating it.
func hasTable(path, name string) (bool, error) {
	conn, err := openRaw(path)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	var n int
	err = conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

func blobIDs(ctx context.Context, conn *sql.DB) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT id FROM blobs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func checkSQLiteHeader(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer f.Close()

	header := make([]byte, len(sqliteMagic))
	if _, err := io.ReadFull(f, header); err != nil || string(header) != sqliteMagic {
		return ErrNotSQLite
	}
	return nil
}

// snapshotStats doubles as the last validity gate: a database with no users is
// either not one of ours or an empty shell, and restoring it would leave an
// instance nobody can sign in to with the previous data already gone.
func snapshotStats(ctx context.Context, snapshot *sql.DB) (Stats, error) {
	var stats Stats
	if err := snapshot.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&stats.Users); err != nil {
		return Stats{}, fmt.Errorf("%w: %v", ErrNotSQLite, err)
	}
	if stats.Users == 0 {
		return Stats{}, ErrNoAccounts
	}
	if err := snapshot.QueryRowContext(ctx, `SELECT COUNT(*) FROM files`).Scan(&stats.Files); err != nil {
		return Stats{}, fmt.Errorf("%w: %v", ErrNotSQLite, err)
	}
	return stats, nil
}

// FilenameStamp is the timestamp a backup's file name carries.
func FilenameStamp(t time.Time) string { return t.UTC().Format("20060102-150405") }

// copyInto attaches the snapshot and replaces every table's contents with its
// rows, inside a single transaction.
func copyInto(ctx context.Context, live *sql.DB, path string) error {
	// One physical connection for ATTACH, the transaction and DETACH: an
	// attached schema is per-connection state, and the pool could otherwise
	// hand the transaction a different connection than the one holding it.
	conn, err := live.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	escaped := strings.ReplaceAll(path, "'", "''")
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE '"+escaped+"' AS restore_src"); err != nil {
		return fmt.Errorf("attach snapshot: %w", err)
	}
	// Detach even when the copy fails, or a later restore on this connection
	// collides with the leftover attachment. WithoutCancel so an aborted
	// request still cleans up after itself.
	defer func() {
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), "DETACH DATABASE restore_src")
	}()

	tables, err := restorableTables(ctx, conn)
	if err != nil {
		return err
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin restore: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, table := range tables {
		cols, err := copyColumns(ctx, tx, table)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM main.`+quoteIdent(table)); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO main.`+quoteIdent(table)+` (`+cols+`) SELECT `+cols+` FROM restore_src.`+quoteIdent(table),
		); err != nil {
			return fmt.Errorf("copy %s: %w", table, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit restore: %w", err)
	}
	return nil
}

// restorableTables lists what to copy: every table of ours, plus sqlite_sequence.
//
// sqlite_sequence is not cosmetic. It holds the highest id ever handed out per
// AUTOINCREMENT table, and SQLite lets you write it. Leaving the live value in
// place while replacing the rows underneath it would let a restore from a larger
// database hand out ids that already exist; copying it keeps the counter
// consistent with the rows it counts.
//
// A table missing from the snapshot is skipped rather than emptied. The snapshot
// has already been migrated to the current schema, so this only fires on
// something genuinely unexpected — and skipping loses less than blanking.
//
// schema_migrations is excluded, and that exclusion is load-bearing. It records
// which migration files this database has applied, which is a property of the
// binary that owns the file, not of the data being restored. Copying it lets a
// snapshot taken on a newer build tell an older one that a migration it never
// ran is already applied — and db.Open skips anything already recorded, so that
// migration would then be skipped *forever*, leaving queries referencing a
// column that does not exist. The live table is already correct: the snapshot
// was opened through db.Open and migrated up to this binary's schema before we
// copied a single row.
func restorableTables(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT name FROM main.sqlite_master
		WHERE type = 'table'
		  AND (name NOT LIKE 'sqlite_%' OR name = 'sqlite_sequence')
		  AND name <> 'schema_migrations'
		  AND name IN (SELECT name FROM restore_src.sqlite_master WHERE type = 'table')
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("%w: it shares no tables with this database", ErrNotSQLite)
	}
	return tables, nil
}

// quoteIdent quotes a schema identifier with brackets rather than double
// quotes, and that choice is a safety property, not a style one.
//
// SQLite's double-quoted-string misfeature means "foo" silently degrades to the
// *string literal* 'foo' when no column of that name exists. In a
// `SELECT "tags" FROM restore_src.files` over a snapshot missing that column,
// that turns a schema mismatch into a successful copy that writes the text
// "tags" into every row. Brackets have no such fallback: they are always an
// identifier, so the same query fails with "no such column".
func quoteIdent(name string) string {
	return "[" + strings.ReplaceAll(name, "]", "]]") + "]"
}

// copyColumns returns the columns to copy for one table, as a bracket-quoted,
// comma-separated list. Naming them explicitly rather than using SELECT * means
// the copy doesn't depend on the two schemas listing their columns in the same
// *order* — only on the same set.
//
// It compares the two schemas up front so a mismatch is reported as itself
// rather than as a bare "no such column" from deep inside the copy. Migrating
// the snapshot should have made this impossible; it is checked because the
// failure it guards against writes wrong data rather than erroring.
func copyColumns(ctx context.Context, tx *sql.Tx, table string) (string, error) {
	live, err := tableColumns(ctx, tx, table, "main")
	if err != nil {
		return "", err
	}
	snapshot, err := tableColumns(ctx, tx, table, "restore_src")
	if err != nil {
		return "", err
	}
	if len(live) == 0 {
		return "", fmt.Errorf("table %s has no columns", table)
	}

	inSnapshot := make(map[string]bool, len(snapshot))
	for _, c := range snapshot {
		inSnapshot[c] = true
	}
	quoted := make([]string, 0, len(live))
	for _, c := range live {
		if !inSnapshot[c] {
			return "", fmt.Errorf("%w: %s.%s is missing", ErrSchemaMismatch, table, c)
		}
		quoted = append(quoted, quoteIdent(c))
	}
	return strings.Join(quoted, ", "), nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table, schema string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?, ?)`, table, schema)
	if err != nil {
		return nil, fmt.Errorf("columns of %s.%s: %w", schema, table, err)
	}
	defer rows.Close()

	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan column of %s.%s: %w", schema, table, err)
		}
		cols = append(cols, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("columns of %s.%s: %w", schema, table, err)
	}
	return cols, nil
}
