// Package web serves what a browser opens directly rather than through the
// API: the sign-in page and form, the account page (password change, sign
// out), the phone app's connect page, the OIDC redirects, the app shell
// (web/index.html) and the website's static files.
//
// The pages are server-rendered forms with no script. Each form carries an anti-forgery token: the HMAC of
// a random per-browser cookie (mn_form, SameSite=Strict), so a page on
// another site cannot submit them. /api writes have their own guard
// (X-Requested-With, internal/httpx).
package web

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"movienight/server/internal/apierr"
	"movienight/server/internal/auth"
	"movienight/server/internal/config"
	"movienight/server/internal/crypto"
	"movienight/server/internal/devices"
	"movienight/server/internal/httpx"
	"movienight/server/internal/oidc"
	"movienight/server/internal/passwords"
	"movienight/server/internal/sessions"
	"movienight/server/internal/users"
)

//go:embed templates/*.html
var templateFS embed.FS

// Content-Security-Policy of the pages: the sign-in and account pages run
// no script at all; the connect page's form answers with a redirect to the
// app's own address, so form-action allows those schemes (some browsers
// apply form-action to redirects).
const (
	signinCSP = "default-src 'self'; script-src 'none'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; " +
		"form-action 'self'; frame-ancestors 'none'"
	connectCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; " +
		"form-action 'self' movienight: exp:; frame-ancestors 'none'"
)

// formCookie binds a browser's form tokens.
const formCookie = "mn_form"

// Form field limits (as the Python's Form(max_length=...)): longer is refused.
const (
	maxNextLength     = 2000
	maxUsernameField  = 200
	maxPasswordField  = 1000
	maxChallengeField = 200
	maxRedirectField  = 300
	maxSigField       = 128
)

// Deps is what the pages need.
type Deps struct {
	Config    *config.Config
	Keys      *crypto.Keys
	Auth      *auth.Authenticator
	Sessions  *sessions.Store
	Passwords *passwords.Service
	Users     *users.Service
	Devices   *devices.Service
	OIDC      *oidc.Flow
	Callers   *httpx.Callers
	Log       *slog.Logger
}

// Web is the set of page handlers.
type Web struct {
	Deps
	pages *template.Template
	shell *Shell
	// appSiteAssociation is the apple-app-site-association body, made once
	// from APPLE_APP_ID; nil when that is not set.
	appSiteAssociation []byte
}

// New parses the templates and loads the shell from WEB_DIR.
func New(d Deps) (*Web, error) {
	pages, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	shell, err := LoadShell(d.Config.WebDir, d.Config.JellyfinPublicURL)
	if err != nil {
		return nil, err
	}
	out := &Web{Deps: d, pages: pages, shell: shell}
	if id := d.Config.AppleAppID; id != "" {
		body, err := json.Marshal(map[string]any{"webcredentials": map[string]any{"apps": []string{id}}})
		if err != nil {
			return nil, err
		}
		out.appSiteAssociation = body
	}
	return out, nil
}

// Register adds the page routes to mux.
func (w *Web) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /signin", w.signinPage)
	mux.HandleFunc("POST /signin", w.signin)
	mux.HandleFunc("GET /account", w.accountPage)
	mux.HandleFunc("POST /account/password", w.changePassword)
	mux.HandleFunc("POST /signout", w.signout)
	mux.HandleFunc("GET /app/connect", w.connectPage)
	mux.HandleFunc("POST /app/connect", w.connect)
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /.well-known/apple-app-site-association", w.appleAppSiteAssociation)
	mux.Handle("GET /static/", w.shell.Static())
	mux.HandleFunc("GET /sw.js", w.shell.ServiceWorker)
	// /kodi/ (the add-on) and /jellyfin/ (the plugin repository) are
	// registered by internal/addons (server.go).
	mux.HandleFunc("GET /", w.appShell)
}

// appleAppSiteAssociation names the phone app (APPLE_APP_ID: Apple team id .
// bundle id, as in the app build's Associated Domains entitlement
// webcredentials:<this site's host>) as one that may use this site's saved
// passwords: with it, iOS offers the Movie Night password saved in Safari or
// a password manager on the app's own sign-in fields. Apple fetches it from
// its CDN, unauthenticated and without following redirects, so it is served
// here at its fixed address (no reverse-proxy rule needed). Without
// APPLE_APP_ID the address answers 404 (not the website's page), and the app
// signs in as before, without autofill.
func (w *Web) appleAppSiteAssociation(rw http.ResponseWriter, r *http.Request) {
	if w.appSiteAssociation == nil {
		http.NotFound(rw, r)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	_, _ = rw.Write(w.appSiteAssociation)
}

// Local returns target when it is a path on this site, else "/": where to
// land after signing in must never be another host (open-redirect guard).
func Local(target string) string {
	if target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, `/\`) {
		return "/"
	}
	return target
}

// quote is Python's urllib.parse.quote(s, safe="/"): everything but
// letters, digits, "_.-~" and "/" percent-encoded, so the sign-in links read
// exactly as the Python wrote them (/signin?next=/lists%3Fx%3D1).
func quote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '.', c == '-', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteString("%" + strings.ToUpper(hex2(c)))
		}
	}
	return b.String()
}

func hex2(c byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[c>>4], digits[c&15]})
}

// SignInURL is where a signed-out browser goes, carrying the request's own
// path and query as `next` so the person comes back to it (the phone app's
// /app/connect?challenge=... round trip depends on it): the sign-in page
// when passwords are on (it offers OIDC too, if set up), else straight to
// the OIDC provider.
func (w *Web) SignInURL(r *http.Request) string {
	target := r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	start := oidc.LoginPath
	if w.Config.PasswordSignin {
		start = "/signin"
	}
	return start + "?next=" + quote(target)
}

// requireUser returns the signed-in person, or answers for them: a browser
// navigation goes to sign-in (when sign-in is set up); anything else gets
// the JSON 401.
func (w *Web) requireUser(rw http.ResponseWriter, r *http.Request) *auth.Identity {
	id, err := w.Auth.User(r.Context())
	if err == nil {
		return id
	}
	var ae *apierr.Error
	if errors.As(err, &ae) {
		if ae.Status == http.StatusUnauthorized && w.Config.SigninEnabled() && wantsHTML(r) {
			http.Redirect(rw, r, w.SignInURL(r), http.StatusFound)
			return nil
		}
		apierr.Write(rw, ae)
		return nil
	}
	w.Log.ErrorContext(r.Context(), "resolving the signed-in person failed", "err", err)
	apierr.Write(rw, apierr.Internal())
	return nil
}

func wantsHTML(r *http.Request) bool { return strings.Contains(r.Header.Get("Accept"), "text/html") }

// BrowserSignIn turns a browser's 401 from the API into the sign-in
// redirect, as the Python's 401 handler did for every route: a person who
// opens an API address (or a page that needs sign-in) while signed out is
// sent to sign in, then back. Only GETs that ask for HTML.
func (w *Web) BrowserSignIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !w.Config.SigninEnabled() || !wantsHTML(r) {
			next.ServeHTTP(rw, r)
			return
		}
		next.ServeHTTP(&signInRedirect{ResponseWriter: rw, r: r, to: w.SignInURL(r)}, r)
	})
}

type signInRedirect struct {
	http.ResponseWriter
	r          *http.Request
	to         string
	redirected bool
}

func (s *signInRedirect) WriteHeader(code int) {
	if code == http.StatusUnauthorized && !s.redirected {
		s.redirected = true
		h := s.ResponseWriter.Header()
		h.Del("Content-Type")
		h.Del("Content-Length")
		h.Set("Location", s.to)
		s.ResponseWriter.WriteHeader(http.StatusFound)
		return
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *signInRedirect) Write(b []byte) (int, error) {
	if s.redirected {
		return len(b), nil // the 401's JSON body is replaced by the redirect
	}
	return s.ResponseWriter.Write(b)
}

func (s *signInRedirect) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// ---------------------------------------------------------------------------
// Rendering and form tokens
// ---------------------------------------------------------------------------

func (w *Web) render(rw http.ResponseWriter, r *http.Request, name, csp string, status int, data any) {
	var b strings.Builder
	if err := w.pages.ExecuteTemplate(&b, name, data); err != nil {
		w.Log.ErrorContext(r.Context(), "rendering a page failed", "page", name, "err", err)
		apierr.Write(rw, apierr.Internal())
		return
	}
	h := rw.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", csp)
	rw.WriteHeader(status)
	_, _ = rw.Write([]byte(b.String()))
}

// formToken returns this browser's form token, setting its binder cookie
// first if it has none.
func (w *Web) formToken(rw http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(formCookie); err == nil && c.Value != "" && len(c.Value) <= 64 {
		return w.Keys.FormToken(c.Value)
	}
	binder := crypto.NewToken(24)
	http.SetCookie(rw, &http.Cookie{
		Name: formCookie, Value: binder, Path: "/", HttpOnly: true, Secure: w.Config.SecureCookies(),
		SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 3600,
	})
	// Make the new cookie visible to the rest of this request too.
	r.AddCookie(&http.Cookie{Name: formCookie, Value: binder})
	return w.Keys.FormToken(binder)
}

func (w *Web) formOK(r *http.Request) bool {
	c, err := r.Cookie(formCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return crypto.SameHex(r.PostFormValue("csrf"), w.Keys.FormToken(c.Value))
}

// field reads a form field, or reports it too long.
func field(r *http.Request, name string, max int) (string, bool) {
	v := r.PostFormValue(name)
	return v, len(v) <= max
}

// parseForm reads a urlencoded form body (capped by httpx.MaxBodyBytes).
func parseForm(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

type signinData struct {
	Error, CSRF, Next, Username, OIDCLabel string
	Password                               bool
	OIDC                                   string
}

func (w *Web) signinPageWith(rw http.ResponseWriter, r *http.Request, status int, next, msg, username string) {
	d := signinData{Error: msg, CSRF: w.formToken(rw, r), Next: next, Username: username,
		Password: w.Config.PasswordSignin, OIDCLabel: w.Config.OIDCButtonLabel}
	if w.Config.OIDCEnabled() {
		d.OIDC = oidc.LoginPath + "?next=" + quote(next)
	}
	w.render(rw, r, "signin.html", signinCSP, status, d)
}

func (w *Web) signinPage(rw http.ResponseWriter, r *http.Request) {
	if !w.Config.SigninEnabled() {
		w.render(rw, r, "signin.html", signinCSP, http.StatusNotFound,
			signinData{Error: "Sign-in isn't set up on this Movie Night.", Next: "/", CSRF: w.formToken(rw, r)})
		return
	}
	next := r.URL.Query().Get("next")
	if len(next) > maxNextLength {
		next = "/"
	}
	w.signinPageWith(rw, r, http.StatusOK, Local(next), "", "")
}

func (w *Web) signin(rw http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		apierr.Write(rw, apierr.New(http.StatusBadRequest, "That form couldn't be read."))
		return
	}
	next, okNext := field(r, "next", maxNextLength)
	target := "/"
	if okNext {
		target = Local(next)
	}
	username, okUser := field(r, "username", maxUsernameField)
	password, okPass := field(r, "password", maxPasswordField)
	if !w.Config.PasswordSignin {
		w.signinPageWith(rw, r, http.StatusNotFound, target, "Signing in with a password is turned off here.", "")
		return
	}
	if !w.formOK(r) {
		w.signinPageWith(rw, r, http.StatusBadRequest, target, "That form had expired. Try again.", username)
		return
	}
	if !okUser || !okPass {
		w.signinPageWith(rw, r, http.StatusUnauthorized, target, "That username and password don't match.", "")
		return
	}
	caller := w.Callers.Addr(r).String()
	u, err := w.Passwords.Authenticate(r.Context(), username, password, caller)
	switch {
	case errors.Is(err, passwords.ErrTooManyTries):
		w.signinPageWith(rw, r, http.StatusTooManyRequests,
			target, "Too many wrong passwords for this username. Wait 15 minutes and try again.", username)
		return
	case err != nil:
		// Fail closed: with the lockout's store down, no password is checked.
		w.Log.ErrorContext(r.Context(), "password sign-in failed to run", "err", err)
		w.signinPageWith(rw, r, http.StatusServiceUnavailable, target, "Signing in isn't working right now. Try again in a minute.", username)
		return
	case u == nil:
		w.signinPageWith(rw, r, http.StatusUnauthorized, target, "That username and password don't match.", username)
		return
	}
	if err := w.startSession(rw, r, *u); err != nil {
		w.Log.ErrorContext(r.Context(), "starting a session failed", "err", err)
		apierr.Write(rw, apierr.Internal())
		return
	}
	http.Redirect(rw, r, target, http.StatusSeeOther)
}

// startSession ends any session this browser had (a fresh one for each
// sign-in, so a session id seen before sign-in is never the signed-in one)
// and starts a new one.
func (w *Web) startSession(rw http.ResponseWriter, r *http.Request, u userRow) error {
	if old, err := w.Sessions.Resolve(r.Context(), r); err == nil && old != nil {
		if err := w.Sessions.End(r.Context(), rw, old.IDHash); err != nil {
			return err
		}
	}
	_, err := w.Sessions.Create(r.Context(), rw, u)
	return err
}

func (w *Web) signout(rw http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err == nil && w.formOK(r) {
		if sess, err := w.Sessions.Resolve(r.Context(), r); err == nil && sess != nil {
			if err := w.Sessions.End(r.Context(), rw, sess.IDHash); err != nil {
				w.Log.ErrorContext(r.Context(), "ending a session failed", "err", err)
				apierr.Write(rw, apierr.Internal())
				return
			}
		} else {
			w.Sessions.ClearCookie(rw)
		}
	}
	to := "/"
	if w.Config.PasswordSignin {
		to = "/signin"
	}
	http.Redirect(rw, r, to, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// OIDC (registered as API operations in internal/api; the work is here)
// ---------------------------------------------------------------------------

// OIDCLogin sends the browser to the provider.
func (w *Web) OIDCLogin(rw http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if len(next) > maxNextLength {
		next = "/"
	}
	to, err := w.OIDC.Login(r.Context(), rw, Local(next))
	if errors.Is(err, oidc.ErrNotConfigured) {
		apierr.Write(rw, apierr.New(http.StatusNotFound, "OIDC sign-in is not configured"))
		return
	}
	if err != nil {
		w.Log.ErrorContext(r.Context(), "starting an OIDC sign-in failed", "err", err)
		w.signinPageWith(rw, r, http.StatusBadGateway, Local(next), "The sign-in provider didn't answer. Try again in a minute.", "")
		return
	}
	http.Redirect(rw, r, to, http.StatusFound)
}

// OIDCCallback finishes the sign-in. A failure shows the sign-in page with
// the reason (and the button to try again) instead of the Python's bare 401
// JSON, which in an OIDC-only install could bounce the browser straight back
// to the provider in a loop.
func (w *Web) OIDCCallback(rw http.ResponseWriter, r *http.Request) {
	res, err := w.OIDC.Callback(r.Context(), rw, r)
	var f *oidc.Failed
	switch {
	case errors.Is(err, oidc.ErrNotConfigured):
		apierr.Write(rw, apierr.New(http.StatusNotFound, "OIDC sign-in is not configured"))
		return
	case errors.As(err, &f):
		w.Log.WarnContext(r.Context(), "OIDC sign-in failed", "why", f.Why)
		w.signinPageWith(rw, r, http.StatusUnauthorized, "/", f.Msg, "")
		return
	case err != nil:
		w.Log.ErrorContext(r.Context(), "OIDC sign-in failed to run", "err", err)
		w.signinPageWith(rw, r, http.StatusBadGateway, "/", "Signing in isn't working right now. Try again in a minute.", "")
		return
	}
	u, err := w.Users.SignInOIDC(r.Context(), res.Claims)
	if errors.Is(err, users.ErrOIDCAccountTaken) {
		w.Log.WarnContext(r.Context(), "OIDC sign-in refused: the username is linked to another subject")
		w.signinPageWith(rw, r, http.StatusForbidden, "/", "That username belongs to another sign-in account. Ask the person who runs this Movie Night.", "")
		return
	}
	if err != nil {
		w.Log.ErrorContext(r.Context(), "OIDC sign-in: finding the person failed", "err", err)
		apierr.Write(rw, apierr.Internal())
		return
	}
	if err := w.startSession(rw, r, u); err != nil {
		w.Log.ErrorContext(r.Context(), "starting a session failed", "err", err)
		apierr.Write(rw, apierr.Internal())
		return
	}
	// Back where the sign-in started; a `next` on the callback is honoured
	// too, for a provider that appends it.
	target := res.Next
	if target == "" || target == "/" {
		if q := r.URL.Query().Get("next"); q != "" && len(q) <= maxNextLength {
			target = q
		}
	}
	http.Redirect(rw, r, Local(target), http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Account
// ---------------------------------------------------------------------------

type accountData struct {
	Name, Username, Error, CSRF string
	Done, Password, HasPassword bool
}

func (w *Web) accountData(rw http.ResponseWriter, r *http.Request, u userRow) accountData {
	return accountData{Name: users.DisplayName(u), Username: u.Username, CSRF: w.formToken(rw, r),
		Password: w.Config.PasswordSignin, HasPassword: u.PasswordHash != nil}
}

func (w *Web) accountPage(rw http.ResponseWriter, r *http.Request) {
	id := w.requireUser(rw, r)
	if id == nil {
		return
	}
	d := w.accountData(rw, r, id.User)
	d.Done = r.URL.Query().Get("done") != ""
	w.render(rw, r, "account.html", signinCSP, http.StatusOK, d)
}

func (w *Web) changePassword(rw http.ResponseWriter, r *http.Request) {
	id := w.requireUser(rw, r)
	if id == nil {
		return
	}
	fail := func(status int, msg string) {
		d := w.accountData(rw, r, id.User)
		d.Error = msg
		w.render(rw, r, "account.html", signinCSP, status, d)
	}
	if err := parseForm(r); err != nil {
		fail(http.StatusBadRequest, "That form couldn't be read.")
		return
	}
	if !w.Config.PasswordSignin {
		fail(http.StatusNotFound, "Passwords are turned off on this Movie Night.")
		return
	}
	if !w.formOK(r) {
		fail(http.StatusBadRequest, "That form had expired. Try again.")
		return
	}
	current, ok1 := field(r, "current", maxPasswordField)
	next, ok2 := field(r, "new", maxPasswordField)
	again, ok3 := field(r, "again", maxPasswordField)
	if !ok1 || !ok2 || !ok3 {
		fail(http.StatusBadRequest, "Use at most 200 characters.")
		return
	}
	if id.User.PasswordHash != nil {
		ok, err := w.Passwords.Matches(r.Context(), id.User, current)
		if err != nil {
			w.Log.ErrorContext(r.Context(), "checking a password failed", "err", err)
			fail(http.StatusServiceUnavailable, "That didn't work just now. Try again in a minute.")
			return
		}
		if !ok {
			fail(http.StatusUnauthorized, "Your current password isn't right.")
			return
		}
	}
	if next != again {
		fail(http.StatusBadRequest, "The two new passwords aren't the same.")
		return
	}
	u, err := w.Passwords.Set(r.Context(), id.User, next)
	var pe *passwords.Error
	if errors.As(err, &pe) {
		fail(http.StatusBadRequest, pe.Msg)
		return
	}
	if err != nil {
		w.Log.ErrorContext(r.Context(), "saving a password failed", "err", err)
		fail(http.StatusServiceUnavailable, "That didn't work just now. Try again in a minute.")
		return
	}
	// Every other browser is now signed out (the epoch went up, and their
	// rows go too); this one stays signed in under the new epoch.
	if id.Via == auth.ViaSession {
		if err := w.Sessions.KeepOnlyThis(r.Context(), id.SessionHash, u); err != nil {
			w.Log.ErrorContext(r.Context(), "keeping this browser signed in failed", "err", err)
		}
	}
	http.Redirect(rw, r, "/account?done=1", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// The phone app's connect page
// ---------------------------------------------------------------------------

type connectData struct {
	Error, Device, Name, Challenge, Redirect, Sig string
	TS                                            int64
	CancelURL                                     template.URL
}

func (w *Web) connectError(rw http.ResponseWriter, r *http.Request, status int, msg string) {
	w.render(rw, r, "connect.html", connectCSP, status, connectData{Error: msg})
}

func (w *Web) connectPage(rw http.ResponseWriter, r *http.Request) {
	id := w.requireUser(rw, r)
	if id == nil {
		return
	}
	q := r.URL.Query()
	challenge, redirect := q.Get("challenge"), q.Get("redirect")
	device := q.Get("device")
	if device == "" {
		device = "Movie Night app"
	}
	if len(challenge) > maxChallengeField || len(redirect) > maxRedirectField || len([]rune(device)) > w.Config.ReportedMaxChars ||
		!devices.ChallengeRe.MatchString(challenge) || !w.Devices.RedirectOK(redirect) {
		w.connectError(rw, r, http.StatusBadRequest, "This sign-in link isn't valid. Start again from the app.")
		return
	}
	ts := nowUnix()
	w.render(rw, r, "connect.html", connectCSP, http.StatusOK, connectData{
		Name: users.DisplayName(id.User), Device: device, Challenge: challenge, Redirect: redirect, TS: ts,
		Sig: w.Devices.FormSig(id.User.Username, challenge, redirect, device, ts),
		// Safe: RedirectOK allowed only the app's own scheme above.
		CancelURL: template.URL(redirect + "?error=cancelled"),
	})
}

func (w *Web) connect(rw http.ResponseWriter, r *http.Request) {
	id := w.requireUser(rw, r)
	if id == nil {
		return
	}
	if err := parseForm(r); err != nil {
		w.connectError(rw, r, http.StatusBadRequest, "This sign-in link isn't valid")
		return
	}
	challenge, ok1 := field(r, "challenge", maxChallengeField)
	redirect, ok2 := field(r, "redirect", maxRedirectField)
	device := r.PostFormValue("device")
	if device == "" {
		device = "Movie Night app"
	}
	sig, ok3 := field(r, "sig", maxSigField)
	ts, tsErr := parseInt(r.PostFormValue("ts"))
	if !ok1 || !ok2 || !ok3 || tsErr != nil || len([]rune(device)) > w.Config.ReportedMaxChars {
		w.connectError(rw, r, http.StatusBadRequest, "This sign-in link isn't valid")
		return
	}
	if e := w.Devices.CheckForm(id.User.Username, challenge, redirect, device, ts, sig); e != nil {
		w.connectError(rw, r, e.Status, e.Detail)
		return
	}
	code, err := w.Devices.MakeCode(r.Context(), id.User.ID, challenge, device)
	if err != nil {
		w.Log.ErrorContext(r.Context(), "making a sign-in code failed", "err", err)
		w.connectError(rw, r, http.StatusServiceUnavailable, "That didn't work just now. Try again in a minute.")
		return
	}
	sep := "?"
	if strings.Contains(redirect, "?") {
		sep = "&"
	}
	// http.Redirect would clean a non-http URL as a path; set the header
	// directly (the scheme was checked by CheckForm).
	rw.Header().Set("Location", redirect+sep+"code="+url.QueryEscape(code))
	rw.WriteHeader(http.StatusSeeOther)
}
