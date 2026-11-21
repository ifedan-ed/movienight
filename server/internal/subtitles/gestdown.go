package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Gestdown is api.gestdown.info, which relays Addic7ed's TV subtitles
// without Addic7ed's login. It needs no key and has TV only. A show is
// found by name, and the one with this series' TMDB id is taken; its
// episode's subtitles come in one call and download as plain text.
type Gestdown struct {
	Web
	base string
}

// GestdownBase is the API's address.
const GestdownBase = "https://api.gestdown.info"

// NewGestdown makes the source.
func NewGestdown(w Web, base string) *Gestdown {
	return &Gestdown{Web: w, base: strings.TrimRight(base, "/")}
}

func (g *Gestdown) Name() string { return "gestdown" }

type gestdownShows struct {
	Shows []struct {
		ID     string `json:"id"`
		TMDBID int    `json:"tmdbId"`
	} `json:"shows"`
}

type gestdownSubs struct {
	MatchingSubtitles []struct {
		SubtitleID      string `json:"subtitleId"`
		Version         string `json:"version"`
		Completed       bool   `json:"completed"`
		HearingImpaired bool   `json:"hearingImpaired"`
		DownloadURI     string `json:"downloadUri"`
		DownloadCount   int    `json:"downloadCount"`
	} `json:"matchingSubtitles"`
}

func (g *Gestdown) call(ctx context.Context, path string, out any) (bool, error) {
	_, body, err := g.get(ctx, g.base+path, maxAnswer, "Accept", "application/json")
	var st *errStatus
	if errors.As(err, &st) {
		switch st.Code {
		case http.StatusNotFound:
			return false, nil
		case http.StatusLocked:
			// 423: Gestdown is fetching the show from Addic7ed right now.
			return false, problem(g.Name(), "is fetching this show, try again in a minute")
		}
	}
	if err != nil {
		return false, failure(g.Name(), err)
	}
	if json.Unmarshal(body, out) != nil {
		return false, problem(g.Name(), "sent an answer Movie Night couldn't read")
	}
	return true, nil
}

func (g *Gestdown) Search(ctx context.Context, q Query) ([]Result, error) {
	if !q.TV() || q.Language.Gestdown == "" || q.Title == "" {
		return nil, nil
	}
	var shows gestdownShows
	if ok, err := g.call(ctx, "/shows/search/"+url.PathEscape(q.Title), &shows); err != nil || !ok {
		return []Result{}, err
	}
	show := ""
	for _, s := range shows.Shows {
		if s.TMDBID == q.TMDBID {
			show = s.ID
			break
		}
	}
	if show == "" {
		return []Result{}, nil
	}
	var subs gestdownSubs
	path := "/subtitles/get/" + url.PathEscape(show) + "/" + strconv.Itoa(q.Season) + "/" + strconv.Itoa(q.Episode) + "/" +
		url.PathEscape(q.Language.Gestdown)
	if ok, err := g.call(ctx, path, &subs); err != nil || !ok {
		return []Result{}, err
	}
	out := []Result{}
	for _, s := range subs.MatchingSubtitles {
		// An unfinished translation is missing lines.
		if !s.Completed || s.SubtitleID == "" || !strings.HasPrefix(s.DownloadURI, "/") {
			continue
		}
		out = append(out, Result{Source: g.Name(), ID: s.SubtitleID, Language: q.Language.Code, Release: s.Version,
			HearingImpaired: s.HearingImpaired, Downloads: s.DownloadCount, Ref: s.DownloadURI})
	}
	return out, nil
}

func (g *Gestdown) Download(ctx context.Context, r Result) ([]byte, error) {
	if !strings.HasPrefix(r.Ref, "/subtitles/download/") {
		return nil, problem(g.Name(), "has no such subtitle")
	}
	_, body, err := g.get(ctx, g.base+r.Ref, maxFile)
	if err != nil {
		return nil, failure(g.Name(), err)
	}
	return body, nil
}
