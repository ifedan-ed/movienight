package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"movienight/server/internal/apierr"
	"movienight/server/internal/devices"
	"movienight/server/internal/httpx"
	"movienight/server/internal/passwords"
)

// The request and response types below carry the Python schemas' names,
// which become the OpenAPI component names the phone
// app's generated types use. Request bodies allow extra fields
// (additionalProperties), as FastAPI did: an older client sending a field
// this version no longer reads must keep working.

// Ok is the plain success answer: {"ok": true}.
type Ok struct {
	Ok bool `json:"ok" default:"true" required:"false"`
}

func ok() *struct{ Body Ok } { return &struct{ Body Ok }{Body: Ok{Ok: true}} }

// AppTokenIn is the phone's one-time code and its PKCE verifier.
type AppTokenIn struct {
	_        struct{} `additionalProperties:"true"`
	Code     string   `json:"code" minLength:"10" maxLength:"200"`
	Verifier string   `json:"verifier" minLength:"43" maxLength:"128"`
}

// AppTokenOut is the phone's new device token.
type AppTokenOut struct {
	Ok       bool   `json:"ok" default:"true" required:"false"`
	Token    string `json:"token"`
	User     string `json:"user"`
	DeviceID int32  `json:"device_id"`
}

// PasswordSignInIn is the phone app's username and password (PASSWORD_SIGNIN).
// The limits are the sign-in form's (internal/web): a password longer than
// passwords.MaxLength is never checked, only counted as a wrong one.
type PasswordSignInIn struct {
	_        struct{} `additionalProperties:"true"`
	Username string   `json:"username" minLength:"1" maxLength:"200"`
	Password string   `json:"password" minLength:"1" maxLength:"1000"`
	Device   string   `json:"device,omitempty" default:"Movie Night app" maxLength:"500" doc:"Shown in Profile, Signed-in devices"`
}

// SignInMethodsOut is how this install lets people sign in, for the phone
// app's sign-in screen (it shows the password fields only when password is
// true). Public: the app asks before anyone is signed in.
type SignInMethodsOut struct {
	Password  bool   `json:"password" doc:"PASSWORD_SIGNIN: POST /api/v1/auth/password works"`
	OIDC      bool   `json:"oidc" doc:"OIDC is set up: the sign-in page (and /app/connect) offers it"`
	OIDCLabel string `json:"oidc_label" doc:"OIDC_BUTTON_LABEL, the OIDC button's words"`
	// Here because it is the one answer both clients can read before
	// anyone signs in, and a crash on the sign-in screen is a crash too.
	ErrorReports ErrorReportsOut `json:"error_reports" doc:"Which clients may send crash reports to /api/v1/errors/{web,app}"`
}

// ErrorReportsOut says which clients' reports this server relays: a client
// starts its error reporting only when its own one is true.
type ErrorReportsOut struct {
	Web bool `json:"web" doc:"ERROR_REPORTS_WEB_DSN is set: the website reports to POST /api/v1/errors/web"`
	App bool `json:"app" doc:"ERROR_REPORTS_APP_DSN is set: the phone app reports to POST /api/v1/errors/app"`
}

// The phone's password sign-in answers in the sign-in page's words
// (internal/web), so a person reads the same sentence on either.
const (
	msgPasswordOff  = "Signing in with a password is turned off here."
	msgWrongPair    = "That username and password don't match."
	msgLockedOut    = "Too many wrong passwords for this username. Wait 15 minutes and try again."
	msgSigninBroken = "Signing in isn't working right now. Try again in a minute."
)

// DeviceOut is one signed-in phone or TV.
type DeviceOut struct {
	ID             int32   `json:"id"`
	Name           string  `json:"name"`
	CreatedAt      string  `json:"created_at"`
	LastUsedAt     *string `json:"last_used_at" required:"false" nullable:"true"`
	Kind           string  `json:"kind" enum:"app,scrobble" default:"app" required:"false"`
	ExpiresAt      string  `json:"expires_at"`
	ExpiresBecause string  `json:"expires_because" enum:"unused,age"`
}

// PairConfirmIn is the code a TV shows, typed by the person.
type PairConfirmIn struct {
	_    struct{} `additionalProperties:"true"`
	Code string   `json:"code" minLength:"6" maxLength:"20"`
}

// PairConfirmOut names the TV that was signed in.
type PairConfirmOut struct {
	Ok     bool   `json:"ok" default:"true" required:"false"`
	Device string `json:"device"`
}

// PairStartIn is a TV asking for a code.
type PairStartIn struct {
	_      struct{} `additionalProperties:"true"`
	Device string   `json:"device,omitempty" default:"Kodi" maxLength:"500"`
}

// PairStartOut is the TV's code, its polling secret, and the timings.
type PairStartOut struct {
	Code      string `json:"code"`
	Poll      string `json:"poll"`
	ExpiresIn int    `json:"expires_in"`
	Interval  int    `json:"interval"`
}

// PairPollIn is a TV polling with its secret.
type PairPollIn struct {
	_    struct{} `additionalProperties:"true"`
	Poll string   `json:"poll" minLength:"20" maxLength:"100"`
}

// PairPollOut is "waiting", or "ok" with the token (null fields while
// waiting, as the Python sent them: the Kodi add-on reads them).
type PairPollOut struct {
	Status  string  `json:"status" enum:"waiting,ok"`
	Token   *string `json:"token" required:"false" nullable:"true"`
	User    *string `json:"user" required:"false" nullable:"true"`
	Account *string `json:"account" required:"false" nullable:"true"`
}

type body[T any] struct{ Body T }

// Stream lets a plain net/http handler serve a documented operation (the
// OIDC redirects, a digest's HTML), whose answer is not JSON.
func Stream(h http.HandlerFunc) (*huma.StreamResponse, error) {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		r, w := humago.Unwrap(hctx)
		h(w, r)
	}}, nil
}

// registerAuth: the OIDC sign-in's two legs, the phone app's password sign-in
// and the methods it asks about first. GET /auth/config, GET
// /auth/oidc/config and POST /auth/oidc/logout were dropped in phase 2: no
// client calls them (the website signs out with POST /signout).
func (a *API) registerAuth() {
	huma.Register(a.huma, huma.Operation{
		OperationID: "oidc_login_api_v1_auth_oidc_login_get", Method: http.MethodGet, Path: "/api/v1/auth/oidc/login",
		Summary: "Oidc Login", Tags: []string{"auth"},
		Description: "Send the person to the provider (a redirect). Where to come back to, the `next` query\n" +
			"parameter, is kept with the sign-in's state on the server.",
	}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) { return Stream(a.Web.OIDCLogin) })

	huma.Register(a.huma, huma.Operation{
		OperationID: "oidc_callback_api_v1_auth_oidc_callback_get", Method: http.MethodGet, Path: "/api/v1/auth/oidc/callback",
		Summary: "Oidc Callback", Tags: []string{"auth"},
		Description: "Finish the sign-in: exchange the code, read the ID token, sign in.",
	}, func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		return Stream(a.Web.OIDCCallback)
	})

	huma.Register(a.huma, huma.Operation{
		OperationID: "sign_in_methods_api_v1_auth_methods_get", Method: http.MethodGet, Path: "/api/v1/auth/methods",
		Summary: "Sign In Methods", Tags: []string{"auth"},
		Description: "How this Movie Night lets people sign in (no sign-in needed): the phone app shows its\n" +
			"username and password fields only when `password` is true, and starts its crash reports only\n" +
			"when `error_reports.app` is.",
	}, func(ctx context.Context, _ *struct{}) (*body[SignInMethodsOut], error) {
		return &body[SignInMethodsOut]{Body: SignInMethodsOut{
			Password: a.Config.PasswordSignin, OIDC: a.Config.OIDCEnabled(), OIDCLabel: a.Config.OIDCButtonLabel,
			ErrorReports: ErrorReportsOut{Web: a.Config.ErrorReports.WebDSN != "", App: a.Config.ErrorReports.AppDSN != ""},
		}}, nil
	})

	huma.Register(a.huma, huma.Operation{
		OperationID: "password_sign_in_api_v1_auth_password_post", Method: http.MethodPost, Path: "/api/v1/auth/password",
		Summary: "Password Sign In", Tags: []string{"auth"},
		Description: "The phone app's username and password sign-in (PASSWORD_SIGNIN): the same device token\n" +
			"as /app/token. 404 when password sign-in is off, 401 for a wrong pair, 429 after five wrong\n" +
			"passwords for one username from one address (for 15 minutes; the sign-in page counts too).",
	}, func(ctx context.Context, in *body[PasswordSignInIn]) (*body[AppTokenOut], error) {
		if !a.Config.PasswordSignin {
			return nil, apierr.New(http.StatusNotFound, msgPasswordOff)
		}
		// The lockout is per (username, caller address), shared with the
		// sign-in page (one counter, internal/passwords), so neither door is
		// a way round the other, and guessing from elsewhere never locks the
		// real person out at their own address.
		caller := a.Callers.Addr(httpx.RequestFrom(ctx)).String()
		u, err := a.Passwords.Authenticate(ctx, in.Body.Username, in.Body.Password, caller)
		switch {
		case errors.Is(err, passwords.ErrTooManyTries):
			return nil, apierr.New(http.StatusTooManyRequests, msgLockedOut)
		case err != nil:
			// Fail closed: with the lockout's store down no password is checked.
			a.Log.ErrorContext(ctx, "password sign-in failed to run", "err", err)
			return nil, apierr.New(http.StatusServiceUnavailable, msgSigninBroken)
		case u == nil:
			return nil, apierr.New(http.StatusUnauthorized, msgWrongPair)
		}
		device := in.Body.Device
		if device == "" {
			device = "Movie Night app"
		}
		got, err := a.Devices.Issue(ctx, *u, device)
		if err != nil {
			return nil, a.fail(ctx, err)
		}
		return &body[AppTokenOut]{Body: AppTokenOut{Ok: true, Token: got.Token, User: got.Username, DeviceID: got.DeviceID}}, nil
	})
}

func (a *API) registerDevices() {
	huma.Register(a.huma, huma.Operation{
		OperationID: "app_token_api_v1_app_token_post", Method: http.MethodPost, Path: "/api/v1/app/token",
		Summary: "App Token", Tags: []string{"app"},
		Description: "Swap the one-time code from /app/connect for this device's token.",
	}, func(ctx context.Context, in *body[AppTokenIn]) (*body[AppTokenOut], error) {
		got, err := a.Devices.Redeem(ctx, in.Body.Code, in.Body.Verifier)
		if err != nil {
			return nil, a.fail(ctx, err)
		}
		return &body[AppTokenOut]{Body: AppTokenOut{Ok: true, Token: got.Token, User: got.Username, DeviceID: got.DeviceID}}, nil
	})

	huma.Register(a.huma, huma.Operation{
		OperationID: "my_devices_api_v1_me_devices_get", Method: http.MethodGet, Path: "/api/v1/me/devices",
		Summary: "My Devices", Tags: []string{"app"},
	}, func(ctx context.Context, _ *struct{}) (*body[[]DeviceOut], error) {
		id, err := a.user(ctx)
		if err != nil {
			return nil, err
		}
		rows, err := a.Devices.List(ctx, id.User.ID)
		if err != nil {
			return nil, a.fail(ctx, err)
		}
		out := make([]DeviceOut, 0, len(rows))
		for _, d := range rows {
			end, why := a.Devices.Expiry(d)
			kind := devices.ScopeApp
			if d.Scope == devices.ScopeScrobble {
				kind = devices.ScopeScrobble
			}
			var last *string
			if d.LastUsedAt != nil {
				s := isoTime(*d.LastUsedAt)
				last = &s
			}
			// expires_at: when the device's sign-in runs out unless it is
			// used before (expires_because "unused"), or for good ("age").
			out = append(out, DeviceOut{ID: d.ID, Name: d.Name, CreatedAt: isoTime(d.CreatedAt), LastUsedAt: last,
				Kind: kind, ExpiresAt: isoTime(end), ExpiresBecause: why})
		}
		return &body[[]DeviceOut]{Body: out}, nil
	})

	huma.Register(a.huma, huma.Operation{
		OperationID: "pair_device_api_v1_me_devices_pair_post", Method: http.MethodPost, Path: "/api/v1/me/devices/pair",
		Summary: "Pair Device", Tags: []string{"app"},
		Description: "Confirm the code Kodi (or another TV app) shows: it can then report\nwhat you watch, and nothing else.",
	}, func(ctx context.Context, in *body[PairConfirmIn]) (*body[PairConfirmOut], error) {
		id, err := a.user(ctx)
		if err != nil {
			return nil, err
		}
		device, err := a.Devices.ConfirmPair(ctx, id.User.ID, in.Body.Code)
		if err != nil {
			return nil, a.fail(ctx, err)
		}
		return &body[PairConfirmOut]{Body: PairConfirmOut{Ok: true, Device: device}}, nil
	})

	huma.Register(a.huma, huma.Operation{
		OperationID: "sign_out_device_api_v1_me_devices__device_id__delete", Method: http.MethodDelete,
		Path: "/api/v1/me/devices/{device_id}", Summary: "Sign Out Device", Tags: []string{"app"},
	}, func(ctx context.Context, in *struct {
		DeviceID int32 `path:"device_id" exclusiveMinimum:"0"`
	}) (*struct{ Body Ok }, error) {
		id, err := a.user(ctx)
		if err != nil {
			return nil, err
		}
		gone, err := a.Devices.SignOut(ctx, id.User.ID, in.DeviceID)
		if err != nil {
			return nil, a.fail(ctx, err)
		}
		if !gone {
			return nil, apierr.New(http.StatusNotFound, "Not found")
		}
		return ok(), nil
	})
}

func (a *API) registerPairing() {
	huma.Register(a.huma, huma.Operation{
		OperationID: "pair_start_api_v1_scrobble_pair_post", Method: http.MethodPost, Path: "/api/v1/scrobble/pair",
		Summary: "Pair Start", Tags: []string{"scrobble"},
	}, func(ctx context.Context, in *body[PairStartIn]) (*body[PairStartOut], error) {
		r := httpx.RequestFrom(ctx)
		allowed, err := a.pairStarts.Take(ctx, a.Callers.Bucket(r))
		if err != nil {
			// Fail closed: the limit is a security measure (docs/limits.md).
			return nil, a.fail(ctx, err)
		}
		if !allowed {
			return nil, apierr.New(http.StatusTooManyRequests, "Too many sign-ins from here; try again in a few minutes")
		}
		code, poll, err := a.Devices.StartPair(ctx, in.Body.Device)
		if err != nil {
			return nil, a.fail(ctx, err)
		}
		// The TV polls /pair/poll every `interval` seconds until the code runs out.
		return &body[PairStartOut]{Body: PairStartOut{Code: code, Poll: poll,
			ExpiresIn: int(devices.PairTTL.Seconds()), Interval: devices.PollInterval}}, nil
	})

	huma.Register(a.huma, huma.Operation{
		OperationID: "pair_poll_api_v1_scrobble_pair_poll_post", Method: http.MethodPost, Path: "/api/v1/scrobble/pair/poll",
		Summary: "Pair Poll", Tags: []string{"scrobble"},
	}, func(ctx context.Context, in *body[PairPollIn]) (*body[PairPollOut], error) {
		got, err := a.Devices.CollectPair(ctx, in.Body.Poll)
		if err != nil {
			return nil, a.fail(ctx, err)
		}
		if got == nil {
			return &body[PairPollOut]{Body: PairPollOut{Status: "waiting"}}, nil
		}
		return &body[PairPollOut]{Body: PairPollOut{Status: "ok", Token: &got.Token, User: &got.DisplayName, Account: &got.Username}}, nil
	})
}

// isoTime writes a time as Python's datetime.isoformat() did for these
// UTC values ("2026-11-12T20:15:03.123456+00:00"), so clients parse it as
// before.
func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000-07:00")
}
