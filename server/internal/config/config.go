// Package config reads the install-wide settings from the environment, once,
// at startup. Each person's own settings (Jellyfin account, players, Home
// rows, digests) live in the database instead.
//
// The names are the Python app's, so an install's
// existing .env keeps working; docs/configuration.md describes each one.
// What changed, on purpose (the "no hidden fallbacks" rule):
//
//   - A setting that names a place or a secret (a URL, a key, the owner) has
//     no placeholder default any more. The Python filled in
//     "https://jellyfin.example.com", "admin" as the owner, and so on, and a
//     setting left out silently took its placeholder; here a missing required
//     setting stops startup with a message naming it.
//   - A tunable with a documented value (OIDC_SESSION_DAYS 30,
//     DEVICE_IDLE_DAYS 90, ...) keeps that value when unset: those are the
//     product's stated limits (docs/limits.md), not guesses.
//   - A half-configured optional service (SEERR_BASE_URL without
//     SEERR_API_KEY, OIDC_ISSUER without OIDC_CLIENT_ID, ...) is an error, not
//     "off": the Python quietly treated it as off, which hid typos.
//
// Every problem is collected and reported together, so a deploy fixes its
// .env in one go rather than one restart per missing line.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	// The zone database inside the binary: the runtime image may have no
	// /usr/share/zoneinfo, and TIMEZONE must load the same everywhere.
	_ "time/tzdata"
)

// appleAppIDRe is an Apple app id: a 10-character team id, then a reverse-DNS
// bundle id of at least two parts.
var appleAppIDRe = regexp.MustCompile(`^[A-Z0-9]{10}\.[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)

// Config is every setting the Go server reads (the API and the worker read
// the same Config). Fields are grouped as the Python app grouped them; the read
// and write routes' own settings are in rw.go (Config.RW), the worker's,
// the digests' and the AI rows' in jobs.go (JobSettings).
type Config struct {
	// ---- Core ----

	// DATABASE_URL: Postgres, as postgresql://user:pass@host:port/db. The
	// SQLAlchemy form the Python used (postgresql+psycopg://) is refused with
	// a message rather than rewritten: one spelling, one meaning.
	DatabaseURL string
	// VALKEY_URL: the shared Valkey (redis://host:6379/0 or valkey://...).
	// Holds everything replicas must agree on that is not durable data:
	// caches, the sign-in lockout, the TV pairing limit, circuit breakers,
	// the OIDC sign-in's short-lived state.
	ValkeyURL string
	// TIMEZONE (an IANA name, e.g. "Europe/London"): the install's own clock.
	// Every schedule of the worker (Monday 09:00, 02:30 ...) runs on it. A
	// person's own dates (history's days, "this year", Home's row sets, a
	// watch date without a time, their digest periods) follow the zone their
	// browser or phone reported (users.time_zone); this zone is the
	// documented default only for someone who has never reported one, and
	// is applied in one place, users.Location. Use cfg.Location directly
	// only for the install's own work (the schedule, the catalog's years).
	// Required: the Python defaulted to America/New_York and used the
	// container's zone (UTC) for date.today(), so an install elsewhere
	// silently grouped its evenings into the wrong day. The zone database is
	// inside the binary (time/tzdata), so it loads the same in any image.
	Location *time.Location
	// SECRET_KEY: encrypts per-person secrets (Jellyfin tokens, Fernet) and
	// keys every HMAC (the phone connect form, browser form tokens). Required,
	// no default: a known key would let anyone forge those.
	SecretKey string
	// PUBLIC_URL: the address people open, e.g. https://movienight.example.com.
	// The OIDC callback is derived from it, and whether cookies are marked
	// Secure (an https PUBLIC_URL means the browser always sees https, even
	// though the hop from Caddy to the app is plain http).
	PublicURL string
	// TRUSTED_PROXY: the reverse proxy's address(es) as seen by the app, as a
	// comma-separated list of IPs or CIDRs, or the word "none". Only a request
	// whose connection comes from one of these has its X-Forwarded-For
	// believed (its last entry: the address the proxy itself saw). This is
	// what the per-address sign-in lockout and the TV pairing limit count by.
	// Required, because both wrong answers are bad: trusting every peer lets
	// any caller pick its own address, and trusting none (behind a proxy)
	// makes every visitor look like the proxy, so one stranger's wrong
	// passwords lock out everyone. "none" says explicitly that the app is
	// reached directly. The Python guessed the proxy from the container's
	// default gateway (/proc/net/route), which breaks with more than one
	// replica or any other network layout.
	TrustedProxies []netip.Prefix
	// WEB_DIR: the repository's web/ directory (index.html and static/), the
	// website's own files, served unchanged.
	WebDir string

	// ---- Sign-in ----

	// AUTH_DEV_ALLOWED: local development and tests only. While true, a
	// request may name its user in X-Dev-User (or AUTH_DEV_USER applies when
	// it sends none). Never on a server others can reach; startup logs a
	// warning while it is on.
	AuthDevAllowed bool
	AuthDevUser    string
	// ADMIN_USERS: usernames that may run install-wide actions. OWNER_USERNAME
	// always may. Admin rights follow these on every request, both ways.
	AdminUsers []string
	// OWNER_USERNAME: the install's owner. Required: the Python defaulted it
	// to "admin", which made whoever signed in as "admin" an admin.
	OwnerUsername string
	// USER_ALIASES: "other=main,other2=main2": the dev sign-in of "other" acts
	// as "main". (In the Python this applied to the removed proxy-header
	// sign-in and survives only on the dev path; kept for the tests that use
	// it.)
	UserAliases map[string]string

	// OIDC_ISSUER and OIDC_CLIENT_ID together turn OIDC sign-in on; one without
	// the other is an error.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	// OIDC_SCOPES: default "openid email profile".
	OIDCScopes []string
	// OIDC_REDIRECT_URL: where the provider sends the person back. Default:
	// PUBLIC_URL + /api/v1/auth/oidc/callback (register that exact URL).
	OIDCRedirectURL string
	// OIDC_BUTTON_LABEL: the button's words. Default "Sign in".
	OIDCButtonLabel string
	// OIDC_USERNAME_CLAIM: the claim a brand-new person's username comes from.
	// Empty: preferred_username, then the email's local part, then the subject.
	OIDCUsernameClaim string
	// OIDC_SESSION_DAYS: how long a browser stays signed in (password and OIDC
	// sign-ins alike, as in the Python). Default 30.
	SessionDays int

	// PASSWORD_SIGNIN: offer the username and password form. Default off.
	PasswordSignin bool
	// PASSWORD_MIN_LENGTH: shortest password accepted when one is set. Default 10.
	PasswordMinLength int

	// ---- Phones and TVs ----

	// APP_DEV_REDIRECTS: also accept Expo Go (exp://) sign-in redirects.
	AppDevRedirects bool
	// APPLE_APP_ID: the iPhone app's Apple app id, "TEAMID.bundle.id" (the
	// team id from the Apple Developer account, the bundle id from
	// mobile/app.json). It is named in /.well-known/apple-app-site-association
	// so iOS offers this site's saved passwords on the app's own sign-in
	// fields. Optional: empty, that address answers 404 and the app's
	// sign-in works without autofill. Each install's app build has its own
	// id, so there is no default.
	AppleAppID string
	// DEVICE_IDLE_DAYS / DEVICE_MAX_DAYS: a device token stops working after
	// this many days unused, or this many days after it signed in. 90 / 365.
	DeviceIdleDays int
	DeviceMaxDays  int
	// PAIR_STARTS_PER_ADDRESS: new TV codes per address (an IPv6 /64 counts
	// as one) in 10 minutes. Default 10.
	PairStartsPerAddress int
	// PAIR_CODES_WAITING: unexpired TV codes waiting at once, install-wide.
	// Default 200.
	PairCodesWaiting int
	// REPORTED_MAX_CHARS: longest name a device may report for itself (the
	// connect page's device name; later phases: titles and other reported
	// text). Default 500.
	ReportedMaxChars int
	// JELLYFIN_WEBHOOK_SECRET: shared secret of the Jellyfin webhook and the
	// Movie Night plugin for Jellyfin. Empty: both endpoints answer 503, a
	// documented "not set up" state (the Python's behaviour, kept).
	JellyfinWebhookSecret string

	// ---- Outside services ----

	// Jellyfin is required: Movie Night is a companion to one Jellyfin server.
	JellyfinBaseURL   string
	JellyfinPublicURL string
	JellyfinAPIKey    string
	JellyfinUserID    string

	// Seerr: optional, both or neither. DRY_RUN: requests are simulated.
	SeerrBaseURL string
	SeerrAPIKey  string
	DryRun       bool

	// TMDB: required.
	TMDBAPIKey      string
	TMDBLanguage    string // default en-US
	WatchRegion     string // default US
	HideAdultTitles bool

	// MDBList: optional.
	MDBListAPIKey string

	// LiteLLM: optional. Every model call goes through LiteLLM (never to a
	// provider directly). LITELLM_BASE_URL is required once LITELLM_API_KEY is
	// set: the Python's default (http://litellm:4000/v1) was a guess about the
	// network.
	LiteLLMBaseURL    string
	LiteLLMAPIKey     string
	LiteLLMModel      string
	LiteLLMMaxTokens  int // default 8000
	SearchAIModel     string
	SearchAIMaxTokens int // default 1500

	// Meilisearch: required (search by meaning). MEILI_BASE_URL has no
	// default; MEILI_EMBEDDER_MODEL keeps the benchmarked model.
	MeiliBaseURL       string
	MeiliAPIKey        string
	MeiliEmbedderModel string
	SearchMaxHits      int // default 1000

	// ---- The read and write routes (config/rw.go) ----
	RW ReadWrite

	// The worker's schedule, digests and AI rows (jobs.go).
	JobSettings
}

// OIDCEnabled reports whether OIDC sign-in is set up.
func (c *Config) OIDCEnabled() bool { return c.OIDCIssuer != "" && c.OIDCClientID != "" }

// SigninEnabled reports whether a browser has any way to sign in.
func (c *Config) SigninEnabled() bool { return c.PasswordSignin || c.OIDCEnabled() }

// SeerrEnabled, MDBListEnabled and AIEnabled report the optional services.
func (c *Config) SeerrEnabled() bool   { return c.SeerrBaseURL != "" }
func (c *Config) MDBListEnabled() bool { return c.MDBListAPIKey != "" }
func (c *Config) AIEnabled() bool      { return c.LiteLLMAPIKey != "" && c.LiteLLMModel != "" }

// SecureCookies reports whether cookies get the Secure attribute: whenever
// the public address is https (see PublicURL).
func (c *Config) SecureCookies() bool { return strings.HasPrefix(c.PublicURL, "https://") }

// IsAdmin reports whether a (lower-case) username holds admin rights.
func (c *Config) IsAdmin(username string) bool {
	if username == c.OwnerUsername {
		return true
	}
	for _, a := range c.AdminUsers {
		if a == username {
			return true
		}
	}
	return false
}

// Load reads the settings from the process environment.
func Load() (*Config, error) { return LoadFrom(os.LookupEnv) }

// LoadFrom reads the settings through lookup (os.LookupEnv in production; a
// map in tests), so tests never depend on the machine's environment.
func LoadFrom(lookup func(string) (string, bool)) (*Config, error) {
	r := reader{lookup: lookup}
	c := &Config{}

	c.DatabaseURL = r.required("DATABASE_URL")
	if name := strings.TrimSpace(r.required("TIMEZONE")); name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil {
			r.fail(fmt.Sprintf("TIMEZONE must be an IANA time zone name (e.g. Europe/London), not %q", name))
		}
		c.Location = loc
	}
	if strings.Contains(c.DatabaseURL, "+psycopg") || strings.Contains(c.DatabaseURL, "+asyncpg") {
		r.fail("DATABASE_URL is in SQLAlchemy's form (postgresql+psycopg://); the Go server needs postgresql://")
	}
	c.ValkeyURL = r.required("VALKEY_URL")
	c.SecretKey = r.required("SECRET_KEY")
	c.PublicURL = strings.TrimRight(r.required("PUBLIC_URL"), "/")
	r.absoluteURL("PUBLIC_URL", c.PublicURL)
	c.TrustedProxies = r.proxies("TRUSTED_PROXY")
	c.WebDir = r.required("WEB_DIR")

	c.AuthDevAllowed = r.boolean("AUTH_DEV_ALLOWED", false)
	c.AuthDevUser = strings.ToLower(strings.TrimSpace(r.optional("AUTH_DEV_USER")))
	c.AdminUsers = r.list("ADMIN_USERS", true)
	c.OwnerUsername = strings.ToLower(strings.TrimSpace(r.required("OWNER_USERNAME")))
	c.UserAliases = r.aliases("USER_ALIASES")

	// Kept exactly as written: OIDC requires the issuer to match the provider's
	// own "issuer" value character for character, and providers differ on the
	// trailing slash (Authentik keeps it). Trimming it broke every sign-in.
	c.OIDCIssuer = strings.TrimSpace(r.optional("OIDC_ISSUER"))
	c.OIDCClientID = r.optional("OIDC_CLIENT_ID")
	c.OIDCClientSecret = r.optional("OIDC_CLIENT_SECRET")
	if (c.OIDCIssuer == "") != (c.OIDCClientID == "") {
		r.fail("OIDC_ISSUER and OIDC_CLIENT_ID must be set together (or both left out for no OIDC sign-in)")
	}
	c.OIDCScopes = strings.Fields(r.withDefault("OIDC_SCOPES", "openid email profile"))
	c.OIDCRedirectURL = r.optional("OIDC_REDIRECT_URL")
	if c.OIDCRedirectURL == "" {
		c.OIDCRedirectURL = c.PublicURL + "/api/v1/auth/oidc/callback"
	}
	c.OIDCButtonLabel = r.withDefault("OIDC_BUTTON_LABEL", "Sign in")
	c.OIDCUsernameClaim = strings.TrimSpace(r.optional("OIDC_USERNAME_CLAIM"))
	c.SessionDays = r.positive("OIDC_SESSION_DAYS", 30)

	c.PasswordSignin = r.boolean("PASSWORD_SIGNIN", false)
	c.PasswordMinLength = r.positive("PASSWORD_MIN_LENGTH", 10)

	c.AppDevRedirects = r.boolean("APP_DEV_REDIRECTS", false)
	c.AppleAppID = r.optional("APPLE_APP_ID")
	if c.AppleAppID != "" && !appleAppIDRe.MatchString(c.AppleAppID) {
		r.fail(fmt.Sprintf("APPLE_APP_ID must be TEAMID.bundle.id (e.g. ABCDE12345.com.example.movienight), not %q", c.AppleAppID))
	}
	c.DeviceIdleDays = r.positive("DEVICE_IDLE_DAYS", 90)
	c.DeviceMaxDays = r.positive("DEVICE_MAX_DAYS", 365)
	c.PairStartsPerAddress = r.positive("PAIR_STARTS_PER_ADDRESS", 10)
	c.PairCodesWaiting = r.positive("PAIR_CODES_WAITING", 200)
	c.ReportedMaxChars = r.positive("REPORTED_MAX_CHARS", 500)
	c.JellyfinWebhookSecret = r.optional("JELLYFIN_WEBHOOK_SECRET")

	c.JellyfinBaseURL = strings.TrimRight(r.required("JELLYFIN_BASE_URL"), "/")
	r.absoluteURL("JELLYFIN_BASE_URL", c.JellyfinBaseURL)
	c.JellyfinPublicURL = strings.TrimRight(r.required("JELLYFIN_PUBLIC_URL"), "/")
	r.absoluteURL("JELLYFIN_PUBLIC_URL", c.JellyfinPublicURL)
	c.JellyfinAPIKey = r.required("JELLYFIN_API_KEY")
	c.JellyfinUserID = r.required("JELLYFIN_USER_ID")

	c.SeerrBaseURL = strings.TrimRight(r.optional("SEERR_BASE_URL"), "/")
	c.SeerrAPIKey = r.optional("SEERR_API_KEY")
	if (c.SeerrBaseURL == "") != (c.SeerrAPIKey == "") {
		r.fail("SEERR_BASE_URL and SEERR_API_KEY must be set together (or both left out for no Seerr)")
	}
	if c.SeerrBaseURL != "" {
		r.absoluteURL("SEERR_BASE_URL", c.SeerrBaseURL)
	}
	c.DryRun = r.boolean("DRY_RUN", false)

	c.TMDBAPIKey = r.required("TMDB_API_KEY")
	c.TMDBLanguage = r.withDefault("TMDB_LANGUAGE", "en-US")
	c.WatchRegion = r.withDefault("WATCH_REGION", "US")
	c.HideAdultTitles = r.boolean("HIDE_ADULT_TITLES", false)

	c.MDBListAPIKey = r.optional("MDBLIST_API_KEY")

	c.LiteLLMAPIKey = r.optional("LITELLM_API_KEY")
	c.LiteLLMModel = r.optional("LITELLM_MODEL")
	c.SearchAIModel = r.optional("SEARCH_AI_MODEL")
	c.LiteLLMBaseURL = strings.TrimRight(r.optional("LITELLM_BASE_URL"), "/")
	if c.LiteLLMAPIKey != "" && c.LiteLLMBaseURL == "" {
		r.fail("LITELLM_BASE_URL is required when LITELLM_API_KEY is set")
	}
	if c.LiteLLMBaseURL != "" {
		r.absoluteURL("LITELLM_BASE_URL", c.LiteLLMBaseURL)
	}
	if (c.LiteLLMModel != "" || c.SearchAIModel != "") && c.LiteLLMAPIKey == "" {
		r.fail("LITELLM_MODEL or SEARCH_AI_MODEL is set but LITELLM_API_KEY is not")
	}
	c.LiteLLMMaxTokens = r.positive("LITELLM_MAX_TOKENS", 8000)
	c.SearchAIMaxTokens = r.positive("SEARCH_AI_MAX_TOKENS", 1500)

	c.MeiliBaseURL = strings.TrimRight(r.required("MEILI_BASE_URL"), "/")
	r.absoluteURL("MEILI_BASE_URL", c.MeiliBaseURL)
	c.MeiliAPIKey = r.optional("MEILI_API_KEY")
	c.MeiliEmbedderModel = r.withDefault("MEILI_EMBEDDER_MODEL", "BAAI/bge-base-en-v1.5")
	c.SearchMaxHits = r.positive("SEARCH_MAX_HITS", 1000)
	c.RW = r.readWrite()

	c.JobSettings = r.jobSettings()

	if len(r.problems) > 0 {
		return nil, errors.New("settings are not usable:\n  - " + strings.Join(r.problems, "\n  - "))
	}
	return c, nil
}

// SessionLifetime is how long a browser session lasts (OIDC_SESSION_DAYS).
func (c *Config) SessionLifetime() time.Duration {
	return time.Duration(c.SessionDays) * 24 * time.Hour
}

// reader collects every problem instead of stopping at the first.
type reader struct {
	lookup   func(string) (string, bool)
	problems []string
}

func (r *reader) fail(msg string) { r.problems = append(r.problems, msg) }

func (r *reader) optional(name string) string {
	v, _ := r.lookup(name)
	return strings.TrimSpace(v)
}

func (r *reader) required(name string) string {
	v := r.optional(name)
	if v == "" {
		r.fail(name + " is required and not set")
	}
	return v
}

func (r *reader) withDefault(name, def string) string {
	if v := r.optional(name); v != "" {
		return v
	}
	return def
}

// boolean accepts the spellings pydantic accepted (true/false, 1/0, yes/no,
// on/off, any case), so existing .env files keep their meaning; anything
// else is an error rather than "false".
func (r *reader) boolean(name string, def bool) bool {
	v := strings.ToLower(r.optional(name))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on", "t", "y":
		return true
	case "0", "false", "no", "off", "f", "n":
		return false
	}
	r.fail(fmt.Sprintf("%s must be true or false, not %q", name, v))
	return def
}

func (r *reader) positive(name string, def int) int {
	v := r.optional(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		r.fail(fmt.Sprintf("%s must be a whole number of at least 1, not %q", name, v))
		return def
	}
	return n
}

func (r *reader) list(name string, lower bool) []string {
	var out []string
	for _, part := range strings.Split(r.optional(name), ",") {
		part = strings.TrimSpace(part)
		if lower {
			part = strings.ToLower(part)
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (r *reader) aliases(name string) map[string]string {
	out := map[string]string{}
	for _, pair := range r.list(name, true) {
		alias, main, ok := strings.Cut(pair, "=")
		alias, main = strings.TrimSpace(alias), strings.TrimSpace(main)
		if !ok || alias == "" || main == "" {
			r.fail(fmt.Sprintf("%s: %q is not alias=main", name, pair))
			continue
		}
		out[alias] = main
	}
	return out
}

func (r *reader) absoluteURL(name, v string) {
	if v == "" {
		return
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		r.fail(fmt.Sprintf("%s must be an http(s) address, not %q", name, v))
	}
}

// proxies parses TRUSTED_PROXY: required; "none" or IPs/CIDRs.
func (r *reader) proxies(name string) []netip.Prefix {
	raw := r.required(name)
	if raw == "" || strings.EqualFold(raw, "none") {
		return nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(part); err == nil {
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		r.fail(fmt.Sprintf("%s: %q is not an IP address or CIDR (or the word none)", name, part))
	}
	return out
}

// DatabaseURL reads only DATABASE_URL, for the migrate command, which needs
// nothing else (a deploy can run it before the rest of .env is in place).
func DatabaseURL(lookup func(string) (string, bool)) (string, error) {
	r := reader{lookup: lookup}
	v := r.required("DATABASE_URL")
	if strings.Contains(v, "+psycopg") || strings.Contains(v, "+asyncpg") {
		r.fail("DATABASE_URL is in SQLAlchemy's form (postgresql+psycopg://); the Go server needs postgresql://")
	}
	if len(r.problems) > 0 {
		return "", errors.New(strings.Join(r.problems, "; "))
	}
	return v, nil
}
