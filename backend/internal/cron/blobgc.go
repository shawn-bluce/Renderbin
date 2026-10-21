// Package cron holds the server's periodic background work. There is one job:
// collecting blobs no file uses any more.
package cron

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// BlobGrace is how long garbage is kept before it is removed: a blob orphaned
// (replaced by an edit, or its file deleted) less than this long ago, or a
// file in the store this recently written, is left alone.
//
// It bounds two things in opposite directions. It is the longest a backup may
// take: a backup copies the blobs its snapshot references after taking the
// snapshot, and those must still exist even if they were let go of a moment
// later. And it is the longest deleted content stays on disk -- emptying the
// trash or deleting an account removes the rows at once and the bytes within
// this window.
const BlobGrace = 24 * time.Hour

// blobGCEvery is how often the collector runs, and blobGCDelay how long after
// start it first does: a restart (an upgrade) should be about serving, not
// about scanning the store.
const (
	blobGCEvery = time.Hour
	blobGCDelay = 5 * time.Minute
)

// BlobGCResult reports one collection.
type BlobGCResult struct {
	Orphans  int // recorded blobs past their grace period, removed
	Strays   int // files in the store with no record, removed
	TempLeft int // abandoned temporary files, removed
}

// CollectBlobs removes the blobs nothing uses any more, as of now. Safe to run
// beside live traffic, backups and restores; see the steps below for why.
func CollectBlobs(ctx context.Context, conn *sql.DB, store *blobstore.Store, now time.Time) (BlobGCResult, error) {
	var res BlobGCResult
	cutoff := now.Add(-BlobGrace)
	queries := sqlcgen.New(conn)

	// 1. Recorded orphans. The statement deletes the records and returns their
	//    IDs in one step, so a blob is claimed exactly once; its file goes
	//    after. Nothing can adopt an orphan this old: every write stores a new
	//    blob under a new ID (see internal/blobstore), and a restore does too.
	ids, err := queries.DeleteOrphanedBlobs(ctx, cutoff.Unix())
	if err != nil {
		return res, fmt.Errorf("claim orphaned blobs: %w", err)
	}
	for _, id := range ids {
		if err := store.Remove(id); err != nil {
			// Its record is gone, so step 2 retries it on a later run.
			return res, fmt.Errorf("remove blob %s: %w", id, err)
		}
		res.Orphans++
	}

	// 2. Files with no record: a write that died between storing its blob
	//    and recording it, a failed restore, the data a restore replaced. The
	//    age check is what makes this safe against a write in progress, which
	//    has stored its blob but not yet recorded it -- it is never older than
	//    the grace period. The record list is read before the walk, so a blob
	//    recorded since is simply too young to qualify.
	recorded, err := queries.ListBlobIDs(ctx)
	if err != nil {
		return res, fmt.Errorf("list blobs: %w", err)
	}
	known := make(map[string]struct{}, len(recorded))
	for _, id := range recorded {
		known[id] = struct{}{}
	}
	err = store.Walk(func(id string, info fs.FileInfo) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := known[id]; ok || !info.ModTime().Before(cutoff) {
			return nil
		}
		if err := store.Remove(id); err != nil {
			return fmt.Errorf("remove stray blob %s: %w", id, err)
		}
		res.Strays++
		return nil
	})
	if err != nil {
		return res, err
	}

	// 3. Temporary files left by a crash mid-write, mid-backup or mid-restore.
	res.TempLeft, err = store.SweepTemp(cutoff)
	if err != nil {
		return res, fmt.Errorf("sweep temp: %w", err)
	}
	return res, nil
}

// RunBlobGC collects blobs every hour until ctx is done. A failed run is
// logged and retried on the next tick: nothing is lost by collecting late.
func RunBlobGC(ctx context.Context, conn *sql.DB, store *blobstore.Store, logger *slog.Logger) {
	timer := time.NewTimer(blobGCDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		started := time.Now()
		res, err := CollectBlobs(ctx, conn, store, started)
		switch {
		case err != nil && ctx.Err() == nil:
			logger.Error("blob collection", "error", err)
		case res.Orphans+res.Strays+res.TempLeft > 0:
			logger.Info("blob collection", "orphans", res.Orphans, "strays", res.Strays,
				"temp", res.TempLeft, "took", time.Since(started).Round(time.Millisecond).String())
		}
		timer.Reset(blobGCEvery)
	}
}
