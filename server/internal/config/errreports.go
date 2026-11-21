package config

import (
	"fmt"

	"github.com/getsentry/sentry-go"
)

// ErrorReports is where crashes and errors are sent: a Sentry-compatible
// collector such as GlitchTip. Every part is optional and off while its DSN
// is empty; one setting each, no defaults borrowed from another.
//
// The website's and the app's DSNs are never given to a browser or a phone.
// The code is public, so a key shipped in it would be public too and anyone
// could fill the collector. Instead the clients send their reports to this
// server (internal/errreports), which checks and limits them and forwards
// them with the DSN it holds.
type ErrorReports struct {
	// ERROR_REPORTS_SERVER_DSN: the api's and the worker's own errors
	// (everything they log at ERROR, panics included).
	ServerDSN string
	// ERROR_REPORTS_WEB_DSN: where the website's reports are forwarded.
	WebDSN string
	// ERROR_REPORTS_APP_DSN: where the phone app's reports are forwarded.
	AppDSN string
	// ERROR_REPORTS_ENVIRONMENT: the environment every report is filed
	// under, the server's and the forwarded ones alike. Default
	// "production".
	Environment string
}

// Any reports whether any part of error reporting is on.
func (e ErrorReports) Any() bool { return e.ServerDSN != "" || e.WebDSN != "" || e.AppDSN != "" }

func (r *reader) errorReports() ErrorReports {
	e := ErrorReports{
		ServerDSN: r.dsn("ERROR_REPORTS_SERVER_DSN"),
		WebDSN:    r.dsn("ERROR_REPORTS_WEB_DSN"),
		AppDSN:    r.dsn("ERROR_REPORTS_APP_DSN"),
	}
	env := r.optional("ERROR_REPORTS_ENVIRONMENT")
	if env != "" && !e.Any() {
		// A lone environment is a half-configured service: most likely a
		// DSN line misspelt, which "off" would hide.
		r.fail("ERROR_REPORTS_ENVIRONMENT is set but none of ERROR_REPORTS_SERVER_DSN, ERROR_REPORTS_WEB_DSN, ERROR_REPORTS_APP_DSN is")
	}
	if env == "" {
		env = "production"
	}
	e.Environment = env
	return e
}

// dsn reads an optional DSN and checks it with the SDK's own parser, so a
// DSN that starts here is one the reporter and the relay can use.
func (r *reader) dsn(name string) string {
	v := r.optional(name)
	if v == "" {
		return ""
	}
	if _, err := sentry.NewDsn(v); err != nil {
		// The parser's message can quote the value; the key in it is a
		// secret, so only the setting is named.
		r.fail(fmt.Sprintf("%s is not a DSN (https://KEY@HOST/PROJECT, as the collector shows it)", name))
		return ""
	}
	return v
}
