package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Seerr (Jellyseerr/Overseerr): requests and their download status
// (app/clients/seerr.py). Requests carry X-Api-User so each is made as the
// person asking. DRY_RUN is enforced here: CreateRequest and
// ImportJellyfinUser then never send (reads still go out).
type Seerr struct {
	http   *http.Client
	base   string // ".../api/v1", or "" when Seerr is not set up
	apiKey string
	dryRun bool
}

// Seerr's media status values (mediaInfo.status).
const (
	SeerrUnknown    = 1
	SeerrPending    = 2
	SeerrProcessing = 3
	SeerrPartial    = 4
	SeerrAvailable  = 5
)

// SeerrError is Seerr refusing or failing, with its own message.
type SeerrError struct {
	Status int
	Msg    string
}

func (e *SeerrError) Error() string { return "Seerr: " + e.Msg }

// NewSeerr makes the client; base "" means Seerr is not set up.
func NewSeerr(c *http.Client, base, apiKey string, dryRun bool) *Seerr {
	s := &Seerr{http: c, apiKey: apiKey, dryRun: dryRun}
	if base != "" {
		s.base = base + "/api/v1"
	}
	return s
}

// Enabled reports whether Seerr is set up.
func (s *Seerr) Enabled() bool { return s.base != "" && s.apiKey != "" }

var errSeerrOff = &SeerrError{Status: http.StatusServiceUnavailable, Msg: "Seerr isn't set up on this install"}

func (s *Seerr) call(ctx context.Context, method, path string, params url.Values, body any, asUser int) (*response, error) {
	if !s.Enabled() {
		return nil, errSeerrOff
	}
	u := s.base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	var req *http.Request
	if body != nil {
		raw, _ := json.Marshal(body)
		req, _ = http.NewRequest(method, u, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, u, nil)
	}
	req.Header.Set("X-Api-Key", s.apiKey)
	if asUser > 0 {
		req.Header.Set("X-Api-User", strconv.Itoa(asUser))
	}
	resp, err := do(ctx, s.http, req, 0, maxBody)
	if err != nil {
		return nil, &SeerrError{Status: http.StatusBadGateway, Msg: "didn't answer (" + err.Error() + ")"}
	}
	return resp, nil
}

func seerrMessage(r *response) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(r.Body, &m) == nil && m.Message != "" {
		return clip([]byte(m.Message), 300)
	}
	return clip(r.Body, 300)
}

// get: nil for a 404, the answer for a 2xx, an error otherwise.
func (s *Seerr) get(ctx context.Context, path string, params url.Values) (any, error) {
	resp, err := s.call(ctx, http.MethodGet, path, params, nil, 0)
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusNotFound {
		return nil, nil
	}
	if resp.Status >= 400 {
		return nil, &SeerrError{Status: resp.Status, Msg: seerrMessage(resp)}
	}
	var out any
	if err := decode(resp.Body, &out); err != nil {
		return nil, &SeerrError{Status: http.StatusBadGateway, Msg: "answer wasn't JSON"}
	}
	return out, nil
}

func mediaPath(media string) string {
	if media == "tv" {
		return "tv"
	}
	return "movie"
}

// MediaPage is one page of everything Seerr knows (status of every title).
func (s *Seerr) MediaPage(ctx context.Context, take, skip int) (JSON, error) {
	v, err := s.get(ctx, "/media", url.Values{"take": {strconv.Itoa(take)}, "skip": {strconv.Itoa(skip)}, "filter": {"all"}, "sort": {"mediaAdded"}})
	m, _ := v.(JSON)
	return m, err
}

// MediaInfo is Seerr's live record of one title; nil when Seerr never saw it.
func (s *Seerr) MediaInfo(ctx context.Context, media string, tmdbID int) (JSON, error) {
	v, err := s.get(ctx, fmt.Sprintf("/%s/%d", mediaPath(media), tmdbID), nil)
	m, _ := v.(JSON)
	if err != nil || m == nil {
		return nil, err
	}
	info, _ := m["mediaInfo"].(JSON)
	return info, nil
}

// RequestsPage is one page of requests, optionally by one Seerr user.
func (s *Seerr) RequestsPage(ctx context.Context, take, skip int, filter string, requestedBy int) (JSON, error) {
	q := url.Values{"take": {strconv.Itoa(take)}, "skip": {strconv.Itoa(skip)}, "filter": {filter}, "sort": {"added"}}
	if requestedBy > 0 {
		q.Set("requestedBy", strconv.Itoa(requestedBy))
	}
	v, err := s.get(ctx, "/request", q)
	m, _ := v.(JSON)
	return m, err
}

// Users is every Seerr user, page by page.
func (s *Seerr) Users(ctx context.Context) ([]JSON, error) {
	var out []JSON
	for skip := 0; ; {
		v, err := s.get(ctx, "/user", url.Values{"take": {"100"}, "skip": {strconv.Itoa(skip)}})
		if err != nil {
			return nil, err
		}
		pageData, _ := v.(JSON)
		results := list(pageData["results"])
		out = append(out, results...)
		skip += len(results)
		info, _ := pageData["pageInfo"].(JSON)
		total, _ := num(info, "results")
		if len(results) == 0 || skip >= int(total) {
			return out, nil
		}
	}
}

// ImportJellyfinUser creates the Seerr user for a Jellyfin account. Under
// DRY_RUN it creates nothing and returns no users (the person then has no
// Seerr user here, which every caller already handles).
func (s *Seerr) ImportJellyfinUser(ctx context.Context, jellyfinUserID string) ([]JSON, error) {
	if s.dryRun {
		return nil, nil
	}
	resp, err := s.call(ctx, http.MethodPost, "/user/import-from-jellyfin", nil, JSON{"jellyfinUserIds": []string{jellyfinUserID}}, 0)
	if err != nil {
		return nil, err
	}
	if resp.Status >= 400 {
		return nil, &SeerrError{Status: resp.Status, Msg: seerrMessage(resp)}
	}
	var out []any
	if err := decode(resp.Body, &out); err != nil {
		return nil, &SeerrError{Status: http.StatusBadGateway, Msg: "answer wasn't a list"}
	}
	return list(out), nil
}

// CreateRequest asks Seerr for a title. {"already": true} when Seerr has a
// request (409). In dry-run mode nothing is sent.
func (s *Seerr) CreateRequest(ctx context.Context, mediaID int, media string, asUser int, seasons []int) (JSON, error) {
	if s.dryRun {
		return JSON{"dry_run": true, "mediaId": mediaID, "mediaType": media, "status": "would_request"}, nil
	}
	body := JSON{"mediaType": mediaPath(media), "mediaId": mediaID}
	if media == "tv" {
		if len(seasons) > 0 {
			body["seasons"] = seasons
		} else {
			body["seasons"] = "all"
		}
	}
	resp, err := s.call(ctx, http.MethodPost, "/request", nil, body, asUser)
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusConflict {
		return JSON{"already": true, "mediaId": mediaID, "mediaType": media}, nil
	}
	if resp.Status >= 400 {
		return nil, &SeerrError{Status: resp.Status, Msg: seerrMessage(resp)}
	}
	var out JSON
	if err := decode(resp.Body, &out); err != nil {
		return nil, &SeerrError{Status: http.StatusBadGateway, Msg: "answer wasn't JSON"}
	}
	return out, nil
}
