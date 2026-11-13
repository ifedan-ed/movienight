package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"movienight/server/internal/crypto"
	"movienight/server/internal/testbase"
)

// withStdin runs f with s on standard input.
func withStdin(t *testing.T, s string, f func()) {
	r, w, _ := os.Pipe()
	_, _ = w.WriteString(s)
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	f()
}

func TestAddResetListAndSignOut(t *testing.T) {
	pool, _ := testbase.Conns(t)
	for k, v := range testbase.BaseEnv(t) {
		t.Setenv(k, v)
	}
	ctx := context.Background()
	withStdin(t, "correct horse battery\n", func() {
		if err := run(ctx, []string{"user", "add", "Erin", "-name", "Erin E", "-stdin"}); err != nil {
			t.Fatal(err)
		}
	})
	var hash, display string
	_ = pool.QueryRow(ctx, "SELECT password_hash, display_name FROM users WHERE username = 'erin'").Scan(&hash, &display)
	if !crypto.CheckPassword("correct horse battery", hash) || display != "Erin E" {
		t.Fatal(hash, display)
	}
	withStdin(t, "short\n", func() {
		if err := run(ctx, []string{"user", "password", "erin", "-stdin"}); err == nil || !strings.Contains(err.Error(), "at least 10") {
			t.Fatal(err)
		}
	})
	withStdin(t, "another long password\n", func() {
		if err := run(ctx, []string{"user", "password", "erin", "-stdin"}); err != nil {
			t.Fatal(err)
		}
	})
	_ = pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE username = 'erin'").Scan(&hash)
	if !crypto.CheckPassword("another long password", hash) {
		t.Fatal("not reset")
	}
	if err := run(ctx, []string{"user", "password", "nobody", "-stdin"}); err == nil {
		t.Fatal("an unknown user was accepted")
	}
	if err := run(ctx, []string{"devices", "signout", "erin"}); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"user", "list"}); err != nil {
		t.Fatal(err)
	}
}
