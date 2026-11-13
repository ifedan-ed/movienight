package server_test

// Request-level protections, the error envelope and the access log.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"movienight/server/internal/server"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func TestSecurityHeaders(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	r := s.Client("alice", false).Do(t, "GET", "/api/v1/me/devices", nil)
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "script-src 'self'") ||
		r.Header.Get("X-Frame-Options") != "DENY" || r.Header.Get("X-Content-Type-Options") != "nosniff" ||
		r.Header.Get("Referrer-Policy") != "same-origin" || r.Header.Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Fatalf("%v", r.Header)
	}
	if len(r.Header.Get("X-Request-ID")) != 12 {
		t.Fatal("no request id")
	}
}

func TestWritesNeedTheCSRFHeader(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	r := s.Client("alice", false).Do(t, "POST", "/api/v1/me/devices/pair", `{"code":"ABCD-EFGH"}`)
	if r.StatusCode != 403 || r.Body != `{"detail":"Missing X-Requested-With: movienight header"}` {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// The scrobble endpoints are called by other servers, never a browser.
	if r := s.Client("", false).Do(t, "POST", "/api/v1/scrobble/pair", `{}`); r.StatusCode != 200 {
		t.Fatal(r.StatusCode, r.Body)
	}
	// A path that only starts with the same letters is not exempt.
	if r := s.Client("", false).Do(t, "POST", "/api/v1/scrobbler", `{}`); r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
}

func TestDotSegmentsAndEncodedSlashesAreRefused(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	c := s.Client("alice", false)
	for _, path := range []string{"/api/v1/scrobble/%2e%2e/me", "/api/v1/scrobble//me", "/api/v1/scrobble/..%2fme", "/static/%5c..%5cetc"} {
		req, _ := http.NewRequest("GET", s.URL, nil)
		req.URL.Opaque = path // sent exactly as written, not cleaned by the client
		resp, err := c.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
}

func TestUnknownAPIPathsAreJSON404s(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	for _, m := range []string{"GET", "POST", "DELETE"} {
		r := s.Client("alice", true).Do(t, m, "/api/v1/nothing-here", nil)
		if r.StatusCode != 404 || r.Body != `{"detail":"Not Found"}` || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("%s: %d %s", m, r.StatusCode, r.Body)
		}
	}
}

func TestValidationErrorsInThePythonShape(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	r := s.Client("", true).Do(t, "POST", "/api/v1/app/token", `{"code":"`+strings.Repeat("x", 300)+`"}`)
	if r.StatusCode != 422 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	var out struct {
		Detail string `json:"detail"`
		Errors []struct {
			Type string         `json:"type"`
			Loc  []any          `json:"loc"`
			Msg  string         `json:"msg"`
			Ctx  map[string]any `json:"ctx"`
		} `json:"errors"`
	}
	if err := json.Unmarshal([]byte(r.Body), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Detail, "Code is too long: at most 200 characters.") || !strings.Contains(out.Detail, "Verifier: Field required.") {
		t.Fatalf("detail: %q", out.Detail)
	}
	kinds := []string{}
	for _, e := range out.Errors {
		kinds = append(kinds, e.Type+":"+toString(e.Loc))
	}
	sort.Strings(kinds)
	if strings.Join(kinds, ",") != "missing:body.verifier,string_too_long:body.code" {
		t.Fatalf("errors: %v", kinds)
	}
	// Extra fields are allowed, as FastAPI allowed them (older clients).
	r = s.Client("", true).Do(t, "POST", "/api/v1/scrobble/pair", `{"device":"Kodi","addon_version":"1.2"}`)
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode, r.Body)
	}
}

func toString(loc []any) string {
	parts := make([]string, len(loc))
	for i, p := range loc {
		b, _ := json.Marshal(p)
		parts[i] = strings.Trim(string(b), `"`)
	}
	return strings.Join(parts, ".")
}

func TestABodyTooLargeIsRefused(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	r := s.Client("", true).Do(t, "POST", "/api/v1/scrobble/pair", `{"device":"`+strings.Repeat("x", 2<<20)+`"}`)
	if r.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatal(r.StatusCode)
	}
}

func TestIdentityHeadersSignNobodyIn(t *testing.T) {
	env := testbase.BaseEnv(t)
	env["AUTH_DEV_ALLOWED"] = "false" // a real install
	s := testenv.Start(t, env)
	c := s.Client("", false)
	for k, v := range map[string]string{"X-Authentik-Username": "owner", "X-Forwarded-User": "owner", "X-Dev-User": "owner", "Remote-User": "owner"} {
		c.Headers.Set(k, v)
	}
	if r := c.Do(t, "GET", "/api/v1/me/devices", nil); r.StatusCode != 401 {
		t.Fatal(r.StatusCode)
	}
	var n int
	_ = s.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users").Scan(&n)
	if n != 0 {
		t.Fatal("somebody was created")
	}
}

func TestTheAPIDescriptionNeedsASignIn(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	fake := bearer(s, "mn_app_madeup", false)
	for _, p := range []string{"/api/v1/openapi.json", "/api/v1/docs"} {
		if r := fake.Do(t, "GET", p, nil); r.StatusCode != 401 {
			t.Fatal(p, r.StatusCode)
		}
	}
	alice := s.Client("alice", false)
	if r := alice.Do(t, "GET", "/api/v1/openapi.json", nil); r.StatusCode != 200 || !strings.Contains(r.Body, "/api/v1/me/devices") {
		t.Fatal(r.StatusCode)
	}
	if r := alice.Do(t, "GET", "/api/v1/docs", nil); !strings.Contains(r.Body, "swagger-ui") || r.Header.Get("Content-Security-Policy") != "" {
		t.Fatal("docs page")
	}
}

func TestOneAccessLinePerRequestWithoutQueryOrPerson(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t))
	alice := s.Client("alice", false)
	r := alice.Do(t, "GET", "/api/v1/me/devices?q=kids+with+a+treasure+map", nil)
	alice.Do(t, "DELETE", "/api/v1/me/devices/12345", nil, "X-Requested-With", "movienight")
	logs := s.Logs.String()
	var line map[string]any
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `"msg":"request"`) && strings.Contains(l, r.Header.Get("X-Request-ID")) {
			_ = json.Unmarshal([]byte(l), &line)
		}
	}
	if line["route"] != "/api/v1/me/devices" || line["status"] != float64(200) || line["user"] == nil {
		t.Fatalf("access line: %v\n%s", line, logs)
	}
	if strings.Contains(logs, "treasure") || strings.Contains(logs, "alice") || strings.Contains(logs, "12345") {
		t.Fatalf("the log holds what it must not:\n%s", logs)
	}
	if !strings.Contains(logs, `"route":"/api/v1/me/devices/{device_id}"`) {
		t.Fatalf("no route pattern:\n%s", logs)
	}
}

func TestTheShellAndStaticFiles(t *testing.T) {
	s := testenv.Start(t, testbase.BaseEnv(t)) // no sign-in set up: the shell is served
	c := s.Client("", false)
	r := c.Do(t, "GET", "/history", nil)
	if r.StatusCode != 200 || strings.Contains(r.Body, "{{") || !strings.Contains(r.Body, `data-v="`) {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	if r := c.Do(t, "GET", "/static/css/app.css", nil); r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal("static", r.StatusCode)
	}
	if r := c.Do(t, "GET", "/static/css/", nil); r.StatusCode != 404 {
		t.Fatal("directory listing", r.StatusCode)
	}
	if r := c.Do(t, "GET", "/sw.js", nil); r.StatusCode != 200 || r.Header.Get("Service-Worker-Allowed") != "/" {
		t.Fatal("service worker", r.StatusCode)
	}
	if r := c.Do(t, "GET", "/healthz", nil); r.Body != `{"status":"ok"}` {
		t.Fatal(r.Body)
	}
}

// TestTheDescriptionMatchesThePhoneAppsTypes compares every operation this
// phase serves with docs/openapi.json (the Python's, which the phone app's
// TypeScript types are generated from): the same operation ids, tags and
// paths, and for every request and response schema the same name, the same
// fields, the same required fields and the same JSON types.
func TestTheDescriptionMatchesThePhoneAppsTypes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testbase.RepoRoot(t), "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var py, gospec map[string]any
	if err := json.Unmarshal(raw, &py); err != nil {
		t.Fatal(err)
	}
	spec, err := server.Spec()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := spec.MarshalJSON()
	_ = json.Unmarshal(b, &gospec)

	pyPaths, goPaths := py["paths"].(map[string]any), gospec["paths"].(map[string]any)
	pySchemas := py["components"].(map[string]any)["schemas"].(map[string]any)
	goSchemas := gospec["components"].(map[string]any)["schemas"].(map[string]any)
	if len(goPaths) == 0 {
		t.Fatal("no operations")
	}
	for path, ops := range goPaths {
		for method, op := range ops.(map[string]any) {
			g := op.(map[string]any)
			pyOps, ok := pyPaths[path].(map[string]any)
			if !ok {
				t.Errorf("%s is not in docs/openapi.json", path)
				continue
			}
			p, ok := pyOps[method].(map[string]any)
			if !ok {
				t.Errorf("%s %s is not in docs/openapi.json", method, path)
				continue
			}
			if g["operationId"] != p["operationId"] {
				t.Errorf("%s %s: operationId %v, Python %v", method, path, g["operationId"], p["operationId"])
			}
			if toJSON(g["tags"]) != toJSON(p["tags"]) {
				t.Errorf("%s %s: tags %v, Python %v", method, path, g["tags"], p["tags"])
			}
			compareRef(t, path+" request", requestRef(g), requestRef(p), goSchemas, pySchemas)
			compareRef(t, path+" response", responseRef(g), responseRef(p), goSchemas, pySchemas)
		}
	}
}

func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func requestRef(op map[string]any) string {
	rb, _ := op["requestBody"].(map[string]any)
	return refIn(rb)
}

func responseRef(op map[string]any) string {
	rs, _ := op["responses"].(map[string]any)
	ok, _ := rs["200"].(map[string]any)
	return refIn(ok)
}

// refIn finds the schema name in a request body or response, following an
// array's items (a list of DeviceOut).
func refIn(v map[string]any) string {
	content, _ := v["content"].(map[string]any)
	j, _ := content["application/json"].(map[string]any)
	sch, _ := j["schema"].(map[string]any)
	if items, ok := sch["items"].(map[string]any); ok {
		sch = items
	}
	ref, _ := sch["$ref"].(string)
	return strings.TrimPrefix(ref, "#/components/schemas/")
}

// compareRef compares one schema with the Python's of the same name and,
// through every property that refers to another schema (directly, as an
// array's items, a map's values, or one of anyOf/oneOf), the schemas it
// uses: a Card inside CardsOut is checked as closely as CardsOut itself.
func compareRef(t *testing.T, where, goRef, pyRef string, goSchemas, pySchemas map[string]any) {
	t.Helper()
	compareSchema(t, where, goRef, pyRef, goSchemas, pySchemas, map[string]bool{})
}

func compareSchema(t *testing.T, where, goRef, pyRef string, goSchemas, pySchemas map[string]any, seen map[string]bool) {
	t.Helper()
	if pyRef == "" {
		return // the Python's was an untyped dict or a redirect: anything typed is better
	}
	if target, _ := pySchemas[pyRef].(map[string]any); target["enum"] != nil {
		// An enum the Python named (MediaType): Go inlines enums; the
		// property's type check covers it.
		return
	}
	if goRef != pyRef {
		t.Errorf("%s: schema %q, Python %q", where, goRef, pyRef)
		return
	}
	if seen[goRef] {
		return
	}
	seen[goRef] = true
	g, _ := goSchemas[goRef].(map[string]any)
	p, _ := pySchemas[pyRef].(map[string]any)
	if g == nil || p == nil {
		t.Errorf("%s: schema %q missing (Go %v, Python %v)", where, goRef, g != nil, p != nil)
		return
	}
	if p["enum"] != nil {
		// An enum the Python named (MediaType): Go inlines enums; the
		// property's type check covers it.
		return
	}
	gp, pp := g["properties"].(map[string]any), p["properties"].(map[string]any)
	if keys(gp) != keys(pp) {
		t.Errorf("%s: fields %s, Python %s", goRef, keys(gp), keys(pp))
	}
	if toJSON(sorted(g["required"])) != toJSON(sorted(p["required"])) {
		t.Errorf("%s: required %v, Python %v", goRef, g["required"], p["required"])
	}
	for name, prop := range pp {
		gt, pt := propTypes(gp[name], goSchemas), propTypes(prop, pySchemas)
		// Huma cannot mark a $ref nullable (OpenAPI 3.1 forbids siblings
		// of $ref); the Go server still sends null there.
		if gt != pt && !(isRef(gp[name]) && pt == "null") {
			t.Errorf("%s.%s: type %s, Python %s", goRef, name, gt, pt)
		}
		// An enum the Python named (MediaType) is inlined by Huma; its type
		// was compared above.
		if pr := nestedRef(prop); pr != "" && !isEnum(pySchemas[pr]) {
			compareSchema(t, goRef+"."+name, nestedRef(gp[name]), pr, goSchemas, pySchemas, seen)
		}
	}
}

// propTypes is jsonTypes, with a reference to an enum schema (Pydantic's
// MediaType) read as that schema's own type.
func propTypes(v any, schemas map[string]any) string {
	m, _ := v.(map[string]any)
	if ref, ok := m["$ref"].(string); ok {
		if target, _ := schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any); target["enum"] != nil {
			return jsonTypes(target)
		}
	}
	// FastAPI writes an optional enum as anyOf [{$ref MediaType}, {null}]:
	// read the referenced enum's type into the set as well.
	types := jsonTypes(v)
	for _, alt := range asList(m["anyOf"]) {
		ref, _ := alt.(map[string]any)["$ref"].(string)
		if target, _ := schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any); ref != "" && target["enum"] != nil {
			parts := append(strings.Split(types, "|"), jsonTypes(target))
			sort.Strings(parts)
			types = strings.Trim(strings.Join(parts, "|"), "|")
		}
	}
	return types
}

// isEnum: a named enum (Pydantic's MediaType), which Go inlines; the
// property's type check has covered it.
func isEnum(v any) bool {
	m, _ := v.(map[string]any)
	return m["enum"] != nil
}

func isRef(v any) bool {
	m, _ := v.(map[string]any)
	_, ok := m["$ref"]
	return ok
}

// nestedRef is the schema a property uses: its $ref, its items' or
// additionalProperties' $ref, or the first $ref among anyOf/oneOf.
func nestedRef(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	if ref, ok := m["$ref"].(string); ok {
		return strings.TrimPrefix(ref, "#/components/schemas/")
	}
	for _, k := range []string{"items", "additionalProperties"} {
		if r := nestedRef(m[k]); r != "" {
			return r
		}
	}
	for _, k := range []string{"anyOf", "oneOf"} {
		for _, alt := range asList(m[k]) {
			if r := nestedRef(alt); r != "" {
				return r
			}
		}
	}
	return ""
}

func keys(m map[string]any) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func sorted(v any) []string {
	var out []string
	for _, x := range asList(v) {
		out = append(out, x.(string))
	}
	sort.Strings(out)
	return out
}

func asList(v any) []any { l, _ := v.([]any); return l }

// jsonTypes is a property's JSON type set, read the same way from 3.1's
// "type": ["string", "null"] (Huma) and "anyOf": [{string}, {null}] (FastAPI).
func jsonTypes(v any) string {
	m, _ := v.(map[string]any)
	var types []string
	switch t := m["type"].(type) {
	case string:
		types = append(types, t)
	case []any:
		for _, x := range t {
			types = append(types, x.(string))
		}
	}
	for _, alt := range asList(m["anyOf"]) {
		if s, ok := alt.(map[string]any)["type"].(string); ok {
			types = append(types, s)
		}
	}
	sort.Strings(types)
	return strings.Join(types, "|")
}
