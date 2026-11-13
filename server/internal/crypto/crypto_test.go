package crypto

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// pythonFixtures are values the Python app produced
// (SECRET_KEY=fixture-secret-key).
type pythonFixtures struct {
	SecretKey string `json:"secret_key"`
	Scrypt    []struct{ Password, Hash string }
	Fernet    []struct{ Plain, Token string }
	TokenHash []struct{ Token, Hash string } `json:"token_hash"`
	PKCE      struct{ Verifier, Challenge string }
	FormSig   struct {
		Username, Challenge, Redirect, Device, Sig string
		TS                                         int64
	} `json:"form_sig"`
}

func load(t *testing.T) pythonFixtures {
	t.Helper()
	raw, err := os.ReadFile("testdata/python.json")
	if err != nil {
		t.Fatal(err)
	}
	var f pythonFixtures
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPythonPasswordHashesVerify(t *testing.T) {
	f := load(t)
	if len(f.Scrypt) == 0 {
		t.Fatal("no fixtures")
	}
	for _, c := range f.Scrypt {
		if !CheckPassword(c.Password, c.Hash) {
			t.Errorf("a hash Python made did not verify (password of %d chars)", len(c.Password))
		}
		if CheckPassword(c.Password+"x", c.Hash) {
			t.Error("a wrong password verified")
		}
	}
}

func TestGoPasswordHashesHaveThePythonFormat(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "scrypt$32768$8$1$") || strings.Count(h, "$") != 5 {
		t.Fatalf("unexpected format %q", h)
	}
	if !CheckPassword("correct horse battery", h) || CheckPassword("correct horse batterY", h) {
		t.Fatal("round trip failed")
	}
}

func TestMalformedHashesNeverMatch(t *testing.T) {
	for _, stored := range []string{
		"", "garbage", "bcrypt$1$2$3$a$b",
		"scrypt$32768$8$1$!!!$AAAA",
		"scrypt$33$8$1$AAAA$AAAA",         // N not a power of two
		"scrypt$1073741824$8$1$AAAA$AAAA", // N far too large: refused, not computed
		"scrypt$32768$8$1$AAAA$",          // no hash
	} {
		if CheckPassword("x", stored) {
			t.Errorf("%q matched", stored)
		}
	}
}

func TestPythonFernetTokensDecrypt(t *testing.T) {
	f := load(t)
	k, err := NewKeys(f.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Fernet {
		got, err := k.Fernet().Decrypt(c.Token)
		if err != nil || got != c.Plain {
			t.Errorf("decrypt %q: got %q, %v", c.Token, got, err)
		}
	}
	other, _ := NewKeys("another-secret")
	if _, err := other.Fernet().Decrypt(f.Fernet[0].Token); err == nil {
		t.Error("a token decrypted under the wrong SECRET_KEY")
	}
}

func TestFernetRoundTripAndTampering(t *testing.T) {
	k, _ := NewKeys("s")
	tok := k.Fernet().Encrypt("jellyfin token")
	if got, err := k.Fernet().Decrypt(tok); err != nil || got != "jellyfin token" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	// Every single-character change must be refused (MAC or base64).
	b := []byte(tok)
	b[len(b)/2] ^= 1
	if _, err := k.Fernet().Decrypt(string(b)); err == nil {
		t.Error("a tampered token decrypted")
	}
	for _, bad := range []string{"", "gAAAA", "not base64 at all!"} {
		if _, err := k.Fernet().Decrypt(bad); err == nil {
			t.Errorf("%q decrypted", bad)
		}
	}
	// A fixed IV and time give a fixed token: the layout is the spec's.
	iv := make([]byte, 16)
	got := k.Fernet().encryptWith("x", iv, time.Unix(0, 0))
	if !strings.HasPrefix(got, "gAAAAAAAAAAA") { // 0x80 then a zero timestamp
		t.Errorf("unexpected layout %q", got)
	}
}

func TestTokenHashAndPKCEMatchPython(t *testing.T) {
	f := load(t)
	for _, c := range f.TokenHash {
		if got := TokenHash(c.Token); got != c.Hash {
			t.Errorf("TokenHash(%q) = %s, Python %s", c.Token, got, c.Hash)
		}
	}
	if got := PKCEChallenge(f.PKCE.Verifier); got != f.PKCE.Challenge {
		t.Errorf("PKCE challenge %s, Python %s", got, f.PKCE.Challenge)
	}
}

func TestConnectSigMatchesPython(t *testing.T) {
	f := load(t)
	k, _ := NewKeys(f.SecretKey)
	s := f.FormSig
	if got := k.ConnectSig(s.Username, s.Challenge, s.Redirect, s.Device, s.TS); got != s.Sig {
		t.Errorf("ConnectSig = %s, Python %s", got, s.Sig)
	}
}

func TestNewTokenShape(t *testing.T) {
	a, b := NewToken(32), NewToken(32)
	if len(a) != 43 || a == b || strings.ContainsAny(a, "+/=") {
		t.Fatalf("unexpected tokens %q %q", a, b)
	}
}

func TestKeysAreSeparate(t *testing.T) {
	k, _ := NewKeys("s")
	if k.FormToken("x") == k.ConnectSig("x", "", "", "", 0) {
		t.Fatal("two uses share a key")
	}
	if _, err := NewKeys(""); err == nil {
		t.Fatal("an empty SECRET_KEY was accepted")
	}
}
