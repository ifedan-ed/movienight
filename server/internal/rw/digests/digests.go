// Package digests is the digest emails' routes (app/api/v1/account.py,
// the digests part): each person's email settings (/me/emails), making a
// digest, its preview and "Send it to me now". There is no list of a
// person's digests: no client ever asked for one (dropped with phase 2). What a digest holds, and
// whom it is sent to, is package digest's; the schedule's sends are the
// worker's (cmd/worker).
package digests

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"movienight/server/internal/api"
	"movienight/server/internal/apierr"
	"movienight/server/internal/digest"
	"movienight/server/internal/mail"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/store"
)

func emailSettingsOut(s digest.Settings) schema.EmailSettings {
	return schema.EmailSettings{Weekly: s.Weekly, Monthly: s.Monthly, Yearly: s.Yearly, Address: s.Address}
}

func digestOut(dg store.Digest) schema.DigestOut {
	out := schema.DigestOut{ID: dg.ID, Kind: dg.Kind, Subject: dg.Subject, PeriodStart: rw.ISOTime(dg.PeriodStart),
		PeriodEnd: rw.ISOTime(dg.PeriodEnd), Model: dg.Model, SentTo: dg.SentTo}
	if dg.SentAt != nil {
		s := rw.ISOTime(*dg.SentAt)
		out.SentAt = &s
	}
	return out
}

// digestCSP: the email's own inline styles and TMDB's posters; nothing else
// may load or run. Movie Night itself may show it in a frame (the preview).
const digestCSP = "default-src 'none'; img-src https://image.tmdb.org; style-src 'unsafe-inline'; " +
	"frame-ancestors 'self'; base-uri 'none'; form-action 'none'"

// Register adds the routes.
func Register(d *rw.Deps) {
	huma.Register(d.Huma(), huma.Operation{
		OperationID: "get_emails_api_v1_me_emails_get", Method: http.MethodGet, Path: "/api/v1/me/emails",
		Summary: "Get Emails", Tags: []string{"account"},
	}, func(ctx context.Context, _ *struct{}) (*rw.Body[schema.EmailSettings], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		got, err := d.Digests.GetSettings(ctx, id.User)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(emailSettingsOut(got)), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "put_emails_api_v1_me_emails_put", Method: http.MethodPut, Path: "/api/v1/me/emails",
		Summary: "Put Emails", Tags: []string{"account"},
	}, func(ctx context.Context, in *rw.Body[schema.EmailSettingsIn]) (*rw.Body[schema.EmailSettings], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		b := in.Body
		got, err := d.Digests.SetSettings(ctx, id.User, digest.Change{Weekly: b.Weekly, Monthly: b.Monthly, Yearly: b.Yearly, Address: b.Address})
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(emailSettingsOut(got)), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "make_digest_api_v1_me_digests__kind__post", Method: http.MethodPost, Path: "/api/v1/me/digests/{kind}",
		Summary: "Make Digest", Tags: []string{"account"},
		Description: "Last week's, month's or year's digest, made now if it doesn't exist yet.",
	}, func(ctx context.Context, in *struct {
		Kind string `path:"kind" pattern:"^(week|month|year)$"`
	}) (*rw.Body[schema.DigestOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		dg, err := d.Digests.Build(ctx, id.User, in.Kind, false)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return rw.Out(digestOut(*dg)), nil
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "digest_html_api_v1_me_digests__digest_id__html_get", Method: http.MethodGet,
		Path: "/api/v1/me/digests/{digest_id}/html", Summary: "Digest Html", Tags: []string{"account"},
	}, func(ctx context.Context, in *struct {
		DigestID int32 `path:"digest_id" exclusiveMinimum:"0"`
	}) (*huma.StreamResponse, error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		dg, err := d.Digests.Get(ctx, id.User.ID, in.DigestID)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		return api.Stream(func(w http.ResponseWriter, _ *http.Request) {
			// Set after the shared security headers, so the email's own
			// policy (posters from TMDB, its inline styles) replaces the
			// app's for this one page.
			w.Header().Set("Content-Security-Policy", digestCSP)
			w.Header().Set("X-Frame-Options", "SAMEORIGIN")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(dg.Html))
		})
	})

	huma.Register(d.Huma(), huma.Operation{
		OperationID: "send_digest_api_v1_me_digests__digest_id__send_post", Method: http.MethodPost,
		Path: "/api/v1/me/digests/{digest_id}/send", Summary: "Send Digest", Tags: []string{"account"},
	}, func(ctx context.Context, in *struct {
		DigestID int32 `path:"digest_id" exclusiveMinimum:"0"`
	}) (*rw.Body[schema.SentOut], error) {
		id, err := d.User(ctx)
		if err != nil {
			return nil, err
		}
		dg, err := d.Digests.Get(ctx, id.User.ID, in.DigestID)
		if err != nil {
			return nil, d.Fail(ctx, err)
		}
		to, err := d.Digests.Send(ctx, id.User, dg, "", false)
		var known *apierr.Error
		switch {
		case err == nil:
			return rw.Out(schema.SentOut{Ok: true, SentTo: to}), nil
		case errors.As(err, &known):
			return nil, known // not set up, no address, being sent: 409
		case mail.IsMaybeSent(err):
			// Recorded as sent: say so, so nobody sends it twice blindly.
			return nil, apierr.New(http.StatusBadGateway, "The email server stopped answering after it was handed the "+
				"email, so it has probably arrived: check your inbox before sending it again")
		}
		d.Log.WarnContext(ctx, "digest send failed", "digest", dg.ID, "err", err)
		return nil, apierr.New(http.StatusBadGateway, "The email server didn't take it — try again")
	})
}
