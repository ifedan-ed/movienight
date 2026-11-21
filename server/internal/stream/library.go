package stream

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"movienight/server/internal/apierr"
	"movienight/server/internal/media/protocol"
)

// A library file's addresses under its ticket. The media service writes
// its playlists with these relative names, so they need no rewriting.
var (
	libPlaylistRe = regexp.MustCompile(`^main\.m3u8$`)
	libInitRe     = regexp.MustCompile(`^hls/init\.mp4$`)
	libSegmentRe  = regexp.MustCompile(`^hls/(\d{1,6})\.m4s$`)
)

// libraryTarget maps a path under a library ticket to the media service's
// route for it. The file, the plan and the play come from the ticket
// alone; of the browser's query only an external subtitle's name is
// passed on, and the media service checks it is one of the file's own.
func (p *Proxy) libraryTarget(c Claims, rel string, query url.Values) (*url.URL, kind, bool) {
	file := url.Values{protocol.KeyFile: {c.File}}
	hls := url.Values{protocol.KeyFile: {c.File}, protocol.KeyPlan: {c.Plan}, protocol.KeyPlay: {c.Play}}
	switch {
	case fileRe.MatchString(rel):
		return p.media.Target(protocol.File, file), kindFile, true
	case libPlaylistRe.MatchString(rel):
		return p.media.Target(protocol.Playlist, hls), kindPlaylist, true
	case libInitRe.MatchString(rel):
		return p.media.Target(protocol.Init, hls), kindSegment, true
	case libSegmentRe.MatchString(rel):
		n := libSegmentRe.FindStringSubmatch(rel)[1]
		return p.media.Target(protocol.SegmentDir+n+".m4s", hls), kindSegment, true
	case subtitleRe.MatchString(rel):
		m := subtitleRe.FindStringSubmatch(rel)
		if name := query.Get(protocol.KeyName); name != "" && len(name) <= 500 {
			file.Set(protocol.KeyName, name)
		}
		return p.media.Target(protocol.SubtitleDir+m[1]+"."+m[2], file), kindSubtitle, true
	case attachmentRe.MatchString(rel):
		m := attachmentRe.FindStringSubmatch(rel)
		return p.media.Target(protocol.FontDir+m[1], file), kindAttachment, true
	}
	return nil, 0, false
}

// library serves a library ticket from the media service, as the Jellyfin
// side does from Jellyfin: Range passed through, the browser's own
// context, so a closed tab or a seek stops the upstream read at once.
func (p *Proxy) library(w http.ResponseWriter, r *http.Request, c Claims) {
	if p.media == nil {
		apierr.Write(w, apierr.NotFound())
		return
	}
	target, k, ok := p.libraryTarget(c, r.PathValue("path"), r.URL.Query())
	if !ok {
		apierr.Write(w, apierr.NotFound())
		return
	}
	method := http.MethodGet
	if r.Method == http.MethodHead && k == kindFile {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(r.Context(), method, target.String(), nil)
	if err != nil {
		apierr.Write(w, apierr.Internal())
		return
	}
	p.media.Authorize(req)
	for _, h := range []string{"Range", "If-Range"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			p.log.WarnContext(r.Context(), "stream: the media service didn't answer", "err", err)
		}
		apierr.Write(w, apierr.New(http.StatusBadGateway, "The media service didn't answer"))
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		apierr.Write(w, apierr.NotFound())
		return
	case resp.StatusCode == http.StatusServiceUnavailable:
		// Every transcode slot is taken (MEDIA_MAX_TRANSCODES).
		w.Header().Set("Retry-After", "10")
		apierr.Write(w, apierr.New(http.StatusServiceUnavailable, "Too many films are being converted right now. Try again in a moment."))
		return
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		p.log.WarnContext(r.Context(), "stream: the media service refused the call (MEDIA_SECRET, or a folder outside MEDIA_LIBRARIES)",
			"status", resp.StatusCode)
		apierr.Write(w, apierr.New(http.StatusBadGateway, "The media service refused the stream"))
		return
	case resp.StatusCode >= 400:
		p.log.WarnContext(r.Context(), "stream: the media service failed", "status", resp.StatusCode, "path", target.Path)
		apierr.Write(w, apierr.New(http.StatusBadGateway, "The media service couldn't send the stream (HTTP "+strconv.Itoa(resp.StatusCode)+")"))
		return
	}
	p.pass(w, r, resp, k, method)
}

// LibraryHLSURL is a library file's HLS playlist under a ticket.
func LibraryHLSURL(ticket string) string { return Prefix + ticket + "/main.m3u8" }

// ExternalSubtitleURL is a library file's external subtitle under a ticket:
// its track index, and its name next to the video for the media service.
func ExternalSubtitleURL(ticket string, index int, format, name string) string {
	return SubtitleURL(ticket, index, format) + "?" + url.Values{protocol.KeyName: {name}}.Encode()
}
