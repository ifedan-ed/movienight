package subtitles

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// SubSource is SubSource's API (https://subsource.net/api-docs). The key
// goes as the api_key query parameter on every call, downloads included,
// as Bazarr's provider sends it. (The docs also name an X-API-Key header;
// Bazarr's shape is the one known to work live.) It is two steps: find the title's id (by IMDb id, or
// by its name), then list that title's subtitles in one language. A TV
// series has one id per season.
type SubSource struct {
	Web
	base, key string
}

// SubSourceBase is the API's address.
const SubSourceBase = "https://api.subsource.net/api/v1"

// NewSubSource makes the source.
func NewSubSource(w Web, base, key string) *SubSource {
	return &SubSource{Web: w, base: strings.TrimRight(base, "/"), key: key}
}

func (s *SubSource) Name() string { return "subsource" }

func (s *SubSource) call(ctx context.Context, path string, p url.Values, out any) error {
	p.Set("api_key", s.key)
	_, body, err := s.get(ctx, s.base+path+"?"+p.Encode(), maxAnswer, "Accept", "application/json")
	if err != nil {
		return failure(s.Name(), err)
	}
	if json.Unmarshal(body, out) != nil {
		return problem(s.Name(), "sent an answer Movie Night couldn't read")
	}
	return nil
}

type subsourceTitles struct {
	Data []struct {
		MovieID     int    `json:"movieId"`
		Title       string `json:"title"`
		ReleaseYear int    `json:"releaseYear"`
	} `json:"data"`
}

type subsourceSubs struct {
	Success *bool `json:"success"`
	Data    []struct {
		SubtitleID      int      `json:"subtitleId"`
		ReleaseInfo     []string `json:"releaseInfo"`
		HearingImpaired bool     `json:"hearingImpaired"`
		ForeignParts    bool     `json:"foreignParts"`
		Commentary      string   `json:"commentary"`
		Downloads       int      `json:"downloads"`
		Rating          float64  `json:"rating"`
	} `json:"data"`
}

func (s *SubSource) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Language.SubSource == "" {
		return nil, nil
	}
	id, err := s.titleID(ctx, q)
	if err != nil || id == 0 {
		return []Result{}, err
	}
	p := url.Values{"movieId": {strconv.Itoa(id)}, "language": {q.Language.SubSource}, "limit": {"100"}}
	if q.TV() {
		p.Set("seasonNumber", strconv.Itoa(q.Season))
		p.Set("episodeNumber", strconv.Itoa(q.Episode))
	}
	var a subsourceSubs
	if err := s.call(ctx, "/subtitles", p, &a); err != nil {
		return nil, err
	}
	if a.Success != nil && !*a.Success {
		return []Result{}, nil
	}
	out := []Result{}
	for _, x := range a.Data {
		if x.ForeignParts || x.SubtitleID == 0 {
			continue
		}
		id := strconv.Itoa(x.SubtitleID)
		c := strings.ToLower(x.Commentary)
		out = append(out, Result{Source: s.Name(), ID: id, Language: q.Language.Code, Release: strings.Join(x.ReleaseInfo, "\n"),
			// Uploaders often say "HI" or "SDH" only in their note.
			HearingImpaired: x.HearingImpaired || strings.Contains(c, "sdh") || strings.Contains(c, "hearing impaired"),
			Machine:         strings.Contains(c, "machine translat") || strings.Contains(c, "ai translat"),
			Downloads:       x.Downloads, Rating: x.Rating, Ref: id})
	}
	return out, nil
}

// titleID is SubSource's id for the film or the series' season: by IMDb
// id when TMDB has one, else by name and year.
func (s *SubSource) titleID(ctx context.Context, q Query) (int, error) {
	p := url.Values{}
	if q.IMDbID != "" {
		p.Set("searchType", "imdb")
		p.Set("imdb", q.IMDbID)
	} else {
		p.Set("searchType", "text")
		p.Set("q", strings.ToLower(q.Title))
	}
	if q.TV() {
		p.Set("season", strconv.Itoa(q.Season))
	}
	var a subsourceTitles
	if err := s.call(ctx, "/movies/search", p, &a); err != nil {
		return 0, err
	}
	for _, t := range a.Data {
		if q.IMDbID != "" {
			return t.MovieID, nil // an IMDb id names one title
		}
		if strings.EqualFold(t.Title, q.Title) && (q.Year == 0 || t.ReleaseYear == 0 || t.ReleaseYear == q.Year || q.TV()) {
			return t.MovieID, nil
		}
	}
	return 0, nil
}

func (s *SubSource) Download(ctx context.Context, r Result) ([]byte, error) {
	if _, err := strconv.Atoi(r.Ref); err != nil {
		return nil, problem(s.Name(), "has no such subtitle")
	}
	_, body, err := s.get(ctx, s.base+"/subtitles/"+r.Ref+"/download?"+url.Values{"api_key": {s.key}}.Encode(), maxFile)
	if err != nil {
		return nil, failure(s.Name(), err)
	}
	return body, nil
}
