package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SubDL is api.subdl.com (https://subdl.com/api-doc). One GET searches by
// IMDb id (or TMDB id, or the title), language and episode; a result's
// file is a zip on dl.subdl.com.
type SubDL struct {
	Web
	base, files, key string
	// busyWait is the shortest wait before trying a busy SubDL again
	// (subdlWait; shorter in tests).
	busyWait time.Duration
}

// SubDL's addresses: the API, and the host its download paths are on.
const (
	SubDLBase  = "https://api.subdl.com/api/v1"
	SubDLFiles = "https://dl.subdl.com"
)

// NewSubDL makes the source.
func NewSubDL(w Web, base, files, key string) *SubDL {
	return &SubDL{Web: w, base: strings.TrimRight(base, "/"), files: strings.TrimRight(files, "/"), key: key, busyWait: subdlWait}
}

func (s *SubDL) Name() string { return "subdl" }

type subdlAnswer struct {
	Status    *bool  `json:"status"`
	Error     string `json:"error"`
	Subtitles []struct {
		ReleaseName  string   `json:"release_name"`
		Name         string   `json:"name"`
		URL          string   `json:"url"`
		Season       int      `json:"season"`
		Episode      int      `json:"episode"`
		EpisodeFrom  *int     `json:"episode_from"`
		EpisodeEnd   int      `json:"episode_end"`
		FullSeason   bool     `json:"full_season"`
		HI           bool     `json:"hi"`
		AITranslated bool     `json:"ai_translated"`
		Releases     []string `json:"releases"`
	} `json:"subtitles"`
}

// titleUnsafe are the characters SubDL refuses in film_name (it answers
// 400 "potentially unsafe characters").
var titleUnsafe = strings.NewReplacer(`'`, " ", `"`, " ", `\`, " ", "/", " ", ";", " ", "<", " ", ">", " ",
	"{", " ", "}", " ", "[", " ", "]", " ", "`", " ", "´", " ", "’", " ")

func (s *SubDL) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Language.SubDL == "" {
		return nil, nil
	}
	p := url.Values{"api_key": {s.key}, "languages": {q.Language.SubDL}, "subs_per_page": {"30"}, "releases": {"1"}}
	switch {
	case q.IMDbID != "":
		p.Set("imdb_id", q.IMDbID)
	case q.TMDBID != 0:
		p.Set("tmdb_id", strconv.Itoa(q.TMDBID))
	default:
		p.Set("film_name", strings.TrimSpace(titleUnsafe.Replace(q.Title)))
		if q.Year > 0 {
			p.Set("year", strconv.Itoa(q.Year))
		}
	}
	if q.TV() {
		p.Set("type", "tv")
		p.Set("season_number", strconv.Itoa(q.Season))
		p.Set("episode_number", strconv.Itoa(q.Episode))
	} else {
		p.Set("type", "movie")
	}
	_, body, err := s.get(ctx, s.base+"/subtitles?"+p.Encode(), maxAnswer, "Accept", "application/json")
	var st *errStatus
	if errors.As(err, &st) {
		// SubDL puts its reason in a JSON body even on an error status.
		var a subdlAnswer
		if json.Unmarshal(st.Body, &a) == nil && notFound(a.Error) {
			return []Result{}, nil
		}
	}
	if err != nil {
		p, _ := s.problem(err)
		return nil, p
	}
	var a subdlAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, problem(s.Name(), "sent an answer Movie Night couldn't read")
	}
	if a.Status != nil && !*a.Status {
		if notFound(a.Error) {
			return []Result{}, nil
		}
		return nil, problem(s.Name(), "refused the search")
	}
	out := []Result{}
	for _, x := range a.Subtitles {
		if x.URL == "" {
			continue
		}
		if q.TV() && !x.FullSeason && !covers(x.Episode, x.EpisodeFrom, x.EpisodeEnd, q.Episode) {
			continue
		}
		release := x.ReleaseName
		if len(x.Releases) > 0 {
			release = strings.Join(x.Releases, "\n")
		}
		if release == "" {
			release = x.Name
		}
		out = append(out, Result{Source: s.Name(), ID: x.URL, Language: q.Language.Code, Release: release,
			HearingImpaired: x.HI, Machine: x.AITranslated, Ref: x.URL})
	}
	return out, nil
}

// notFound is SubDL's "no such title" answer, which is no results rather
// than a failure.
func notFound(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "can't find") || strings.Contains(m, "not found")
}

// covers reports whether a result is for episode ep: its own episode, or
// a range of a pack (episode_from to episode_end). Unknown (0) counts.
func covers(episode int, from *int, end, ep int) bool {
	if from != nil && *from > 0 && end >= *from {
		return ep >= *from && ep <= end
	}
	return episode == 0 || episode == ep
}

// Download fetches the zip from dl.subdl.com, as Bazarr does: the path
// from the search result on that host, no key. A "rate_limit" or
// "service_busy" answer is waited out once (its Retry-After, at most
// subdlWaitMax); a daily limit is said as one.
func (s *SubDL) Download(ctx context.Context, r Result) ([]byte, error) {
	if !strings.HasPrefix(r.Ref, "/") {
		return nil, problem(s.Name(), "has no such subtitle")
	}
	for attempt := 0; ; attempt++ {
		_, body, err := s.get(ctx, s.files+r.Ref, maxFile)
		if err == nil {
			return body, nil
		}
		p, wait := s.problem(err)
		if wait == 0 || attempt > 0 {
			return nil, p
		}
		select {
		case <-ctx.Done():
			return nil, p
		case <-time.After(wait):
		}
	}
}

// subdlWait is how long a busy SubDL is waited for when it names no time,
// subdlWaitMax the longest wait it may ask for within one download.
const (
	subdlWait    = 5 * time.Second
	subdlWaitMax = 10 * time.Second
)

// problem reads SubDL's error answers. Its 429 carries a JSON "error":
// "daily_limit" or "api_download_limit_exceeded" (the day's downloads are
// used up), "rate_limit" or "service_busy" (try again shortly: wait is
// how long). Everything else is the shared mapping.
func (s *SubDL) problem(err error) (p *Problem, wait time.Duration) {
	var st *errStatus
	if !errors.As(err, &st) {
		return asProblem(s.Name(), failure(s.Name(), err)), 0
	}
	p = asProblem(s.Name(), failure(s.Name(), err))
	var b struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(st.Body, &b)
	switch {
	case st.Code == http.StatusTooManyRequests && (b.Error == "daily_limit" || b.Error == "api_download_limit_exceeded"):
		p.Msg, p.Quota = "has no downloads left today", true
	case st.Code == http.StatusTooManyRequests && (b.Error == "rate_limit" || b.Error == "service_busy"):
		wait = min(max(st.RetryAfter, s.busyWait), subdlWaitMax)
	case st.Code == http.StatusPaymentRequired:
		p.Msg = "wants a paid plan for this"
	}
	return p, wait
}
