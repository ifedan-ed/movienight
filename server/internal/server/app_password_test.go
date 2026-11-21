package server_test

// The phone app's username and password sign-in (POST /api/v1/auth/password),
// the methods it asks about first (GET /api/v1/auth/methods), and the Apple
// app-site association file that lets iOS fill saved passwords into the app.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"movienight/server/internal/passwords"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func appPassword(t *testing.T, s *testenv.Server, c *testenv.Client, username, password string) *testenv.Response {
	t.Helper()
	if c == nil {
		c = s.Client("", true)
	}
	body, _ := json.Marshal(map[string]string{"username": username, "password": password, "device": "Carol's iPhone (Movie Night app)"})
	return c.Do(t, "POST", "/api/v1/auth/password", string(body))
}

func detail(t *testing.T, r *testenv.Response) string {
	t.Helper()
	var out struct{ Detail string }
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	return out.Detail
}

func TestAppPasswordSignInGivesAWorkingDeviceToken(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	r := appPassword(t, s, nil, " Carol ", pw) // the form's rules: case and spaces do not matter
	var out struct {
		Ok       bool   `json:"ok"`
		Token    string `json:"token"`
		User     string `json:"user"`
		DeviceID int    `json:"device_id"`
	}
	r.JSON(t, &out)
	if r.StatusCode != 200 || !out.Ok || !strings.HasPrefix(out.Token, "mn_app_") || out.User != "carol" || out.DeviceID <= 0 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// The token opens the API as carol, and the phone is listed under the
	// name it sent, like a phone signed in through /app/connect.
	phone := bearer(s, out.Token, true)
	got := phone.Do(t, "GET", "/api/v1/me/devices", nil)
	var list []device
	got.JSON(t, &list)
	if got.StatusCode != 200 || len(list) != 1 || list[0].ID != out.DeviceID || list[0].Name != "Carol's iPhone (Movie Night app)" || list[0].Kind != "app" {
		t.Fatalf("%d %s", got.StatusCode, got.Body)
	}
	// Signing the phone out (the app's Sign out) ends the token.
	if r := phone.Do(t, "DELETE", "/api/v1/me/devices/"+strconv.Itoa(out.DeviceID), nil); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if r := phone.Do(t, "GET", "/api/v1/me/devices", nil); r.StatusCode != 401 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestAppPasswordSignInNamesAPhoneThatSentNoName(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	r := s.Client("", true).Do(t, "POST", "/api/v1/auth/password", `{"username":"carol","password":"`+pw+`"}`)
	var out struct{ Token string }
	r.JSON(t, &out)
	var list []device
	bearer(s, out.Token, true).Do(t, "GET", "/api/v1/me/devices", nil).JSON(t, &list)
	if len(list) != 1 || list[0].Name != "Movie Night app" {
		t.Fatal(list)
	}
}

func TestAppPasswordWrongPairAndUnknownUserSayTheSame(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	makeUser(t, s, "dave", "") // no password: OIDC only
	for _, pair := range [][2]string{{"carol", "nope nope nope"}, {"nobody", pw}, {"dave", pw}} {
		r := appPassword(t, s, nil, pair[0], pair[1])
		if r.StatusCode != 401 || detail(t, r) != "That username and password don't match." {
			t.Fatalf("%v: %d %s", pair, r.StatusCode, r.Body)
		}
	}
	// A 401 here is a wrong password, never "signed out": no browser
	// redirect to the sign-in page either.
	r := s.Client("", true).Do(t, "POST", "/api/v1/auth/password", `{"username":"carol","password":"x"}`, "Accept", "text/html")
	if r.StatusCode != 401 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestAppPasswordLockoutIsSharedWithTheSigninPage(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	// Three wrong in the app and two on the website: one counter for the
	// (username, address), so neither door is a way round the other.
	for i := 0; i < passwords.FailLimit-2; i++ {
		if r := appPassword(t, s, nil, "carol", "wrong password!"); r.StatusCode != 401 {
			t.Fatal(r.StatusCode)
		}
	}
	browser := s.Client("", false)
	for i := 0; i < 2; i++ {
		if r := signIn(t, browser, "carol", "wrong password!", "/"); r.StatusCode != 401 {
			t.Fatal(r.StatusCode)
		}
	}
	// Even the right password is refused while locked, in plain words.
	r := appPassword(t, s, nil, "carol", pw)
	if r.StatusCode != http.StatusTooManyRequests || detail(t, r) != "Too many wrong passwords for this username. Wait 15 minutes and try again." {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if r := signIn(t, browser, "carol", pw, "/"); r.StatusCode != http.StatusTooManyRequests {
		t.Fatal(r.StatusCode)
	}
}

func TestAppPasswordLockoutIsPerAddress(t *testing.T) {
	env := passwordEnv(t)
	env["TRUSTED_PROXY"] = "127.0.0.1"
	s := testenv.Start(t, env)
	makeUser(t, s, "carol", pw)
	stranger := s.Client("", true)
	stranger.Headers.Set("X-Forwarded-For", "203.0.113.9")
	for i := 0; i < passwords.FailLimit; i++ {
		appPassword(t, s, stranger, "carol", "wrong password!")
	}
	if r := appPassword(t, s, stranger, "carol", pw); r.StatusCode != http.StatusTooManyRequests {
		t.Fatal(r.StatusCode)
	}
	carol := s.Client("", true)
	carol.Headers.Set("X-Forwarded-For", "198.51.100.4")
	if r := appPassword(t, s, carol, "carol", pw); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestAppPasswordOffIsRefusedAndSaidSo(t *testing.T) {
	env := testbase.BaseEnv(t) // PASSWORD_SIGNIN off
	env["OIDC_ISSUER"], env["OIDC_CLIENT_ID"], env["OIDC_BUTTON_LABEL"] = "https://sso.test", "cid", "Sign in with the SSO provider"
	s := testenv.Start(t, env)
	makeUser(t, s, "carol", pw) // a password set earlier is not a way in
	r := appPassword(t, s, nil, "carol", pw)
	if r.StatusCode != 404 || detail(t, r) != "Signing in with a password is turned off here." {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	m := s.Client("", false).Do(t, "GET", "/api/v1/auth/methods", nil)
	if m.StatusCode != 200 || m.Body != `{"password":false,"oidc":true,"oidc_label":"Sign in with the SSO provider","error_reports":{"web":false,"app":false}}` {
		t.Fatalf("%d %s", m.StatusCode, m.Body)
	}
}

func TestAuthMethodsWithPasswordsOn(t *testing.T) {
	s := testenv.Start(t, passwordEnv(t))
	m := s.Client("", false).Do(t, "GET", "/api/v1/auth/methods", nil)
	if m.StatusCode != 200 || m.Body != `{"password":true,"oidc":false,"oidc_label":"Sign in","error_reports":{"web":false,"app":false}}` {
		t.Fatalf("%d %s", m.StatusCode, m.Body)
	}
}

func TestAppPasswordNeedsTheWriteHeader(t *testing.T) {
	// The API's one write rule holds here too: no X-Requested-With, no try
	// (and no count against the lockout).
	s := testenv.Start(t, passwordEnv(t))
	makeUser(t, s, "carol", pw)
	r := appPassword(t, s, s.Client("", false), "carol", pw)
	if r.StatusCode != 403 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

// apple-app-site-association names APPLE_APP_ID when it is set (with or
// without password sign-in), and is a 404, not the website's page, without.
func TestAppleAppSiteAssociation(t *testing.T) {
	for _, env := range []testbase.Env{testbase.BaseEnv(t), passwordEnv(t)} {
		env["APPLE_APP_ID"] = "ABCDE12345.com.example.movienight"
		s := testenv.Start(t, env)
		r := s.Client("", false).Do(t, "GET", "/.well-known/apple-app-site-association", nil, "Accept", "text/html")
		if r.StatusCode != 200 || r.Header.Get("Content-Type") != "application/json" ||
			r.Body != `{"webcredentials":{"apps":["ABCDE12345.com.example.movienight"]}}` {
			t.Fatalf("%d %q %s", r.StatusCode, r.Header.Get("Content-Type"), r.Body)
		}
	}
	s := testenv.Start(t, testbase.BaseEnv(t))
	if r := s.Client("", false).Do(t, "GET", "/.well-known/apple-app-site-association", nil, "Accept", "text/html"); r.StatusCode != 404 {
		t.Fatalf("unset: %d %s", r.StatusCode, r.Body)
	}
}
