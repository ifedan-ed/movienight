// Package mediaclient is the API's and the worker's client of the media
// service: scans (a stream of events), stopping a play's ffmpeg, and the
// service's status. The stream proxy (package stream) forwards the
// player's own requests with Target.
package mediaclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"movienight/server/internal/media/protocol"
	"movienight/server/internal/media/scan"
)

// Client calls one media service.
type Client struct {
	base   *url.URL
	secret string
	http   *http.Client // calls with an answer (stop, status)
	stream *http.Client // scans: no overall timeout, they take as long as the library
}

// New makes the client for base (MEDIA_URL) and secret (MEDIA_SECRET).
func New(base, secret string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || u.Host == "" {
		return nil, errors.New("MEDIA_URL is not an address")
	}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{base: u, secret: secret, http: &http.Client{Transport: tr, Timeout: 15 * time.Second},
		stream: &http.Client{Transport: tr}}, nil
}

// Target is the media service's address for path with query, for the
// stream proxy.
func (c *Client) Target(path string, q url.Values) *url.URL {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	return &u
}

// Authorize adds the shared secret to a request for the media service.
func (c *Client) Authorize(r *http.Request) { protocol.Authorize(r, c.secret) }

// Error is the media service failing or refusing.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return "the media service didn't answer (" + e.Msg + ")"
	}
	return fmt.Sprintf("the media service answered HTTP %d: %s", e.Status, e.Msg)
}

// Scan runs a scan and calls each for every event as it arrives. It
// returns each's first error, or the stream's: a scan that ends without
// its "done" line is an error (the service went away mid-scan), so the
// caller never takes a cut-short scan for a complete one.
func (c *Client) Scan(ctx context.Context, req scan.Request, each func(scan.Event) error) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Target(protocol.Scan, nil).String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	c.Authorize(r)
	resp, err := c.stream.Do(r)
	if err != nil {
		return &Error{Msg: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return answerError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	// One event carries one file's probe: a few KB, rarely more.
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	done := false
	for sc.Scan() {
		var e scan.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return fmt.Errorf("the media service sent a line that isn't JSON: %w", err)
		}
		if err := each(e); err != nil {
			return err
		}
		if e.Type == scan.EventDone {
			done = true
		}
	}
	if err := sc.Err(); err != nil {
		return &Error{Msg: "the scan was cut short: " + err.Error()}
	}
	if !done {
		return &Error{Msg: "the scan was cut short"}
	}
	return nil
}

// Stop ends a play's ffmpeg jobs.
func (c *Client) Stop(ctx context.Context, play string) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Target(protocol.Stop, url.Values{protocol.KeyPlay: {play}}).String(), nil)
	if err != nil {
		return err
	}
	c.Authorize(r)
	resp, err := c.http.Do(r)
	if err != nil {
		return &Error{Msg: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return answerError(resp)
	}
	return nil
}

// Status is the media service's state (engine.Status).
type Status struct {
	Jobs          int    `json:"jobs"`
	Transcodes    int    `json:"transcodes"`
	MaxTranscodes int    `json:"max_transcodes"`
	HWAccel       string `json:"hwaccel"`
	// Folders are the media service's MEDIA_LIBRARIES.
	Folders []string `json:"folders"`
}

// Status asks the media service how it is.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Target(protocol.Status, nil).String(), nil)
	if err != nil {
		return s, err
	}
	c.Authorize(r)
	resp, err := c.http.Do(r)
	if err != nil {
		return s, &Error{Msg: err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return s, answerError(resp)
	}
	return s, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&s)
}

func answerError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
	msg := strings.TrimSpace(string(b))
	if resp.StatusCode == http.StatusUnauthorized {
		msg = "MEDIA_SECRET differs between the API and the media service"
	}
	return &Error{Status: resp.StatusCode, Msg: msg}
}
