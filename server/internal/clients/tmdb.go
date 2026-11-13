package clients

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// TMDBAPI is TMDB's v3 address.
const TMDBAPI = "https://api.themoviedb.org/3"

// TMDBError is TMDB failing to answer properly.
type TMDBError struct {
	Status int
	Msg    string
}

func (e *TMDBError) Error() string { return "TMDB: " + e.Msg }

// TMDB reads titles, discovery, search and people from TMDB directly.
// Answers are TMDB's own shapes.
type TMDB struct {
	http      *http.Client
	base      string
	key       string
	language  string
	region    string
	hideAdult bool
	sleep     func(context.Context, time.Duration) error
}

// NewTMDB makes the client. base is TMDBAPI outside tests.
func NewTMDB(c *http.Client, base, key, language, region string, hideAdult bool) *TMDB {
	return &TMDB{http: c, base: base, key: key, language: language, region: region, hideAdult: hideAdult, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// maxRetryAfter bounds how long one call waits on TMDB's Retry-After.
const maxRetryAfter = 10 * time.Second

// get is one TMDB call: nil (no error) for a 404, the decoded answer for a
// 200. A 429 is retried twice after TMDB's Retry-After; a third 429 is an
// error (the Python returned None there, which looked like "not found").
func (t *TMDB) get(ctx context.Context, path string, params map[string]string) (JSON, error) {
	q := url.Values{"api_key": {t.key}, "language": {t.language}}
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, t.base+path+"?"+q.Encode(), nil)
		resp, err := do(ctx, t.http, req, DefaultTimeout, maxBody)
		if err != nil {
			return nil, &TMDBError{Status: http.StatusBadGateway, Msg: "didn't answer (" + err.Error() + ")"}
		}
		switch {
		case resp.Status == http.StatusNotFound:
			return nil, nil
		case resp.Status == http.StatusTooManyRequests && attempt < 2:
			// Retry-After in seconds, as TMDB sends it; one second when absent.
			wait := time.Second
			if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && s >= 0 {
				wait = time.Duration(s * float64(time.Second))
			}
			if wait > maxRetryAfter {
				wait = maxRetryAfter
			}
			if err := t.sleep(ctx, wait); err != nil {
				return nil, err
			}
			continue
		case resp.Status != http.StatusOK:
			return nil, &TMDBError{Status: resp.Status, Msg: fmt.Sprintf("answered HTTP %d", resp.Status)}
		}
		var out JSON
		if err := decode(resp.Body, &out); err != nil {
			return nil, &TMDBError{Status: http.StatusBadGateway, Msg: "answer wasn't JSON"}
		}
		return out, nil
	}
}

func results(data JSON, media string) []JSON {
	out := list(data["results"])
	if media != "" {
		for _, r := range out {
			if _, ok := r["media_type"]; !ok {
				r["media_type"] = media
			}
		}
	}
	return out
}

// Keep drops titles TMDB marks adult, only with HIDE_ADULT_TITLES: search,
// Home, Discover and smart lists ask TMDB for everything (include_adult),
// because TMDB's own filter hides more than the titles it marks adult.
func (t *TMDB) Keep(rows []JSON) []JSON {
	if !t.hideAdult {
		return rows
	}
	out := rows[:0:0]
	for _, r := range rows {
		if adult, _ := r["adult"].(bool); !adult {
			out = append(out, r)
		}
	}
	return out
}

func page(n int) string { return strconv.Itoa(n) }

// SearchMultiPage is one page of TMDB's title and people search and how
// many pages it has.
func (t *TMDB) SearchMultiPage(ctx context.Context, query string, p int) ([]JSON, int, error) {
	data, err := t.get(ctx, "/search/multi", map[string]string{"query": query, "page": page(p), "include_adult": "true"})
	if err != nil {
		return nil, 0, err
	}
	pages, _ := num(data, "total_pages")
	return t.Keep(results(data, "")), int(pages), nil
}

// Trending is TMDB's trending list ("all", "movie" or "tv"; "day" or "week").
func (t *TMDB) Trending(ctx context.Context, media, window string, p int) ([]JSON, error) {
	data, err := t.get(ctx, "/trending/"+media+"/"+window, map[string]string{"page": page(p)})
	if media == "all" {
		media = ""
	}
	return results(data, media), err
}

// Popular and TopRated are TMDB's lists in WATCH_REGION.
func (t *TMDB) Popular(ctx context.Context, media string, p int) ([]JSON, error) {
	data, err := t.get(ctx, "/"+media+"/popular", map[string]string{"page": page(p), "region": t.region})
	return results(data, media), err
}

func (t *TMDB) TopRated(ctx context.Context, media string, p int) ([]JSON, error) {
	data, err := t.get(ctx, "/"+media+"/top_rated", map[string]string{"page": page(p), "region": t.region})
	return results(data, media), err
}

// Upcoming is releases (films: theatrical, in WATCH_REGION) and first air
// dates in the next 120 days (docs/limits.md).
func (t *TMDB) Upcoming(ctx context.Context, media string, p int) ([]JSON, error) {
	today := time.Now()
	from, to := today.Format("2006-01-02"), today.AddDate(0, 0, 120).Format("2006-01-02")
	params := map[string]string{"page": page(p), "sort_by": "popularity.desc", "include_adult": "true"}
	path := "/discover/tv"
	if media == "movie" {
		path = "/discover/movie"
		params["region"], params["with_release_type"] = t.region, "2|3"
		params["release_date.gte"], params["release_date.lte"] = from, to
	} else {
		params["first_air_date.gte"], params["first_air_date.lte"] = from, to
	}
	data, err := t.get(ctx, path, params)
	return t.Keep(results(data, media)), err
}

// Discover takes TMDB's own filter names (with_genres, watch_region, ...).
func (t *TMDB) Discover(ctx context.Context, media string, p int, filters map[string]string) ([]JSON, error) {
	params := map[string]string{"page": page(p), "include_adult": "true"}
	for k, v := range filters {
		params[k] = v
	}
	data, err := t.get(ctx, discoverPath(media), params)
	return t.Keep(results(data, media)), err
}

// DiscoverPage is one raw discover page (results plus total_pages), for
// walking a whole slice of the catalog; adult marks are left for the caller.
func (t *TMDB) DiscoverPage(ctx context.Context, media string, p int, includeAdult bool, filters map[string]string) (JSON, error) {
	params := map[string]string{"page": page(p), "include_adult": strconv.FormatBool(includeAdult)}
	for k, v := range filters {
		params[k] = v
	}
	return t.get(ctx, discoverPath(media), params)
}

func discoverPath(media string) string {
	if media == "tv" {
		return "/discover/tv"
	}
	return "/discover/movie"
}

// Recommendations and Similar are TMDB's related titles.
func (t *TMDB) Recommendations(ctx context.Context, media string, id, p int) ([]JSON, error) {
	data, err := t.get(ctx, fmt.Sprintf("/%s/%d/recommendations", media, id), map[string]string{"page": page(p)})
	return results(data, media), err
}

func (t *TMDB) Similar(ctx context.Context, media string, id, p int) ([]JSON, error) {
	data, err := t.get(ctx, fmt.Sprintf("/%s/%d/similar", media, id), map[string]string{"page": page(p)})
	return results(data, media), err
}

// Basic is the small details call for the title cache; nil when TMDB does
// not know the title.
func (t *TMDB) Basic(ctx context.Context, media string, id int) (JSON, error) {
	params := map[string]string{}
	if media == "tv" {
		params["append_to_response"] = "external_ids"
	}
	data, err := t.get(ctx, fmt.Sprintf("/%s/%d", media, id), params)
	if data != nil {
		data["media_type"] = media
	}
	return data, err
}

// Details is the full title page's call (credits, videos, ids, providers,
// keywords and ratings).
func (t *TMDB) Details(ctx context.Context, media string, id int) (JSON, error) {
	extra := "credits,videos,external_ids,watch/providers,keywords,"
	if media == "movie" {
		extra += "release_dates"
	} else {
		extra += "content_ratings"
	}
	data, err := t.get(ctx, fmt.Sprintf("/%s/%d", media, id), map[string]string{"append_to_response": extra})
	if data != nil {
		data["media_type"] = media
	}
	return data, err
}

// SearchMovie and SearchTV search one kind, optionally by year.
func (t *TMDB) SearchMovie(ctx context.Context, query string, year int) ([]JSON, error) {
	params := map[string]string{"query": query, "include_adult": "true"}
	if year > 0 {
		params["year"] = strconv.Itoa(year)
	}
	data, err := t.get(ctx, "/search/movie", params)
	return t.Keep(results(data, "movie")), err
}

func (t *TMDB) SearchTV(ctx context.Context, query string, year int) ([]JSON, error) {
	params := map[string]string{"query": query, "include_adult": "true"}
	if year > 0 {
		params["first_air_date_year"] = strconv.Itoa(year)
	}
	data, err := t.get(ctx, "/search/tv", params)
	return t.Keep(results(data, "tv")), err
}

// Season, Collection and Person are single things (nil when unknown).
func (t *TMDB) Season(ctx context.Context, tvID, number int) (JSON, error) {
	return t.get(ctx, fmt.Sprintf("/tv/%d/season/%d", tvID, number), nil)
}

func (t *TMDB) Collection(ctx context.Context, id int) (JSON, error) {
	return t.get(ctx, fmt.Sprintf("/collection/%d", id), nil)
}

func (t *TMDB) Person(ctx context.Context, id int) (JSON, error) {
	return t.get(ctx, fmt.Sprintf("/person/%d", id), map[string]string{"append_to_response": "combined_credits"})
}

// FindExternal finds a title by another site's id (source imdb_id, tvdb_id).
func (t *TMDB) FindExternal(ctx context.Context, externalID, source string) (JSON, error) {
	data, err := t.get(ctx, "/find/"+url.PathEscape(externalID), map[string]string{"external_source": source})
	if err != nil || data == nil {
		return nil, err
	}
	for _, kind := range [][2]string{{"movie_results", "movie"}, {"tv_results", "tv"}} {
		if hits := list(data[kind[0]]); len(hits) > 0 {
			hits[0]["media_type"] = kind[1]
			return hits[0], nil
		}
	}
	return nil, nil
}

// Genres and WatchProviders are TMDB's lists of those.
func (t *TMDB) Genres(ctx context.Context, media string) ([]JSON, error) {
	data, err := t.get(ctx, "/genre/"+media+"/list", nil)
	return list(data["genres"]), err
}

func (t *TMDB) WatchProviders(ctx context.Context, media string) ([]JSON, error) {
	data, err := t.get(ctx, "/watch/providers/"+media, map[string]string{"watch_region": t.region})
	return results(data, ""), err
}
