package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/shawn-bluce/renderbin/backend/internal/auth"
	"github.com/shawn-bluce/renderbin/backend/internal/buildinfo"
	"github.com/shawn-bluce/renderbin/backend/internal/db/sqlcgen"
)

// Subcommands. Running the binary with no arguments starts the server, which is
// what the container's ENTRYPOINT does and must keep doing.
//
// reset-password is the escape hatch for the one lockout the app cannot fix
// itself: the super admin (id=1) can reset anyone else's password from the
// settings page, but nothing can reset *theirs*, and there is no email-based
// recovery by design. Without this, that means editing the SQLite file by hand.
// The data subcommands (backup, restore, fsck, migrate) are in datacmds.go.

const usage = `renderbin — self-hosted sharing for HTML, Markdown and text files

Usage:
  server                      Start the HTTP server (default)
  server reset-password ...   Set an account's password directly
  server backup --out=FILE    Write a complete backup (.tar)
  server restore --in=FILE    Replace all data with a backup
  server fsck                 Check that every file's content is intact
  server migrate --check      Show what starting this version would do
  server --help               Show this help
  server --version            Print the version

Run a subcommand with -h for its details.

Environment (server and subcommands):
  LISTEN_ADDR   Address to bind            (default ":8080")
  DB_PATH       SQLite database file       (default "data/app.db")
  BLOB_DIR      File content directory     (default "blobs" beside DB_PATH)
`

const resetPasswordUsage = `server reset-password --user=NAME [--password=SECRET]

Sets an account's password without needing the old one, and signs out every
session it currently has. Intended for the super admin locking themselves out,
since no one can reset that account through the web UI.

With --password omitted the new password is read from stdin, which is the safer
form because the secret stays out of your shell history and the process list:

  echo 'new-secret' | server reset-password --user=admin

Uses the same DB_PATH as the server, so run it against the same database (for a
container: docker compose exec app ./server reset-password --user=admin).
`

// runCLI handles argv. It returns handled=false when the arguments mean "start
// the server", leaving main to do that.
func runCLI(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, handled bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0, true
	case "-v", "--version", "version":
		fmt.Fprintln(stdout, buildinfo.Version)
		return 0, true
	case "reset-password":
		return exitCode(resetPassword(args[1:], stdin, stdout, stderr), stderr), true
	case "backup":
		return exitCode(backupCmd(args[1:], stdout, stderr), stderr), true
	case "restore":
		return exitCode(restoreCmd(args[1:], stdout, stderr), stderr), true
	case "fsck":
		return exitCode(fsckCmd(args[1:], stdout, stderr), stderr), true
	case "migrate":
		return exitCode(migrateCmd(args[1:], stdout, stderr), stderr), true
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2, true
	}
}

// exitCode turns a subcommand's result into its exit status. `cmd -h` has
// already printed its usage; asking for help is not a failure.
func exitCode(err error, stderr io.Writer) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	fmt.Fprintln(stderr, "error:", err)
	return 1
}

func resetPassword(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, resetPasswordUsage) }
	username := fs.String("user", "", "username of the account to reset (required)")
	password := fs.String("password", "", "new password; read from stdin when omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}

	*username = strings.TrimSpace(*username)
	if *username == "" {
		fs.Usage()
		return errors.New("--user is required")
	}

	secret := *password
	if secret == "" {
		read, err := readPasswordLine(stdin)
		if err != nil {
			return err
		}
		secret = read
	}
	if msg := auth.ValidatePassword(secret); msg != "" {
		return errors.New(msg)
	}

	dbPath, _ := dataPaths()
	conn, _, err := openData()
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx := context.Background()
	queries := sqlcgen.New(conn)
	user, err := queries.GetUserByUsername(ctx, *username)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no account named %q in %s", *username, dbPath)
	}
	if err != nil {
		return fmt.Errorf("look up %q: %w", *username, err)
	}

	hash, err := auth.HashPassword(secret)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := queries.UpdateUserPassword(ctx, sqlcgen.UpdateUserPasswordParams{
		PasswordHash: hash,
		ID:           user.ID,
	}); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	// Same reasoning as the super admin's reset endpoint: this is reached for
	// lockouts and compromises, and both want current sessions ended.
	if err := queries.DeleteUserSessions(ctx, user.ID); err != nil {
		return fmt.Errorf("clear sessions: %w", err)
	}

	fmt.Fprintf(stdout, "password updated for %q (id %d); its sessions were signed out\n", user.Username, user.ID)
	// Worth saying out loud: a reset does not un-suspend, and someone reaching
	// for this tool may well assume it does.
	if user.DisabledAt.Valid {
		fmt.Fprintf(stdout, "note: this account is disabled and still cannot sign in — re-enable it in Settings\n")
	}
	return nil
}

// readPasswordLine reads one line from stdin. Deliberately not a no-echo
// terminal prompt: that needs golang.org/x/term, and a dependency for one
// convenience isn't worth it in a binary whose whole point is having none. The
// documented usage pipes the secret in, which never displays it at all.
func readPasswordLine(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(stdin).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && (err != io.EOF || line == "") {
		return "", errors.New("no password on stdin; pass --password or pipe one in")
	}
	if line == "" {
		return "", errors.New("the password read from stdin was empty")
	}
	return line, nil
}
