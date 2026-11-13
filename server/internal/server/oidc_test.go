package server_test

// Sign-in with OIDC against a real (fake) provider: discovery, keys, the
// authorize redirect with PKCE and a nonce, the code exchange and a signed ID
// token. The Python's tests stubbed the OIDC client out; here the whole flow
// runs.

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"movienight/server/internal/crypto"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

// provider is a minimal OIDC provider: it "authorizes" whatever the test
// tells it to (claims), remembering each code's nonce and PKCE challenge.
type provider struct {
	*httptest.Server
	key      *rsa.PrivateKey
	clientID string
	mu       sync.Mutex
	codes    map[string]grant
	claims   map[string]any
	// badNonce makes the next ID token carry a different nonce.
	badNonce bool
}

type grant struct{ nonce, challenge string }

func newProvider(t *testing.T) *provider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{key: key, clientID: "movienight", codes: map[string]grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
			"jwks_uri": p.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		g, ok := p.codes[r.PostForm.Get("code")]
		delete(p.codes, r.PostForm.Get("code"))
		claims, bad := p.claims, p.badNonce
		p.mu.Unlock()
		// PKCE: the verifier must hash to the challenge sent at /authorize.
		if !ok || crypto.PKCEChallenge(r.PostForm.Get("code_verifier")) != g.challenge {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		nonce := g.nonce
		if bad {
			nonce = "someone-elses"
		}
		body := map[string]any{"iss": p.URL, "aud": p.clientID, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce}
		for k, v := range claims {
			body[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": p.sign(t, body)})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func (p *provider) sign(t *testing.T, claims map[string]any) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(claims)
	obj, err := signer.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := obj.CompactSerialize()
	return out
}

// authorize plays the person approving at the provider: it reads the
// authorize URL Movie Night sent the browser to and returns the callback
// path the provider would send it back to.
func (p *provider) authorize(t *testing.T, authorizeURL string, claims map[string]any) string {
	t.Helper()
	u, err := url.Parse(authorizeURL)
	if err != nil || !strings.HasPrefix(authorizeURL, p.URL+"/authorize") {
		t.Fatalf("not sent to the provider: %q", authorizeURL)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" || q.Get("client_id") != p.clientID ||
		q.Get("redirect_uri") != "http://mn.test/api/v1/auth/oidc/callback" {
		t.Fatalf("authorize request: %v", q)
	}
	code := crypto.NewToken(16)
	p.mu.Lock()
	p.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	p.claims = claims
	p.mu.Unlock()
	return "/api/v1/auth/oidc/callback?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
}

func oidcEnv(t *testing.T, p *provider) testbase.Env {
	env := testbase.BaseEnv(t)
	env["OIDC_ISSUER"], env["OIDC_CLIENT_ID"], env["OIDC_CLIENT_SECRET"] = p.URL, p.clientID, "secret"
	env["AUTH_DEV_ALLOWED"] = "false"
	return env
}

var person = map[string]any{"sub": "sub-roundtrip", "email": "rt@example.org", "name": "Round Trip", "preferred_username": "roundtrip"}

func TestOIDCSignInRoundTripReturnsToNext(t *testing.T) {
	p := newProvider(t)
	s := testenv.Start(t, oidcEnv(t, p))
	browser := s.Client("", false)
	login := browser.Do(t, "GET", "/api/v1/auth/oidc/login?next="+url.QueryEscape("/app/connect?challenge=x"), nil)
	if login.StatusCode != http.StatusFound {
		t.Fatalf("%d %s", login.StatusCode, login.Body)
	}
	back := browser.Do(t, "GET", p.authorize(t, login.Header.Get("Location"), person), nil)
	if back.StatusCode != http.StatusSeeOther || back.Header.Get("Location") != "/app/connect?challenge=x" {
		t.Fatalf("%d %q %s", back.StatusCode, back.Header.Get("Location"), back.Body)
	}
	if !signedIn(t, browser) {
		t.Fatal("not signed in after the callback")
	}
	var username, email string
	_ = s.Pool.QueryRow(t.Context(), "SELECT username, email FROM users WHERE oidc_sub = 'sub-roundtrip'").Scan(&username, &email)
	if username != "roundtrip" || email != "rt@example.org" {
		t.Fatal(username, email)
	}
}

func TestOIDCCallbackRefusesAnOffsiteNext(t *testing.T) {
	p := newProvider(t)
	s := testenv.Start(t, oidcEnv(t, p))
	browser := s.Client("", false)
	login := browser.Do(t, "GET", "/api/v1/auth/oidc/login?next="+url.QueryEscape("//evil.example.com"), nil)
	back := browser.Do(t, "GET", p.authorize(t, login.Header.Get("Location"), person), nil)
	if back.StatusCode != http.StatusSeeOther || back.Header.Get("Location") != "/" {
		t.Fatalf("%d %q", back.StatusCode, back.Header.Get("Location"))
	}
}

func TestOIDCCallbackOnlyInTheBrowserThatStartedIt(t *testing.T) {
	// Login CSRF: an attacker starts a sign-in and hands a victim the
	// callback link. The victim's browser lacks the flow's binder cookie.
	p := newProvider(t)
	s := testenv.Start(t, oidcEnv(t, p))
	attacker, victim := s.Client("", false), s.Client("", false)
	login := attacker.Do(t, "GET", "/api/v1/auth/oidc/login", nil)
	callback := p.authorize(t, login.Header.Get("Location"), person)
	r := victim.Do(t, "GET", callback, nil)
	if r.StatusCode != 401 || !strings.Contains(r.Body, "Signing in didn&#39;t work") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if signedIn(t, victim) {
		t.Fatal("the victim was signed in")
	}
	// And the state was used up: the attacker cannot finish it either.
	if r := attacker.Do(t, "GET", callback, nil); r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
}

func TestOIDCRefusesAWrongNonceAndAProviderError(t *testing.T) {
	p := newProvider(t)
	s := testenv.Start(t, oidcEnv(t, p))
	browser := s.Client("", false)
	login := browser.Do(t, "GET", "/api/v1/auth/oidc/login", nil)
	callback := p.authorize(t, login.Header.Get("Location"), person)
	p.mu.Lock()
	p.badNonce = true
	p.mu.Unlock()
	if r := browser.Do(t, "GET", callback, nil); r.StatusCode != 401 {
		t.Fatal("wrong nonce:", r.StatusCode)
	}
	if r := browser.Do(t, "GET", "/api/v1/auth/oidc/callback?error=access_denied", nil); r.StatusCode != 401 ||
		!strings.Contains(r.Header.Get("Content-Type"), "text/html") {
		t.Fatal("provider error:", r.StatusCode)
	}
	if signedIn(t, browser) {
		t.Fatal("signed in")
	}
	if !strings.Contains(s.Logs.String(), "nonce mismatch") {
		t.Fatal("the reason was not logged")
	}
}

func TestOIDCNeverTakesOverAnAccountLinkedToAnotherSubject(t *testing.T) {
	p := newProvider(t)
	s := testenv.Start(t, oidcEnv(t, p))
	first := s.Client("", false)
	login := first.Do(t, "GET", "/api/v1/auth/oidc/login", nil)
	first.Do(t, "GET", p.authorize(t, login.Header.Get("Location"), map[string]any{"sub": "sub-1", "preferred_username": "alice"}), nil)
	// Someone else at the provider picks the username "alice".
	other := s.Client("", false)
	login = other.Do(t, "GET", "/api/v1/auth/oidc/login", nil)
	r := other.Do(t, "GET", p.authorize(t, login.Header.Get("Location"), map[string]any{"sub": "sub-2", "preferred_username": "alice"}), nil)
	if r.StatusCode != 403 || signedIn(t, other) {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestOIDCLoginWhenOff(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	if r := s.Client("", false).Do(t, "GET", "/api/v1/auth/oidc/login", nil); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}
