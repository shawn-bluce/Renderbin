package cron

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

type fixture struct {
	conn  *sql.DB
	store *blobstore.Store
	q     *sqlcgen.Queries
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	store, err := blobstore.Open(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Open(filepath.Join(dir, "app.db"), store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Exec(`INSERT INTO users (username, nickname, password_hash) VALUES ('u', 'u', 'h')`); err != nil {
		t.Fatal(err)
	}
	return fixture{conn: conn, store: store, q: sqlcgen.New(conn)}
}

// blob stores and records content, born orphaned, as a handler does.
func (f fixture) blob(t *testing.T, content string) string {
	t.Helper()
	b, err := f.store.Put([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.q.InsertBlob(context.Background(), sqlcgen.InsertBlobParams{ID: b.ID, Size: b.Size, Sha256: b.SHA256}); err != nil {
		t.Fatal(err)
	}
	return b.ID
}

func (f fixture) file(t *testing.T, slug, blobID string) {
	t.Helper()
	if _, err := f.q.CreateFile(context.Background(), sqlcgen.CreateFileParams{
		Slug: slug, Name: slug, BlobID: blobID, Kind: "html", AccessCode: "c", UserID: 1,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) onDisk(id string) bool {
	_, err := f.store.Read(id)
	return !errors.Is(err, blobstore.ErrNotFound)
}

func (f fixture) recorded(t *testing.T, id string) bool {
	t.Helper()
	var n int
	if err := f.conn.QueryRow(`SELECT COUNT(*) FROM blobs WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func backdate(t *testing.T, f fixture, id string, by time.Duration) {
	t.Helper()
	if _, err := f.conn.Exec(`UPDATE blobs SET orphaned_at = ? WHERE id = ?`,
		time.Now().Add(-by).UTC().Format("2006-01-02 15:04:05"), id); err != nil {
		t.Fatal(err)
	}
}

func TestCollectRemovesOnlyExpiredOrphans(t *testing.T) {
	f := newFixture(t)
	live := f.blob(t, "in use")
	f.file(t, "live", live)
	fresh := f.blob(t, "orphaned a minute ago") // a write in progress looks like this
	stale := f.blob(t, "orphaned two days ago")
	backdate(t, f, stale, 48*time.Hour)

	res, err := CollectBlobs(context.Background(), f.conn, f.store, time.Now())
	if err != nil {
		t.Fatalf("CollectBlobs: %v", err)
	}
	if res.Orphans != 1 {
		t.Errorf("removed %d orphans, want 1", res.Orphans)
	}
	if f.onDisk(stale) || f.recorded(t, stale) {
		t.Error("the expired orphan survived")
	}
	for name, id := range map[string]string{"live": live, "fresh": fresh} {
		if !f.onDisk(id) || !f.recorded(t, id) {
			t.Errorf("the %s blob was removed", name)
		}
	}
}

// orphaned_at is maintained by triggers, but if it were ever wrong on a blob a
// file still uses, the collector must not take the file's content with it.
func TestCollectNeverRemovesReferencedBlobs(t *testing.T) {
	f := newFixture(t)
	id := f.blob(t, "in use")
	f.file(t, "live", id)
	backdate(t, f, id, 48*time.Hour) // wrong on purpose

	if _, err := CollectBlobs(context.Background(), f.conn, f.store, time.Now()); err != nil {
		t.Fatalf("CollectBlobs: %v", err)
	}
	if !f.onDisk(id) || !f.recorded(t, id) {
		t.Fatal("a blob a file uses was removed because its orphaned_at said so")
	}
}

// Files on disk with no record are what a crash between storing a blob and
// recording it leaves, and what a restore leaves of the data it replaced. They
// go once they are older than the grace period, and not before: a write in
// progress is exactly a young file with no record yet.
func TestCollectRemovesOldUnrecordedFiles(t *testing.T) {
	f := newFixture(t)
	young, _ := f.store.Put([]byte("being written"))
	old, _ := f.store.Put([]byte("left by a crash"))
	recorded := f.blob(t, "recorded, orphaned just now")
	twoDaysAgo := time.Now().Add(-48 * time.Hour)
	for _, id := range []string{old.ID, recorded} {
		if err := os.Chtimes(filepath.Join(f.store.Dir(), id[:2], id), twoDaysAgo, twoDaysAgo); err != nil {
			t.Fatal(err)
		}
	}
	tmp := filepath.Join(f.store.TempDir(), "abandoned")
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(tmp, twoDaysAgo, twoDaysAgo); err != nil {
		t.Fatal(err)
	}

	res, err := CollectBlobs(context.Background(), f.conn, f.store, time.Now())
	if err != nil {
		t.Fatalf("CollectBlobs: %v", err)
	}
	if res.Strays != 1 || res.TempLeft != 1 {
		t.Errorf("result = %+v, want 1 stray and 1 temp file", res)
	}
	if f.onDisk(old.ID) {
		t.Error("an old unrecorded file survived")
	}
	if !f.onDisk(young.ID) {
		t.Error("a young unrecorded file -- a write in progress -- was removed")
	}
	// Old on disk but recorded, and orphaned less than the grace period ago:
	// its record decides, not its file's age.
	if !f.onDisk(recorded) {
		t.Error("a recorded blob was removed by the directory sweep")
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Error("an abandoned temp file survived")
	}
}

// The whole cycle: an edit's old version, a hard-deleted file's content, both
// gone a grace period later; the trash keeps its content.
func TestCollectAfterEditsAndDeletes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	v1 := f.blob(t, "v1")
	f.file(t, "edited", v1)
	v2 := f.blob(t, "v2")
	if _, err := f.q.UpdateFile(ctx, sqlcgen.UpdateFileParams{
		Name: "edited", NewSlug: "edited", BlobID: v2, AccessCode: "c", Slug: "edited", UserID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	trashed := f.blob(t, "in the trash")
	f.file(t, "trashed", trashed)
	gone := f.blob(t, "deleted for good")
	f.file(t, "gone", gone)
	for _, slug := range []string{"trashed", "gone"} {
		if _, err := f.q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{Slug: slug, UserID: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.q.HardDeleteFile(ctx, sqlcgen.HardDeleteFileParams{Slug: "gone", UserID: 1}); err != nil {
		t.Fatal(err)
	}

	// Not yet: within the grace period.
	if res, _ := CollectBlobs(ctx, f.conn, f.store, time.Now()); res.Orphans != 0 {
		t.Fatalf("collected %d blobs within the grace period", res.Orphans)
	}
	// A day and a bit later.
	if _, err := CollectBlobs(ctx, f.conn, f.store, time.Now().Add(BlobGrace+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f.onDisk(v1) || f.onDisk(gone) {
		t.Error("an edit's old version or a hard-deleted file's content survived the grace period")
	}
	if !f.onDisk(v2) || !f.onDisk(trashed) {
		t.Error("current content or the trash's content was collected")
	}
}
