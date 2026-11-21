package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/errreports"
)

// registerErrorReports adds the two tunnel endpoints the clients' Sentry
// SDKs send to (their `tunnel` option). They are plain handlers behind a
// documented operation (Stream): the body is an envelope, not JSON, and it
// must not be read before the relay's own checks have passed.
func (a *API) registerErrorReports() {
	envelope := &huma.RequestBody{
		Description: "A Sentry envelope, at most 200 KB. Only its error events are forwarded.",
		Content: map[string]*huma.MediaType{
			"application/x-sentry-envelope": {Schema: &huma.Schema{Type: huma.TypeString}},
		},
	}
	for _, ep := range []struct{ kind, client, check, setting string }{
		{errreports.Web, "website", "an Origin header naming this site", "ERROR_REPORTS_WEB_DSN"},
		{errreports.App, "phone app", "X-Requested-With: movienight", "ERROR_REPORTS_APP_DSN"},
	} {
		kind := ep.kind
		huma.Register(a.huma, huma.Operation{
			OperationID: "error_report_" + kind + "_api_v1_errors_" + kind + "_post", Method: http.MethodPost,
			Path: "/api/v1/errors/" + kind, Summary: "Error Report " + kind, Tags: []string{"errors"},
			Description: "Takes a crash report from the " + ep.client + " and forwards it to the collector\n" +
				"(no sign-in needed; no person is attached). Needs " + ep.check + ". 404 when " + ep.setting + "\n" +
				"is empty, 413 over 200 KB, 400 for anything but an envelope, 429 over 20 reports from one\n" +
				"address in 10 minutes or 300 from everyone in an hour (the report is dropped).",
			RequestBody: envelope,
		}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
			return Stream(func(w http.ResponseWriter, r *http.Request) { a.relay(kind).ServeHTTP(w, r) })
		})
	}
}

func (a *API) relay(kind string) *errreports.Relay {
	if a.Relays == nil {
		return nil // only while describing the API; a nil relay answers 404
	}
	if kind == errreports.Web {
		return a.Relays.Web
	}
	return a.Relays.App
}
