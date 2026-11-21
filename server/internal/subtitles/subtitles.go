// Package subtitles is the player's "Search online": it asks the subtitle
// sites an install has keys for (and the two that need none), ranks what
// they offer for the file that is playing, and keeps the one a person picks
// as a track of that file for the whole household.
//
// The pieces:
//
//   - One Source per site (opensubtitles.go, subdl.go, subsource.go,
//     jimaku.go, podnapisi.go, gestdown.go). Each knows its site's request
//     and answer shapes and nothing else.
//   - Service (service.go) asks every source at once with a short timeout,
//     caches the merged answer in Valkey for an hour, and stores a picked
//     subtitle in Postgres (online_subtitles).
//   - rank.go orders the results: the file's own hash first, then the
//     release name closest to the file's name, then downloads and rating.
//   - hash.go is OpenSubtitles' moviehash, read with two small Range
//     requests through the stream proxy (never the whole file).
//   - convert.go unpacks a zip, finds the text's charset, and turns SRT and
//     MicroDVD into WebVTT (ASS stays ASS, for JASSUB).
//
// Subtitles on these sites are uploaded by their users, and each site has
// its own terms. Nothing here runs on the stream's hot path: the player
// fetches a stored track once, as it fetches Jellyfin's.
package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Query is what a source is asked for: one film or one episode in one
// language, with what is known about the file.
type Query struct {
	Media    string // "movie" or "tv"
	TMDBID   int    // the film's or the series' TMDB id
	IMDbID   string // "tt0133093", the film's or the series'; "" when TMDB has none
	Title    string // the film's or the series' title
	Year     int    // 0 when unknown
	Season   int    // 0 for a film
	Episode  int    // 0 for a film
	Language Language
	// Hash is the file's OpenSubtitles moviehash (16 hex digits), "" when
	// it could not be read. FileName is the file's name without folders.
	Hash     string
	FileName string
}

// TV reports whether the query is for an episode.
func (q Query) TV() bool { return q.Media == "tv" }

// Result is one subtitle a source offers.
type Result struct {
	Source          string  `json:"s"`
	ID              string  `json:"i"` // the source's own id, unique within the source
	Language        string  `json:"l"` // Movie Night's language code (Language.Code)
	Release         string  `json:"r"` // the release name(s) it was made for
	HearingImpaired bool    `json:"hi,omitempty"`
	Machine         bool    `json:"m,omitempty"` // machine or AI translated (or transcribed)
	HashMatch       bool    `json:"h,omitempty"` // the source matched the file's moviehash
	Downloads       int     `json:"d,omitempty"`
	Rating          float64 `json:"ra,omitempty"`
	// Ref is what Download needs (a file id, an address). It never leaves
	// the server: the player names a result by Source and ID.
	Ref string `json:"ref,omitempty"`
	// score is rank.go's similarity of Release to the file's name.
	score float64
}

// Source is one subtitle site.
type Source interface {
	// Name is the source's name in SUBTITLE_SOURCES (config.Source...).
	Name() string
	// Search returns the subtitles for q. No result is an empty list, not
	// an error.
	Search(ctx context.Context, q Query) ([]Result, error)
	// Download fetches one result from a Search, as the source sends it:
	// maybe a zip, maybe text in any charset (Prepare sorts that out).
	Download(ctx context.Context, r Result) ([]byte, error)
}

// QuotaSource is a source with a download allowance it reports
// (OpenSubtitles): Quota says whether it is used up and until when.
type QuotaSource interface {
	Quota(ctx context.Context) (usedUp bool, until time.Time)
}

// Problem is a source failing or refusing, in words a person can act on.
// Quota marks a used-up download allowance; Until is when it comes back
// (zero when the source did not say).
type Problem struct {
	Source string
	Msg    string
	Quota  bool
	Until  time.Time
	// Status and Detail are what the site answered (its HTTP status and
	// the start of its body), for the log only: a person reads Msg.
	Status int    `json:",omitempty"`
	Detail string `json:",omitempty"`
}

func (p *Problem) Error() string { return Labels[p.Source] + " " + p.Msg }

// Labels are the sources' names as people read them.
var Labels = map[string]string{
	"opensubtitles": "OpenSubtitles", "subdl": "SubDL", "subsource": "SubSource",
	"jimaku": "Jimaku", "podnapisi": "Podnapisi", "gestdown": "Addic7ed (Gestdown)",
}

// problem makes a Problem.
func problem(source, msg string) *Problem { return &Problem{Source: source, Msg: msg} }

// ---------------------------------------------------------------------------
// HTTP, shared by the sources
// ---------------------------------------------------------------------------

// maxAnswer caps a search answer read into memory, maxFile a downloaded
// subtitle or archive (a season pack of 20 episodes is a few MB).
const (
	maxAnswer = 4 << 20
	maxFile   = 16 << 20
)

// Web is one source's HTTP side: the shared client and the User-Agent
// every site asks for ("Movie Night v2.0").
type Web struct {
	hc *http.Client
	ua string
}

// errStatus is an answer with an HTTP error status the caller turns into
// its source's own Problem.
type errStatus struct {
	Code       int
	Body       []byte
	RetryAfter time.Duration // the Retry-After header in seconds; 0 when absent
}

func (e *errStatus) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }

// do sends req and reads up to max bytes of a 2xx answer. A non-2xx answer
// is an *errStatus with the start of its body (sources put their reasons
// there); a body over max is an error rather than a silently cut file.
func (w Web) do(req *http.Request, max int64) (*http.Response, []byte, error) {
	req.Header.Set("User-Agent", w.ua)
	resp, err := w.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if len(body) > 2000 {
			body = body[:2000]
		}
		st := &errStatus{Code: resp.StatusCode, Body: body}
		if n, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && n > 0 {
			st.RetryAfter = time.Duration(n) * time.Second
		}
		return resp, nil, st
	}
	if int64(len(body)) > max {
		return resp, nil, errors.New("answer too large")
	}
	return resp, body, nil
}

// get is do for a plain GET with optional headers (name, value, ...).
func (w Web) get(ctx context.Context, u string, max int64, headers ...string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return w.do(req, max)
}

// failure turns a request error into the source's Problem: a refusal, a
// limit, or the site not answering.
func failure(source string, err error) error {
	var p *Problem
	if errors.As(err, &p) {
		return p
	}
	var st *errStatus
	if errors.As(err, &st) {
		p := statusProblem(source, st)
		p.Status, p.Detail = st.Code, detail(st.Body)
		return p
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return problem(source, "didn't answer in time")
	}
	return problem(source, "couldn't be reached")
}

// statusProblem words an HTTP error. Only a 429 is "busy": a 403 is the
// key only when the site's own JSON says so (key, auth, forbidden), since a
// bare 403 is usually a firewall in front of the site (Cloudflare) refusing
// the request, which a new key would not fix.
func statusProblem(source string, st *errStatus) *Problem {
	switch c := st.Code; {
	case c == http.StatusUnauthorized:
		return problem(source, "refused the install's key")
	case c == http.StatusForbidden:
		var b struct {
			Error   any    `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(st.Body, &b) == nil {
			why := strings.ToLower(fmt.Sprint(b.Error) + " " + b.Message)
			if strings.Contains(why, "key") || strings.Contains(why, "auth") || strings.Contains(why, "forbidden") {
				return problem(source, "refused the install's key")
			}
		}
		return problem(source, "refused the request (HTTP 403)")
	case c == http.StatusTooManyRequests:
		return problem(source, "is busy, try again in a minute")
	case c == http.StatusNotFound:
		return problem(source, "has no such subtitle")
	case c >= 500:
		return problem(source, "is having trouble right now")
	}
	return problem(source, fmt.Sprintf("answered HTTP %d", st.Code))
}

// detail is what an error body says, for the log: only its JSON "error"
// and "message" fields (a site's code and reason), never the rest, which
// may echo a title back. A body that isn't JSON (a firewall's page) is
// only counted.
func detail(body []byte) string {
	var b struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &b) != nil {
		return fmt.Sprintf("not JSON, %d bytes", len(body))
	}
	s := strings.TrimSpace(fmt.Sprint(b.Error) + " " + b.Message)
	if b.Error == nil {
		s = strings.TrimSpace(b.Message)
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// imdbNumber is an IMDb id without "tt" and leading zeros ("tt0133093" ->
// "133093"), as OpenSubtitles wants it; "" for none.
func imdbNumber(id string) string {
	n := strings.TrimLeft(strings.TrimPrefix(strings.ToLower(id), "tt"), "0")
	for _, c := range n {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return n
}
