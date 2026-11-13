package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Jellyfin talks to the install's Jellyfin server.
// Two kinds of access:
//   - the install's API key (admin): the library, anyone's plays, resume and
//     next up, marking items played. It never leaves the server.
//   - a person's own access token (from linking their account): it goes into
//     their play links (stream and subtitle URLs), as Jellyfin's apps do.
type Jellyfin struct {
	http   *http.Client
	images *http.Client
	base   string
	public string
	apiKey string
	// dryRun (DRY_RUN): the calls that change Jellyfin (played marks,
	// signing a session out) return without sending anything. Reads and
	// the sign-in flows still go out: linking only adds the person's own
	// new session.
	dryRun bool
}

// Jellyfin client identity, sent in the MediaBrowser authorization header.
const (
	jellyfinClient  = "Movie Night"
	jellyfinVersion = "2.0"
	serverDeviceID  = "movienight-server"
)

// Whole-list reads go page by page until Jellyfin's own count is reached:
// no cap on how many items (docs/limits.md).
const (
	libraryPage  = 500 // library items carry Overview and People, so pages stay small
	playedPage   = 1000
	continuePage = 100
	libraryWait  = 120 * time.Second
)

// JellyfinError is Jellyfin refusing or failing.
type JellyfinError struct {
	Status int
	Msg    string
}

func (e *JellyfinError) Error() string { return "Jellyfin: " + e.Msg }

// IncompleteRead is a whole-list read that did not come back whole (an
// error, an empty or non-JSON answer, a page that stopped short, a list that
// changed while it was read). It is refused as a whole: a partial library
// would make titles look gone that are not.
type IncompleteRead struct{ JellyfinError }

func incomplete(status int, msg string) *IncompleteRead {
	return &IncompleteRead{JellyfinError{Status: status, Msg: msg}}
}

// NewJellyfin makes the client. images is the separate pool for posters.
func NewJellyfin(c, images *http.Client, base, public, apiKey string, dryRun bool) *Jellyfin {
	return &Jellyfin{http: c, images: images, base: strings.TrimRight(base, "/"), public: strings.TrimRight(public, "/"),
		apiKey: apiKey, dryRun: dryRun}
}

// AuthHeader is the MediaBrowser form Jellyfin 10.9+ wants; sign-in calls
// also need the client and device fields.
func AuthHeader(token, deviceID, device string) string {
	parts := []string{
		fmt.Sprintf("Client=%q", jellyfinClient), fmt.Sprintf("Device=%q", device),
		fmt.Sprintf("DeviceId=%q", deviceID), fmt.Sprintf("Version=%q", jellyfinVersion),
	}
	if token != "" {
		parts = append(parts, fmt.Sprintf("Token=%q", token))
	}
	return "MediaBrowser " + strings.Join(parts, ", ")
}

func (j *Jellyfin) newRequest(method, path string, params url.Values, body any, token, deviceID string) (*http.Request, error) {
	u := j.base + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(raw)
	}
	var req *http.Request
	var err error
	if rd != nil {
		req, err = http.NewRequest(method, u, rd)
	} else {
		req, err = http.NewRequest(method, u, nil)
	}
	if err != nil {
		return nil, err
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", AuthHeader(token, deviceID, "Movie Night"))
	return req, nil
}

// request is a call with the API key (or a person's token): nil for a 404
// or an empty body; 401/403 become "Jellyfin refused the sign-in".
func (j *Jellyfin) request(ctx context.Context, method, path string, params url.Values, body any, token, deviceID string, timeout time.Duration) (JSON, error) {
	if token == "" {
		token = j.apiKey
	}
	if deviceID == "" {
		deviceID = serverDeviceID
	}
	req, err := j.newRequest(method, path, params, body, token, deviceID)
	if err != nil {
		return nil, err
	}
	resp, err := do(ctx, j.http, req, timeout, maxBody)
	if err != nil {
		return nil, &JellyfinError{Status: http.StatusBadGateway, Msg: "didn't answer (" + err.Error() + ")"}
	}
	switch {
	case resp.Status == http.StatusUnauthorized || resp.Status == http.StatusForbidden:
		return nil, &JellyfinError{Status: resp.Status, Msg: "Jellyfin refused the sign-in"}
	case resp.Status == http.StatusNotFound:
		return nil, nil
	case resp.Status >= 400:
		return nil, &JellyfinError{Status: resp.Status, Msg: clip(resp.Body, 200)}
	case len(resp.Body) == 0:
		return JSON{}, nil
	}
	var out JSON
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		// Not the Python's silent {}: an answer we cannot read is an error.
		return nil, &JellyfinError{Status: http.StatusBadGateway, Msg: "answer wasn't JSON"}
	}
	return out, nil
}

// itemsPage is one strict page of an Items query: (items, Jellyfin's total).
func (j *Jellyfin) itemsPage(ctx context.Context, path string, params url.Values, timeout time.Duration) ([]JSON, int, error) {
	req, _ := j.newRequest(http.MethodGet, path, params, nil, j.apiKey, serverDeviceID)
	resp, err := do(ctx, j.http, req, timeout, maxBody)
	if err != nil {
		return nil, 0, incomplete(http.StatusBadGateway, "Jellyfin didn't answer ("+err.Error()+")")
	}
	if resp.Status != http.StatusOK {
		return nil, 0, incomplete(resp.Status, fmt.Sprintf("Jellyfin answered HTTP %d", resp.Status))
	}
	if len(resp.Body) == 0 {
		return nil, 0, incomplete(http.StatusBadGateway, "Jellyfin sent an empty answer")
	}
	var data struct {
		Items            []JSON `json:"Items"`
		TotalRecordCount *int   `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(resp.Body, &data); err != nil {
		return nil, 0, incomplete(http.StatusBadGateway, "Jellyfin's answer wasn't JSON")
	}
	if data.Items == nil || data.TotalRecordCount == nil {
		return nil, 0, incomplete(http.StatusBadGateway, "Jellyfin's answer had no item list or no count")
	}
	return data.Items, *data.TotalRecordCount, nil
}

// allItems reads every item of an Items query, or IncompleteRead. Complete
// means: every page answered, the items add up to Jellyfin's count, the
// count did not change between pages, and no item came back twice.
func (j *Jellyfin) allItems(ctx context.Context, path string, pageSize int, timeout time.Duration, params url.Values) ([]JSON, error) {
	var out []JSON
	seen := map[string]bool{}
	first := -1
	for {
		q := url.Values{}
		for k, v := range params {
			q[k] = v
		}
		q.Set("StartIndex", strconv.Itoa(len(out)))
		q.Set("Limit", strconv.Itoa(pageSize))
		q.Set("EnableTotalRecordCount", "true")
		items, total, err := j.itemsPage(ctx, path, q, timeout)
		if err != nil {
			return nil, err
		}
		if first < 0 {
			first = total
		} else if total != first {
			return nil, incomplete(http.StatusBadGateway, fmt.Sprintf("Jellyfin's list changed while it was read (%d items, then %d); read again later", first, total))
		}
		for _, it := range items {
			id := str(it, "Id")
			if id == "" {
				continue
			}
			if seen[id] {
				return nil, incomplete(http.StatusBadGateway, "Jellyfin's list moved while it was read (an item came back twice); read again later")
			}
			seen[id] = true
		}
		out = append(out, items...)
		if len(out) >= total {
			break
		}
		if len(items) == 0 {
			return nil, incomplete(http.StatusBadGateway, fmt.Sprintf("Jellyfin stopped after %d of %d items", len(out), total))
		}
	}
	if len(out) != first {
		return nil, incomplete(http.StatusBadGateway, fmt.Sprintf("Jellyfin sent %d items but counts %d", len(out), first))
	}
	return out, nil
}

// LibraryItems is every movie and series with what search and the library
// table need, page by page (120 s a page), or IncompleteRead.
func (j *Jellyfin) LibraryItems(ctx context.Context, userID string) ([]JSON, error) {
	return j.allItems(ctx, "/Users/"+url.PathEscape(userID)+"/Items", libraryPage, libraryWait, url.Values{
		"IncludeItemTypes": {"Movie,Series"}, "Recursive": {"true"},
		"Fields":       {"Overview,Genres,People,ProviderIds,ProductionYear,OriginalTitle,DateCreated"},
		"EnableImages": {"false"},
		// A steady order, so pages do not shift as titles are renamed.
		"SortBy": {"DateCreated,SortName"}, "SortOrder": {"Ascending"},
	})
}

// Played is every played movie/episode of a person, or IncompleteRead.
func (j *Jellyfin) Played(ctx context.Context, userID, itemTypes string) ([]JSON, error) {
	if itemTypes == "" {
		itemTypes = "Movie,Episode"
	}
	return j.allItems(ctx, "/Users/"+url.PathEscape(userID)+"/Items", playedPage, 0, url.Values{
		"IncludeItemTypes": {itemTypes}, "Filters": {"IsPlayed"},
		// By arrival, not by play: a replay during the read does not shift pages.
		"SortBy": {"DateCreated,SortName"}, "SortOrder": {"Ascending"}, "Recursive": {"true"},
		"Fields": {"ProviderIds,UserData"}, "EnableImages": {"false"},
	})
}

// everyPage reads a list query until Jellyfin's count (no cap). Lenient as
// in the Python: it stops at an empty page or a missing count, for the
// "continue watching" style lists where a short read only means fewer rows.
func (j *Jellyfin) everyPage(ctx context.Context, path string, params url.Values) ([]JSON, error) {
	var out []JSON
	for {
		q := url.Values{}
		for k, v := range params {
			q[k] = v
		}
		q.Set("StartIndex", strconv.Itoa(len(out)))
		q.Set("Limit", strconv.Itoa(continuePage))
		q.Set("EnableTotalRecordCount", "true")
		data, err := j.request(ctx, http.MethodGet, path, q, nil, "", "", 0)
		if err != nil {
			return nil, err
		}
		items := list(data["Items"])
		out = append(out, items...)
		total, ok := num(data, "TotalRecordCount")
		if len(items) == 0 || !ok || len(out) >= int(total) {
			return out, nil
		}
	}
}

// Resume is every partly watched movie and episode ("Continue watching").
func (j *Jellyfin) Resume(ctx context.Context, userID string) ([]JSON, error) {
	return j.everyPage(ctx, "/Users/"+url.PathEscape(userID)+"/Items/Resume", url.Values{"MediaTypes": {"Video"}, "Fields": {"ProviderIds,Overview"}})
}

// NextUp is the next episode of every show in progress.
func (j *Jellyfin) NextUp(ctx context.Context, userID string) ([]JSON, error) {
	return j.everyPage(ctx, "/Shows/NextUp", url.Values{"UserId": {userID}, "Fields": {"ProviderIds,Overview"}})
}

// Item is one item as a person sees it (nil when unknown).
func (j *Jellyfin) Item(ctx context.Context, userID, itemID, fields string) (JSON, error) {
	if fields == "" {
		fields = "ProviderIds,MediaSources"
	}
	return j.request(ctx, http.MethodGet, "/Users/"+url.PathEscape(userID)+"/Items/"+url.PathEscape(itemID), url.Values{"Fields": {fields}}, nil, "", "", 0)
}

// Episodes lists a series' episodes (one season, or all with season < 0).
func (j *Jellyfin) Episodes(ctx context.Context, seriesID, userID string, season int) ([]JSON, error) {
	q := url.Values{"UserId": {userID}, "Fields": {"ProviderIds"}, "EnableImages": {"false"}}
	if season >= 0 {
		q.Set("Season", strconv.Itoa(season))
	}
	data, err := j.request(ctx, http.MethodGet, "/Shows/"+url.PathEscape(seriesID)+"/Episodes", q, nil, "", "", 0)
	return list(data["Items"]), err
}

// MarkPlayed and MarkUnplayed set a person's played state. Under DRY_RUN
// they send nothing and succeed.
func (j *Jellyfin) MarkPlayed(ctx context.Context, userID, itemID string, when time.Time) error {
	if j.dryRun {
		return nil
	}
	q := url.Values{}
	if !when.IsZero() {
		q.Set("datePlayed", when.UTC().Format("2006-01-02T15:04:05.000Z"))
	}
	_, err := j.request(ctx, http.MethodPost, "/Users/"+url.PathEscape(userID)+"/PlayedItems/"+url.PathEscape(itemID), q, nil, "", "", 0)
	return err
}

func (j *Jellyfin) MarkUnplayed(ctx context.Context, userID, itemID string) error {
	if j.dryRun {
		return nil
	}
	_, err := j.request(ctx, http.MethodDelete, "/Users/"+url.PathEscape(userID)+"/PlayedItems/"+url.PathEscape(itemID), nil, nil, "", "", 0)
	return err
}

// ServerID is the server's own id (Swiftfin's links name the server by it).
func (j *Jellyfin) ServerID(ctx context.Context) (string, error) {
	data, err := j.request(ctx, http.MethodGet, "/System/Info/Public", nil, nil, "", "", 0)
	if err != nil {
		return "", err
	}
	if data == nil {
		return "", &JellyfinError{Status: http.StatusBadGateway, Msg: "no public system info"}
	}
	return str(data, "Id"), nil
}

// ---- Linking a person's account ----

// QuickConnectEnabled asks whether Quick Connect is on.
func (j *Jellyfin) QuickConnectEnabled(ctx context.Context) (bool, error) {
	req, _ := j.newRequest(http.MethodGet, "/QuickConnect/Enabled", nil, nil, "", serverDeviceID)
	resp, err := do(ctx, j.http, req, 0, 1<<10)
	if err != nil {
		return false, &JellyfinError{Status: http.StatusBadGateway, Msg: "didn't answer"}
	}
	return resp.Status == http.StatusOK && strings.EqualFold(strings.TrimSpace(string(resp.Body)), "true"), nil
}

// QuickConnectInitiate returns {"Secret", "Code", ...}: the person types the
// code into a Jellyfin app where they are signed in.
func (j *Jellyfin) QuickConnectInitiate(ctx context.Context, deviceID string) (JSON, error) {
	return j.signInCall(ctx, http.MethodPost, "/QuickConnect/Initiate", nil, nil, deviceID,
		func(int) *JellyfinError {
			return &JellyfinError{Status: http.StatusServiceUnavailable, Msg: "Quick Connect is turned off on this Jellyfin server"}
		})
}

// QuickConnectReady reports whether the person approved the code.
func (j *Jellyfin) QuickConnectReady(ctx context.Context, secret, deviceID string) (bool, error) {
	data, err := j.signInCall(ctx, http.MethodGet, "/QuickConnect/Connect", url.Values{"secret": {secret}}, nil, deviceID,
		func(status int) *JellyfinError {
			if status == http.StatusBadRequest || status == http.StatusNotFound {
				return &JellyfinError{Status: http.StatusNotFound, Msg: "That code expired — start again"}
			}
			return &JellyfinError{Status: status, Msg: fmt.Sprintf("Jellyfin answered HTTP %d", status)}
		})
	if err != nil {
		return false, err
	}
	ok, _ := data["Authenticated"].(bool)
	return ok, nil
}

// QuickConnectFinish swaps an approved secret for the person's token.
func (j *Jellyfin) QuickConnectFinish(ctx context.Context, secret, deviceID string) (JSON, error) {
	return j.signInCall(ctx, http.MethodPost, "/Users/AuthenticateWithQuickConnect", nil, JSON{"Secret": secret}, deviceID,
		func(status int) *JellyfinError {
			return &JellyfinError{Status: status, Msg: "Quick Connect wasn't approved"}
		})
}

// SignIn signs a person in with their Jellyfin password (never kept).
func (j *Jellyfin) SignIn(ctx context.Context, username, password, deviceID string) (JSON, error) {
	return j.signInCall(ctx, http.MethodPost, "/Users/AuthenticateByName", nil, JSON{"Username": username, "Pw": password}, deviceID,
		func(status int) *JellyfinError {
			if status == 400 || status == 401 || status == 403 {
				return &JellyfinError{Status: http.StatusUnauthorized, Msg: "Wrong Jellyfin username or password"}
			}
			return &JellyfinError{Status: status, Msg: "Jellyfin didn't answer"}
		})
}

// SignOut ends a person's Jellyfin session. Best effort, as in the Python:
// it runs when they unlink, and the link is gone on our side either way.
// Under DRY_RUN nothing is sent: a copy of an install holds the live
// install's tokens, and signing one out would sign the live link out too.
func (j *Jellyfin) SignOut(ctx context.Context, token, deviceID string) error {
	if j.dryRun {
		return nil
	}
	req, _ := j.newRequest(http.MethodPost, "/Sessions/Logout", nil, nil, token, deviceID)
	_, err := do(ctx, j.http, req, 0, 1<<10)
	return err
}

// Me is the account behind a person's token (nil when it is gone).
func (j *Jellyfin) Me(ctx context.Context, token, deviceID string) (JSON, error) {
	return j.request(ctx, http.MethodGet, "/Users/Me", nil, nil, token, deviceID, 0)
}

// signInCall is a call without the API key (sign-in flows identify the
// device only); onError words an HTTP failure.
func (j *Jellyfin) signInCall(ctx context.Context, method, path string, params url.Values, body any, deviceID string, onError func(int) *JellyfinError) (JSON, error) {
	req, err := j.newRequest(method, path, params, body, "", deviceID)
	if err != nil {
		return nil, err
	}
	resp, err := do(ctx, j.http, req, 0, maxBody)
	if err != nil {
		return nil, &JellyfinError{Status: http.StatusBadGateway, Msg: "Jellyfin didn't answer"}
	}
	if resp.Status >= 400 {
		return nil, onError(resp.Status)
	}
	var out JSON
	if err := decode(resp.Body, &out); err != nil {
		return nil, &JellyfinError{Status: http.StatusBadGateway, Msg: "answer wasn't JSON"}
	}
	return out, nil
}

// Image fetches one item image for the image proxy, on its own connection
// pool. (nil, "", nil) when Jellyfin has none.
func (j *Jellyfin) Image(ctx context.Context, itemID, kind string, maxWidth int) ([]byte, string, error) {
	q := url.Values{"maxWidth": {strconv.Itoa(maxWidth)}, "quality": {"85"}}
	req, _ := j.newRequest(http.MethodGet, "/Items/"+url.PathEscape(itemID)+"/Images/"+url.PathEscape(kind), q, nil, j.apiKey, serverDeviceID)
	resp, err := do(ctx, j.images, req, 0, maxImage)
	if err != nil {
		return nil, "", &JellyfinError{Status: http.StatusBadGateway, Msg: "didn't answer"}
	}
	if resp.Status == http.StatusNotFound {
		return nil, "", nil
	}
	if resp.Status != http.StatusOK {
		return nil, "", &JellyfinError{Status: resp.Status, Msg: fmt.Sprintf("image answered HTTP %d", resp.Status)}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	return resp.Body, ct, nil
}

// ---- Links handed to players (a person's own token) ----

// WebURL is the item's page in Jellyfin's web app.
func (j *Jellyfin) WebURL(itemID string) string {
	return j.public + "/web/#/details?id=" + url.QueryEscape(itemID)
}

// StreamURL is the original file, untouched (players read embedded tracks).
func (j *Jellyfin) StreamURL(itemID, mediaSourceID, container, token string) string {
	ext := ""
	if container != "" {
		ext = "." + strings.Split(container, ",")[0]
	}
	q := url.Values{"static": {"true"}, "mediaSourceId": {mediaSourceID}, "api_key": {token}}
	return fmt.Sprintf("%s/Videos/%s/stream%s?%s", j.public, url.PathEscape(itemID), ext, q.Encode())
}

// SubtitleURL is one subtitle track as a file.
func (j *Jellyfin) SubtitleURL(itemID, mediaSourceID string, index int, token, format string) string {
	if format == "" {
		format = "srt"
	}
	q := url.Values{"api_key": {token}}
	return fmt.Sprintf("%s/Videos/%s/%s/Subtitles/%d/0/Stream.%s?%s", j.public, url.PathEscape(itemID), url.PathEscape(mediaSourceID), index, format, q.Encode())
}
