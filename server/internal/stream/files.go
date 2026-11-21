package stream

// Small reads of a ticket's file for the server itself, not the browser:
// the online subtitle search (internal/subtitles) needs the file's name
// and OpenSubtitles' moviehash, which is two 64 KB reads. They go to the
// same address the proxy serves the file from (target, or libraryTarget
// for a library file), with the install's key or the media service's
// secret, so whatever file a ticket can play can be hashed, and nothing is
// fetched whole.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"movienight/server/internal/clients"
)

// maxRead caps one ReadRange.
const maxRead = 1 << 20

// ReadRange reads n bytes of the ticket's file from off, with a Range
// request, and returns them with the whole file's size (from
// Content-Range).
func (p *Proxy) ReadRange(ctx context.Context, c Claims, off int64, n int) ([]byte, int64, error) {
	if n <= 0 || n > maxRead || off < 0 {
		return nil, 0, errors.New("stream: bad range")
	}
	var target *url.URL
	switch {
	case c.Library() && p.media != nil:
		target, _, _ = p.libraryTarget(c, "stream", nil)
	case !c.Library() && p.base != nil:
		target, _, _ = p.target(c, "stream", nil)
	default:
		return nil, 0, errors.New("stream: the ticket's source is not set up")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	if c.Library() {
		p.media.Authorize(req)
	} else {
		req.Header.Set("Authorization", clients.AuthHeader(p.apiKey, c.Device, "Movie Night"))
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(n)-1))
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		// A 200 would be the whole file: never read it.
		return nil, 0, fmt.Errorf("stream: a range read got HTTP %d", resp.StatusCode)
	}
	_, total, ok := strings.Cut(resp.Header.Get("Content-Range"), "/")
	size, err := strconv.ParseInt(total, 10, 64)
	if !ok || err != nil || size <= 0 {
		return nil, 0, errors.New("stream: a range read named no file size")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(n)))
	return b, size, err
}

// FileName is the name of the ticket's file without its folders
// ("Film.2010.1080p.BluRay.x264-GROUP.mkv"): a library ticket carries its
// path; a Jellyfin one is read from Jellyfin's item.
func (p *Proxy) FileName(ctx context.Context, c Claims) (string, error) {
	if c.Library() {
		return path.Base(c.File), nil
	}
	if p.base == nil {
		return "", errors.New("stream: Jellyfin is not set up")
	}
	u := *p.base
	u.Path += "/Items"
	u.RawQuery = url.Values{"Ids": {c.Item}, "Fields": {"MediaSources,Path"}, "EnableImages": {"false"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", clients.AuthHeader(p.apiKey, c.Device, "Movie Night"))
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("stream: Jellyfin's item answered HTTP %d", resp.StatusCode)
	}
	var ans struct {
		Items []struct {
			Path         string `json:"Path"`
			MediaSources []struct {
				ID   string `json:"Id"`
				Path string `json:"Path"`
			} `json:"MediaSources"`
		} `json:"Items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&ans); err != nil || len(ans.Items) == 0 {
		return "", errors.New("stream: Jellyfin's item couldn't be read")
	}
	it := ans.Items[0]
	name := it.Path
	for _, s := range it.MediaSources {
		if strings.EqualFold(s.ID, c.Source) && s.Path != "" {
			name = s.Path
		}
	}
	if name == "" {
		return "", nil
	}
	return path.Base(strings.ReplaceAll(name, "\\", "/")), nil
}
