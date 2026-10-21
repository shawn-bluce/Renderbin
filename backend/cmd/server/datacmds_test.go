package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// addFile stores a file the way the handlers do: blob first, then its row.
func addFile(t *testing.T, q *sqlcgen.Queries, store *blobstore.Store, slug, content string) sqlcgen.File {
	t.Helper()
	ctx := context.Background()
	b, err := store.Put([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if err := q.InsertBlob(ctx, sqlcgen.InsertBlobParams{ID: b.ID, Size: b.Size, Sha256: b.SHA256}); err != nil {
		t.Fatal(err)
	}
	f, err := q.CreateFile(ctx, sqlcgen.CreateFileParams{
		Slug: slug, Name: slug, BlobID: b.ID, Kind: "html", AccessCode: "c", UserID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func liveSlugs(t *testing.T, q *sqlcgen.Queries) string {
	t.Helper()
	files, err := q.ListUserFiles(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		out = append(out, f.Slug)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestBackupAndRestoreCommands(t *testing.T) {
	q, store := seedData(t)
	addFile(t, q, store, "kept", "<p>kept</p>")
	out := filepath.Join(t.TempDir(), "backup.tar")

	code, _, stdout, stderr := run(t, "", "backup", "--out="+out)
	if code != 0 {
		t.Fatalf("backup exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, out) {
		t.Errorf("backup output %q does not say where it wrote", stdout)
	}
	if _, err := os.Stat(out + ".partial"); !os.IsNotExist(err) {
		t.Errorf("the .partial file was left behind: %v", err)
	}

	addFile(t, q, store, "later", "<p>after the backup</p>")

	// Without --yes nothing happens.
	if code, _, stdout, _ := run(t, "", "restore", "--in="+out); code != 0 || !strings.Contains(stdout, "--yes") {
		t.Fatalf("restore without --yes = %d, %q", code, stdout)
	}
	if got := liveSlugs(t, q); got != "kept,later" {
		t.Fatalf("files after a restore without --yes = %s, want both", got)
	}

	if code, _, stdout, stderr := run(t, "", "restore", "--in="+out, "--yes"); code != 0 {
		t.Fatalf("restore exited %d: %s %s", code, stdout, stderr)
	}
	if got := liveSlugs(t, q); got != "kept" {
		t.Errorf("files after restore = %s, want the backup's", got)
	}
}

func TestFsckReportsDamage(t *testing.T) {
	q, store := seedData(t)
	addFile(t, q, store, "fine", "<p>fine</p>")
	missing := addFile(t, q, store, "missing", "<p>will vanish</p>")
	corrupt := addFile(t, q, store, "corrupt", "<p>will rot</p>")

	if code, _, stdout, stderr := run(t, "", "fsck"); code != 0 {
		t.Fatalf("fsck on intact data exited %d: %s %s", code, stdout, stderr)
	}

	if err := store.Remove(missing.BlobID); err != nil {
		t.Fatal(err)
	}
	rotted := []byte("<p>will ROT</p>") // same size, different bytes
	if err := os.WriteFile(filepath.Join(store.Dir(), corrupt.BlobID[:2], corrupt.BlobID), rotted, 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, stdout, _ := run(t, "", "fsck")
	if code != 1 {
		t.Errorf("fsck on damaged data exited %d, want 1", code)
	}
	for _, want := range []string{"/res/missing", "missing from disk", "/res/corrupt", "checksum", "2 damaged"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("fsck output lacks %q:\n%s", want, stdout)
		}
	}
	// --quick checks presence and size only, so the rot goes unnoticed.
	_, _, quick, _ := run(t, "", "fsck", "--quick")
	if !strings.Contains(quick, "1 damaged") {
		t.Errorf("fsck --quick output:\n%s\nwant only the missing file reported", quick)
	}
}

func TestMigrateCheckOnACurrentDatabase(t *testing.T) {
	seedData(t)
	code, _, stdout, stderr := run(t, "", "migrate", "--check")
	if code != 0 || !strings.Contains(stdout, "up to date") {
		t.Errorf("migrate --check = %d, %q %q", code, stdout, stderr)
	}
	if code, _, _, _ := run(t, "", "migrate"); code == 0 {
		t.Error("migrate without --check succeeded; migrations only run on server start")
	}
}
