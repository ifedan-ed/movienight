// Package profile is the Profile tab and the person's own settings: /me,
// their time zone, history, requests, the rate prompt, the data export, the
// scrobble token, the Jellyfin account link and players.
//
// Not here: digest emails (/me/emails, /me/digests/*: rw/digests, with the
// digest code); /sync/* (rw/devices, with the syncs).
package profile

import (
	"movienight/server/internal/episodes"
	"movienight/server/internal/rw"
	"movienight/server/internal/users"
)

// Register adds the routes.
func Register(d *rw.Deps) {
	a := &account{d: d, players: d.Players, status: d.Status}
	a.register()
	m := &me{account: a, history: d.History, users: users.New(d.Q, d.Cfg),
		episodes: episodes.New(d.Q, d.Cfg, d.Cache, d.Clients, d.History, d.Titles, d.JFSync, d.Libraries, d.Log)}
	m.register()
	registerExport(d)
}
