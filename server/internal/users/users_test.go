package users_test

import (
	"context"
	"errors"
	"testing"

	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/users"
)

func service(t *testing.T, env testbase.Env) *users.Service {
	p, _ := testbase.Conns(t)
	return users.New(store.New(p), env.Config(t))
}

func TestSignInOIDCMatchesBySubjectAndLinksOnlyUnlinkedAccounts(t *testing.T) {
	s := service(t, testbase.BaseEnv(t))
	ctx := context.Background()
	u1, err := s.SignInOIDC(ctx, users.OIDCClaims{Subject: "sub-1", Email: "a@example.org", Name: "Alice", UsernameHint: "alice"})
	if err != nil || u1.Username != "alice" || *u1.Email != "a@example.org" {
		t.Fatal(u1, err)
	}
	// Same subject: same person, email updated, no duplicate.
	u2, err := s.SignInOIDC(ctx, users.OIDCClaims{Subject: "sub-1", Email: "a2@example.org", UsernameHint: "renamed"})
	if err != nil || u2.ID != u1.ID || *u2.Email != "a2@example.org" || u2.Username != "alice" {
		t.Fatal(u2, err)
	}
	// A different subject with the same username: refused (the Python linked it).
	if _, err := s.SignInOIDC(ctx, users.OIDCClaims{Subject: "sub-2", UsernameHint: "alice"}); !errors.Is(err, users.ErrOIDCAccountTaken) {
		t.Fatal(err)
	}
	// An account without a subject yet (made with a password) is linked.
	carol, _ := s.GetOrCreate(ctx, "carol", "", "")
	u3, err := s.SignInOIDC(ctx, users.OIDCClaims{Subject: "sub-3", Email: "c@example.org", UsernameHint: "Carol"})
	if err != nil || u3.ID != carol.ID || *u3.OidcSub != "sub-3" || *u3.Email != "c@example.org" {
		t.Fatal(u3, err)
	}
}

func TestSignInOIDCUsernameFallbacks(t *testing.T) {
	s := service(t, testbase.BaseEnv(t))
	ctx := context.Background()
	u, err := s.SignInOIDC(ctx, users.OIDCClaims{Subject: "sub-xyz", Email: "Bob@Example.org"})
	if err != nil || u.Username != "bob" || *u.Email != "Bob@Example.org" {
		t.Fatal(u, err)
	}
	u, err = s.SignInOIDC(ctx, users.OIDCClaims{Subject: "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123"})
	if err != nil || u.Username != "user-abcdefghijklmnopqrstuvwx" {
		t.Fatal(u.Username, err)
	}
	if _, err := s.SignInOIDC(ctx, users.OIDCClaims{}); err == nil {
		t.Fatal("no subject accepted")
	}
}

func TestAdminRightsFollowTheSettingsBothWays(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["ADMIN_USERS"] = "carol"
	p, _ := testbase.Conns(t)
	s := users.New(store.New(p), env.Config(t))
	ctx := context.Background()
	carol, _ := s.GetOrCreate(ctx, "carol", "", "")
	if !carol.IsAdmin {
		t.Fatal("not admin")
	}
	delete(env, "ADMIN_USERS") // taken off ADMIN_USERS
	later := users.New(store.New(p), env.Config(t))
	carol, err := later.Seen(ctx, carol, "", "")
	if err != nil || carol.IsAdmin {
		t.Fatal("still admin")
	}
	owner, _ := later.GetOrCreate(ctx, "owner", "", "")
	if !owner.IsAdmin {
		t.Fatal("the owner is not admin")
	}
}

func TestCleanUsername(t *testing.T) {
	for in, want := range map[string]string{"  Carol ": "carol", "": "", "a\x01b": ""} {
		if got := users.CleanUsername(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	if users.CleanUsername(string(long)) != "" {
		t.Error("a 201-character name was accepted")
	}
}
