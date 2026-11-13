package profile

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"movienight/server/internal/apierr"
	"movienight/server/internal/clients"
	"movienight/server/internal/crypto"
	"movienight/server/internal/players"
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/schema"
	"movienight/server/internal/status"
	"movienight/server/internal/store"
)

// account holds the account routes' services.
type account struct {
	d       *rw.Deps
	players *players.Service
	status  *status.Service
}

// deviceID is the person's own Jellyfin "device", so their Jellyfin session
// stands alone (the Python's users.device_id).
func deviceID(u store.User) string { return "movienight-" + strconv.Itoa(int(u.ID)) }

func (a *account) register() {
	d := a.d
	h := d.Huma()

	// ---- Jellyfin account ----

	huma.Register(h, rw.Op("jellyfin_status_api_v1_me_jellyfin_get", http.MethodGet, "/api/v1/me/jellyfin", "Jellyfin Status", "account"),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.JellyfinLink], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			out, err := a.link(ctx, id.User)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			// Whether the server offers Quick Connect; unknown (null) when
			// Jellyfin doesn't answer: the page then offers the password
			// sign-in only. A deliberate degrade, logged.
			qc, err := d.Clients.Jellyfin.QuickConnectEnabled(ctx)
			if err != nil {
				d.Log.WarnContext(ctx, "Jellyfin Quick Connect check failed", "err", err)
			} else {
				out.QuickConnect = &qc
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("quick_connect_start_api_v1_me_jellyfin_quick_connect_post", http.MethodPost,
		"/api/v1/me/jellyfin/quick-connect", "Quick Connect Start", "account",
		"A code to type into a Jellyfin app where you're signed in\n(Settings → Quick Connect)."),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.QuickConnectOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			got, err := d.Clients.Jellyfin.QuickConnectInitiate(ctx, deviceID(id.User))
			if err != nil {
				return nil, jellyfinErr(ctx, d, err, http.StatusConflict)
			}
			code, _ := got["Code"].(string)
			secret, _ := got["Secret"].(string)
			return rw.Out(schema.QuickConnectOut{Code: code, Secret: secret}), nil
		})

	huma.Register(h, rw.Op("quick_connect_check_api_v1_me_jellyfin_quick_connect_check_post", http.MethodPost,
		"/api/v1/me/jellyfin/quick-connect/check", "Quick Connect Check", "account"),
		func(ctx context.Context, in *rw.Body[schema.QuickConnectIn]) (*rw.Body[schema.QuickConnectStateOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			jf := d.Clients.Jellyfin
			ready, err := jf.QuickConnectReady(ctx, in.Body.Secret, deviceID(id.User))
			if err != nil {
				return nil, jellyfinErr(ctx, d, err, http.StatusConflict)
			}
			if !ready {
				return rw.Out(schema.QuickConnectStateOut{Ok: true}), nil
			}
			auth, err := jf.QuickConnectFinish(ctx, in.Body.Secret, deviceID(id.User))
			if err != nil {
				return nil, jellyfinErr(ctx, d, err, http.StatusConflict)
			}
			u, err := a.saveLink(ctx, id.User, auth)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.QuickConnectStateOut{Ok: true, Linked: true, UserName: u.JellyfinUserName}), nil
		})

	huma.Register(h, rw.Op("password_link_api_v1_me_jellyfin_password_post", http.MethodPost, "/api/v1/me/jellyfin/password",
		"Password Link", "account", "Sign in to Jellyfin once; only the resulting token is kept (encrypted),\nnever the password."),
		func(ctx context.Context, in *rw.Body[schema.PasswordLinkIn]) (*rw.Body[schema.QuickConnectStateOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			auth, err := d.Clients.Jellyfin.SignIn(ctx, strings.TrimSpace(in.Body.Username), in.Body.Password, deviceID(id.User))
			if err != nil {
				// 401 is kept for this device's own Movie Night token: a wrong
				// Jellyfin password is a rejected request (422), not a sign-out.
				var je *clients.JellyfinError
				if errors.As(err, &je) && je.Status == http.StatusUnauthorized {
					return nil, apierr.New(http.StatusUnprocessableEntity, je.Msg)
				}
				return nil, jellyfinErr(ctx, d, err, http.StatusBadGateway)
			}
			u, err := a.saveLink(ctx, id.User, auth)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.QuickConnectStateOut{Ok: true, Linked: true, UserName: u.JellyfinUserName}), nil
		})

	huma.Register(h, rw.Op("unlink_api_v1_me_jellyfin_delete", http.MethodDelete, "/api/v1/me/jellyfin", "Unlink", "account"),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.Ok], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			u := id.User
			if err := d.Q.UnlinkJellyfin(ctx, u.ID); err != nil {
				return nil, d.Fail(ctx, err)
			}
			// An old account's failed reads are no longer news.
			if err := a.status.Forget(ctx, status.Plays(u.ID)); err != nil {
				return nil, d.Fail(ctx, err)
			}
			if u.JellyfinTokenEnc != nil {
				// Best effort, as in the Python: the link is gone here either
				// way; a Jellyfin that doesn't answer keeps a session that no
				// one here holds the token to any more.
				if token, err := d.Keys.Fernet().Decrypt(*u.JellyfinTokenEnc); err == nil {
					if err := d.Clients.Jellyfin.SignOut(ctx, token, deviceID(u)); err != nil {
						d.Log.WarnContext(ctx, "Jellyfin sign-out after unlink failed", "err", err)
					}
				}
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})

	// ---- Players ----

	huma.Register(h, rw.Op("get_players_api_v1_me_players_get", http.MethodGet, "/api/v1/me/players", "Get Players", "account"),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.PlayerSettingsOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			out, err := a.players.Get(ctx, id.User.ID)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(out), nil
		})

	huma.Register(h, rw.Op("put_players_api_v1_me_players_put", http.MethodPut, "/api/v1/me/players", "Put Players", "account"),
		func(ctx context.Context, in *rw.Body[schema.PlayerSettingsIn]) (*rw.Body[schema.PlayerSettingsOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			custom := make([]players.Choice, len(in.Body.Custom))
			for i, c := range in.Body.Custom {
				custom[i] = players.Choice{Name: c.Name, Template: c.Template}
			}
			out, err := a.players.Save(ctx, id.User.ID, custom, in.Body.DefaultPlayer, in.Body.SubtitleLanguage,
				in.Body.Enabled, in.Body.AskFinished)
			if err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(out), nil
		})
	d.Limit("CustomPlayerIn", "name", rw.NameChars)
	d.Limit("CustomPlayerIn", "template", rw.TextChars)

	// ---- Scrobble token (Kodi add-on and other players) ----

	huma.Register(h, rw.Op("new_scrobble_token_api_v1_me_scrobble_token_post", http.MethodPost, "/api/v1/me/scrobble-token",
		"New Scrobble Token", "account", "A new token for other players to report watches; shown once. Making a\nnew one turns the old one off."),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.TokenOut], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			token := "mn_" + crypto.NewToken(30)
			hash := crypto.TokenHash(token)
			if err := d.Q.SetScrobbleTokenHash(ctx, store.SetScrobbleTokenHashParams{ID: id.User.ID, ScrobbleTokenHash: &hash}); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.TokenOut{Ok: true, Token: token}), nil
		})

	huma.Register(h, rw.Op("drop_scrobble_token_api_v1_me_scrobble_token_delete", http.MethodDelete, "/api/v1/me/scrobble-token",
		"Drop Scrobble Token", "account"),
		func(ctx context.Context, _ *struct{}) (*rw.Body[schema.Ok], error) {
			id, err := d.User(ctx)
			if err != nil {
				return nil, err
			}
			if err := d.Q.SetScrobbleTokenHash(ctx, store.SetScrobbleTokenHashParams{ID: id.User.ID}); err != nil {
				return nil, d.Fail(ctx, err)
			}
			return rw.Out(schema.Ok{Ok: true}), nil
		})

}

// link is the person's Jellyfin connection (without the Quick Connect
// check), as /me and /me/jellyfin show it.
func (a *account) link(ctx context.Context, u store.User) (schema.JellyfinLink, error) {
	linked := u.JellyfinUserID != nil && *u.JellyfinUserID != ""
	out := schema.JellyfinLink{Linked: linked, CanPlay: u.JellyfinTokenEnc != nil && *u.JellyfinTokenEnc != "",
		UserName: u.JellyfinUserName, Server: a.d.Cfg.JellyfinPublicURL}
	if u.JellyfinLinkedAt != nil {
		s := rw.ISOTime(*u.JellyfinLinkedAt)
		out.LinkedAt = &s
	}
	notice, err := a.status.PlaysNotice(ctx, u.ID, linked, a.d.Zone(u))
	if err != nil {
		return out, err
	}
	if notice != "" {
		out.PlaysNotice = &notice
	}
	return out, nil
}

// saveLink keeps a Jellyfin sign-in: the account and an encrypted token
// (the password never). One Jellyfin account belongs to one person here.
func (a *account) saveLink(ctx context.Context, u store.User, auth clients.JSON) (store.User, error) {
	jfUser, _ := auth["User"].(map[string]any)
	jfID, _ := jfUser["Id"].(string)
	token, _ := auth["AccessToken"].(string)
	if jfID == "" || token == "" {
		return u, apierr.New(http.StatusConflict, "Jellyfin didn't return an account")
	}
	other, err := a.d.Q.OtherUserWithJellyfin(ctx, store.OtherUserWithJellyfinParams{JellyfinUserID: &jfID, UserID: u.ID})
	if err == nil && other != 0 {
		return u, apierr.New(http.StatusConflict, "That Jellyfin account is already connected to someone else here")
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return u, err
	}
	var name *string
	if n, _ := jfUser["Name"].(string); n != "" {
		name = &n
	}
	enc := a.d.Keys.Fernet().Encrypt(token)
	saved, err := a.d.Q.LinkJellyfin(ctx, store.LinkJellyfinParams{ID: u.ID, JellyfinUserID: jfID, JellyfinUserName: name, JellyfinTokenEnc: &enc})
	if err != nil {
		return u, err
	}
	// A new account's plays start with a clean slate.
	return saved, a.status.Forget(ctx, status.Plays(u.ID))
}

// jellyfinErr turns a Jellyfin failure into the answer: its own message
// with status for a refusal Jellyfin worded, 502 when it didn't answer.
func jellyfinErr(ctx context.Context, d *rw.Deps, err error, status int) error {
	var je *clients.JellyfinError
	if errors.As(err, &je) {
		if je.Status == http.StatusBadGateway {
			status = http.StatusBadGateway
		}
		return apierr.New(status, je.Msg)
	}
	return d.Fail(ctx, err)
}
