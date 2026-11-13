package rw

import (
	"context"

	"movienight/server/internal/clients"
	"movienight/server/internal/store"
)

// Each person's Seerr user is the one Seerr made from their Jellyfin
// account, so requests made here (rw/title) and "my requests" (rw/profile)
// are theirs. Shared by both.

// SeerrUser is the person's Seerr user id, found or created from their
// Jellyfin account and remembered; 0 when they have no Jellyfin account.
func (d *Deps) SeerrUser(ctx context.Context, u store.User) (int, error) {
	if u.SeerrUserID != nil && *u.SeerrUserID > 0 {
		return int(*u.SeerrUserID), nil
	}
	if u.JellyfinUserID == nil || *u.JellyfinUserID == "" {
		return 0, nil
	}
	jfID := *u.JellyfinUserID
	s := d.Clients.Seerr
	users, err := s.Users(ctx)
	if err != nil {
		return 0, err
	}
	var match clients.JSON
	for _, x := range users {
		if x["jellyfinUserId"] == jfID {
			match = x
			break
		}
	}
	if match == nil {
		created, err := s.ImportJellyfinUser(ctx, jfID)
		if err != nil {
			return 0, err
		}
		for _, x := range created {
			if x["jellyfinUserId"] == jfID {
				match = x
				break
			}
		}
		if match == nil && len(created) > 0 {
			match = created[0]
		}
		if match != nil {
			d.Log.InfoContext(ctx, "created a Seerr user from the person's Jellyfin account", "user_id", u.ID)
		}
	}
	id, _ := match["id"].(float64)
	if id <= 0 {
		return 0, nil
	}
	sid := int32(id)
	if err := d.Q.SetSeerrUser(ctx, store.SetSeerrUserParams{ID: u.ID, SeerrUserID: &sid}); err != nil {
		return 0, err
	}
	return int(id), nil
}
