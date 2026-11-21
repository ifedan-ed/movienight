// Package protocol is the contract between the API and the media service:
// the routes, the query keys and how a call proves it comes from the API.
// Both sides import it, so a route is spelled once.
//
// Every route but Health takes "Authorization: Bearer <MEDIA_SECRET>". The
// browser never calls the media service: the API's stream proxy does, on
// a ticket it checked, and adds the file, plan and play from that ticket.
package protocol

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Routes of the media service.
const (
	Health      = "/healthz"
	Scan        = "/v1/scan" // POST scan.Request -> NDJSON scan.Event lines
	File        = "/v1/file" // GET/HEAD the file itself, with Range
	Playlist    = "/v1/hls/main.m3u8"
	Init        = "/v1/hls/init.mp4"
	SegmentDir  = "/v1/hls/"         // + "<n>.m4s"
	SubtitleDir = "/v1/subtitles/"   // + "<stream index>.<vtt|ass>"; ?name= for an external file
	FontDir     = "/v1/attachments/" // + "<stream index>"
	Stop        = "/v1/stop"         // POST ?play=: end the play's ffmpeg jobs
	Status      = "/v1/status"       // GET engine.Status
)

// Query keys.
const (
	KeyFile = "file" // the video's path on the media machine
	KeyPlan = "plan" // plan.Plan.String()
	KeyPlay = "play" // the Movie Night play (play_sessions.id)
	KeyName = "name" // an external subtitle's name next to the video
)

// Authorize sets the shared secret on a request to the media service.
func Authorize(r *http.Request, secret string) { r.Header.Set("Authorization", "Bearer "+secret) }

// Authorized checks a request carries the shared secret, in constant time.
func Authorized(r *http.Request, secret string) bool {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && secret != "" && subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
}
