package stream

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// keyParams are the query parameters Jellyfin reads a key from. They are
// removed from every playlist line and from every request passed upstream:
// the browser must never see a key, and must not be able to supply one.
var keyParams = map[string]bool{"api_key": true, "apikey": true}

// pinned are the parameters the ticket decides. Whatever the browser sends
// for them is replaced, so a ticket for one play cannot be pointed at
// another media source or another play's transcode.
var pinned = map[string]bool{"mediasourceid": true, "playsessionid": true, "deviceid": true}

// insideItem is path relative to the item's directory at Jellyfin
// (/videos/<item>/...), and whether it is inside it at all. Jellyfin writes
// "videos" and "Videos", and item ids with or without dashes, in
// different places, so both are compared loosely.
func insideItem(path, item string) (string, bool) {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) != 3 || !strings.EqualFold(parts[0], "videos") || normID(parts[1]) != normID(item) {
		return "", false
	}
	rel := parts[2]
	if rel == "" || strings.Contains(rel, "..") || strings.Contains(rel, "//") {
		return "", false
	}
	return rel, true
}

func normID(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }

// cleanQuery drops key parameters and pinned ones (any letter case:
// Jellyfin binds parameters case-insensitively).
func cleanQuery(q url.Values, dropPinned bool) url.Values {
	out := url.Values{}
	for k, v := range q {
		lk := strings.ToLower(k)
		if keyParams[lk] || (dropPinned && pinned[lk]) {
			continue
		}
		out[k] = v
	}
	return out
}

var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

var errOutside = errors.New("playlist points outside the item")

// rewritePlaylist makes an HLS playlist from Jellyfin point back at the
// proxy. upstream is the playlist's own address at Jellyfin
// (/videos/<item>/main.m3u8); every address in it is resolved against
// that, must fall inside the item's directory (/videos/<item>/), and is
// written relative to it, which is the ticket's directory at the proxy (the
// browser resolves it against /api/v1/stream/<ticket>/main.m3u8). Key
// parameters are removed from every address. basePath is the path part of
// JELLYFIN_BASE_URL ("" or "/jellyfin" behind a sub-path).
func rewritePlaylist(body []byte, upstream *url.URL, basePath, item string) ([]byte, error) {
	fix := func(raw string) (string, error) {
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return "", err
		}
		abs := upstream.ResolveReference(ref)
		if abs.Host != upstream.Host {
			return "", errOutside
		}
		path := abs.Path
		if basePath != "" && strings.HasPrefix(path, basePath+"/") {
			path = path[len(basePath):]
		}
		rel, ok := insideItem(path, item)
		if !ok {
			return "", errOutside
		}
		q := cleanQuery(abs.Query(), false)
		if len(q) == 0 {
			return rel, nil
		}
		return rel + "?" + q.Encode(), nil
	}
	var out bytes.Buffer
	out.Grow(len(body) + len(body)/8)
	for _, line := range bytes.SplitAfter(body, []byte("\n")) {
		text := string(bytes.TrimRight(line, "\r\n"))
		end := line[len(text):] // keep the line's own ending
		switch {
		case text == "":
		case strings.HasPrefix(text, "#"):
			var bad error
			text = uriAttr.ReplaceAllStringFunc(text, func(m string) string {
				got, err := fix(uriAttr.FindStringSubmatch(m)[1])
				if err != nil {
					bad = err
					return m
				}
				return `URI="` + got + `"`
			})
			if bad != nil {
				return nil, bad
			}
		default:
			got, err := fix(text)
			if err != nil {
				return nil, err
			}
			text = got
		}
		out.WriteString(text)
		out.Write(end)
	}
	return out.Bytes(), nil
}
