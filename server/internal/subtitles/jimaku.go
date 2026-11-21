package subtitles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Jimaku is jimaku.cc (https://jimaku.cc/api/docs), a directory of anime
// subtitles, nearly all Japanese. An entry is one anime, found by its TMDB
// id ("tv:1234" or "movie:1234"); its files are listed per episode and
// fetched from their own address. The key goes in Authorization as it is,
// with no "Bearer".
type Jimaku struct {
	Web
	base, key string
}

// JimakuBase is the API's address.
const JimakuBase = "https://jimaku.cc/api"

// NewJimaku makes the source.
func NewJimaku(w Web, base, key string) *Jimaku {
	return &Jimaku{Web: w, base: strings.TrimRight(base, "/"), key: key}
}

func (j *Jimaku) Name() string { return "jimaku" }

type jimakuEntry struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	EnglishName string `json:"english_name"`
}

type jimakuFile struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int    `json:"size"`
}

func (j *Jimaku) call(ctx context.Context, path string, p url.Values, out any) error {
	u := j.base + path
	if len(p) > 0 {
		u += "?" + p.Encode()
	}
	_, body, err := j.get(ctx, u, maxAnswer, "Authorization", j.key, "Accept", "application/json")
	if err != nil {
		return failure(j.Name(), err)
	}
	if json.Unmarshal(body, out) != nil {
		return problem(j.Name(), "sent an answer Movie Night couldn't read")
	}
	return nil
}

// Search is for Japanese only: Jimaku's files carry no language field, and
// nearly all of them are Japanese.
func (j *Jimaku) Search(ctx context.Context, q Query) ([]Result, error) {
	if q.Language.Code != "ja" {
		return nil, nil
	}
	kind := "movie"
	if q.TV() {
		kind = "tv"
	}
	var entries []jimakuEntry
	if err := j.call(ctx, "/entries/search", url.Values{"tmdb_id": {kind + ":" + strconv.Itoa(q.TMDBID)}}, &entries); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []Result{}, nil
	}
	p := url.Values{}
	if q.TV() {
		p.Set("episode", strconv.Itoa(q.Episode))
	}
	var files []jimakuFile
	if err := j.call(ctx, "/entries/"+strconv.Itoa(entries[0].ID)+"/files", p, &files); err != nil {
		return nil, err
	}
	out := []Result{}
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Name))
		// Under 500 bytes is a broken upload; a .7z or .rar can't be
		// opened here.
		if f.URL == "" || (f.Size > 0 && f.Size < 500) || (subtitleExt[ext] == 0 && ext != ".zip") {
			continue
		}
		sum := sha256.Sum256([]byte(f.URL))
		lower := strings.ToLower(f.Name)
		out = append(out, Result{Source: j.Name(), ID: hex.EncodeToString(sum[:12]), Language: q.Language.Code,
			Release: f.Name, Machine: strings.Contains(lower, "whisper"), Ref: f.URL})
	}
	return out, nil
}

func (j *Jimaku) Download(ctx context.Context, r Result) ([]byte, error) {
	u, err := url.Parse(r.Ref)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, problem(j.Name(), "has no such subtitle")
	}
	// Jimaku's own download addresses want the key too; it never goes to
	// another host.
	var headers []string
	if b, err := url.Parse(j.base); err == nil && b.Host == u.Host {
		headers = []string{"Authorization", j.key}
	}
	_, body, err := j.get(ctx, r.Ref, maxFile, headers...)
	if err != nil {
		return nil, failure(j.Name(), err)
	}
	return body, nil
}
