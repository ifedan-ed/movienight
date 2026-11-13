package server_test

// Sign-in with a username and password, browser sessions, the account page
// and sign-out, plus what server-side sessions add (sign-out truly revokes).

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"movienight/server/internal/passwords"
	"movienight/server/internal/store"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

const pw = "correct horse battery"

func passwordEnv(t *testing.T) testbase.Env {
	env := testbase.BaseEnv(t)
	env["PASSWORD_SIGNIN"] = "true"
	return env
}

// makeUser creates a person with a password, through the real services.
func makeUser(t *testing.T, s *testenv.Server, name, password string) store.User {
	t.Helper()
	ctx := context.Background()
	u, err := s.App.Users.GetOrCreate(ctx, name, "", strings.ToUpper(name[:1])+name[1:])
	if err != nil {
		t.Fatal(err)
	}
	if password != "" {
		if u, err = s.App.Passwords.Set(ctx, u, password); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func formToken(t *testing.T, c *testenv.Client, path string) string {
	t.Helper()
	r := c.Do(t, "GET", path, nil)
	m := csrfRe.FindStringSubmatch(r.Body)
	if m == nil {
		t.Fatalf("no form token on %s (%d): %s", path, r.StatusCode, r.Body)
	}
	return m[1]
}

func signIn(t *testing.T, c *testenv.Client, username, password, next string) *testenv.Response {
	t.Helper()
	token := formToken(t, c, "/signin")
	return c.Do(t, "POST", "/signin", url.Values{"username": {username}, "password": {password}, "csrf": {token}, "next": {next}})
}

// signedIn reports whether c's session opens the API.
func signedIn(t *testing.T, c *testenv.Client) bool {
	t.Helper()
	r := c.Do(t, "GET", "/api/v1/me/devices", nil)
	switch r.StatusCode {
	case 200:
		return true
	case 401:
		return false
	}
	t.Fatalf("unexpected %d: %s", r.StatusCode, r.Body)
	return false
}

func TestRoutesNoClientCallsAreGone(t *testing.T) {
	// Dropped in phase 2: no client of the API calls them.
	s := testenv.Start(t, passwordEnv(t))
	c := s.Client("alice", true)
	for _, r := range [][2]string{{"GET", "/api/v1/auth/config"}, {"GET", "/api/v1/auth/oidc/config"},
		{"POST", "/api/v1/auth/oidc/logout"}, {"GET", "/api/v1/me/digests"}, {"GET", "/api/v1/ratings"},
		{"POST", "/api/v1/history/remove"}, {"POST", "/api/v1/title/tv/1399/episode/1/1/rating"}} {
		got := c.Do(t, r[0], r[1], nil)
		if got.StatusCode != http.StatusNotFound && got.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s: %d", r[0], r[1], got.StatusCode)
		}
	}
}

func TestSignedOutPageGoesToSigninWithNext(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	r := s.Client("", false).Do(t, "GET", "/lists?x=1", nil, "Accept", "text/html")
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/signin?next=/lists%3Fx%3D1" {
		t.Fatalf("%d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// A browser opening an API address while signed out goes to sign in too.
	r = s.Client("", false).Do(t, "GET", "/api/v1/me/devices", nil, "Accept", "text/html")
	if r.StatusCode != http.StatusFound || !strings.HasPrefix(r.Header.Get("Location"), "/signin?next=/api/v1/me/devices") {
		t.Fatalf("%d %q", r.StatusCode, r.Header.Get("Location"))
	}
	// An API caller keeps the JSON 401.
	r = s.Client("", false).Do(t, "GET", "/api/v1/me/devices", nil)
	if r.StatusCode != 401 || r.Body != `{"detail":"Not signed in"}` {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestSigninPageShowsFormAndOIDCWhenBoth(t *testing.T) {
	env := passwordEnv(t)
	env["OIDC_ISSUER"], env["OIDC_CLIENT_ID"], env["OIDC_BUTTON_LABEL"] = "https://sso.test", "cid", "Sign in with SSO"
	s := testenv.Start(t, env)
	r := s.Client("", false).Do(t, "GET", "/signin?next=/lists", nil)
	if r.StatusCode != 200 || !strings.Contains(r.Body, `name="password"`) || !strings.Contains(r.Body, "Sign in with SSO") ||
		!strings.Contains(r.Body, `href="/api/v1/auth/oidc/login?next=/lists"`) {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "script-src 'none'") {
		t.Fatal(r.Header.Get("Content-Security-Policy"))
	}
}

func TestOIDCOnlyGoesStraightToTheProvider(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["OIDC_ISSUER"], env["OIDC_CLIENT_ID"] = "https://sso.test", "cid"
	s := testenv.Start(t, env)
	r := s.Client("", false).Do(t, "GET", "/", nil, "Accept", "text/html")
	if !strings.HasPrefix(r.Header.Get("Location"), "/api/v1/auth/oidc/login?next=/") {
		t.Fatal(r.Header.Get("Location"))
	}
	// The phone app's connect page, signed out, comes back to itself.
	url := "/app/connect?challenge=" + strings.Repeat("A", 43) + "&redirect=movienight%3A%2F%2Fauth&device=Phone"
	r = s.Client("", false).Do(t, "GET", url, nil, "Accept", "text/html")
	if loc := r.Header.Get("Location"); !strings.HasPrefix(loc, "/api/v1/auth/oidc/login?next=/app/connect") || !strings.Contains(loc, "movienight") {
		t.Fatal(loc)
	}
}

func TestSignInAndUseTheAPI(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	r := signIn(t, c, "carol", pw, "/lists")
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/lists" {
		t.Fatalf("%d %q %s", r.StatusCode, r.Header.Get("Location"), r.Body)
	}
	if !signedIn(t, c) {
		t.Fatal("the session does not open the API")
	}
	// The shell is served now, not the sign-in redirect.
	if r := c.Do(t, "GET", "/lists", nil, "Accept", "text/html"); r.StatusCode != 200 || !strings.Contains(r.Body, `data-jf="https://jf.test"`) {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestUsernameIsCaseInsensitive(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	if r := signIn(t, s.Client("", false), "  Carol ", pw, "/"); r.StatusCode != http.StatusSeeOther {
		t.Fatal(r.StatusCode)
	}
}

func TestWrongPasswordAndUnknownUserSayTheSame(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	bad := signIn(t, c, "carol", "nope nope nope", "/")
	nobody := signIn(t, c, "nobody", pw, "/")
	if bad.StatusCode != 401 || nobody.StatusCode != 401 {
		t.Fatal(bad.StatusCode, nobody.StatusCode)
	}
	if !strings.Contains(bad.Body, "don&#39;t match") || !strings.Contains(nobody.Body, "don&#39;t match") {
		t.Fatal(bad.Body)
	}
}

func TestLockoutAfterFiveWrongPasswords(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	for i := 0; i < passwords.FailLimit; i++ {
		if r := signIn(t, c, "carol", "wrong password!", "/"); r.StatusCode != 401 {
			t.Fatal(r.StatusCode)
		}
	}
	if r := signIn(t, c, "carol", pw, "/"); r.StatusCode != http.StatusTooManyRequests { // even the right one, while locked
		t.Fatal(r.StatusCode)
	}
}

func TestLockoutDoesNotShutOutThePersonFromTheirOwnAddress(t *testing.T) {
	// The test client's address plays Caddy (TRUSTED_PROXY): five wrong
	// guesses from a stranger's address lock only that address.
	env := passwordEnv(t)
	env["TRUSTED_PROXY"] = "127.0.0.1"
	s := testenv.Start(t, env)
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	c.Headers.Set("X-Forwarded-For", "203.0.113.9")
	for i := 0; i < passwords.FailLimit; i++ {
		signIn(t, c, "carol", "wrong password!", "/")
	}
	if r := signIn(t, c, "carol", pw, "/"); r.StatusCode != http.StatusTooManyRequests {
		t.Fatal(r.StatusCode)
	}
	c.Headers.Set("X-Forwarded-For", "198.51.100.4")
	if r := signIn(t, c, "carol", pw, "/"); r.StatusCode != http.StatusSeeOther {
		t.Fatal(r.StatusCode)
	}
}

func TestForwardedForIsIgnoredFromAnUntrustedPeer(t *testing.T) {
	// TRUSTED_PROXY=none: a caller cannot dodge the lockout by sending a new
	// X-Forwarded-For with every guess.
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	for i := 0; i < passwords.FailLimit; i++ {
		c.Headers.Set("X-Forwarded-For", "203.0.113."+string(rune('1'+i)))
		signIn(t, c, "carol", "wrong password!", "/")
	}
	c.Headers.Set("X-Forwarded-For", "198.51.100.4")
	if r := signIn(t, c, "carol", pw, "/"); r.StatusCode != http.StatusTooManyRequests {
		t.Fatal(r.StatusCode)
	}
}

func TestFormWithoutItsTokenIsRefused(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	c.Do(t, "GET", "/signin", nil)
	r := c.Do(t, "POST", "/signin", url.Values{"username": {"carol"}, "password": {pw}, "csrf": {"forged"}})
	if r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	// Another browser's token does not work either.
	other := formToken(t, s.Client("", false), "/signin")
	r = c.Do(t, "POST", "/signin", url.Values{"username": {"carol"}, "password": {pw}, "csrf": {other}})
	if r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}

func TestNextNeverLeavesTheSite(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	for _, next := range []string{"//evil.example/x", "https://evil.example", `/\evil.example`} {
		if r := signIn(t, s.Client("", false), "carol", pw, next); r.Header.Get("Location") != "/" {
			t.Fatalf("%s -> %s", next, r.Header.Get("Location"))
		}
	}
}

func TestPasswordOffRefusesTheForm(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t)) // PASSWORD_SIGNIN off
	c := s.Client("", false)
	r := c.Do(t, "POST", "/signin", url.Values{"username": {"carol"}, "password": {pw}})
	if r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}

func TestUserWithoutPasswordCannotSignInWithOne(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "dave", "")
	if r := signIn(t, s.Client("", false), "dave", "", "/"); r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
}

func TestChangePasswordSignsOtherBrowsersOut(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	one, two := s.Client("", false), s.Client("", false)
	signIn(t, one, "carol", pw, "/")
	signIn(t, two, "carol", pw, "/")
	token := formToken(t, one, "/account")
	r := one.Do(t, "POST", "/account/password", url.Values{"current": {pw}, "new": {"a brand new password"},
		"again": {"a brand new password"}, "csrf": {token}})
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/account?done=1" {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if !signedIn(t, one) {
		t.Fatal("the browser that changed it was signed out")
	}
	if signedIn(t, two) {
		t.Fatal("the other browser is still signed in")
	}
	if r := signIn(t, s.Client("", false), "carol", "a brand new password", "/"); r.StatusCode != http.StatusSeeOther {
		t.Fatal(r.StatusCode)
	}
	// The account page says it was saved.
	if r := one.Do(t, "GET", "/account?done=1", nil); !strings.Contains(r.Body, "Password saved") {
		t.Fatal(r.Body)
	}
}

func TestChangePasswordChecksCurrentAndLength(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	signIn(t, c, "carol", pw, "/")
	token := formToken(t, c, "/account")
	post := func(current, next, again string) *testenv.Response {
		return c.Do(t, "POST", "/account/password", url.Values{"current": {current}, "new": {next}, "again": {again}, "csrf": {token}})
	}
	if r := post("not it at all", strings.Repeat("x", 12), strings.Repeat("x", 12)); r.StatusCode != 401 {
		t.Fatal("wrong current", r.StatusCode)
	}
	if r := post(pw, "short", "short"); r.StatusCode != 400 || !strings.Contains(r.Body, "at least 10") {
		t.Fatal("short", r.StatusCode)
	}
	if r := post(pw, strings.Repeat("x", 12), strings.Repeat("y", 12)); r.StatusCode != 400 {
		t.Fatal("differ", r.StatusCode)
	}
}

func TestSignOutTrulyRevokesTheSession(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	signIn(t, c, "carol", pw, "/")
	// Keep a copy of the cookie, as a thief would.
	u, _ := url.Parse(s.URL)
	stolen := c.Jar.Cookies(u)
	token := formToken(t, c, "/account")
	r := c.Do(t, "POST", "/signout", url.Values{"csrf": {token}})
	if r.Header.Get("Location") != "/signin" {
		t.Fatal(r.Header.Get("Location"))
	}
	if signedIn(t, c) {
		t.Fatal("still signed in")
	}
	thief := s.Client("", false)
	thief.Jar.SetCookies(u, stolen)
	if signedIn(t, thief) {
		t.Fatal("a copied cookie still works after sign-out (the Python's signed cookie did)")
	}
}

func TestAPasswordResetElsewhereEndsEverySession(t *testing.T) {
	// An admin's reset (or any epoch rise) ends sessions even though their
	// rows were not deleted: the epoch is checked on every request.
	s := testenv.Start(t, passwordEnv(t))
	u := makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	signIn(t, c, "carol", pw, "/")
	if _, err := s.App.Users.SetPassword(context.Background(), u.ID, nil); err != nil {
		t.Fatal(err)
	}
	if signedIn(t, c) {
		t.Fatal("a session from before the reset still counts")
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	env := passwordEnv(t)
	env["PUBLIC_URL"] = "https://mn.test"
	s := testenv.Start(t, env)
	makeUser(t, s, "carol", pw)
	c := s.Client("", false)
	// The form cookie is Secure here, so the jar will not send it back over
	// the test's plain http: read it from the answer and hand it over.
	page := c.Do(t, "GET", "/signin", nil)
	token := csrfRe.FindStringSubmatch(page.Body)[1]
	var binder string
	for _, ck := range page.Cookies() {
		if ck.Name == "mn_form" {
			binder = ck.Value
			if !ck.Secure || ck.SameSite != http.SameSiteStrictMode {
				t.Fatalf("form cookie attributes: %+v", ck)
			}
		}
	}
	r := c.Do(t, "POST", "/signin", url.Values{"username": {"carol"}, "password": {pw}, "csrf": {token}}, "Cookie", "mn_form="+binder)
	set := strings.Join(r.Header.Values("Set-Cookie"), "\n")
	if r.StatusCode != http.StatusSeeOther || !strings.Contains(set, "mn_session=") || !strings.Contains(set, "HttpOnly") ||
		!strings.Contains(set, "Secure") || !strings.Contains(set, "SameSite=Lax") {
		t.Fatalf("%d %s", r.StatusCode, set)
	}
	// The database keeps only the cookie's hash.
	var hash string
	if err := s.Pool.QueryRow(context.Background(), "SELECT id_hash FROM web_sessions").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(set, hash) {
		t.Fatal("the cookie holds what the database keeps")
	}
}
