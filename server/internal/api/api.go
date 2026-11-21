// Package api is the JSON API under /api/v1, described with Huma so the
// OpenAPI document is generated from the same Go types that serve it. The
// phone app's TypeScript types come from docs/openapi.json, so operation ids,
// schema names and field names follow the Python's (FastAPI's) exactly for
// every route ported; api_test.go compares them with docs/openapi.json.
//
// This package holds the sign-in routes (OIDC, the phone app's token swap and
// password sign-in, signed-in devices, TV pairing) and the shared plumbing
// (errors, the description). The read and write routes (home, titles, search, lists,
// profile, account) live in internal/rw/... and register on Huma().
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/apierr"
	"movienight/server/internal/auth"
	"movienight/server/internal/config"
	"movienight/server/internal/devices"
	"movienight/server/internal/errreports"
	"movienight/server/internal/httpx"
	"movienight/server/internal/passwords"
	"movienight/server/internal/ratelimit"
	"movienight/server/internal/web"
)

// Title and Version are the API description's, as in the Python.
const (
	Title   = "Movie Night"
	Version = "2.0"
)

// Deps is what the routes need.
type Deps struct {
	Config  *config.Config
	Auth    *auth.Authenticator
	Devices *devices.Service
	// Passwords checks the phone app's password sign-in (the sign-in page
	// uses the same service, with the same lockout).
	Passwords *passwords.Service
	Web       *web.Web
	Callers   *httpx.Callers
	KV        valkey.Client
	Log       *slog.Logger
	// Relays take the website's and the app's error reports
	// (internal/errreports); nil only while building the description.
	Relays *errreports.Relays
}

// API is the registered routes and their description.
type API struct {
	Deps
	huma       huma.API
	pairStarts *ratelimit.Window
}

func init() {
	// A list is a list: never null unless a field says so (nullable:"true").
	// FastAPI sent [] for an empty list, and the phone app's types say
	// "array"; Huma's default would publish every slice as "array or null".
	// Handlers therefore always send a non-nil slice.
	huma.DefaultArrayNullable = false
	// Every error Huma itself makes (validation, a bad body, a panic it
	// catches) takes the Python's shape: {"detail": "..."} and, for 422, the
	// FastAPI-style "errors" list (package apierr).
	huma.NewError = newError
}

// Config is the Huma configuration: OpenAPI 3.1, no docs or spec routes of
// Huma's own (both are served behind sign-in, below), and no "$schema"
// links added to responses (Huma's default link transformer would add a
// "$schema" field to every JSON body the clients never had).
func Config() huma.Config {
	c := huma.DefaultConfig(Title, Version)
	c.OpenAPIPath = ""
	c.DocsPath = ""
	c.SchemasPath = ""
	c.CreateHooks = nil
	return c
}

// New registers the routes on mux.
func New(mux *http.ServeMux, d Deps) *API {
	a := &API{
		Deps: d,
		huma: humago.New(mux, Config()),
		// At most PAIR_STARTS_PER_ADDRESS new TV codes per address in any 10
		// minutes (the life of a code), so no one caller can use up the
		// install-wide waiting codes and block every TV.
		pairStarts: ratelimit.New(d.KV, "pair-start", d.Config.PairStartsPerAddress, devices.PairTTL),
	}
	a.registerAuth()
	a.registerDevices()
	a.registerPairing()
	a.registerErrorReports()

	// The API description and its docs need a real sign-in, like every other
	// API route (the proxy lets through anything that looks like a token).
	mux.HandleFunc("GET /api/v1/openapi.json", a.openapi)
	mux.HandleFunc("GET /api/v1/docs", a.docs)
	// Anything else under /api is a JSON 404, never the app shell. One
	// pattern per method: a method-less "/api/" would clash with "GET /".
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		mux.HandleFunc(m+" /api/", func(w http.ResponseWriter, _ *http.Request) { apierr.Write(w, apierr.NotFound()) })
	}
	return a
}

// Huma is the registered API, for the route packages of later phases
// (internal/rw/...), which register their operations on it.
func (a *API) Huma() huma.API { return a.huma }

// User is the signed-in person for an operation, or its 401 (exported for
// the route packages).
func (a *API) User(ctx context.Context) (*auth.Identity, error) { return a.user(ctx) }

// Fail turns any error into what a handler returns (see fail).
func (a *API) Fail(ctx context.Context, err error) error { return a.fail(ctx, err) }

// OpenAPI is the generated description (for docs/openapi.json and tests).
func (a *API) OpenAPI() *huma.OpenAPI { return a.huma.OpenAPI() }

func (a *API) openapi(w http.ResponseWriter, r *http.Request) {
	if _, err := a.Auth.User(r.Context()); err != nil {
		a.writeErr(w, r, err)
		return
	}
	body, err := a.huma.OpenAPI().MarshalJSON()
	if err != nil {
		a.writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// The docs page: Swagger UI (as FastAPI served), pinned to one version.
const docsPage = `<!doctype html><html><head><meta charset="utf-8"><title>Movie Night API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css"></head>
<body><div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: "/api/v1/openapi.json", dom_id: "#swagger-ui"})</script></body></html>`

func (a *API) docs(w http.ResponseWriter, r *http.Request) {
	if _, err := a.Auth.User(r.Context()); err != nil {
		a.writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}

// fail turns any error into the error a handler returns: an *apierr.Error as
// it is; anything else is logged and becomes a plain 500 (its text could
// hold internals, so it is never sent).
func (a *API) fail(ctx context.Context, err error) error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}
	a.Log.ErrorContext(ctx, "request failed", "err", err)
	return apierr.Internal()
}

func (a *API) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apierr.Error
	if !errors.As(a.fail(r.Context(), err), &ae) {
		ae = apierr.Internal()
	}
	apierr.Write(w, ae)
}

// user is the signed-in person for an operation, or its 401.
func (a *API) user(ctx context.Context) (*auth.Identity, error) {
	id, err := a.Auth.User(ctx)
	if err != nil {
		return nil, a.fail(ctx, err)
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Errors in the Python's shape
// ---------------------------------------------------------------------------

func newError(status int, msg string, errs ...error) huma.StatusError {
	e := &apierr.Error{Status: status}
	for _, err := range errs {
		if err == nil {
			continue
		}
		var d *huma.ErrorDetail
		if errors.As(err, &d) {
			e.Errors = append(e.Errors, item(d))
		} else {
			e.Errors = append(e.Errors, apierr.ValidationError{Type: "value_error", Loc: []any{}, Msg: err.Error()})
		}
	}
	switch {
	case status == http.StatusUnprocessableEntity && len(e.Errors) > 0:
		var parts []string
		for i, it := range e.Errors {
			if i == 3 {
				break
			}
			parts = append(parts, plain(it))
		}
		e.Detail = strings.Join(parts, " ")
	case status == http.StatusUnprocessableEntity:
		e.Detail = "That isn't valid."
	case status >= 500:
		e.Detail = "Internal Server Error"
		e.Errors = nil
	default:
		e.Detail = sentence(msg)
		if status == http.StatusBadRequest && len(e.Errors) > 0 {
			// A body that is not JSON at all: say so plainly.
			e.Detail = "The request body isn't valid JSON."
		}
		e.Errors = nil
	}
	return e
}

func sentence(msg string) string {
	if msg == "" {
		return "That isn't valid."
	}
	return strings.ToUpper(msg[:1]) + msg[1:]
}

var (
	requiredRe = regexp.MustCompile(`^expected required property (\S+) to be present$`)
	maxLenRe   = regexp.MustCompile(`^expected length <= (\d+)$`)
	minLenRe   = regexp.MustCompile(`^expected length >= (\d+)$`)
	maxItemsRe = regexp.MustCompile(`^expected array length <= (\d+)$`)
	exclMinRe  = regexp.MustCompile(`^expected number > (\S+)$`)
	minRe      = regexp.MustCompile(`^expected number >= (\S+)$`)
	maxRe      = regexp.MustCompile(`^expected number <= (\S+)$`)
	indexRe    = regexp.MustCompile(`^([^\[]*)((?:\[\d+\])*)$`)
	digitsRe   = regexp.MustCompile(`\d+`)
)

// item turns Huma's "body.items[3].tags: expected length <= 5" into the
// FastAPI-style entry the clients knew.
func item(d *huma.ErrorDetail) apierr.ValidationError {
	loc := splitLocation(d.Location)
	it := apierr.ValidationError{Loc: loc, Msg: sentence(d.Message), Input: d.Value}
	switch m := d.Message; {
	case requiredRe.MatchString(m):
		it.Loc = append(loc, requiredRe.FindStringSubmatch(m)[1])
		it.Type, it.Msg, it.Input = "missing", "Field required", nil
	case maxLenRe.MatchString(m):
		n, _ := strconv.Atoi(maxLenRe.FindStringSubmatch(m)[1])
		it.Type, it.Ctx = "string_too_long", map[string]any{"max_length": n}
		it.Msg = fmt.Sprintf("String should have at most %d characters", n)
	case minLenRe.MatchString(m):
		n, _ := strconv.Atoi(minLenRe.FindStringSubmatch(m)[1])
		it.Type, it.Ctx = "string_too_short", map[string]any{"min_length": n}
		it.Msg = fmt.Sprintf("String should have at least %d characters", n)
	case maxItemsRe.MatchString(m):
		n, _ := strconv.Atoi(maxItemsRe.FindStringSubmatch(m)[1])
		it.Type, it.Ctx = "too_long", map[string]any{"max_length": n}
	case exclMinRe.MatchString(m):
		it.Type, it.Ctx = "greater_than", map[string]any{"gt": exclMinRe.FindStringSubmatch(m)[1]}
		it.Msg = "Input should be greater than " + exclMinRe.FindStringSubmatch(m)[1]
	case maxRe.MatchString(m):
		// "Page: Input should be less than or equal to 25." (search's last page).
		n := maxRe.FindStringSubmatch(m)[1]
		it.Type, it.Ctx = "less_than_equal", map[string]any{"le": n}
		it.Msg = "Input should be less than or equal to " + n
	case minRe.MatchString(m):
		it.Type, it.Ctx = "greater_than_equal", map[string]any{"ge": minRe.FindStringSubmatch(m)[1]}
	default:
		it.Type = "value_error"
	}
	return it
}

// splitLocation: "body.items[3].tags" -> ["body", "items", 3, "tags"].
func splitLocation(s string) []any {
	out := []any{}
	if s == "" {
		return out
	}
	for _, part := range strings.Split(s, ".") {
		m := indexRe.FindStringSubmatch(part)
		if m == nil {
			out = append(out, part)
			continue
		}
		if m[1] != "" {
			out = append(out, m[1])
		}
		for _, idx := range digitsRe.FindAllString(m[2], -1) {
			n, _ := strconv.Atoi(idx)
			out = append(out, n)
		}
	}
	return out
}

// fieldNames: how a field is named in a sentence.
var fieldNames = map[string]string{
	"review": "Your comment", "description": "The description", "q": "A search", "name": "The name",
}

// plain is one validation error as a sentence a person can act on: "Your
// comment is too long: at most 5,000 characters."
func plain(it apierr.ValidationError) string {
	field := ""
	for i := len(it.Loc) - 1; i >= 0; i-- {
		if s, ok := it.Loc[i].(string); ok && s != "body" && s != "query" && s != "path" && s != "header" {
			field = s
			break
		}
	}
	name := fieldNames[field]
	if name == "" {
		name = strings.ReplaceAll(field, "_", " ")
		if name == "" {
			name = "It"
		} else {
			name = strings.ToUpper(name[:1]) + name[1:]
		}
	}
	// Limits the owner keeps (docs/limits.md; schema.PlayerSettingsIn).
	switch {
	case it.Type == "too_long" && field == "custom":
		return fmt.Sprintf("You can have up to %s players of your own.", thousands(it.Ctx["max_length"]))
	case it.Type == "too_long" && field == "enabled":
		return fmt.Sprintf("Up to %s players can be ticked.", thousands(it.Ctx["max_length"]))
	}
	switch it.Type {
	case "string_too_long":
		return fmt.Sprintf("%s is too long: at most %s characters.", name, thousands(it.Ctx["max_length"]))
	case "too_long":
		return fmt.Sprintf("%s: at most %s at a time.", name, thousands(it.Ctx["max_length"]))
	}
	return fmt.Sprintf("%s: %s.", name, strings.TrimSuffix(it.Msg, "."))
}

// thousands writes 5000 as "5,000", as Python's {:,} did.
func thousands(v any) string {
	n, _ := v.(int)
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// Spec builds the API description without any connection: the routes are
// registered on a throwaway mux with empty services, which only the handlers
// (never run here) would use.
func Spec() (*huma.OpenAPI, error) {
	cfg := &config.Config{PairStartsPerAddress: 1}
	a := New(http.NewServeMux(), Deps{Config: cfg, Log: slog.Default()})
	return a.OpenAPI(), nil
}
