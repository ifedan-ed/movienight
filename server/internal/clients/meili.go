package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Meilisearch indexes: the Jellyfin library, and the TMDB catalog (every
// title with enough ratings), searched by meaning and by typo-tolerant
// words together.
const (
	LibraryIndex = "library"
	// LibraryOwnPrefix starts the ids of the library index's documents for
	// Movie Night's own media libraries ("mn-movie-603"); the rest are
	// Jellyfin's item ids. Each sync removes only its own.
	LibraryOwnPrefix = "mn-"
	CatalogIndex     = "catalog"
)

// Meili is the Meilisearch client.
type Meili struct {
	http          *http.Client
	base          string
	apiKey        string
	embedderModel string
	maxHits       int // SEARCH_MAX_HITS
	log           *slog.Logger
}

// MeiliError is Meilisearch failing.
type MeiliError struct {
	Status int
	Msg    string
}

func (e *MeiliError) Error() string { return "Meilisearch: " + e.Msg }

// NewMeili makes the client.
func NewMeili(c *http.Client, base, apiKey, embedderModel string, maxHits int, log *slog.Logger) *Meili {
	return &Meili{http: c, base: strings.TrimRight(base, "/"), apiKey: apiKey, embedderModel: embedderModel, maxHits: maxHits, log: log}
}

func (m *Meili) call(ctx context.Context, method, path string, body any, timeout time.Duration) (*response, error) {
	var req *http.Request
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		req, _ = http.NewRequest(method, m.base+path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, _ = http.NewRequest(method, m.base+path, nil)
	}
	if m.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	resp, err := do(ctx, m.http, req, timeout, maxBody)
	if err != nil {
		return nil, &MeiliError{Status: http.StatusBadGateway, Msg: "didn't answer (" + err.Error() + ")"}
	}
	return resp, nil
}

func meiliFail(r *response) error {
	return &MeiliError{Status: r.Status, Msg: fmt.Sprintf("HTTP %d: %s", r.Status, clip(r.Body, 200))}
}

// EmbedderSettings: the embedder reads only searchText (the shape the
// 2026-10-02 benchmark scored).
func (m *Meili) EmbedderSettings() JSON {
	return JSON{"source": "huggingFace", "model": m.embedderModel, "pooling": "useModel",
		"documentTemplate": "{{doc.searchText}}", "documentTemplateMaxBytes": 2000}
}

// EnsureIndex creates an index if needed and applies its settings.
func (m *Meili) EnsureIndex(ctx context.Context, index string) error {
	resp, err := m.call(ctx, http.MethodPost, "/indexes", JSON{"uid": index, "primaryKey": "id"}, 0)
	if err != nil {
		return err
	}
	if resp.Status >= 400 {
		var e struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(resp.Body, &e) != nil || e.Code != "index_already_exists" {
			return meiliFail(resp)
		}
	}
	settings := JSON{
		"searchableAttributes": []string{"title", "original_title", "overview", "genres", "people", "searchText"},
		"filterableAttributes": []string{"media_type", "year", "genres"},
		"embedders":            JSON{"default": m.EmbedderSettings()},
		// SEARCH_MAX_HITS: how deep "Show more" can go (docs/limits.md).
		"pagination": JSON{"maxTotalHits": m.maxHits},
	}
	if index == CatalogIndex {
		settings["searchableAttributes"] = []string{"title", "original_title", "overview", "genres", "searchText"}
		settings["filterableAttributes"] = []string{"media_type", "year"}
	}
	resp, err = m.call(ctx, http.MethodPatch, "/indexes/"+index+"/settings", settings, 0)
	if err != nil {
		return err
	}
	if resp.Status >= 400 {
		return meiliFail(resp)
	}
	return nil
}

// ReplaceDocuments adds or updates documents; returns Meilisearch's task id.
func (m *Meili) ReplaceDocuments(ctx context.Context, index string, docs []JSON) (int, error) {
	if len(docs) == 0 {
		return 0, nil
	}
	resp, err := m.call(ctx, http.MethodPut, "/indexes/"+index+"/documents", docs, 120*time.Second)
	if err != nil {
		return 0, err
	}
	if resp.Status >= 400 {
		return 0, meiliFail(resp)
	}
	var out struct {
		TaskUID int `json:"taskUid"`
	}
	return out.TaskUID, decode(resp.Body, &out)
}

// DeleteDocuments removes documents by id. Its failure is an error (the
// Python ignored the answer, so a failed delete left stale titles in search
// without a word).
func (m *Meili) DeleteDocuments(ctx context.Context, index string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	resp, err := m.call(ctx, http.MethodPost, "/indexes/"+index+"/documents/delete-batch", ids, 60*time.Second)
	if err != nil {
		return err
	}
	if resp.Status >= 400 {
		return meiliFail(resp)
	}
	return nil
}

// FieldValues maps every document's id to one field (empty without an index).
func (m *Meili) FieldValues(ctx context.Context, index, field string) (map[string]any, error) {
	out := map[string]any{}
	for offset := 0; ; offset += 10000 {
		resp, err := m.call(ctx, http.MethodPost, "/indexes/"+index+"/documents/fetch",
			JSON{"fields": []string{"id", field}, "limit": 10000, "offset": offset}, 60*time.Second)
		if err != nil {
			return nil, err
		}
		if resp.Status == http.StatusNotFound {
			return out, nil
		}
		if resp.Status >= 400 {
			return nil, meiliFail(resp)
		}
		var pageData struct {
			Results []JSON `json:"results"`
			Total   int    `json:"total"`
		}
		if err := decode(resp.Body, &pageData); err != nil {
			return nil, err
		}
		for _, d := range pageData.Results {
			if id := fmt.Sprint(d["id"]); id != "" {
				out[id] = d[field]
			}
		}
		if offset+10000 >= pageData.Total {
			return out, nil
		}
	}
}

// Task reads one Meilisearch task.
func (m *Meili) Task(ctx context.Context, uid int) (JSON, error) {
	resp, err := m.call(ctx, http.MethodGet, "/tasks/"+strconv.Itoa(uid), nil, 0)
	if err != nil {
		return nil, err
	}
	if resp.Status >= 400 {
		return nil, meiliFail(resp)
	}
	var out JSON
	return out, decode(resp.Body, &out)
}

// Stats is an index's stats; nil when the index does not exist.
func (m *Meili) Stats(ctx context.Context, index string) (JSON, error) {
	resp, err := m.call(ctx, http.MethodGet, "/indexes/"+index+"/stats", nil, 0)
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusNotFound {
		return nil, nil
	}
	if resp.Status >= 400 {
		return nil, meiliFail(resp)
	}
	var out JSON
	return out, decode(resp.Body, &out)
}

// SearchOptions are one search's knobs.
type SearchOptions struct {
	Index         string
	SemanticRatio float64
	Limit         int
	MediaType     string // "movie", "tv" or "" for both
	Scores        bool   // ask for _rankingScore on each hit
}

// Search is a hybrid (meaning + words) search. When Meilisearch refuses the
// hybrid part (the embedder not ready: a fresh volume, the model still
// downloading), it falls back to keyword search: a deliberate degrade kept
// from the Python, so search works while the model loads. It is logged now
// (the Python did it silently), and a failure of the keyword search too is
// an error.
func (m *Meili) Search(ctx context.Context, query string, o SearchOptions) ([]JSON, error) {
	body := JSON{"q": query, "limit": o.Limit}
	if o.MediaType == "movie" || o.MediaType == "tv" {
		body["filter"] = "media_type = " + o.MediaType
	}
	if o.Scores {
		body["showRankingScore"] = true
	}
	hybrid := JSON{}
	for k, v := range body {
		hybrid[k] = v
	}
	hybrid["hybrid"] = JSON{"semanticRatio": o.SemanticRatio, "embedder": "default"}
	path := "/indexes/" + o.Index + "/search"
	resp, err := m.call(ctx, http.MethodPost, path, hybrid, 0)
	if err != nil {
		return nil, err
	}
	if resp.Status >= 400 {
		m.log.WarnContext(ctx, "hybrid search refused; searching by words only", "index", o.Index, "status", resp.Status)
		if resp, err = m.call(ctx, http.MethodPost, path, body, 0); err != nil {
			return nil, err
		}
		if resp.Status >= 400 {
			return nil, meiliFail(resp)
		}
	}
	var out struct {
		Hits []JSON `json:"hits"`
	}
	return out.Hits, decode(resp.Body, &out)
}

// LibraryDoc turns one Jellyfin movie/series into its search document (nil
// without an Id). searchText is the shape the embedder benchmark scored:
// title | original title | (year) | genres | overview | Cast/crew.
func LibraryDoc(item JSON) JSON {
	id := str(item, "Id")
	if id == "" {
		return nil
	}
	name := str(item, "Name")
	original := str(item, "OriginalTitle")
	if original == "" {
		original = name
	}
	overview := str(item, "Overview")
	var genres []string
	for _, g := range asSlice(item["Genres"]) {
		if s, ok := g.(string); ok {
			genres = append(genres, s)
		}
	}
	people := PeopleNames(item, 8)
	parts := []string{name}
	if original != name {
		parts = append(parts, original)
	}
	var year any
	if y, ok := num(item, "ProductionYear"); ok && y > 0 {
		year = int(y)
		parts = append(parts, fmt.Sprintf("(%d)", int(y)))
	}
	if len(genres) > 0 {
		parts = append(parts, strings.Join(genres, ", "))
	}
	parts = append(parts, overview)
	if len(people) > 0 {
		parts = append(parts, "Cast/crew: "+strings.Join(people, ", "))
	}
	var text []string
	for _, p := range parts {
		if p != "" {
			text = append(text, p)
		}
	}
	var tmdb any
	if ids, ok := item["ProviderIds"].(JSON); ok {
		if raw := str(ids, "Tmdb"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				tmdb = n
			}
		}
	}
	media := "movie"
	if str(item, "Type") == "Series" {
		media = "tv"
	}
	if genres == nil {
		genres = []string{}
	}
	return JSON{"id": id, "tmdb_id": tmdb, "media_type": media, "title": name, "original_title": original,
		"overview": overview, "genres": genres, "people": people, "year": year, "searchText": strings.Join(text, " | ")}
}

// PeopleNames is the first limit distinct names among an item's People.
func PeopleNames(item JSON, limit int) []string {
	names := []string{}
	seen := map[string]bool{}
	for _, p := range list(item["People"]) {
		n := str(p, "Name")
		if n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
		if len(names) >= limit {
			break
		}
	}
	return names
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
