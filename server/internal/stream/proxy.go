package stream

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"movienight/server/internal/apierr"
	"movienight/server/internal/clients"
	"movienight/server/internal/media/mediaclient"
)

// Route is the proxy's address pattern; the player's URLs are built from
// Prefix.
const (
	Route  = "GET /api/v1/stream/{ticket}/{path...}"
	Prefix = "/api/v1/stream/"
)

// Limits of the hot path (docs/limits.md).
const (
	// maxPlaylist caps a playlist read into memory to rewrite it. A
	// three-hour film in 3-second segments is about 3,600 lines, ~600 KB.
	maxPlaylist = 8 << 20
	// writeIdle is how long one write to the browser may stall before the
	// connection is dropped. The API server's own WriteTimeout (5 minutes
	// for the whole response) would cut a film off, so the proxy moves the
	// deadline forward with every chunk instead.
	writeIdle = 2 * time.Minute
	copyBuf   = 64 << 10
)

// NewClient is the proxy's own connection pool to Jellyfin: kept apart
// from the API calls' pool so a few people watching cannot use up the
// connections Home needs, and tuned for long bodies (no overall timeout,
// connections kept alive between segments).
func NewClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxConnsPerHost:     128,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		// A transcode's first segment can take a while; nothing else should.
		ResponseHeaderTimeout: 90 * time.Second,
		// Bytes pass through untouched: Go's transparent gzip would drop
		// Content-Length and break Range answers.
		DisableCompression: true,
		ForceAttemptHTTP2:  true,
	},
		// Jellyfin's addresses are followed by the player, never by us.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Proxy serves Route.
type Proxy struct {
	signer *Signer
	client *http.Client
	base   *url.URL // Jellyfin; nil when there is none
	apiKey string
	media  *mediaclient.Client // the media service; nil when libraries are off
	log    *slog.Logger
	bufs   sync.Pool
}

// NewProxy makes the proxy. base is Jellyfin's internal address
// (JELLYFIN_BASE_URL, "" for none) and apiKey the install's key: the proxy
// fetches with the install's key, never a person's token, so it needs
// nothing from the database. The key reaches only what the ticket names:
// one item's media source, in Jellyfin's play session the person's own
// PlaybackInfo made. media is the media service (nil for none), reached
// the same way for a library file's ticket (library.go).
func NewProxy(signer *Signer, client *http.Client, base, apiKey string, media *mediaclient.Client, log *slog.Logger) (*Proxy, error) {
	p := &Proxy{signer: signer, client: client, apiKey: apiKey, media: media, log: log}
	if base != "" {
		u, err := url.Parse(strings.TrimRight(base, "/"))
		if err != nil || u.Host == "" {
			return nil, errors.New("JELLYFIN_BASE_URL is not an address")
		}
		p.base = u
	}
	p.bufs.New = func() any { b := make([]byte, copyBuf); return &b }
	return p, nil
}

// Register adds the route (GET also answers HEAD).
func (p *Proxy) Register(mux *http.ServeMux) { mux.Handle(Route, p) }

// kind is what a path under the ticket asks for.
type kind int

const (
	kindFile kind = iota
	kindPlaylist
	kindSegment
	kindSubtitle
	kindAttachment
)

var (
	fileRe       = regexp.MustCompile(`^stream(\.[A-Za-z0-9]{1,8})?$`)
	playlistRe   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}\.m3u8$`)
	segmentRe    = regexp.MustCompile(`^hls1/[A-Za-z0-9_-]{1,40}/-?\d{1,7}\.(ts|mp4|m4s|aac|vtt)$`)
	subtitleRe   = regexp.MustCompile(`^subtitles/(\d{1,4})\.(vtt|ass|ssa)$`)
	attachmentRe = regexp.MustCompile(`^attachments/(\d{1,4})$`)
	extRe        = regexp.MustCompile(`^[A-Za-z0-9]{1,8}$`)
)

// target maps a path under the ticket to Jellyfin's address for it. Only
// these shapes exist; anything else is a 404, so the install's key can
// reach nothing but the ticket's item.
func (p *Proxy) target(c Claims, rel string, query url.Values) (*url.URL, kind, bool) {
	u := *p.base
	pinnedQuery := func(q url.Values) string {
		q.Set("MediaSourceId", c.Source)
		q.Set("PlaySessionId", c.PlaySession)
		q.Set("DeviceId", c.Device)
		return q.Encode()
	}
	item := url.PathEscape(c.Item)
	switch {
	case fileRe.MatchString(rel):
		// The original file. The browser's query is ignored entirely.
		u.Path += "/Videos/" + item + "/" + rel
		u.RawQuery = pinnedQuery(url.Values{"static": {"true"}})
		return &u, kindFile, true
	case playlistRe.MatchString(rel), segmentRe.MatchString(rel):
		// Jellyfin's HLS: the playlists carry the transcode's settings in
		// their query (codecs, audio track, burnt-in subtitle), so the
		// browser's query is passed on, cleaned and pinned.
		u.Path += "/videos/" + item + "/" + rel
		u.RawQuery = pinnedQuery(cleanQuery(query, true))
		if playlistRe.MatchString(rel) {
			return &u, kindPlaylist, true
		}
		return &u, kindSegment, true
	case subtitleRe.MatchString(rel):
		m := subtitleRe.FindStringSubmatch(rel)
		u.Path += "/Videos/" + item + "/" + url.PathEscape(c.Source) + "/Subtitles/" + m[1] + "/0/Stream." + m[2]
		return &u, kindSubtitle, true
	case attachmentRe.MatchString(rel):
		m := attachmentRe.FindStringSubmatch(rel)
		u.Path += "/Videos/" + item + "/" + url.PathEscape(c.Source) + "/Attachments/" + m[1]
		return &u, kindAttachment, true
	}
	return nil, 0, false
}

// passHeaders are the upstream answer's headers the browser gets.
var passHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := p.signer.Verify(r.PathValue("ticket"))
	if err != nil {
		// One answer for a bad and an expired ticket: the player asks for
		// a fresh one either way.
		apierr.Write(w, apierr.New(http.StatusForbidden, "This stream link has expired"))
		return
	}
	if c.Library() {
		p.library(w, r, c)
		return
	}
	if p.base == nil {
		apierr.Write(w, apierr.NotFound())
		return
	}
	target, k, ok := p.target(c, r.PathValue("path"), r.URL.Query())
	if !ok {
		apierr.Write(w, apierr.NotFound())
		return
	}
	method := http.MethodGet
	if r.Method == http.MethodHead && k != kindPlaylist {
		method = http.MethodHead
	}
	// The browser's own context: a closed tab or a seek (which cancels
	// the segment in flight) stops the upstream read at once.
	req, err := http.NewRequestWithContext(r.Context(), method, target.String(), nil)
	if err != nil {
		apierr.Write(w, apierr.Internal())
		return
	}
	req.Header.Set("Authorization", clients.AuthHeader(p.apiKey, c.Device, "Movie Night"))
	if k != kindPlaylist {
		// Ranges are what make seeking in the original file work.
		for _, h := range []string{"Range", "If-Range"} {
			if v := r.Header.Get(h); v != "" {
				req.Header.Set(h, v)
			}
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if r.Context().Err() == nil {
			p.log.WarnContext(r.Context(), "stream: Jellyfin didn't answer", "err", err)
		}
		apierr.Write(w, apierr.New(http.StatusBadGateway, "Jellyfin didn't answer"))
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		apierr.Write(w, apierr.NotFound())
		return
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		p.log.WarnContext(r.Context(), "stream: Jellyfin refused the install's key", "status", resp.StatusCode)
		apierr.Write(w, apierr.New(http.StatusBadGateway, "Jellyfin refused the stream"))
		return
	case resp.StatusCode >= 500:
		p.log.WarnContext(r.Context(), "stream: Jellyfin failed", "status", resp.StatusCode)
		apierr.Write(w, apierr.New(http.StatusBadGateway, "Jellyfin couldn't send the stream"))
		return
	}
	if k == kindPlaylist {
		p.playlist(w, r.Context(), resp, target, c.Item)
		return
	}
	p.pass(w, r, resp, k, method)
}

// pass sends an upstream answer on as it is: the headers that matter, and
// the body streamed through a pooled buffer, never held whole.
func (p *Proxy) pass(w http.ResponseWriter, r *http.Request, resp *http.Response, k kind, method string) {
	h := w.Header()
	for _, name := range passHeaders {
		if v := resp.Header.Get(name); v != "" {
			h.Set(name, v)
		}
	}
	if k == kindFile || k == kindPlaylist {
		h.Set("Cache-Control", "private, no-store")
	} else {
		// A segment, subtitle or font never changes under its address
		// (the ticket is part of it).
		h.Set("Cache-Control", "private, max-age=21600")
	}
	w.WriteHeader(resp.StatusCode)
	if method == http.MethodHead {
		return
	}
	buf := p.bufs.Get().(*[]byte)
	defer p.bufs.Put(buf)
	dw := &deadlineWriter{w: w, rc: http.NewResponseController(w)}
	if _, err := io.CopyBuffer(dw, resp.Body, *buf); err != nil && r.Context().Err() == nil {
		p.log.InfoContext(r.Context(), "stream: copy ended early", "err", err)
	}
}

// playlist rewrites an HLS playlist so every address in it points back
// here, without any key.
func (p *Proxy) playlist(w http.ResponseWriter, ctx context.Context, resp *http.Response, target *url.URL, item string) {
	if resp.StatusCode != http.StatusOK {
		apierr.Write(w, apierr.New(http.StatusBadGateway, "Jellyfin answered HTTP "+strconv.Itoa(resp.StatusCode)))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPlaylist+1))
	if err != nil || len(body) > maxPlaylist {
		apierr.Write(w, apierr.New(http.StatusBadGateway, "Jellyfin's playlist couldn't be read"))
		return
	}
	out, err := rewritePlaylist(body, target, p.base.Path, item)
	if err != nil {
		p.log.WarnContext(ctx, "stream: a playlist pointed outside its item", "err", err)
		apierr.Write(w, apierr.New(http.StatusBadGateway, "Jellyfin's playlist couldn't be read"))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/vnd.apple.mpegurl")
	h.Set("Content-Length", strconv.Itoa(len(out)))
	// A live transcode's playlist changes; the player must always ask.
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

// deadlineWriter moves the connection's write deadline forward before each
// write, so a long film is never cut by the server's whole-response
// WriteTimeout while a stalled browser still lets go after writeIdle.
type deadlineWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (d *deadlineWriter) Write(b []byte) (int, error) {
	// ErrNotSupported (a writer without deadlines, as in tests) is fine:
	// that writer has no timeout to escape either.
	_ = d.rc.SetWriteDeadline(time.Now().Add(writeIdle))
	return d.w.Write(b)
}

// ---------------------------------------------------------------------------
// The player's addresses (built here, next to the routes that serve them)
// ---------------------------------------------------------------------------

// FileURL is the original file under a ticket. ext is the file's container
// ("mp4"), so the browser sees a sensible name.
func FileURL(ticket, container string) string {
	ext, _, _ := strings.Cut(container, ",")
	if !extRe.MatchString(ext) {
		return Prefix + ticket + "/stream"
	}
	return Prefix + ticket + "/stream." + strings.ToLower(ext)
}

// HLSURL is the proxy's address for Jellyfin's TranscodingUrl: the same
// playlist under the ticket, with the key and the pinned values taken out
// of its query (the proxy puts the pinned ones back from the ticket).
func HLSURL(ticket, transcodingURL, item string) (string, error) {
	u, err := url.Parse(transcodingURL)
	if err != nil {
		return "", err
	}
	rel, ok := insideItem(u.Path, item)
	if !ok || !playlistRe.MatchString(rel) {
		return "", errOutside
	}
	q := cleanQuery(u.Query(), true)
	return Prefix + ticket + "/" + rel + "?" + q.Encode(), nil
}

// SubtitleURL is one subtitle track under a ticket, as WebVTT ("vtt") or
// as the original ASS/SSA ("ass").
func SubtitleURL(ticket string, index int, format string) string {
	return Prefix + ticket + "/subtitles/" + strconv.Itoa(index) + "." + format
}

// AttachmentURL is one embedded file (a font) under a ticket.
func AttachmentURL(ticket string, index int) string {
	return Prefix + ticket + "/attachments/" + strconv.Itoa(index)
}
