package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// storeContent writes data to the blob store and records the blob, which
// starts out orphaned: the files write the caller makes next adopts it. Every
// path that stores content goes through here, after its quota check and before
// its files write, so a failure anywhere in between leaves only an orphan the
// collector removes.
func storeContent(ctx context.Context, queries *sqlcgen.Queries, store *blobstore.Store, data []byte) (string, error) {
	b, err := store.Put(data)
	if err != nil {
		return "", fmt.Errorf("store content: %w", err)
	}
	if err := queries.InsertBlob(ctx, sqlcgen.InsertBlobParams{ID: b.ID, Size: b.Size, Sha256: b.SHA256}); err != nil {
		// The file is on disk with no record; the collector's sweep of the
		// directory removes it.
		return "", fmt.Errorf("record blob: %w", err)
	}
	return b.ID, nil
}

// contentBlob is storeContent for an edit: when data is byte for byte what the
// file already holds (oldSHA256 is its blob's checksum), it keeps the existing
// blob instead of writing an identical copy. The dashboard's editor sends the
// whole document with every save, rename-only saves included.
func contentBlob(ctx context.Context, queries *sqlcgen.Queries, store *blobstore.Store, data []byte, oldBlobID, oldSHA256 string) (string, error) {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) == oldSHA256 {
		return oldBlobID, nil
	}
	return storeContent(ctx, queries, store, data)
}

// readContent returns the stored source of a file: a document's text, or a
// site's zip.
func readContent(store *blobstore.Store, file sqlcgen.File) ([]byte, error) {
	data, err := store.Read(file.BlobID)
	if err != nil {
		return nil, fmt.Errorf("read content of %s: %w", file.Slug, err)
	}
	return data, nil
}
