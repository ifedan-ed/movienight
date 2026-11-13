// Package clients talks to the services outside Movie Night: TMDB, Jellyfin,
// Seerr, MDBList, Meilisearch and the AI model (through LiteLLM only). Ported
// from app/clients/.
//
// What every client shares:
//
//   - One pooled HTTP client for all upstream calls (Upstream), with a 10 s
//     connect timeout and at most 40 connections per host, and every call
//     bounded by its own deadline (DefaultTimeout, 25 s, unless the call needs
//     longer: Jellyfin's library pages 120 s, the AI 180 s, Meilisearch
//     uploads 120 s; docs/limits.md). The Python shared one httpx client the
//     same way.
//   - A separate pool for the Jellyfin image proxy (Images), so a page of
//     posters loading cannot use up the connections API calls need.
//   - Bodies are read up to a cap (maxBody), so a broken upstream cannot
//     make one call hold unbounded memory.
//   - Errors are returned, never swallowed into an empty answer. A "not
//     found" (404) is a nil result with no error where the Python did the
//     same on purpose; any other failure is an error the caller must handle.
//     The deliberate degrades are kept and commented where they are.
//   - State that replicas must share (MDBList's circuit breaker and its
//     "refused" notice) lives in Valkey (Breaker), not in process memory.
package clients

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// DefaultTimeout bounds an upstream call that names no other.
const DefaultTimeout = 25 * time.Second

// maxBody caps any upstream answer read into memory. The largest expected
// ones are Jellyfin's library pages (500 items with overviews and people)
// and Meilisearch's document fetches, a few MB each.
const maxBody = 64 << 20

// maxImage caps one proxied Jellyfin image.
const maxImage = 15 << 20

// Upstream is the shared client for API calls to outside services.
func Upstream() *http.Client {
	return &http.Client{Transport: transport(40, 20)}
}

// Images is the separate client for the Jellyfin image proxy.
func Images() *http.Client {
	return &http.Client{Transport: transport(16, 16)}
}

func transport(perHost, idle int) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		MaxConnsPerHost:       perHost,
		MaxIdleConns:          idle * 4,
		MaxIdleConnsPerHost:   idle,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
}

// JSON is an upstream answer as decoded JSON. The services' own shapes are
// kept as they are (TMDB's snake_case, Jellyfin's PascalCase); the services
// layer turns them into Movie Night's types, as app/services/ did.
type JSON = map[string]any

// response is one finished upstream call.
type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// do runs one request within timeout and reads its body (capped). A
// transport failure or a timeout is an error; any HTTP status is a response.
func do(ctx context.Context, c *http.Client, req *http.Request, timeout time.Duration, limit int64) (*response, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("the answer was larger than %d bytes", limit)
	}
	return &response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// decode reads a JSON body into out.
func decode(body []byte, out any) error {
	if len(body) == 0 {
		return errors.New("empty answer")
	}
	return json.Unmarshal(body, out)
}

// clip shortens an upstream's error text for a message.
func clip(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

// list reads a JSON array of objects from v (TMDB's "results" and the like),
// skipping anything that is not an object.
func list(v any) []JSON {
	arr, _ := v.([]any)
	out := make([]JSON, 0, len(arr))
	for _, x := range arr {
		if m, ok := x.(JSON); ok {
			out = append(out, m)
		}
	}
	return out
}

// str and num read loosely-typed JSON fields.
func str(m JSON, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m JSON, k string) (float64, bool) {
	f, ok := m[k].(float64)
	return f, ok
}
