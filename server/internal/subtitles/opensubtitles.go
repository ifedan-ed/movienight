package subtitles

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
	"golang.org/x/sync/singleflight"
)

// OpenSubtitles is opensubtitles.com's REST API v1
// (https://opensubtitles.stoplight.io). Searching needs only the API key.
// Downloads count against a quota: with a username and password, the
// account's (a free account has 20 a day); without, the key's small
// per-address allowance. The login's token is kept in Valkey until it
// expires, so every API replica shares one login; the site asks clients
// not to log in more than needed (its limit is 10 logins a minute).
type OpenSubtitles struct {
	Web
	base               string
	key                string
	username, password string
	kv                 valkey.Client
	login              singleflight.Group
	now                func() time.Time
}

// OpenSubtitlesBase is the API's address. A login may name the VIP host
// instead (vipHost), which VIP accounts must then use for every call.
const (
	OpenSubtitlesBase = "https://api.opensubtitles.com/api/v1"
	vipHost           = "vip-api.opensubtitles.com"
)

// Valkey keys: the login (token and host) and the account's download
// quota, both shared by every replica.
const (
	osLoginKey   = "subs:os:login"
	osQuotaKey   = "subs:os:quota"
	osRefusedKey = "subs:os:refused"
)

// NewOpenSubtitles makes the source. base is OpenSubtitlesBase outside
// tests; username and password may both be empty.
func NewOpenSubtitles(w Web, base, key, username, password string, kv valkey.Client) *OpenSubtitles {
	return &OpenSubtitles{Web: w, base: strings.TrimRight(base, "/"), key: key, username: username, password: password,
		kv: kv, now: time.Now}
}

func (o *OpenSubtitles) Name() string { return "opensubtitles" }

type osLogin struct {
	Token string `json:"token"`
	Base  string `json:"base"`
}

type osSearch struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Language          string  `json:"language"`
			DownloadCount     int     `json:"download_count"`
			Ratings           float64 `json:"ratings"`
			HearingImpaired   bool    `json:"hearing_impaired"`
			AITranslated      bool    `json:"ai_translated"`
			MachineTranslated bool    `json:"machine_translated"`
			Release           string  `json:"release"`
			MoviehashMatch    bool    `json:"moviehash_match"`
			ForeignPartsOnly  bool    `json:"foreign_parts_only"`
			FeatureDetails    struct {
				SeasonNumber  int `json:"season_number"`
				EpisodeNumber int `json:"episode_number"`
			} `json:"feature_details"`
			Files []struct {
				FileID   int    `json:"file_id"`
				FileName string `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

// Search asks /subtitles. The parameters go in alphabetical order and in
// lower case, with ids as bare numbers: the API answers anything else with
// a redirect to that spelling, which costs a round trip.
func (o *OpenSubtitles) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Language.OpenSubtitles == "" {
		return nil, nil
	}
	p := url.Values{"languages": {q.Language.OpenSubtitles}}
	if q.Hash != "" {
		p.Set("moviehash", q.Hash)
	}
	imdb := imdbNumber(q.IMDbID)
	switch {
	case q.TV():
		if imdb != "" {
			p.Set("parent_imdb_id", imdb)
		} else {
			p.Set("parent_tmdb_id", strconv.Itoa(q.TMDBID))
		}
		p.Set("season_number", strconv.Itoa(q.Season))
		p.Set("episode_number", strconv.Itoa(q.Episode))
	case imdb != "":
		p.Set("imdb_id", imdb)
	default:
		p.Set("tmdb_id", strconv.Itoa(q.TMDBID))
	}
	login, _ := o.cachedLogin(ctx)
	headers := o.headers(login)
	_, body, err := o.get(ctx, o.baseFor(login)+"/subtitles?"+p.Encode(), maxAnswer, headers...)
	if err != nil {
		return nil, failure(o.Name(), err)
	}
	var ans osSearch
	if err := json.Unmarshal(body, &ans); err != nil {
		return nil, problem(o.Name(), "sent an answer Movie Night couldn't read")
	}
	out := []Result{}
	for _, d := range ans.Data {
		a := d.Attributes
		if len(a.Files) == 0 || a.ForeignPartsOnly {
			continue // nothing to download, or only the foreign-language lines
		}
		if q.TV() && a.FeatureDetails.EpisodeNumber != 0 && a.FeatureDetails.EpisodeNumber != q.Episode {
			continue
		}
		release := a.Release
		if release == "" {
			release = a.Files[0].FileName
		}
		out = append(out, Result{Source: o.Name(), ID: strconv.Itoa(a.Files[0].FileID), Language: q.Language.Code,
			Release: release, HearingImpaired: a.HearingImpaired, Machine: a.AITranslated || a.MachineTranslated,
			HashMatch: a.MoviehashMatch, Downloads: a.DownloadCount, Rating: a.Ratings, Ref: strconv.Itoa(a.Files[0].FileID)})
	}
	return out, nil
}

func (o *OpenSubtitles) headers(l osLogin) []string {
	h := []string{"Api-Key", o.key, "Accept", "application/json"}
	if l.Token != "" {
		// Needed on the VIP host for every call; elsewhere it makes the
		// search count as the account's.
		h = append(h, "Authorization", "Bearer "+l.Token)
	}
	return h
}

// baseFor is the API address to use after a login: the VIP host when the
// login named it, else the configured one. Only the documented VIP host is
// followed, so a test's base (or a changed answer) never sends calls
// elsewhere.
func (o *OpenSubtitles) baseFor(l osLogin) string {
	if l.Base == vipHost && o.base == OpenSubtitlesBase {
		return "https://" + vipHost + "/api/v1"
	}
	return o.base
}

// cachedLogin is the shared login, or none (no credentials, or the site
// refused them). It logs in when none is kept.
func (o *OpenSubtitles) cachedLogin(ctx context.Context) (osLogin, error) {
	if o.username == "" {
		return osLogin{}, nil
	}
	if raw, err := o.kv.Do(ctx, o.kv.B().Get().Key(osLoginKey).Build()).AsBytes(); err == nil {
		var l osLogin
		if json.Unmarshal(raw, &l) == nil && l.Token != "" {
			return l, nil
		}
	}
	// One login at a time per replica; the others wait for its answer.
	v, err, _ := o.login.Do("login", func() (any, error) { return o.logIn(ctx) })
	if err != nil {
		return osLogin{}, err
	}
	return v.(osLogin), nil
}

// refusedFor is how long a refused username and password are not tried
// again: the site asks clients to stop after a 401 rather than retry.
const refusedFor = 15 * time.Minute

func (o *OpenSubtitles) logIn(ctx context.Context) (osLogin, error) {
	if n, _ := o.kv.Do(ctx, o.kv.B().Exists().Key(osRefusedKey).Build()).AsInt64(); n > 0 {
		return osLogin{}, problem(o.Name(), "refused the install's username or password")
	}
	body, _ := json.Marshal(map[string]string{"username": o.username, "password": o.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/login", bytes.NewReader(body))
	if err != nil {
		return osLogin{}, err
	}
	req.Header.Set("Api-Key", o.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	_, raw, err := o.do(req, maxAnswer)
	var st *errStatus
	if errors.As(err, &st) && st.Code == http.StatusUnauthorized {
		o.kv.Do(ctx, o.kv.B().Set().Key(osRefusedKey).Value("1").Ex(refusedFor).Build())
		return osLogin{}, problem(o.Name(), "refused the install's username or password")
	}
	if err != nil {
		return osLogin{}, failure(o.Name(), err)
	}
	var ans struct {
		Token   string `json:"token"`
		BaseURL string `json:"base_url"`
	}
	if json.Unmarshal(raw, &ans) != nil || ans.Token == "" {
		return osLogin{}, problem(o.Name(), "sent a login Movie Night couldn't read")
	}
	l := osLogin{Token: ans.Token, Base: ans.BaseURL}
	if ttl := tokenLife(ans.Token, o.now()); ttl > 0 {
		v, _ := json.Marshal(l)
		o.kv.Do(ctx, o.kv.B().Set().Key(osLoginKey).Value(string(v)).Ex(ttl).Build())
	}
	return l, nil
}

// tokenLife is how long to keep a login: until a minute before the JWT's
// own expiry ("exp"). A token whose expiry can't be read is kept 12 hours,
// the life OpenSubtitles' tokens have had.
func tokenLife(token string, now time.Time) time.Duration {
	parts := strings.Split(token, ".")
	if len(parts) == 3 {
		if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			var c struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(raw, &c) == nil && c.Exp > 0 {
				return time.Unix(c.Exp, 0).Sub(now) - time.Minute
			}
		}
	}
	return 12 * time.Hour
}

type osQuota struct {
	Until time.Time `json:"until"` // zero: the site named no time
}

// Quota reports a used-up download allowance and when it comes back.
func (o *OpenSubtitles) Quota(ctx context.Context) (bool, time.Time) {
	raw, err := o.kv.Do(ctx, o.kv.B().Get().Key(osQuotaKey).Build()).AsBytes()
	if err != nil {
		return false, time.Time{}
	}
	var q osQuota
	_ = json.Unmarshal(raw, &q)
	return true, q.Until
}

// usedUp records the allowance as used up until reset (an RFC 3339 time
// from the site). The key expires then, so the next download after it is
// tried again; with no time given it is tried again after an hour.
func (o *OpenSubtitles) usedUp(ctx context.Context, reset string) *Problem {
	until, _ := time.Parse(time.RFC3339, reset)
	ttl := time.Hour
	if !until.IsZero() && until.After(o.now()) {
		ttl = until.Sub(o.now())
	} else {
		until = time.Time{}
	}
	v, _ := json.Marshal(osQuota{Until: until})
	o.kv.Do(ctx, o.kv.B().Set().Key(osQuotaKey).Value(string(v)).Ex(ttl).Build())
	return &Problem{Source: o.Name(), Msg: "has no downloads left today", Quota: true, Until: until}
}

type osDownload struct {
	Link         string `json:"link"`
	Remaining    int    `json:"remaining"`
	ResetTimeUTC string `json:"reset_time_utc"`
	Message      string `json:"message"`
}

// Download asks /download for a temporary link (this is what counts
// against the quota), then fetches the file from it. A 401 means the kept
// login ran out early: it is dropped and the download tried once more.
func (o *OpenSubtitles) Download(ctx context.Context, r Result) ([]byte, error) {
	if used, until := o.Quota(ctx); used {
		return nil, &Problem{Source: o.Name(), Msg: "has no downloads left today", Quota: true, Until: until}
	}
	fileID, err := strconv.Atoi(r.Ref)
	if err != nil {
		return nil, problem(o.Name(), "has no such subtitle")
	}
	for attempt := 0; ; attempt++ {
		login, err := o.cachedLogin(ctx)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(map[string]int{"file_id": fileID})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseFor(login)+"/download", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		h := o.headers(login)
		for i := 0; i+1 < len(h); i += 2 {
			req.Header.Set(h[i], h[i+1])
		}
		req.Header.Set("Content-Type", "application/json")
		_, raw, err := o.do(req, maxAnswer)
		var st *errStatus
		if errors.As(err, &st) {
			switch {
			case st.Code == http.StatusNotAcceptable:
				// The quota: the body says when it resets.
				var d osDownload
				_ = json.Unmarshal(st.Body, &d)
				return nil, o.usedUp(ctx, d.ResetTimeUTC)
			case st.Code == http.StatusUnauthorized && login.Token != "" && attempt == 0:
				o.kv.Do(ctx, o.kv.B().Del().Key(osLoginKey).Build())
				continue
			case st.Code == http.StatusGone:
				return nil, problem(o.Name(), "no longer has this subtitle")
			}
		}
		if err != nil {
			return nil, failure(o.Name(), err)
		}
		var d osDownload
		if json.Unmarshal(raw, &d) != nil || d.Link == "" {
			return nil, problem(o.Name(), "sent a download Movie Night couldn't read")
		}
		if d.Remaining <= 0 && d.ResetTimeUTC != "" {
			// This was the last one: say so before the next person tries.
			o.usedUp(ctx, d.ResetTimeUTC)
		}
		// The link is a plain temporary address: no key goes with it.
		_, file, err := o.get(ctx, d.Link, maxFile)
		if err != nil {
			return nil, failure(o.Name(), err)
		}
		return file, nil
	}
}
