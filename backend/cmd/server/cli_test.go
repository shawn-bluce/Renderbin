package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/blobstore"
	"github.com/shawn-bluce/renderbin/backend/internal/db"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

const seedPassword = "original-pass"

// One bcrypt hash per test binary: the tests only verify it.
var seedHash = sync.OnceValue(func() string {
	h, err := auth.HashPassword(seedPassword)
	if err != nil {
		panic(err)
	}
	return h
})

// seedAccount creates a database at a temp path with one account ("admin",
// seedPassword), points DB_PATH at it (the subcommands read the same env var
// the server does) and returns the queries handle for assertions.
func seedAccount(t *testing.T) *sqlcgen.Queries {
	t.Helper()
	queries, _ := seedData(t)
	return queries
}

// seedData is seedAccount plus the blob store the subcommands will find
// beside the database (BLOB_DIR's default).
func seedData(t *testing.T) (*sqlcgen.Queries, *blobstore.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.db")
	t.Setenv("DB_PATH", path)
	t.Setenv("BLOB_DIR", "")

	store, err := blobstore.Open(filepath.Join(filepath.Dir(path), "blobs"))
	if err != nil {
		t.Fatalf("blobstore.Open: %v", err)
	}
	conn, err := db.Open(path, store)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	queries := sqlcgen.New(conn)
	user, err := queries.CreateUser(context.Background(), sqlcgen.CreateUserParams{
		Username:     "admin",
		Nickname:     "admin",
		PasswordHash: seedHash(),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := queries.CreateSession(context.Background(), sqlcgen.CreateSessionParams{
		Token:     "live-token",
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return queries, store
}

// run invokes the CLI the way main does, capturing both streams.
func run(t *testing.T, stdin string, args ...string) (code int, handled bool, out, errOut string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code, handled = runCLI(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, handled, stdout.String(), stderr.String()
}

// No arguments must mean "start the server": the container's ENTRYPOINT is the
// bare binary, so a subcommand dispatcher that swallowed that would break every
// deployment.
func TestNoArgsRunsTheServer(t *testing.T) {
	_, handled, _, _ := run(t, "")
	if handled {
		t.Error("runCLI handled an empty argument list; the server would never start")
	}
}

func TestHelpListsResetPassword(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		code, handled, out, _ := run(t, "", arg)
		if !handled || code != 0 {
			t.Errorf("%s: handled=%v code=%d, want true/0", arg, handled, code)
		}
		// The requirement for this subcommand is that it be discoverable here.
		if !strings.Contains(out, "reset-password") {
			t.Errorf("%s output does not mention reset-password:\n%s", arg, out)
		}
	}
}

func TestVersionAndUnknownCommand(t *testing.T) {
	if code, handled, out, _ := run(t, "", "--version"); !handled || code != 0 || out == "" {
		t.Errorf("--version: handled=%v code=%d out=%q", handled, code, out)
	}
	code, handled, _, errOut := run(t, "", "frobnicate")
	if !handled || code != 2 {
		t.Errorf("unknown command: handled=%v code=%d, want true/2", handled, code)
	}
	if !strings.Contains(errOut, "frobnicate") || !strings.Contains(errOut, "Usage") {
		t.Errorf("unknown command should name it and show usage, got:\n%s", errOut)
	}
}

func TestResetPasswordFromStdin(t *testing.T) {
	queries := seedAccount(t)

	code, handled, out, errOut := run(t, "brand-new-pass\n", "reset-password", "--user=admin")
	if !handled || code != 0 {
		t.Fatalf("code=%d handled=%v stderr=%q", code, handled, errOut)
	}
	if !strings.Contains(out, "admin") {
		t.Errorf("output should confirm which account changed, got %q", out)
	}

	user, err := queries.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if !auth.VerifyPassword(user.PasswordHash, "brand-new-pass") {
		t.Error("the new password does not verify")
	}
	if auth.VerifyPassword(user.PasswordHash, seedPassword) {
		t.Error("the old password still verifies")
	}
	// Same rule as the super admin's reset endpoint: a reset ends the account's
	// sessions, since it exists for lockouts and compromises.
	if _, err := queries.GetValidSession(context.Background(), "live-token"); err == nil {
		t.Error("the account's existing session survived the reset")
	}
}

func TestResetPasswordFlagForm(t *testing.T) {
	queries := seedAccount(t)

	if code, _, _, errOut := run(t, "", "reset-password", "--user=admin", "--password=flag-pass"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	user, err := queries.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if !auth.VerifyPassword(user.PasswordHash, "flag-pass") {
		t.Error("--password was not applied")
	}
}

func TestResetPasswordRejections(t *testing.T) {
	cases := []struct {
		name, stdin string
		args        []string
		wantIn      string
	}{
		{"missing user", "x\n", []string{"reset-password"}, "--user is required"},
		{"unknown user", "long-enough\n", []string{"reset-password", "--user=ghost"}, "no account named"},
		{"too short", "abc\n", []string{"reset-password", "--user=admin"}, "at least 6 characters"},
		// bcrypt's own ceiling: this used to get past the length check and die
		// in the hasher with an opaque error.
		{"too long", strings.Repeat("x", 73) + "\n", []string{"reset-password", "--user=admin"}, "at most 72 bytes"},
		{"empty stdin", "", []string{"reset-password", "--user=admin"}, "no password on stdin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			queries := seedAccount(t)

			code, handled, _, errOut := run(t, c.stdin, c.args...)
			if !handled || code != 1 {
				t.Errorf("handled=%v code=%d, want true/1", handled, code)
			}
			if !strings.Contains(errOut, c.wantIn) {
				t.Errorf("stderr %q does not contain %q", errOut, c.wantIn)
			}
			// A rejected reset must leave the account exactly as it was.
			user, err := queries.GetUserByUsername(context.Background(), "admin")
			if err != nil {
				t.Fatalf("GetUserByUsername: %v", err)
			}
			if !auth.VerifyPassword(user.PasswordHash, seedPassword) {
				t.Error("a rejected reset changed the password anyway")
			}
		})
	}
}

// Resetting a suspended account's password works, but says so: someone reaching
// for this tool could easily assume it also restores access.
func TestResetPasswordWarnsOnDisabledAccount(t *testing.T) {
	queries := seedAccount(t)
	if _, err := queries.DisableUser(context.Background(), 1); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}

	code, _, out, errOut := run(t, "brand-new-pass\n", "reset-password", "--user=admin")
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("output should warn that the account is still disabled, got %q", out)
	}
}
