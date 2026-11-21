package subtitles

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Podnapisi is podnapisi.net's search, which answers JSON when asked with
// Accept: application/json. It needs no key. It searches by title (no ids),
// so the answer is checked against the year and the episode. A result's
// file is a zip.
type Podnapisi struct {
	Web
	base string
}

// PodnapisiBase is the site's subtitles address.
const PodnapisiBase = "https://www.podnapisi.net/subtitles"

// NewPodnapisi makes the source.
func NewPodnapisi(w Web, base string) *Podnapisi {
	return &Podnapisi{Web: w, base: strings.TrimRight(base, "/")}
}

func (p *Podnapisi) Name() string { return "podnapisi" }

type podnapisiAnswer struct {
	Data []struct {
		ID             string   `json:"id"`
		Flags          []string `json:"flags"`
		Releases       []string `json:"releases"`
		CustomReleases []string `json:"custom_releases"`
		Movie          struct {
			Year        int `json:"year"`
			EpisodeInfo struct {
				Season  int `json:"season"`
				Episode int `json:"episode"`
			} `json:"episode_info"`
		} `json:"movie"`
		Stats struct {
			Downloads int `json:"downloads"`
		} `json:"stats"`
	} `json:"data"`
}

func (p *Podnapisi) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Language.Podnapisi == "" {
		return nil, nil
	}
	v := url.Values{"keywords": {q.Title}, "language": {q.Language.Podnapisi}}
	if q.TV() {
		v["movie_type"] = []string{"tv-series", "mini-series"}
		v.Set("seasons", strconv.Itoa(q.Season))
		v.Set("episodes", strconv.Itoa(q.Episode))
	} else {
		v.Set("movie_type", "movie")
		if q.Year > 0 {
			v.Set("year", strconv.Itoa(q.Year))
		}
	}
	_, body, err := p.get(ctx, p.base+"/search/advanced?"+v.Encode(), maxAnswer, "Accept", "application/json")
	if err != nil {
		return nil, failure(p.Name(), err)
	}
	var a podnapisiAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return nil, problem(p.Name(), "sent an answer Movie Night couldn't read")
	}
	out := []Result{}
	for _, x := range a.Data {
		if x.ID == "" {
			continue
		}
		// A title search finds remakes and namesakes: the year (a year
		// either way, for releases late in December) and the episode keep
		// only this one.
		if !q.TV() && q.Year > 0 && x.Movie.Year != 0 && (x.Movie.Year < q.Year-1 || x.Movie.Year > q.Year+1) {
			continue
		}
		if q.TV() && x.Movie.EpisodeInfo.Episode != 0 && (x.Movie.EpisodeInfo.Episode != q.Episode || x.Movie.EpisodeInfo.Season != q.Season) {
			continue
		}
		rel := append(append([]string{}, x.Releases...), x.CustomReleases...)
		flags := strings.Join(x.Flags, " ")
		out = append(out, Result{Source: p.Name(), ID: x.ID, Language: q.Language.Code, Release: strings.Join(rel, "\n"),
			HearingImpaired: strings.Contains(flags, "hearing_impaired"), Machine: strings.Contains(flags, "machine"),
			Downloads: x.Stats.Downloads, Ref: x.ID})
	}
	return out, nil
}

func (p *Podnapisi) Download(ctx context.Context, r Result) ([]byte, error) {
	if r.Ref == "" || strings.ContainsAny(r.Ref, "/?#") {
		return nil, problem(p.Name(), "has no such subtitle")
	}
	_, body, err := p.get(ctx, p.base+"/"+url.PathEscape(r.Ref)+"/download?container=zip", maxFile)
	if err != nil {
		return nil, failure(p.Name(), err)
	}
	return body, nil
}
