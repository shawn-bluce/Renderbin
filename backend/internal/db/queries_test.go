package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// testStore is an empty blob store in the test's temp dir.
func testStore(t *testing.T) *blobstore.Store {
	t.Helper()
	store, err := blobstore.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("blobstore.Open: %v", err)
	}
	return store
}

// openTest opens a fresh database in the test's temp dir.
func openTest(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := Open(filepath.Join(t.TempDir(), "app.db"), testStore(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func newQueries(t *testing.T) *sqlcgen.Queries {
	t.Helper()
	return sqlcgen.New(openTest(t))
}

// newBlob records a blob holding content and returns its id. These tests
// exercise SQL, so the bytes never need to exist on disk: the record is what
// the files statements and triggers look at.
func newBlob(t *testing.T, q *sqlcgen.Queries, content string) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(content))
	if err := q.InsertBlob(context.Background(), sqlcgen.InsertBlobParams{
		ID: id, Size: int64(len(content)), Sha256: hex.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatalf("InsertBlob: %v", err)
	}
	return id
}

// ensureUser returns the named user, creating it on first use. Tests that only
// care about files still need real owner rows: GetFileBySlugAnyOwner joins
// users to hide a suspended owner's files, so a file whose user_id points at
// nothing is invisible on the public render path.
func ensureUser(t *testing.T, q *sqlcgen.Queries, username string) sqlcgen.User {
	t.Helper()
	ctx := context.Background()
	if u, err := q.GetUserByUsername(ctx, username); err == nil {
		return u
	}
	u, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username:     username,
		Nickname:     username,
		PasswordHash: "hash-" + username,
	})
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", username, err)
	}
	return u
}

// createFile inserts a file owned by user 1, seeding that user if needed. The
// id assertion catches a test that created some other user first: ids are
// handed out in insertion order, so that would silently attribute these files
// to the wrong account and make an isolation assertion pass vacuously.
func createFile(t *testing.T, q *sqlcgen.Queries, slug string) sqlcgen.File {
	t.Helper()
	if owner := ensureUser(t, q, "owner"); owner.ID != 1 {
		t.Fatalf("createFile's owner has id %d, not 1 -- create it before any other user", owner.ID)
	}
	f, err := q.CreateFile(context.Background(), sqlcgen.CreateFileParams{
		Slug:       slug,
		Name:       "name-" + slug,
		BlobID:     newBlob(t, q, "<p>"+slug+"</p>"),
		Kind:       "html",
		IsPublic:   false,
		AccessCode: "code-" + slug,
		UserID:     1,
	})
	if err != nil {
		t.Fatalf("CreateFile(%q): %v", slug, err)
	}
	return f
}

func TestGetFileBySlugAnyOwnerFiltersDeleted(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a")

	got, err := q.GetFileBySlugAnyOwner(ctx, "a")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if got.Slug != "a" {
		t.Errorf("slug = %q, want %q", got.Slug, "a")
	}

	if _, err := q.GetFileBySlugAnyOwner(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetFileBySlugAnyOwner(missing) err = %v, want ErrNoRows", err)
	}

	if _, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{
		Slug: "a", UserID: 1,
	}); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}
	if _, err := q.GetFileBySlugAnyOwner(ctx, "a"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetFileBySlugAnyOwner after delete err = %v, want ErrNoRows", err)
	}
}

func TestListFilesVsDeleted(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "keep")
	createFile(t, q, "trash")
	if _, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{
		Slug: "trash", UserID: 1,
	}); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}

	active, err := q.ListUserFiles(ctx, 1)
	if err != nil {
		t.Fatalf("ListUserFiles: %v", err)
	}
	if len(active) != 1 || active[0].Slug != "keep" {
		t.Errorf("ListUserFiles = %v, want only [keep]", slugsOf(active))
	}

	deleted, err := q.ListUserDeletedFiles(ctx, 1)
	if err != nil {
		t.Fatalf("ListUserDeletedFiles: %v", err)
	}
	if len(deleted) != 1 || deleted[0].Slug != "trash" {
		t.Errorf("ListUserDeletedFiles = %v, want only [trash]", slugsOf(deleted))
	}
}

func TestRestoreFile(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a")
	if _, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{
		Slug: "a", UserID: 1,
	}); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}

	restored, err := q.RestoreFile(ctx, sqlcgen.RestoreFileParams{Slug: "a", UserID: 1})
	if err != nil {
		t.Fatalf("RestoreFile: %v", err)
	}
	if restored.DeletedAt.Valid {
		t.Error("restored file should have a null deleted_at")
	}
	if _, err := q.GetFileBySlugAnyOwner(ctx, "a"); err != nil {
		t.Errorf("restored file not visible: %v", err)
	}

	// Restoring a file that isn't deleted matches no row.
	if _, err := q.RestoreFile(ctx, sqlcgen.RestoreFileParams{Slug: "a", UserID: 1}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("RestoreFile on active file err = %v, want ErrNoRows", err)
	}
}

func TestUpdateFileChangesSlugNameContent(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "old")
	v2 := newBlob(t, q, "<h1>v2</h1>")

	updated, err := q.UpdateFile(ctx, sqlcgen.UpdateFileParams{
		Name:       "new name",
		NewSlug:    "new-slug",
		BlobID:     v2,
		AccessCode: "code-new",
		Slug:       "old", // WHERE (old) slug
		UserID:     1,
	})
	if err != nil {
		t.Fatalf("UpdateFile: %v", err)
	}
	if updated.Slug != "new-slug" || updated.Name != "new name" || updated.BlobID != v2 ||
		updated.ContentSize != int64(len("<h1>v2</h1>")) {
		t.Errorf("UpdateFile result = %+v", updated)
	}
	if _, err := q.GetFileBySlugAnyOwner(ctx, "old"); !errors.Is(err, sql.ErrNoRows) {
		t.Error("old slug should no longer resolve")
	}
	if _, err := q.GetFileBySlugAnyOwner(ctx, "new-slug"); err != nil {
		t.Errorf("new slug should resolve: %v", err)
	}
}

func TestUpdateFileDuplicateSlugFails(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a")
	createFile(t, q, "b")

	_, err := q.UpdateFile(ctx, sqlcgen.UpdateFileParams{
		Name:       "x",
		NewSlug:    "a", // collide with existing slug "a"
		BlobID:     newBlob(t, q, "<p>x</p>"),
		AccessCode: "code-x",
		Slug:       "b",
		UserID:     1,
	})
	if err == nil {
		t.Fatal("expected a uniqueness error, got nil")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("error = %v, want it to mention UNIQUE", err)
	}
}

func TestSetAndClearExpiry(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a") // starts private

	withViews, err := q.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
		MaxViews: sql.NullInt64{Int64: 5, Valid: true},
		Slug:     "a",
		UserID:   1,
	})
	if err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}
	if !withViews.IsPublic {
		t.Error("setting an expiry limit must force the file public")
	}
	if !withViews.MaxViews.Valid || withViews.MaxViews.Int64 != 5 {
		t.Errorf("MaxViews = %+v, want {5 true}", withViews.MaxViews)
	}
	if withViews.ViewCount != 0 {
		t.Errorf("ViewCount = %d, want 0 after setting a limit", withViews.ViewCount)
	}

	if err := q.IncrementFileViewCount(ctx, "a"); err != nil {
		t.Fatalf("IncrementFileViewCount: %v", err)
	}

	cleared, err := q.ClearFileExpiry(ctx, sqlcgen.ClearFileExpiryParams{Slug: "a", UserID: 1})
	if err != nil {
		t.Fatalf("ClearFileExpiry: %v", err)
	}
	if cleared.MaxViews.Valid || cleared.ExpiresAt.Valid {
		t.Error("ClearFileExpiry should null out both limit columns")
	}
	if cleared.ViewCount != 0 {
		t.Errorf("ViewCount = %d, want 0 after clear", cleared.ViewCount)
	}
	if !cleared.IsPublic {
		t.Error("ClearFileExpiry must not change visibility (was public)")
	}
}

func TestExpireFileGoesPrivateAndClearsLimits(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a")
	if _, err := q.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
		ExpiresAt: sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
		Slug:      "a",
		UserID:    1,
	}); err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}

	if err := q.ExpireFile(ctx, sqlcgen.ExpireFileParams{ExpiredReason: "ttl", Slug: "a"}); err != nil {
		t.Fatalf("ExpireFile: %v", err)
	}
	got, err := q.GetFileBySlugAnyOwner(ctx, "a")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if got.IsPublic {
		t.Error("expired file should be private")
	}
	if got.ExpiresAt.Valid || got.MaxViews.Valid {
		t.Error("expired file should have its limit columns cleared")
	}
	if got.ExpiredReason != "ttl" || !got.ExpiredAt.Valid {
		t.Errorf("expiry marker = %q at %v, want ttl with a timestamp", got.ExpiredReason, got.ExpiredAt)
	}

	// A second event overwrites the first: the marker is the *last* reason,
	// never a history.
	if err := q.ExpireFile(ctx, sqlcgen.ExpireFileParams{ExpiredReason: "views", Slug: "a"}); err != nil {
		t.Fatalf("ExpireFile (again): %v", err)
	}
	if got, err = q.GetFileBySlugAnyOwner(ctx, "a"); err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if got.ExpiredReason != "views" {
		t.Errorf("expiry marker = %q, want the latest reason (views)", got.ExpiredReason)
	}

	// Configuring a new limit clears it, so the badge can never contradict a
	// live expiry sitting next to it.
	if _, err := q.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
		MaxViews: sql.NullInt64{Int64: 3, Valid: true}, Slug: "a", UserID: 1,
	}); err != nil {
		t.Fatalf("SetFileExpiry: %v", err)
	}
	if got, err = q.GetFileBySlugAnyOwner(ctx, "a"); err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if got.ExpiredReason != "" || got.ExpiredAt.Valid {
		t.Errorf("expiry marker = %q at %v, want cleared by a new limit", got.ExpiredReason, got.ExpiredAt)
	}
}

// TestSuspendedOwnerHidesFiles pins the SQL half of account suspension: the
// public render path must stop finding a suspended user's files, and finding
// them again is exactly what un-suspending means.
func TestSuspendedOwnerHidesFiles(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	f := createFile(t, q, "a")

	if _, err := q.GetFileBySlugAnyOwner(ctx, f.Slug); err != nil {
		t.Fatalf("file should be visible while its owner is active: %v", err)
	}

	if _, err := q.DisableUser(ctx, f.UserID); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if _, err := q.GetFileBySlugAnyOwner(ctx, f.Slug); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("suspended owner's file err = %v, want ErrNoRows so /res/{slug} 404s", err)
	}
	// The owner-scoped read is untouched: suspension is enforced on the
	// identity (CurrentUser) for authenticated paths, not by hiding rows.
	if _, err := q.GetUserFileBySlug(ctx, sqlcgen.GetUserFileBySlugParams{
		Slug: f.Slug, UserID: f.UserID,
	}); err != nil {
		t.Errorf("owner-scoped read err = %v, want the row to still exist", err)
	}

	if _, err := q.EnableUser(ctx, f.UserID); err != nil {
		t.Fatalf("EnableUser: %v", err)
	}
	if _, err := q.GetFileBySlugAnyOwner(ctx, f.Slug); err != nil {
		t.Errorf("file should be visible again after un-suspending: %v", err)
	}
}

func TestHardDeleteUserTrash(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "keep")
	createFile(t, q, "trash1")
	createFile(t, q, "trash2")
	stranger := ensureUser(t, q, "stranger")
	if _, err := q.CreateFile(ctx, sqlcgen.CreateFileParams{
		Slug: "theirs", Name: "theirs", BlobID: newBlob(t, q, "<p>x</p>"), Kind: "html",
		AccessCode: "c", UserID: stranger.ID,
	}); err != nil {
		t.Fatalf("CreateFile(theirs): %v", err)
	}
	for _, slug := range []string{"trash1", "trash2"} {
		if _, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{Slug: slug, UserID: 1}); err != nil {
			t.Fatalf("SoftDeleteFile(%q): %v", slug, err)
		}
	}
	if _, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{Slug: "theirs", UserID: stranger.ID}); err != nil {
		t.Fatalf("SoftDeleteFile(theirs): %v", err)
	}

	rows, err := q.HardDeleteUserTrash(ctx, 1)
	if err != nil {
		t.Fatalf("HardDeleteUserTrash: %v", err)
	}
	if rows != 2 {
		t.Errorf("deleted %d rows, want 2", rows)
	}
	// The live file survives -- this is the only unbounded delete in the query
	// file, so "it only took the trash" is the property worth pinning.
	if _, err := q.GetFileBySlugAnyOwner(ctx, "keep"); err != nil {
		t.Errorf("live file was purged with the trash: %v", err)
	}
	// And it is scoped: the stranger's trash is untouched.
	theirs, err := q.ListUserDeletedFiles(ctx, stranger.ID)
	if err != nil {
		t.Fatalf("ListUserDeletedFiles: %v", err)
	}
	if len(theirs) != 1 {
		t.Errorf("stranger's trash = %v, want their own file left alone", slugsOf(theirs))
	}

	// Emptying an empty trash is a no-op, not an error.
	if rows, err = q.HardDeleteUserTrash(ctx, 1); err != nil || rows != 0 {
		t.Errorf("second empty = %d rows, %v; want 0, nil", rows, err)
	}
}

func TestListUsersWithFileCounts(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a")
	createFile(t, q, "b")
	if _, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{Slug: "b", UserID: 1}); err != nil {
		t.Fatalf("SoftDeleteFile: %v", err)
	}
	// A user with no files at all must still appear, which is why the counts
	// are correlated subqueries rather than a join.
	ensureUser(t, q, "empty")

	rows, err := q.ListUsersWithFileCounts(ctx)
	if err != nil {
		t.Fatalf("ListUsersWithFileCounts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d users, want 2", len(rows))
	}
	if rows[0].FileCount != 1 || rows[0].TrashedCount != 1 {
		t.Errorf("owner counts = %d active / %d trashed, want 1 / 1", rows[0].FileCount, rows[0].TrashedCount)
	}
	if rows[1].FileCount != 0 || rows[1].TrashedCount != 0 {
		t.Errorf("file-less user counts = %d / %d, want 0 / 0", rows[1].FileCount, rows[1].TrashedCount)
	}
}

func TestIncrementCounts(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a")

	if err := q.IncrementFileSuccessCount(ctx, "a"); err != nil {
		t.Fatalf("IncrementFileSuccessCount: %v", err)
	}
	if err := q.IncrementFileFailureCount(ctx, "a"); err != nil {
		t.Fatalf("IncrementFileFailureCount: %v", err)
	}
	if err := q.IncrementFileFailureCount(ctx, "a"); err != nil {
		t.Fatalf("IncrementFileFailureCount: %v", err)
	}
	got, err := q.GetFileBySlugAnyOwner(ctx, "a")
	if err != nil {
		t.Fatalf("GetFileBySlugAnyOwner: %v", err)
	}
	if got.SuccessCount != 1 {
		t.Errorf("SuccessCount = %d, want 1", got.SuccessCount)
	}
	if got.FailureCount != 2 {
		t.Errorf("FailureCount = %d, want 2", got.FailureCount)
	}
}

func TestSessionLifecycle(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()

	// A one-minute margin on purpose. This used to need hours: the driver wrote
	// local wall-clock time while GetValidSession compared it against UTC
	// CURRENT_TIMESTAMP as text, so validity was skewed by the host's offset and
	// a tight margin failed anywhere but UTC. Both sides now go through
	// unixepoch(), which makes the comparison an instant-to-instant one -- so a
	// minute is enough, and this test fails if that regresses in a machine
	// whose TZ isn't UTC.
	if err := q.CreateSession(ctx, sqlcgen.CreateSessionParams{
		Token:     "valid",
		UserID:    1,
		ExpiresAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatalf("CreateSession(valid): %v", err)
	}
	if err := q.CreateSession(ctx, sqlcgen.CreateSessionParams{
		Token:     "expired",
		UserID:    1,
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("CreateSession(expired): %v", err)
	}

	if _, err := q.GetValidSession(ctx, "valid"); err != nil {
		t.Errorf("GetValidSession(valid) err = %v, want nil", err)
	}
	if _, err := q.GetValidSession(ctx, "expired"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetValidSession(expired) err = %v, want ErrNoRows", err)
	}

	// DeleteExpiredSessions removes only the expired one.
	if err := q.DeleteExpiredSessions(ctx); err != nil {
		t.Fatalf("DeleteExpiredSessions: %v", err)
	}
	if _, err := q.GetValidSession(ctx, "valid"); err != nil {
		t.Errorf("valid session removed by expired-sweep: %v", err)
	}

	// DeleteSession removes a specific token.
	if err := q.DeleteSession(ctx, "valid"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := q.GetValidSession(ctx, "valid"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetValidSession after delete err = %v, want ErrNoRows", err)
	}
}

func TestUserLifecycle(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()

	if n, err := q.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("CountUsers = %d, %v; want 0, nil", n, err)
	}

	first, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username:     "admin",
		Nickname:     "Boss",
		PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if first.ID != 1 {
		t.Errorf("first user id = %d, want 1 (super admin)", first.ID)
	}

	// Duplicate usernames are rejected.
	if _, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
		Username: "admin", Nickname: "x", PasswordHash: "h",
	}); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("duplicate username err = %v, want UNIQUE violation", err)
	}

	byName, err := q.GetUserByUsername(ctx, "admin")
	if err != nil || byName.ID != first.ID {
		t.Errorf("GetUserByUsername = %+v, %v", byName, err)
	}

	if _, err := q.UpdateUserNickname(ctx, sqlcgen.UpdateUserNicknameParams{
		Nickname: "Renamed", ID: first.ID,
	}); err != nil {
		t.Fatalf("UpdateUserNickname: %v", err)
	}
	if rows, err := q.UpdateUserPassword(ctx, sqlcgen.UpdateUserPasswordParams{
		PasswordHash: "hash2", ID: first.ID,
	}); err != nil || rows != 1 {
		t.Fatalf("UpdateUserPassword = %d rows, %v; want 1, nil", rows, err)
	}
	// The admin reset path distinguishes "changed it" from "no such user" by
	// this count, so an unknown id must report zero rather than succeed.
	if rows, err := q.UpdateUserPassword(ctx, sqlcgen.UpdateUserPasswordParams{
		PasswordHash: "hash3", ID: 9999,
	}); err != nil || rows != 0 {
		t.Errorf("UpdateUserPassword(unknown id) = %d rows, %v; want 0, nil", rows, err)
	}

	// Suspension round-trip, including the COALESCE that keeps the original
	// timestamp when an already-suspended account is suspended again.
	disabled, err := q.DisableUser(ctx, first.ID)
	if err != nil || !disabled.DisabledAt.Valid {
		t.Fatalf("DisableUser = %+v, %v", disabled, err)
	}
	again, err := q.DisableUser(ctx, first.ID)
	if err != nil {
		t.Fatalf("DisableUser (repeat): %v", err)
	}
	if !again.DisabledAt.Time.Equal(disabled.DisabledAt.Time) {
		t.Errorf("re-suspending moved disabled_at from %v to %v", disabled.DisabledAt.Time, again.DisabledAt.Time)
	}
	if enabled, err := q.EnableUser(ctx, first.ID); err != nil || enabled.DisabledAt.Valid {
		t.Errorf("EnableUser = %+v, %v; want disabled_at cleared", enabled, err)
	}
	if _, err := q.DisableUser(ctx, 9999); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DisableUser(unknown id) err = %v, want ErrNoRows", err)
	}
	if err := q.SetUserAPIKey(ctx, sqlcgen.SetUserAPIKeyParams{
		ApiKey: sql.NullString{String: "rb_abc", Valid: true}, ID: first.ID,
	}); err != nil {
		t.Fatalf("SetUserAPIKey: %v", err)
	}

	got, err := q.GetUserByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if got.Nickname != "Renamed" || got.PasswordHash != "hash2" || got.ApiKey.String != "rb_abc" {
		t.Errorf("user after updates = %+v", got)
	}
}

func TestConfigSetOverwrites(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()

	if _, err := q.GetConfig(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetConfig(missing) err = %v, want ErrNoRows", err)
	}

	if err := q.SetConfig(ctx, sqlcgen.SetConfigParams{Key: "k", Value: "true"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	if err := q.SetConfig(ctx, sqlcgen.SetConfigParams{Key: "k", Value: "false"}); err != nil {
		t.Fatalf("SetConfig (overwrite): %v", err)
	}
	if v, err := q.GetConfig(ctx, "k"); err != nil || v != "false" {
		t.Errorf("GetConfig = %q, %v; want false, nil", v, err)
	}
}

func slugsOf(rows []sqlcgen.File) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Slug
	}
	return out
}

// TestFileQueriesRejectForeignOwner pins owner isolation at the layer that
// actually enforces it. Handlers turn "no row" into 404; this checks that the
// SQL produces no row in the first place, for every scoped query.
func TestFileQueriesRejectForeignOwner(t *testing.T) {
	const owner, stranger = 1, 2
	ctx := context.Background()

	// Each case runs against a fresh DB holding one file owned by user 1,
	// so a leak in one query can't be masked by an earlier one.
	cases := []struct {
		name string
		// run performs the query as the stranger and reports whether it
		// matched a row.
		run func(t *testing.T, q *sqlcgen.Queries) bool
	}{
		{"GetUserFileBySlug", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.GetUserFileBySlug(ctx, sqlcgen.GetUserFileBySlugParams{Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"RenameFile", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.RenameFile(ctx, sqlcgen.RenameFileParams{Name: "hacked", Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"SetFileVisibility", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.SetFileVisibility(ctx, sqlcgen.SetFileVisibilityParams{IsPublic: true, Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"SetFileTags", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.SetFileTags(ctx, sqlcgen.SetFileTagsParams{Tags: "hacked", Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"RefreshFileAccessCode", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.RefreshFileAccessCode(ctx, sqlcgen.RefreshFileAccessCodeParams{AccessCode: "hacked", Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"UpdateFile", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.UpdateFile(ctx, sqlcgen.UpdateFileParams{
				Name: "hacked", NewSlug: "hacked", BlobID: newBlob(t, q, "<p>hacked</p>"),
				AccessCode: "hacked", Slug: "a", UserID: stranger,
			})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"SetFileExpiry", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.SetFileExpiry(ctx, sqlcgen.SetFileExpiryParams{
				MaxViews: sql.NullInt64{Int64: 1, Valid: true}, Slug: "a", UserID: stranger,
			})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"ClearFileExpiry", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.ClearFileExpiry(ctx, sqlcgen.ClearFileExpiryParams{Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"SoftDeleteFile", func(t *testing.T, q *sqlcgen.Queries) bool {
			rows, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{Slug: "a", UserID: stranger})
			if err != nil {
				t.Fatalf("SoftDeleteFile: %v", err)
			}
			return rows != 0
		}},
		{"ListUserFiles", func(t *testing.T, q *sqlcgen.Queries) bool {
			rows, err := q.ListUserFiles(ctx, stranger)
			if err != nil {
				t.Fatalf("ListUserFiles: %v", err)
			}
			return len(rows) != 0
		}},
		{"GetUserFileSize", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.GetUserFileSize(ctx, sqlcgen.GetUserFileSizeParams{Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"SumUserContentSize", func(t *testing.T, q *sqlcgen.Queries) bool {
			used, err := q.SumUserContentSize(ctx, stranger)
			if err != nil {
				t.Fatalf("SumUserContentSize: %v", err)
			}
			return used != 0
		}},
		{"SearchUserFiles", func(t *testing.T, q *sqlcgen.Queries) bool {
			rows, err := q.SearchUserFiles(ctx, sqlcgen.SearchUserFilesParams{UserID: stranger, Query: "name"})
			if err != nil {
				t.Fatalf("SearchUserFiles: %v", err)
			}
			return len(rows) != 0
		}},
		{"ListUserFilesWithSearchMeta", func(t *testing.T, q *sqlcgen.Queries) bool {
			rows, err := q.ListUserFilesWithSearchMeta(ctx, stranger)
			if err != nil {
				t.Fatalf("ListUserFilesWithSearchMeta: %v", err)
			}
			return len(rows) != 0
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := newQueries(t)
			created := createFile(t, q, "a") // owned by user 1
			if c.run(t, q) {
				t.Errorf("%s matched user %d's file as user %d", c.name, owner, stranger)
			}
			// The row must be exactly as created.
			got, err := q.GetFileBySlugAnyOwner(ctx, "a")
			if err != nil {
				t.Fatalf("file should still exist: %v", err)
			}
			if got.Name != "name-a" || got.BlobID != created.BlobID || got.AccessCode != "code-a" ||
				got.IsPublic || got.Tags != "" || got.DeletedAt.Valid {
				t.Errorf("file was mutated by a foreign-owner query: %+v", got)
			}
		})
	}

	// Site-only statements carry AND kind = 'site', so against a document they
	// match nothing for anyone -- a stranger's miss would prove nothing. Run
	// them against a real site, with the owner's own call as the control that
	// the statement can match at all.
	for _, c := range []struct {
		name string
		run  func(q *sqlcgen.Queries, userID int64) error
	}{
		{"UpdateSiteMeta", func(q *sqlcgen.Queries, userID int64) error {
			_, err := q.UpdateSiteMeta(ctx, sqlcgen.UpdateSiteMetaParams{
				Name: "hacked", NewSlug: "site", AccessCode: "hacked", Slug: "site", UserID: userID,
			})
			return err
		}},
		{"ReplaceSiteBlob", func(q *sqlcgen.Queries, userID int64) error {
			site, err := q.GetSiteBySlugAnyOwner(ctx, "site")
			if err != nil {
				return err
			}
			_, err = q.ReplaceSiteBlob(ctx, sqlcgen.ReplaceSiteBlobParams{
				BlobID: newBlob(t, q, "hacked"), Slug: "site", UserID: userID, OldBlobID: site.BlobID,
			})
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQueries(t)
			createFile(t, q, "a") // seeds user 1
			original := newBlob(t, q, "original")
			if _, err := q.CreateSite(ctx, sqlcgen.CreateSiteParams{
				Slug: "site", Name: "site", BlobID: original, AccessCode: "code-site", UserID: owner,
			}); err != nil {
				t.Fatalf("CreateSite: %v", err)
			}
			if err := c.run(q, stranger); !errors.Is(err, sql.ErrNoRows) {
				t.Errorf("%s as user %d: err = %v, want no rows", c.name, stranger, err)
			}
			got, err := q.GetFileBySlugAnyOwner(ctx, "site")
			if err != nil {
				t.Fatalf("site should still exist: %v", err)
			}
			if got.Name != "site" || got.AccessCode != "code-site" || got.BlobID != original {
				t.Errorf("site was mutated by a foreign-owner statement: %+v", got)
			}
			if err := c.run(q, owner); err != nil {
				t.Errorf("%s as the owner: %v (the statement never matches, so the check above is vacuous)", c.name, err)
			}
		})
	}

	// Trash-only queries need a soft-deleted file to be meaningful.
	for _, c := range []struct {
		name string
		run  func(t *testing.T, q *sqlcgen.Queries) bool
	}{
		{"RestoreFile", func(t *testing.T, q *sqlcgen.Queries) bool {
			_, err := q.RestoreFile(ctx, sqlcgen.RestoreFileParams{Slug: "a", UserID: stranger})
			return !errors.Is(err, sql.ErrNoRows)
		}},
		{"HardDeleteFile", func(t *testing.T, q *sqlcgen.Queries) bool {
			rows, err := q.HardDeleteFile(ctx, sqlcgen.HardDeleteFileParams{Slug: "a", UserID: stranger})
			if err != nil {
				t.Fatalf("HardDeleteFile: %v", err)
			}
			return rows != 0
		}},
		{"ListUserDeletedFiles", func(t *testing.T, q *sqlcgen.Queries) bool {
			rows, err := q.ListUserDeletedFiles(ctx, stranger)
			if err != nil {
				t.Fatalf("ListUserDeletedFiles: %v", err)
			}
			return len(rows) != 0
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQueries(t)
			createFile(t, q, "a")
			if _, err := q.SoftDeleteFile(ctx, sqlcgen.SoftDeleteFileParams{Slug: "a", UserID: owner}); err != nil {
				t.Fatalf("SoftDeleteFile: %v", err)
			}
			if c.run(t, q) {
				t.Errorf("%s matched user %d's trashed file as user %d", c.name, owner, stranger)
			}
			// Still in the owner's trash: neither restored nor purged.
			rows, err := q.ListUserDeletedFiles(ctx, owner)
			if err != nil {
				t.Fatalf("ListUserDeletedFiles: %v", err)
			}
			if len(rows) != 1 || rows[0].Slug != "a" {
				t.Errorf("owner's trash = %v, want [a]", slugsOf(rows))
			}
		})
	}
}

// ReplaceSiteBlob is a compare-and-swap: update_site builds the new archive
// with no lock held, so the write must only land on the version it read.
func TestReplaceSiteBlobRefusesAStaleBase(t *testing.T) {
	q := newQueries(t)
	ctx := context.Background()
	createFile(t, q, "a") // seeds user 1
	v1 := newBlob(t, q, "v1")
	if _, err := q.CreateSite(ctx, sqlcgen.CreateSiteParams{
		Slug: "site", Name: "site", BlobID: v1, AccessCode: "c", UserID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Another edit lands first.
	v2 := newBlob(t, q, "v2!")
	if _, err := q.ReplaceSiteBlob(ctx, sqlcgen.ReplaceSiteBlobParams{
		BlobID: v2, Slug: "site", UserID: 1, OldBlobID: v1,
	}); err != nil {
		t.Fatalf("first replace: %v", err)
	}
	// This one was built on v1, which is gone.
	_, err := q.ReplaceSiteBlob(ctx, sqlcgen.ReplaceSiteBlobParams{
		BlobID: newBlob(t, q, "v3 on top of v1"), Slug: "site", UserID: 1, OldBlobID: v1,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale replace = %v, want no rows", err)
	}
	if got, _ := q.GetSiteBySlugAnyOwner(ctx, "site"); got.BlobID != v2 || got.ContentSize != 3 {
		t.Errorf("site = blob %s size %d, want the first edit's", got.BlobID, got.ContentSize)
	}
}
