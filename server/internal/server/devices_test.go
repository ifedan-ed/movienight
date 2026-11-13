package server_test

// The phone app's sign-in (connect page, PKCE code swap, device tokens) and
// TV pairing, plus the
// server-to-server checks (scrobble tokens, the Jellyfin plugin secret).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"movienight/server/internal/crypto"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

var hiddenRe = regexp.MustCompile(`name="(\w+)" value="([^"]*)"`)

// signInApp goes through the connect page as c and returns the one-time code.
func signInApp(t *testing.T, c *testenv.Client, verifier, device string) string {
	t.Helper()
	q := url.Values{"challenge": {crypto.PKCEChallenge(verifier)}, "redirect": {"movienight://auth"}, "device": {device}}
	page := c.Do(t, "GET", "/app/connect?"+q.Encode(), nil)
	if page.StatusCode != 200 || !strings.Contains(page.Body, "Sign in the app?") {
		t.Fatalf("connect page %d: %s", page.StatusCode, page.Body)
	}
	if !strings.Contains(page.Body, `href="movienight://auth?error=cancelled"`) {
		t.Fatalf("the cancel link was not kept: %s", page.Body)
	}
	form := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page.Body, -1) {
		form.Set(m[1], m[2])
	}
	r := c.Do(t, "POST", "/app/connect", form)
	loc := r.Header.Get("Location")
	if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "movienight://auth?code=") {
		t.Fatalf("%d %q %s", r.StatusCode, loc, r.Body)
	}
	return strings.TrimPrefix(loc, "movienight://auth?code=")
}

func swap(t *testing.T, s *testenv.Server, code, verifier string) *testenv.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"code": code, "verifier": verifier})
	return s.Client("", true).Do(t, "POST", "/api/v1/app/token", string(body))
}

func appToken(t *testing.T, s *testenv.Server, user string) string {
	t.Helper()
	verifier := crypto.NewToken(48)
	code := signInApp(t, s.Client(user, false), verifier, "Test phone")
	r := swap(t, s, code, verifier)
	var out struct{ Token string }
	if r.StatusCode != 200 || json.Unmarshal([]byte(r.Body), &out) != nil {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	return out.Token
}

func bearer(s *testenv.Server, token string, csrf bool) *testenv.Client {
	c := s.Client("", csrf)
	c.Headers.Set("Authorization", "Bearer "+token)
	return c
}

type device struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	CreatedAt      string  `json:"created_at"`
	LastUsedAt     *string `json:"last_used_at"`
	Kind           string  `json:"kind"`
	ExpiresAt      string  `json:"expires_at"`
	ExpiresBecause string  `json:"expires_because"`
}

func listDevices(t *testing.T, c *testenv.Client) []device {
	t.Helper()
	r := c.Do(t, "GET", "/api/v1/me/devices", nil)
	var out []device
	if r.StatusCode != 200 || json.Unmarshal([]byte(r.Body), &out) != nil {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	return out
}

func TestAppSignInRoundTrip(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	alice := s.Client("alice", true)
	verifier := crypto.NewToken(48)
	code := signInApp(t, s.Client("alice", false), verifier, "Test phone")

	if r := swap(t, s, code, crypto.NewToken(48)); r.StatusCode != 400 || !strings.Contains(r.Body, "meant for another app") {
		t.Fatalf("someone else's verifier: %d %s", r.StatusCode, r.Body)
	}
	ok := swap(t, s, code, verifier) // the wrong verifier did not burn the code
	if ok.StatusCode != 200 {
		t.Fatalf("%d %s", ok.StatusCode, ok.Body)
	}
	var got struct {
		Ok       bool   `json:"ok"`
		Token    string `json:"token"`
		User     string `json:"user"`
		DeviceID int    `json:"device_id"`
	}
	_ = json.Unmarshal([]byte(ok.Body), &got)
	if !got.Ok || got.User != "alice" || !strings.HasPrefix(got.Token, "mn_app_") || got.DeviceID == 0 {
		t.Fatalf("%+v", got)
	}
	if r := swap(t, s, code, verifier); r.StatusCode != 400 || !strings.Contains(r.Body, "expired") {
		t.Fatalf("one use only: %d %s", r.StatusCode, r.Body)
	}

	phone := bearer(s, got.Token, true)
	devs := listDevices(t, phone)
	if len(devs) != 1 || devs[0].Name != "Test phone" || devs[0].Kind != "app" || devs[0].LastUsedAt == nil {
		t.Fatalf("%+v", devs)
	}
	if r := alice.Do(t, "DELETE", "/api/v1/me/devices/"+itoa(devs[0].ID), nil); r.StatusCode != 200 || r.Body != `{"ok":true}` {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if r := phone.Do(t, "GET", "/api/v1/me/devices", nil); r.StatusCode != 401 || !strings.Contains(r.Body, "signed out") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if r := alice.Do(t, "DELETE", "/api/v1/me/devices/"+itoa(devs[0].ID), nil); r.StatusCode != 404 {
		t.Fatal("deleting twice:", r.StatusCode)
	}
	if r := alice.Do(t, "DELETE", "/api/v1/me/devices/0", nil); r.StatusCode != 422 {
		t.Fatal("device id 0:", r.StatusCode, r.Body)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestConnectPageRefusesOtherRedirectsAndForgedForms(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	alice := s.Client("alice", false)
	challenge := crypto.PKCEChallenge(crypto.NewToken(48))
	for _, redirect := range []string{"https://evil.example/cb", "exp://192.168.1.2:8081/--/auth", "movienight://auth?x=1"} {
		q := url.Values{"challenge": {challenge}, "redirect": {redirect}}
		if r := alice.Do(t, "GET", "/app/connect?"+q.Encode(), nil); r.StatusCode != 400 {
			t.Fatalf("%s: %d", redirect, r.StatusCode)
		}
	}
	r := alice.Do(t, "POST", "/app/connect", url.Values{"challenge": {challenge}, "redirect": {"movienight://auth"},
		"device": {"x"}, "ts": {"1"}, "sig": {strings.Repeat("0", 64)}})
	if r.StatusCode != 400 {
		t.Fatal("unsigned form:", r.StatusCode)
	}
	// A page signed for alice does not work for bob.
	q := url.Values{"challenge": {challenge}, "redirect": {"movienight://auth"}, "device": {"Phone"}}
	page := alice.Do(t, "GET", "/app/connect?"+q.Encode(), nil)
	form := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page.Body, -1) {
		form.Set(m[1], m[2])
	}
	if r := s.Client("bob", false).Do(t, "POST", "/app/connect", form); r.StatusCode != 400 {
		t.Fatal("another person's page:", r.StatusCode)
	}
}

func TestExpoRedirectsOnlyWhenAllowed(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["APP_DEV_REDIRECTS"] = "true"
	s := testenv.Start(t, env)
	q := url.Values{"challenge": {crypto.PKCEChallenge(crypto.NewToken(48))}, "redirect": {"exp://192.168.1.2:8081/--/auth"}}
	if r := s.Client("alice", false).Do(t, "GET", "/app/connect?"+q.Encode(), nil); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
}

func TestBadBearerIsRefused(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	for _, token := range []string{"mn_app_nope", "something", "mn_app_" + strings.Repeat("x", 300)} {
		if r := bearer(s, token, false).Do(t, "GET", "/api/v1/me/devices", nil); r.StatusCode != 401 {
			t.Fatal(token, r.StatusCode)
		}
	}
}

func TestUnusedAndOldDeviceTokensRunOut(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	ctx := context.Background()
	now := time.Now()
	cases := []struct {
		created, used time.Time
		why           string
	}{
		{now.AddDate(0, 0, -366), now, "it signed in 365 days ago"},                     // in use, but over a year old
		{now.AddDate(0, 0, -200), now.AddDate(0, 0, -91), "it wasn't used for 90 days"}, // not used for months
	}
	for _, c := range cases {
		token := appToken(t, s, "alice")
		if _, err := s.Pool.Exec(ctx, "UPDATE app_tokens SET created_at = $1, last_used_at = $2", c.created, c.used); err != nil {
			t.Fatal(err)
		}
		r := bearer(s, token, false).Do(t, "GET", "/api/v1/me/devices", nil)
		if r.StatusCode != 401 || !strings.Contains(r.Body, c.why) {
			t.Fatalf("%d %s", r.StatusCode, r.Body)
		}
		if devs := listDevices(t, s.Client("alice", false)); len(devs) != 0 {
			t.Fatalf("still listed: %+v", devs)
		}
	}
	token := appToken(t, s, "alice")
	if r := bearer(s, token, false).Do(t, "GET", "/api/v1/me/devices", nil); r.StatusCode != 200 {
		t.Fatal("a fresh one does not work:", r.StatusCode)
	}
}

func TestAnExpiredDeviceLeavesTheListWithoutBeingUsed(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	appToken(t, s, "alice")
	if _, err := s.Pool.Exec(context.Background(), "UPDATE app_tokens SET last_used_at = now() - interval '91 days'"); err != nil {
		t.Fatal(err)
	}
	appToken(t, s, "alice") // a live one, made after
	if devs := listDevices(t, s.Client("alice", false)); len(devs) != 1 {
		t.Fatalf("%+v", devs)
	}
}

func TestSignedInDevicesShowWhenTheyRunOut(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	appToken(t, s, "alice")
	alice := s.Client("alice", false)
	d := listDevices(t, alice)[0]
	created, err := time.Parse(time.RFC3339Nano, d.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	expires, _ := time.Parse(time.RFC3339Nano, d.ExpiresAt)
	if d.ExpiresBecause != "unused" || expires.Sub(created.AddDate(0, 0, 90)).Abs() > 5*time.Second {
		t.Fatalf("%+v", d)
	}
	// Used every day for ten months: what is left is the year since it signed in.
	if _, err := s.Pool.Exec(context.Background(), "UPDATE app_tokens SET created_at = $1, last_used_at = $2",
		created.AddDate(0, 0, -300), created); err != nil {
		t.Fatal(err)
	}
	if d := listDevices(t, alice)[0]; d.ExpiresBecause != "age" {
		t.Fatalf("%+v", d)
	}
}

// ---------------------------------------------------------------------------
// TVs
// ---------------------------------------------------------------------------

var tvCodeRe = regexp.MustCompile(`^[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}$`)

type pairStart struct {
	Code      string `json:"code"`
	Poll      string `json:"poll"`
	ExpiresIn int    `json:"expires_in"`
	Interval  int    `json:"interval"`
}

func startPair(t *testing.T, tv *testenv.Client, device string) pairStart {
	t.Helper()
	r := tv.Do(t, "POST", "/api/v1/scrobble/pair", `{"device":"`+device+`"}`)
	var out pairStart
	if r.StatusCode != 200 || json.Unmarshal([]byte(r.Body), &out) != nil {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	return out
}

func poll(t *testing.T, tv *testenv.Client, secret string) *testenv.Response {
	return tv.Do(t, "POST", "/api/v1/scrobble/pair/poll", `{"poll":"`+secret+`"}`)
}

// pair signs a TV in for user and returns its token and code.
func pair(t *testing.T, s *testenv.Server, user, device string) (string, string) {
	t.Helper()
	tv := s.Client("", false) // Kodi: no sign-in, no browser header
	start := startPair(t, tv, device)
	if !tvCodeRe.MatchString(start.Code) || start.ExpiresIn != 600 || start.Interval != 3 {
		t.Fatalf("%+v", start)
	}
	if r := poll(t, tv, start.Poll); r.Body != `{"status":"waiting","token":null,"user":null,"account":null}` {
		t.Fatal(r.Body)
	}
	typed := strings.ToLower(strings.ReplaceAll(start.Code, "-", " "))
	confirm := s.Client(user, true).Do(t, "POST", "/api/v1/me/devices/pair", `{"code":"`+typed+`"}`)
	if confirm.StatusCode != 200 || confirm.Body != `{"ok":true,"device":"`+device+`"}` {
		t.Fatalf("%d %s", confirm.StatusCode, confirm.Body)
	}
	r := poll(t, tv, start.Poll)
	var got struct{ Status, Token, User, Account string }
	_ = json.Unmarshal([]byte(r.Body), &got)
	if got.Status != "ok" || !strings.HasPrefix(got.Token, "mn_kodi_") || got.Account != user {
		t.Fatalf("%s", r.Body)
	}
	if r := poll(t, tv, start.Poll); r.StatusCode != http.StatusGone { // handed over once
		t.Fatal(r.StatusCode)
	}
	return got.Token, start.Code
}

func TestKodiPairsAndItsTokenOpensNothingElse(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	token, _ := pair(t, s, "alice", "Kodi on SHIELD")
	if r := bearer(s, token, true).Do(t, "GET", "/api/v1/me/devices", nil); r.StatusCode != 401 {
		t.Fatal("a TV token opened the API:", r.StatusCode)
	}
	devs := listDevices(t, s.Client("alice", false))
	if len(devs) != 1 || devs[0].Name != "Kodi on SHIELD" || devs[0].Kind != "scrobble" {
		t.Fatalf("%+v", devs)
	}
}

func TestWrongUsedAndExpiredPairCodes(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	alice, bob := s.Client("alice", true), s.Client("bob", true)
	if r := alice.Do(t, "POST", "/api/v1/me/devices/pair", `{"code":"ABCD-EFGH"}`); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
	_, code := pair(t, s, "alice", "Kodi")
	if r := bob.Do(t, "POST", "/api/v1/me/devices/pair", `{"code":"`+code+`"}`); r.StatusCode != 404 { // collected and gone
		t.Fatal(r.StatusCode)
	}
	tv := s.Client("", false)
	start := startPair(t, tv, "Kodi")
	if _, err := s.Pool.Exec(context.Background(), "UPDATE pair_codes SET expires_at = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if r := alice.Do(t, "POST", "/api/v1/me/devices/pair", `{"code":"`+start.Code+`"}`); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
	if r := poll(t, tv, start.Poll); r.StatusCode != http.StatusGone {
		t.Fatal(r.StatusCode)
	}
}

func TestACodeConfirmedOnceCannotBeConfirmedAgain(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	start := startPair(t, s.Client("", false), "Kodi")
	if r := s.Client("alice", true).Do(t, "POST", "/api/v1/me/devices/pair", `{"code":"`+start.Code+`"}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if r := s.Client("bob", true).Do(t, "POST", "/api/v1/me/devices/pair", `{"code":"`+start.Code+`"}`); r.StatusCode != 409 {
		t.Fatal(r.StatusCode)
	}
	// A confirmed but uncollected code leaves no device behind.
	if devs := listDevices(t, s.Client("alice", false)); len(devs) != 0 {
		t.Fatalf("%+v", devs)
	}
}

func TestOneAddressCannotBlockPairingForEveryTV(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["TRUSTED_PROXY"] = "127.0.0.0/8"
	s := testenv.Start(t, env)
	tv := s.Client("", false)
	tv.Headers.Set("X-Forwarded-For", "203.0.113.9")
	for i := 0; i < 10; i++ {
		startPair(t, tv, "Kodi")
	}
	if r := tv.Do(t, "POST", "/api/v1/scrobble/pair", `{"device":"Kodi"}`); r.StatusCode != http.StatusTooManyRequests ||
		!strings.Contains(r.Body, "Too many sign-ins from here") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// Another household behind the same proxy is not affected (in the
	// Python, every TV behind Caddy shared Caddy's one address).
	other := s.Client("", false)
	other.Headers.Set("X-Forwarded-For", "198.51.100.4")
	startPair(t, other, "Kodi")
	// An IPv6 /64 counts as one address.
	v6 := s.Client("", false)
	for i := 0; i < 10; i++ {
		v6.Headers.Set("X-Forwarded-For", "2001:db8:1:2::"+strconv.FormatInt(int64(i+1), 16))
		startPair(t, v6, "Kodi")
	}
	v6.Headers.Set("X-Forwarded-For", "2001:db8:1:2::ffff")
	if r := v6.Do(t, "POST", "/api/v1/scrobble/pair", `{}`); r.StatusCode != http.StatusTooManyRequests {
		t.Fatal(r.StatusCode)
	}
}

func TestTooManyCodesWaitingInstallWide(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["PAIR_CODES_WAITING"] = "2"
	s := testenv.Start(t, env)
	tv := s.Client("", false)
	startPair(t, tv, "Kodi")
	startPair(t, tv, "Kodi")
	if r := tv.Do(t, "POST", "/api/v1/scrobble/pair", `{"device":"Kodi"}`); r.StatusCode != 429 || !strings.Contains(r.Body, "Too many sign-ins are waiting") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

// ---------------------------------------------------------------------------
// Server-to-server
// ---------------------------------------------------------------------------

// scrobbleProbe answers with whose a scrobble call is, through the same
// Authenticator the scrobble routes will use (phase 2 adds those routes).
func scrobbleProbe(s *testenv.Server) *httptest.Server {
	h := s.App.Auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jellyfin" {
			if err := s.App.Auth.JellyfinSecret(r.Context()); err != nil {
				http.Error(w, err.Error(), 401)
				return
			}
			_, _ = w.Write([]byte("ok"))
			return
		}
		u, err := s.App.Auth.Scrobbler(r.Context())
		if err != nil {
			http.Error(w, err.Error(), 401)
			return
		}
		_, _ = w.Write([]byte(u.Username))
	}))
	return httptest.NewServer(h)
}

func probe(t *testing.T, srv *httptest.Server, path string, headers ...string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func TestScrobbleTokensAndTheJellyfinSecret(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	srv := scrobbleProbe(s)
	defer srv.Close()
	kodi, _ := pair(t, s, "alice", "Kodi")
	phone := appToken(t, s, "bob")
	if code, who := probe(t, srv, "/", "Authorization", "Bearer "+kodi); code != 200 || who != "alice" {
		t.Fatal("TV token:", code, who)
	}
	if code, who := probe(t, srv, "/", "X-MovieNight-Token", phone); code != 200 || who != "bob" {
		t.Fatal("phone token in X-MovieNight-Token:", code, who)
	}
	// A person's own scrobble token (users.scrobble_token_hash).
	own := "personal-scrobble-token-0123456789"
	if _, err := s.Pool.Exec(context.Background(), "UPDATE users SET scrobble_token_hash = $1 WHERE username = 'alice'", crypto.TokenHash(own)); err != nil {
		t.Fatal(err)
	}
	if code, who := probe(t, srv, "/", "X-MovieNight-Token", own); code != 200 || who != "alice" {
		t.Fatal("own token:", code, who)
	}
	if code, msg := probe(t, srv, "/", "X-MovieNight-Token", "mn_kodi_unknown"); code != 401 || !strings.Contains(msg, "signed out of Movie Night") {
		t.Fatal(code, msg)
	}
	if code, msg := probe(t, srv, "/"); code != 401 || msg != "Bad or missing token" {
		t.Fatal(code, msg)
	}
	if code, _ := probe(t, srv, "/jellyfin", "X-MovieNight-Token", "hook-secret"); code != 200 {
		t.Fatal("the right secret:", code)
	}
	if code, _ := probe(t, srv, "/jellyfin", "X-MovieNight-Token", "hook-secreT"); code != 401 {
		t.Fatal("a wrong secret:", code)
	}
}
