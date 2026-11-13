package title

import (
	"context"
	"errors"
	"net/http"

	"movienight/server/internal/clients"
	"movienight/server/internal/rw"
	"movienight/server/internal/store"
)

// Requests to the install's Seerr, made as the person asking.
// Each person's Seerr user is the one
// Seerr made from their Jellyfin account (Seerr's Jellyfin import), so
// requests, quotas and approvals are theirs; the install's admins may
// request without a linked Jellyfin (as the API key's user).

// createRequest asks Seerr for a title as the person. already: Seerr had a
// request (a 409 there is not an error here); dryRun: DRY_RUN, nothing sent.
func (t *Title) createRequest(ctx context.Context, u store.User, media string, id int, seasons []int) (already, dryRun bool, err error) {
	s := t.d.Clients.Seerr
	if !s.Enabled() && !t.d.Cfg.DryRun {
		return false, false, rw.Err(http.StatusServiceUnavailable, "Requests aren't set up on this install")
	}
	asUser := 0
	if !t.d.Cfg.DryRun {
		asUser, err = t.d.SeerrUser(ctx, u)
		var se *clients.SeerrError
		if errors.As(err, &se) {
			return false, false, rw.Err(http.StatusBadGateway, "Seerr: "+se.Msg)
		}
		if err != nil {
			return false, false, t.seerrDown(ctx, err)
		}
		if asUser == 0 && !u.IsAdmin {
			return false, false, rw.Err(http.StatusConflict, "Connect your Jellyfin account first (Profile → Jellyfin) so requests are yours")
		}
	}
	result, err := s.CreateRequest(ctx, id, media, asUser, seasons)
	if err != nil {
		var se *clients.SeerrError
		if errors.As(err, &se) {
			status := http.StatusBadRequest
			if se.Status >= 500 {
				status = http.StatusBadGateway
			}
			return false, false, rw.Err(status, se.Msg)
		}
		return false, false, t.seerrDown(ctx, err)
	}
	already, _ = result["already"].(bool)
	dryRun, _ = result["dry_run"].(bool)
	if !already {
		status := "requested"
		if dryRun {
			status = "dry_run"
		}
		var rid *int32
		if f, ok := result["id"].(float64); ok {
			v := int32(f)
			rid = &v
		}
		if err := t.d.Q.RecordRequest(ctx, store.RecordRequestParams{UserID: u.ID, MediaType: media, TmdbID: int32(id),
			SeerrRequestID: rid, Status: status, DryRun: dryRun}); err != nil {
			return false, false, err
		}
	}
	// Who asked, by numeric id, never what (docs/limits.md, "Logs").
	t.d.Log.InfoContext(ctx, "request made", "user_id", u.ID, "already", already, "dry_run", dryRun)
	return already, dryRun, nil
}

func (t *Title) seerrDown(ctx context.Context, err error) error {
	t.d.Log.WarnContext(ctx, "Seerr request failed", "err", err)
	return rw.Err(http.StatusBadGateway, "Couldn't reach Seerr — try again")
}
