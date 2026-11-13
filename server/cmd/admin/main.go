// Command admin manages accounts and devices from the command line. Run it inside the
// API container, which has the install's settings:
//
//	admin user add alice [-name "Alice"] [-email a@example.com] [-stdin]
//	admin user password alice [-stdin]     set or reset; signs their browsers out
//	admin user nopassword alice            remove (OIDC sign-in only); signs their browsers out
//	admin user list
//	admin devices signout alice            every phone and TV of alice
//
// The password is asked for twice without echo, or read from standard input
// with -stdin (one line), so it never appears in the shell history or `ps`.
// Admin rights come from ADMIN_USERS / OWNER_USERNAME, as for every account.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/term"

	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/devices"
	"movienight/server/internal/passwords"
	"movienight/server/internal/pg"
	"movienight/server/internal/store"
	"movienight/server/internal/users"
)

const usage = `usage:
  admin user add NAME [-name NAME] [-email EMAIL] [-stdin]
  admin user password NAME [-stdin]
  admin user nopassword NAME
  admin user list
  admin devices signout NAME`

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New(usage)
	}
	group, action, rest := args[0], args[1], args[2:]
	fs := flag.NewFlagSet("admin", flag.ContinueOnError)
	name := fs.String("name", "", "display name (user add)")
	email := fs.String("email", "", "email (user add)")
	stdin := fs.Bool("stdin", false, "read the password from standard input")
	// The username comes first, flags after (as the Python's argparse allowed).
	var who string
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		who, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	pool, err := pg.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	q := store.New(pool)
	people := users.New(q, cfg)
	pw := passwords.NewService(passwords.NewHasher(), people, nil, cfg.PasswordMinLength)

	find := func() (store.User, error) {
		clean := users.CleanUsername(who)
		u, err := people.ByUsername(ctx, clean)
		if clean == "" || errors.Is(err, pgx.ErrNoRows) {
			return u, fmt.Errorf("no user %q. Make one with: admin user add %s", who, who)
		}
		return u, err
	}

	switch group + " " + action {
	case "user list":
		all, err := q.ListUsers(ctx)
		if err != nil {
			return err
		}
		for _, u := range all {
			var kinds []string
			if u.PasswordHash != nil {
				kinds = append(kinds, "password")
			}
			if u.OidcSub != nil {
				kinds = append(kinds, "oidc")
			}
			if u.IsAdmin {
				kinds = append(kinds, "admin")
			}
			display := ""
			if u.DisplayName != nil {
				display = *u.DisplayName
			}
			fmt.Printf("%s\t%s\t%s\n", u.Username, display, strings.Join(kinds, ", "))
		}
		return nil
	case "user add":
		clean := users.CleanUsername(who)
		if clean == "" {
			return errors.New("that username can't be used")
		}
		if _, err := people.ByUsername(ctx, clean); err == nil {
			return fmt.Errorf("%s already exists. To set their password: admin user password %s", clean, clean)
		}
		secret, err := readPassword(*stdin)
		if err != nil {
			return err
		}
		if err := pw.Validate(secret); err != nil {
			return err
		}
		u, err := people.GetOrCreate(ctx, clean, *email, *name)
		if err != nil {
			return err
		}
		if u, err = pw.Set(ctx, u, secret); err != nil {
			return err
		}
		suffix := ""
		if u.IsAdmin {
			suffix = " (admin)"
		}
		fmt.Printf("Made %s%s. They can sign in at /signin.\n", u.Username, suffix)
		return nil
	case "user password":
		u, err := find()
		if err != nil {
			return err
		}
		secret, err := readPassword(*stdin)
		if err != nil {
			return err
		}
		if _, err := pw.Set(ctx, u, secret); err != nil {
			return err
		}
		if _, err := q.DeleteUserWebSessions(ctx, u.ID); err != nil {
			return err
		}
		fmt.Printf("Password set for %s; their browsers were signed out.\n", u.Username)
		return nil
	case "user nopassword":
		u, err := find()
		if err != nil {
			return err
		}
		if _, err := people.SetPassword(ctx, u.ID, nil); err != nil {
			return err
		}
		if _, err := q.DeleteUserWebSessions(ctx, u.ID); err != nil {
			return err
		}
		fmt.Printf("Removed %s's password; their browsers were signed out.\n", u.Username)
		return nil
	case "devices signout":
		u, err := find()
		if err != nil {
			return err
		}
		keys, err := crypto.NewKeys(cfg.SecretKey)
		if err != nil {
			return err
		}
		n, err := devices.New(pool, cfg, keys).SignOutEverywhere(ctx, u.ID)
		if err != nil {
			return err
		}
		plural := "s"
		if n == 1 {
			plural = ""
		}
		fmt.Printf("Signed out %d device%s of %s\n", n, plural, u.Username)
		return nil
	}
	return errors.New(usage)
}

func readPassword(fromStdin bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", errors.New("no password on standard input")
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "New password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading the password (use -stdin when not at a terminal): %w", err)
	}
	fmt.Fprint(os.Stderr, "Again: ")
	again, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(again) {
		return "", errors.New("the two passwords aren't the same")
	}
	return string(first), nil
}
