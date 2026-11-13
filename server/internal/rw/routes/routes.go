// Package routes registers every read and write route (internal/rw/...)
// and then applies the request limits that are settings. server.go calls
// RegisterAll once; Spec uses it too, so the published description is the
// served one.
package routes

import (
	"movienight/server/internal/rw"
	"movienight/server/internal/rw/devices"
	"movienight/server/internal/rw/digests"
	"movienight/server/internal/rw/home"
	"movienight/server/internal/rw/lists"
	"movienight/server/internal/rw/profile"
	"movienight/server/internal/rw/search"
	"movienight/server/internal/rw/title"
)

// RegisterAll adds every area's routes.
func RegisterAll(d *rw.Deps) error {
	home.Register(d)
	title.Register(d)
	search.Register(d)
	lists.Register(d)
	profile.Register(d)
	devices.Register(d)
	digests.Register(d)
	return d.ApplyLimits()
}
