package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"

	"github.com/shawn-bluce/renderbin/backend/internal/backup"
	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// The data subcommands: backup, restore, fsck and migrate --check. They exist
// because the database stopped being the whole of an instance's state -- the
// content lives in a blob directory beside it -- and because an instance big
// enough to care is one whose backup should not depend on a browser download
// surviving several gigabytes. Each is safe to run beside a live server, for
// the reasons given on each.

const backupUsage = `server backup --out=FILE

Writes a complete backup (the database plus every file's content, as one .tar)
to FILE, or to stdout with --out=-. Safe while the server is running. The same
format the settings page downloads; restore it with "server restore" or there.

  docker compose exec app ./server backup --out=/data/backup.tar
`

const restoreUsage = `server restore --in=FILE --yes

Replaces every account and every file with the contents of a backup: a .tar
from "server backup" or the settings page, or a .db downloaded from a version
that kept content inside the database. Without --yes it only checks that FILE
exists and explains what would happen. Safe while the server is running; every
signed-in session ends, since the backup's sessions replace the live ones.
`

const fsckUsage = `server fsck [--quick]

Checks that every file's content is on disk and intact: present, the size the
database records, and (unless --quick) the checksum it records. Reports what is
waiting for the hourly cleanup. Read-only; exits 1 if anything is damaged.
`

const migrateUsage = `server migrate --check

Reports what starting this version would do to the database, without doing it:
the migrations still to apply, and how much file content would move out of the
database into the blob directory -- which happens before the server starts
listening, so it is the downtime of the upgrade -- against the free space it
needs. Run it with the new binary before upgrading; with Docker, after pulling
the new image and before "up -d":

  docker compose pull && docker compose run --rm app migrate --check
`

func backupCmd(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("backup", backupUsage, stderr)
	out := flags.String("out", "", "file to write, or - for stdout (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		flags.Usage()
		return errors.New("--out is required")
	}

	conn, store, err := openData()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *out == "-" {
		return backup.Write(ctx, conn, store, stdout)
	}
	// Written under a temporary name and renamed when complete, so an
	// interrupted backup never leaves a file that looks like a good one.
	partial := *out + ".partial"
	f, err := os.Create(partial)
	if err != nil {
		return err
	}
	if err := backup.Write(ctx, conn, store, f); err != nil {
		f.Close()
		os.Remove(partial)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(partial, *out); err != nil {
		return err
	}
	info, err := os.Stat(*out)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "backup written to %s (%s)\n", *out, db.HumanBytes(uint64(info.Size())))
	return nil
}

func restoreCmd(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("restore", restoreUsage, stderr)
	in := flags.String("in", "", "backup file to restore (required)")
	yes := flags.Bool("yes", false, "really replace all current data")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		flags.Usage()
		return errors.New("--in is required")
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !*yes {
		dbPath, blobDir := dataPaths()
		fmt.Fprintf(stdout, "This replaces every account and every file in %s (content in %s)\nwith the contents of %s (%s). Anything created since that backup is lost.\nRun again with --yes to proceed.\n",
			dbPath, blobDir, *in, db.HumanBytes(uint64(info.Size())))
		return nil
	}

	conn, store, err := openData()
	if err != nil {
		return err
	}
	defer conn.Close()
	if free, ok := store.FreeBytes(); ok && uint64(info.Size())+64<<20 > free {
		return fmt.Errorf("not enough free space in %s: the backup is %s and %s is available",
			store.Dir(), db.HumanBytes(uint64(info.Size())), db.HumanBytes(free))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stats, err := backup.Restore(ctx, conn, store, f)
	if err != nil {
		return fmt.Errorf("%w (nothing was changed)", err)
	}
	fmt.Fprintf(stdout, "restored %d account(s) and %d file(s)\n", stats.Users, stats.Files)
	return nil
}

func fsckCmd(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("fsck", fsckUsage, stderr)
	quick := flags.Bool("quick", false, "skip the checksums (presence and size only)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	conn, store, err := openData()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	q := sqlcgen.New(conn)

	files, err := q.ListFileBlobs(ctx)
	if err != nil {
		return err
	}
	damaged := 0
	report := func(f sqlcgen.ListFileBlobsRow, format string, a ...any) {
		damaged++
		fmt.Fprintf(stdout, "DAMAGED file %d (/res/%s): %s\n", f.FileID, f.Slug, fmt.Sprintf(format, a...))
	}
	for _, f := range files {
		if !f.BlobSize.Valid {
			report(f, "its content (blob %s) has no record", f.BlobID)
			continue
		}
		if f.ContentSize != f.BlobSize.Int64 {
			report(f, "content_size %d disagrees with its blob's %d", f.ContentSize, f.BlobSize.Int64)
		}
		problem, err := checkBlob(store.Open, f.BlobID, f.BlobSize.Int64, f.BlobSha256.String, *quick)
		if err != nil {
			return err
		}
		if problem != "" {
			report(f, "%s", problem)
		}
	}

	orphans, err := q.OrphanedBlobStats(ctx)
	if err != nil {
		return err
	}
	recorded, err := q.ListBlobIDs(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]bool, len(recorded))
	for _, id := range recorded {
		known[id] = true
	}
	var strays, strayBytes int64
	if err := store.Walk(func(id string, info fs.FileInfo) error {
		if !known[id] {
			strays++
			strayBytes += info.Size()
		}
		return nil
	}); err != nil {
		return err
	}

	checked := "checked"
	if *quick {
		checked = "checked without checksums"
	}
	fmt.Fprintf(stdout, "%d file(s) %s, %d damaged\n", len(files), checked, damaged)
	fmt.Fprintf(stdout, "awaiting cleanup: %d orphaned blob(s), %s; %d unrecorded file(s), %s\n",
		orphans.Blobs, db.HumanBytes(uint64(orphans.Bytes)), strays, db.HumanBytes(uint64(strayBytes)))
	if damaged > 0 {
		return fmt.Errorf("%d file(s) damaged", damaged)
	}
	return nil
}

// checkBlob returns what is wrong with one blob, or "" when it is intact. An
// error is reserved for failing to check at all.
func checkBlob(open func(string) (*os.File, error), id string, size int64, sum string, quick bool) (string, error) {
	f, err := open(id)
	if errors.Is(err, blobstore.ErrNotFound) {
		return fmt.Sprintf("its content (blob %s) is missing from disk", id), nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() != size {
		return fmt.Sprintf("blob %s is %d bytes on disk, recorded as %d", id, info.Size(), size), nil
	}
	if quick {
		return "", nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != sum {
		return fmt.Sprintf("blob %s does not match its checksum", id), nil
	}
	return "", nil
}

func migrateCmd(args []string, stdout, stderr io.Writer) error {
	flags := newFlagSet("migrate", migrateUsage, stderr)
	check := flags.Bool("check", false, "report what would happen, change nothing")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*check {
		flags.Usage()
		return errors.New("only --check is supported: migrations run when the server starts")
	}
	dbPath, blobDir := dataPaths()
	plan, err := db.Inspect(dbPath, blobDir)
	if err != nil {
		return err
	}
	if len(plan.Pending) == 0 {
		fmt.Fprintf(stdout, "%s is up to date; starting this version changes nothing.\n", dbPath)
		return nil
	}
	fmt.Fprintf(stdout, "Starting this version applies %d migration(s) to %s:\n  %s\n",
		len(plan.Pending), dbPath, strings.Join(plan.Pending, "\n  "))
	if plan.MoveFiles == 0 {
		return nil
	}
	fmt.Fprintf(stdout, "\nIt moves the content of %d file(s), %s, into %s before it starts listening.\n",
		plan.MoveFiles, db.HumanBytes(uint64(plan.MoveBytes)), blobDir)
	fmt.Fprintf(stdout, "That needs about %s free there", db.HumanBytes(plan.Need))
	switch {
	case !plan.FreeKnown:
		fmt.Fprintf(stdout, " (free space could not be measured here).\n")
	case plan.Free < plan.Need:
		fmt.Fprintf(stdout, ", and only %s is: the server would refuse to start. Free some space first.\n", db.HumanBytes(plan.Free))
		return errors.New("not enough free disk space for the upgrade")
	default:
		fmt.Fprintf(stdout, "; %s is available.\n", db.HumanBytes(plan.Free))
	}
	return nil
}

func newFlagSet(name, usage string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, usage) }
	return flags
}
