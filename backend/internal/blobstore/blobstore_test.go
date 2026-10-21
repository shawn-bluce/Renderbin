package blobstore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestPutReadRoundTrip(t *testing.T) {
	s := newStore(t)
	data := []byte("<h1>hello</h1>")

	b, err := s.Put(data)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !ValidID(b.ID) {
		t.Fatalf("Put returned malformed id %q", b.ID)
	}
	sum := sha256.Sum256(data)
	if b.Size != int64(len(data)) || b.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("Put = %+v, want size %d and the content's sha256", b, len(data))
	}

	got, err := s.Read(b.ID)
	if err != nil || string(got) != string(data) {
		t.Fatalf("Read = %q, %v; want %q", got, err, data)
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), b.ID[:2], b.ID)); err != nil {
		t.Fatalf("blob not at its bucketed path: %v", err)
	}

	// Nothing is left behind in tmp/ by a successful write.
	if entries, _ := os.ReadDir(s.TempDir()); len(entries) != 0 {
		t.Fatalf("tmp/ holds %d entries after Put", len(entries))
	}
}

// Every write is a new blob, even of identical bytes: an edit must never land
// on a blob some other row (or the collector) already has an opinion about.
func TestPutNeverReusesAnID(t *testing.T) {
	s := newStore(t)
	a, _ := s.Put([]byte("same"))
	b, _ := s.Put([]byte("same"))
	if a.ID == b.ID {
		t.Fatalf("two writes of the same bytes share id %s", a.ID)
	}
}

func TestEmptyBlob(t *testing.T) {
	s := newStore(t)
	b, err := s.Put(nil)
	if err != nil || b.Size != 0 {
		t.Fatalf("Put(nil) = %+v, %v", b, err)
	}
	got, err := s.Read(b.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("Read = %q, %v", got, err)
	}
}

func TestMissingAndInvalidIDs(t *testing.T) {
	s := newStore(t)
	if _, err := s.Read(strings.Repeat("a", 32)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Read(missing) = %v, want ErrNotFound", err)
	}
	for _, id := range []string{"", "../../etc/passwd", strings.Repeat("A", 32), strings.Repeat("a", 31), strings.Repeat("g", 32)} {
		if _, err := s.Open(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Open(%q) = %v, want ErrInvalidID", id, err)
		}
		if err := s.Remove(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Remove(%q) = %v, want ErrInvalidID", id, err)
		}
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	s := newStore(t)
	b, _ := s.Put([]byte("x"))
	if err := s.Remove(b.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := s.Remove(b.ID); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if _, err := s.Read(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read after Remove = %v, want ErrNotFound", err)
	}
}

// Walk reports blobs and nothing else: the collector deletes what it is given,
// so a stray file somebody dropped into the directory must not be on the list.
func TestWalkSkipsWhatIsNotABlob(t *testing.T) {
	s := newStore(t)
	a, _ := s.Put([]byte("a"))
	b, _ := s.Put([]byte("b"))

	strays := []string{
		filepath.Join(s.Dir(), "README"),
		filepath.Join(s.Dir(), "zz", strings.Repeat("a", 32)),                // not a hex bucket
		filepath.Join(s.Dir(), a.ID[:2], "notes.txt"),                        // not an id
		filepath.Join(s.Dir(), a.ID[:2], swapPrefix(a.ID)),                   // id in the wrong bucket
		filepath.Join(s.TempDir(), strings.Repeat("c", 32)),                  // in-progress write
		filepath.Join(s.Dir(), a.ID[:2], strings.ToUpper(a.ID[:2])+a.ID[2:]), // uppercase
	}
	for _, p := range strays {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stray"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	if err := s.Walk(func(id string, _ fs.FileInfo) error {
		seen[id] = true
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(seen) != 2 || !seen[a.ID] || !seen[b.ID] {
		t.Fatalf("Walk saw %v, want exactly %s and %s", seen, a.ID, b.ID)
	}
}

// swapPrefix returns a well-formed id whose first two characters differ from
// id's, i.e. one that does not belong in id's bucket.
func swapPrefix(id string) string {
	if id[:2] == "00" {
		return "11" + id[2:]
	}
	return "00" + id[2:]
}

func TestSweepTempRemovesOnlyStaleEntries(t *testing.T) {
	s := newStore(t)
	stale := filepath.Join(s.TempDir(), "stale")
	fresh := filepath.Join(s.TempDir(), "fresh")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	n, err := s.SweepTemp(time.Now().Add(-24 * time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("SweepTemp = %d, %v; want 1", n, err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stale temp file survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh temp file was removed: %v", err)
	}
}

func TestFreeBytes(t *testing.T) {
	s := newStore(t)
	if free, ok := s.FreeBytes(); ok && free == 0 {
		t.Fatalf("FreeBytes = 0 on a writable temp dir")
	}
}
