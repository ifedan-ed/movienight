package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func base() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgresql://u:p@db/mn", "VALKEY_URL": "redis://kv:6379/0", "SECRET_KEY": "s",
		"PUBLIC_URL": "https://mn.example/", "TRUSTED_PROXY": "172.18.0.1", "WEB_DIR": "/app/web",
		"OWNER_USERNAME": "Alice", "JELLYFIN_BASE_URL": "http://jellyfin:8096", "JELLYFIN_PUBLIC_URL": "https://jf.example",
		"JELLYFIN_API_KEY": "k", "JELLYFIN_USER_ID": "u", "TMDB_API_KEY": "t", "MEILI_BASE_URL": "http://meili:7700", "TIMEZONE": "Europe/London",
	}
}

func load(m map[string]string) (*Config, error) {
	return LoadFrom(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
}

func TestACompleteInstallLoadsWithTheDocumentedDefaults(t *testing.T) {
	c, err := load(base())
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "https://mn.example" || c.OwnerUsername != "alice" || !c.IsAdmin("alice") || c.IsAdmin("admin") {
		t.Fatalf("%+v", c)
	}
	if c.SessionDays != 30 || c.DeviceIdleDays != 90 || c.DeviceMaxDays != 365 || c.PasswordMinLength != 10 ||
		c.OIDCRedirectURL != "https://mn.example/api/v1/auth/oidc/callback" || strings.Join(c.OIDCScopes, " ") != "openid email profile" {
		t.Fatalf("%+v", c)
	}
	if !c.SecureCookies() || c.OIDCEnabled() || c.SigninEnabled() || c.SeerrEnabled() || c.AIEnabled() {
		t.Fatalf("%+v", c)
	}
	if len(c.TrustedProxies) != 1 || c.TrustedProxies[0].String() != "172.18.0.1/32" {
		t.Fatal(c.TrustedProxies)
	}
}

func TestEveryMissingRequiredSettingIsNamedAtOnce(t *testing.T) {
	_, err := load(map[string]string{})
	if err == nil {
		t.Fatal("an empty environment loaded")
	}
	for _, name := range []string{"DATABASE_URL", "VALKEY_URL", "SECRET_KEY", "PUBLIC_URL", "TRUSTED_PROXY", "WEB_DIR",
		"OWNER_USERNAME", "TMDB_API_KEY", "MEILI_BASE_URL", "TIMEZONE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s is not named in: %v", name, err)
		}
	}
}

func TestNoGuessesAndNoHalfConfiguredServices(t *testing.T) {
	cases := map[string]map[string]string{
		"SQLAlchemy URL":       {"DATABASE_URL": "postgresql+psycopg://u:p@db/mn"},
		"OIDC half":            {"OIDC_ISSUER": "https://sso"},
		"Seerr half":           {"SEERR_API_KEY": "k"},
		"Jellyfin half":        {"JELLYFIN_API_KEY": ""},
		"media without secret": {"MEDIA_URL": "http://media:8100"},
		"media secret short":   {"MEDIA_URL": "http://media:8100", "MEDIA_SECRET": "short"},
		"LiteLLM without URL":  {"LITELLM_API_KEY": "k"},
		"model without key":    {"LITELLM_MODEL": "m"},
		"unknown time zone":    {"TIMEZONE": "Mars/Olympus"},
		"mail sender typo":     {"MAIL_SENDER": "smpt"},
		"bad boolean":          {"PASSWORD_SIGNIN": "sure"},
		"bad number":           {"DEVICE_IDLE_DAYS": "0"},
		"bad proxy":            {"TRUSTED_PROXY": "caddy"},
		"bad alias":            {"USER_ALIASES": "a"},
		"relative public URL":  {"PUBLIC_URL": "mn.example"},
		"Apple id, no team":    {"APPLE_APP_ID": "com.example.movienight"},
		"Apple id, bad team":   {"APPLE_APP_ID": "abc.com.example.movienight"},
		"report DSN not a DSN": {"ERROR_REPORTS_SERVER_DSN": "glitchtip.example"},
		"web DSN, no project":  {"ERROR_REPORTS_WEB_DSN": "https://key@glitchtip.example/"},
		"app DSN, no key":      {"ERROR_REPORTS_APP_DSN": "https://glitchtip.example/3"},
		"environment alone":    {"ERROR_REPORTS_ENVIRONMENT": "staging"},
	}
	for name, change := range cases {
		m := base()
		for k, v := range change {
			m[k] = v
		}
		if _, err := load(m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Error reports: each DSN optional and on its own; the environment is
// "production" unless set; the site's origin is PUBLIC_URL's.
func TestErrorReportSettings(t *testing.T) {
	c, err := load(base())
	if err != nil || c.ErrorReports.Any() || c.ErrorReports.Environment != "production" {
		t.Fatalf("unset: %+v %v", c.ErrorReports, err)
	}
	m := base()
	m["ERROR_REPORTS_APP_DSN"] = "https://abc@glitchtip.example/3"
	m["ERROR_REPORTS_ENVIRONMENT"] = "staging"
	m["PUBLIC_URL"] = "https://Movies.Example:8443/"
	c, err = load(m)
	if err != nil {
		t.Fatal(err)
	}
	e := c.ErrorReports
	if e.AppDSN != "https://abc@glitchtip.example/3" || e.WebDSN != "" || e.ServerDSN != "" || e.Environment != "staging" || !e.Any() {
		t.Fatalf("%+v", e)
	}
	if c.PublicOrigin() != "https://movies.example:8443" {
		t.Fatal(c.PublicOrigin())
	}
	// A bad DSN's message names the setting, never the value (its key).
	m["ERROR_REPORTS_SERVER_DSN"] = "secretkey"
	if _, err := load(m); err == nil || !strings.Contains(err.Error(), "ERROR_REPORTS_SERVER_DSN") || strings.Contains(err.Error(), "secretkey") {
		t.Fatal(err)
	}
}

// APPLE_APP_ID is optional and kept as given when it is TEAMID.bundle.id.
func TestAppleAppID(t *testing.T) {
	m := base()
	if c, err := load(m); err != nil || c.AppleAppID != "" {
		t.Fatalf("unset: %q %v", c.AppleAppID, err)
	}
	m["APPLE_APP_ID"] = "ABCDE12345.com.example.movienight"
	if c, err := load(m); err != nil || c.AppleAppID != "ABCDE12345.com.example.movienight" {
		t.Fatalf("set: %v", err)
	}
}

// The SMTP2GO key file is checked at start-up: missing or empty stops it
// with the setting named; a readable one loads.
func TestTheSMTP2GOKeyFileMustBeReadable(t *testing.T) {
	dir := t.TempDir()
	good, empty := filepath.Join(dir, "key"), filepath.Join(dir, "empty")
	if err := os.WriteFile(good, []byte("api-k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{filepath.Join(dir, "none"): "can't be read", empty: "is empty", good: ""} {
		m := base()
		m["MAIL_SENDER"], m["SMTP2GO_API_KEY_FILE"] = "smtp2go", file
		_, err := load(m)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), "SMTP2GO_API_KEY_FILE "+want)) {
			t.Errorf("%s: %v", file, err)
		}
	}
}

func TestPythonSpellingsOfBooleansKeepTheirMeaning(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "1": true, "YES": true, "on": true, "false": false, "0": false, "off": false} {
		m := base()
		m["PASSWORD_SIGNIN"] = v
		c, err := load(m)
		if err != nil || c.PasswordSignin != want {
			t.Errorf("%q: %v %v", v, c, err)
		}
	}
}

func TestTrustedProxyNoneAndCIDRs(t *testing.T) {
	m := base()
	m["TRUSTED_PROXY"] = "none"
	if c, err := load(m); err != nil || len(c.TrustedProxies) != 0 {
		t.Fatal(c, err)
	}
	m["TRUSTED_PROXY"] = "172.16.0.0/12, ::1"
	if c, err := load(m); err != nil || len(c.TrustedProxies) != 2 {
		t.Fatal(c, err)
	}
}

func TestDatabaseURLAlone(t *testing.T) {
	if _, err := DatabaseURL(func(string) (string, bool) { return "", false }); err == nil {
		t.Fatal("no DATABASE_URL accepted")
	}
	if v, err := DatabaseURL(func(string) (string, bool) { return "postgresql://x/y", true }); err != nil || v != "postgresql://x/y" {
		t.Fatal(v, err)
	}
}

// The issuer must reach the OIDC library exactly as written: Authentik's
// ends in a slash, and discovery refuses any difference.
func TestOIDCIssuerKeepsTrailingSlash(t *testing.T) {
	m := base()
	m["OIDC_ISSUER"] = "https://sso.example/application/o/movienight/"
	m["OIDC_CLIENT_ID"] = "movienight"
	c, err := load(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.OIDCIssuer != "https://sso.example/application/o/movienight/" {
		t.Fatalf("issuer changed to %q", c.OIDCIssuer)
	}
}

// Jellyfin is optional (all four settings or none), and the media libraries
// are on with MEDIA_URL and MEDIA_SECRET.
func TestNoJellyfinWithLibraries(t *testing.T) {
	m := base()
	for _, k := range []string{"JELLYFIN_BASE_URL", "JELLYFIN_PUBLIC_URL", "JELLYFIN_API_KEY", "JELLYFIN_USER_ID"} {
		delete(m, k)
	}
	m["MEDIA_URL"], m["MEDIA_SECRET"] = "http://media:8100/", "0123456789abcdef"
	c, err := load(m)
	if err != nil {
		t.Fatal(err)
	}
	if c.JellyfinEnabled() || !c.Media.Enabled() || c.Media.URL != "http://media:8100" {
		t.Fatalf("jellyfin %v media %+v", c.JellyfinEnabled(), c.Media)
	}
}

func TestMediaServiceSettings(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	c, err := LoadMediaService(env(map[string]string{"MEDIA_SECRET": "0123456789abcdef", "MEDIA_LIBRARIES": "/media, /mnt/films/"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Libraries) != 2 || c.Libraries[1] != "/mnt/films" || c.MaxTranscodes != 2 || c.ProbeWorkers != 4 ||
		c.HWAccel != "none" || c.HWDevice != "/dev/dri/renderD128" || c.CacheDir == "" {
		t.Fatalf("%+v", c)
	}
	for name, m := range map[string]map[string]string{
		"nothing":       {},
		"relative path": {"MEDIA_SECRET": "0123456789abcdef", "MEDIA_LIBRARIES": "media"},
		"bad hwaccel":   {"MEDIA_SECRET": "0123456789abcdef", "MEDIA_LIBRARIES": "/media", "MEDIA_HWACCEL": "cuda"},
		"short secret":  {"MEDIA_SECRET": "x", "MEDIA_LIBRARIES": "/media"},
	} {
		if _, err := LoadMediaService(env(m)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSubtitleSources(t *testing.T) {
	with := func(extra map[string]string) (*Config, error) {
		m := base()
		for k, v := range extra {
			m[k] = v
		}
		return load(m)
	}
	c, err := with(nil)
	if err != nil || strings.Join(c.Subtitles.Sources, ",") != "podnapisi,gestdown" {
		t.Fatalf("no keys: %v %v", c.Subtitles.Sources, err)
	}
	c, err = with(map[string]string{"OPENSUBTITLES_API_KEY": "k", "JIMAKU_API_KEY": "j"})
	if err != nil || strings.Join(c.Subtitles.Sources, ",") != "opensubtitles,jimaku,podnapisi,gestdown" {
		t.Fatalf("keys: %v %v", c.Subtitles.Sources, err)
	}
	c, err = with(map[string]string{"SUBTITLE_SOURCES": "Gestdown, opensubtitles", "OPENSUBTITLES_API_KEY": "k"})
	if err != nil || strings.Join(c.Subtitles.Sources, ",") != "opensubtitles,gestdown" || c.Subtitles.On("podnapisi") {
		t.Fatalf("listed: %v %v", c.Subtitles.Sources, err)
	}
	c, err = with(map[string]string{"SUBTITLE_SOURCES": "none", "SUBDL_API_KEY": "k"})
	if err != nil || len(c.Subtitles.Sources) != 0 {
		t.Fatalf("none: %v %v", c.Subtitles.Sources, err)
	}
	for name, extra := range map[string]map[string]string{
		"unknown source":       {"SUBTITLE_SOURCES": "addic7ed"},
		"listed without a key": {"SUBTITLE_SOURCES": "subdl"},
		"username alone":       {"OPENSUBTITLES_API_KEY": "k", "OPENSUBTITLES_USERNAME": "u"},
		"login without a key":  {"OPENSUBTITLES_USERNAME": "u", "OPENSUBTITLES_PASSWORD": "p"},
	} {
		if _, err := with(extra); err == nil {
			t.Errorf("%s loaded", name)
		}
	}
}
