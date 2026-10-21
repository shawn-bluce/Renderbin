// Package blobstore keeps file content on disk, next to the database, so that
// the database itself only holds metadata.
//
// It is pure and DB-free, like internal/auth and internal/site: it knows how to
// write, read and remove immutable blobs, and nothing about who references
// them. The lifecycle -- which blob a file row points at, when one becomes
// garbage -- lives in SQL (the blobs table and its triggers on files), and the
// collector that acts on it lives in internal/cron.
//
// Three properties are load-bearing:
//
//   - A blob is written once and never modified. An edit writes a new blob and
//     points the row at it; the old one becomes garbage. Nothing ever has to
//     coordinate a reader with a writer of the same bytes.
//   - IDs are random, not content hashes. Content addressing would let a new
//     write land on a blob the collector has just decided nobody uses, which
//     needs a lock to get right; with fresh random IDs a new write can never
//     be the same blob as an old orphan, so there is nothing to race.
//   - Put returns only after the bytes are durable under their final name, so
//     a row that names a blob can never be committed before the blob exists.
//     The worst a crash can leave behind is an unreferenced blob, which the
//     collector removes.
//
// IDs are generated here and validated at every entry point, and paths are
// built only from validated IDs: no caller-supplied string ever reaches the
// filesystem, so there is no path traversal to defend against.
package blobstore

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// ErrNotFound is returned for a well-formed ID with no blob on disk. For a
// blob a files row references, that is data loss, not bad input.
var ErrNotFound = errors.New("blob not found")

// ErrInvalidID is returned for anything that is not a 32-character lowercase
// hex string.
var ErrInvalidID = errors.New("invalid blob id")

const (
	idBytes  = 16
	idLen    = 2 * idBytes
	tempName = "tmp"
)

// Blob describes one stored blob.
type Blob struct {
	ID     string
	Size   int64
	SHA256 string // lowercase hex
}

// Store is a directory of blobs, fanned out over 256 subdirectories by the
// first two characters of the ID, plus a tmp/ directory for writes in progress
// (same filesystem, so the final rename is atomic).
type Store struct {
	dir string
}

// Open prepares dir (and its tmp/) and returns a Store over it.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, tempName), 0o755); err != nil {
		return nil, fmt.Errorf("create blob dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// Dir is the store's root directory.
func (s *Store) Dir() string { return s.dir }

// TempDir is where writes in progress live. Other large temporary files (a
// backup's database snapshot, an uploaded restore) belong here too: it is on
// the data volume, whose free space is what the callers check, rather than on
// the container's own filesystem, and the collector removes anything left in
// it by a crash.
func (s *Store) TempDir() string { return filepath.Join(s.dir, tempName) }

// ValidID reports whether id is a well-formed blob ID.
func ValidID(id string) bool {
	return len(id) == idLen && isLowerHex(id)
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func newID() (string, error) {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate blob id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) bucket(id string) string { return filepath.Join(s.dir, id[:2]) }

func (s *Store) path(id string) string { return filepath.Join(s.bucket(id), id) }

// Put stores data as a new blob.
func (s *Store) Put(data []byte) (Blob, error) {
	return s.PutReader(bytes.NewReader(data))
}

// PutReader stores everything r yields as a new blob. The caller bounds r;
// the store does not impose a size limit of its own.
func (s *Store) PutReader(r io.Reader) (Blob, error) {
	id, err := newID()
	if err != nil {
		return Blob{}, err
	}
	tmp, err := os.CreateTemp(s.TempDir(), id+"-*")
	if err != nil {
		return Blob{}, fmt.Errorf("create blob temp file: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return Blob{}, fmt.Errorf("write blob: %w", err)
	}
	// Durable before it gets its final name: once a row names this blob, a
	// power cut must not leave an empty file behind that name.
	if err := tmp.Sync(); err != nil {
		return Blob{}, fmt.Errorf("sync blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Blob{}, fmt.Errorf("close blob: %w", err)
	}

	bucket := s.bucket(id)
	_, statErr := os.Stat(bucket)
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		return Blob{}, fmt.Errorf("create blob bucket: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path(id)); err != nil {
		return Blob{}, fmt.Errorf("move blob into place: %w", err)
	}
	committed = true
	// The rename is only durable once the directory holding the new name is;
	// a bucket created just now also needs its own entry in the root synced.
	if err := syncDir(bucket); err != nil {
		return Blob{}, err
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		if err := syncDir(s.dir); err != nil {
			return Blob{}, err
		}
	}
	return Blob{ID: id, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir for sync: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync dir: %w", err)
	}
	return nil
}

// Open opens a blob for reading. The caller closes it.
func (s *Store) Open(id string) (*os.File, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	f, err := os.Open(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return f, err
}

// Read returns a blob's whole content.
func (s *Store) Read(id string) ([]byte, error) {
	f, err := s.Open(id)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Remove deletes a blob. A blob that is already gone is not an error: removal
// is idempotent so the collector can simply retry.
func (s *Store) Remove(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Walk calls fn for every blob on disk. Anything in the store directory that
// is not a well-formed blob in its right bucket is skipped, never reported:
// Walk feeds the collector, and a stray file is not something to delete.
func (s *Store) Walk(fn func(id string, info fs.FileInfo) error) error {
	buckets, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, b := range buckets {
		if !b.IsDir() || len(b.Name()) != 2 || !isLowerHex(b.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.dir, b.Name()))
		if err != nil {
			return err
		}
		for _, e := range entries {
			id := e.Name()
			if e.IsDir() || !ValidID(id) || id[:2] != b.Name() {
				continue
			}
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed since the directory was read
			}
			if err != nil {
				return err
			}
			if err := fn(id, info); err != nil {
				return err
			}
		}
	}
	return nil
}

// SweepTemp removes everything in tmp/ last modified before cutoff: writes a
// crash interrupted, and temporary files a backup or restore did not get to
// clean up. Returns how many entries it removed.
func (s *Store) SweepTemp(cutoff time.Time) (int, error) {
	entries, err := os.ReadDir(s.TempDir())
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.TempDir(), e.Name())); err == nil {
			removed++
		}
	}
	return removed, nil
}

// FreeBytes reports the space available to this process on the store's
// volume. ok is false where the platform gives no answer, and callers then
// skip their check rather than refuse to work.
func (s *Store) FreeBytes() (free uint64, ok bool) {
	return FreeBytesAt(s.dir)
}

// FreeBytesAt is FreeBytes for a path that may not exist yet (a store that
// has not been created): it measures the nearest ancestor that does.
func FreeBytesAt(path string) (free uint64, ok bool) {
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, false
		}
		path = parent
	}
	free, err := freeBytes(path)
	if err != nil {
		return 0, false
	}
	return free, true
}
