package errreports

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/valkey-io/valkey-go"

	"movienight/server/internal/apierr"
	"movienight/server/internal/httpx"
	"movienight/server/internal/ratelimit"
)

// The relay's limits. Constants, not settings: they are a flood guard on a
// door anyone can knock on, sized so a real crash loop is still seen.
const (
	// MaxEnvelopeBytes: an error event with its stack is a few KB; a deep
	// stack with breadcrumbs stays well under this.
	MaxEnvelopeBytes = 200 << 10
	// Per caller address (an IPv6 /64 counts as one): a page or an app
	// stuck failing sends a handful of distinct errors a minute, and the
	// SDKs drop repeats, so 20 in 10 minutes says all there is to say.
	perAddressLimit  = 20
	perAddressPeriod = 10 * time.Minute
	// Per endpoint, everyone together: what the collector is asked to take
	// in an hour however many addresses send. Above it, events are dropped.
	overallLimit  = 300
	overallPeriod = time.Hour
	// forwardTimeout: the collector answers in milliseconds; one that does
	// not within this loses the event (no retries).
	forwardTimeout = 5 * time.Second
)

// Kinds of relay, one per client.
const (
	Web = "web"
	App = "app"
)

// RelayOptions sets up one relay.
type RelayOptions struct {
	// Kind is Web or App: it names the rate-limit keys and decides the check.
	Kind string
	// DSN is where accepted envelopes go; empty turns the relay off (404).
	DSN string
	// Origin is the website's own origin (scheme://host[:port]). The Web
	// relay accepts only requests whose Origin header is exactly this.
	Origin      string
	Environment string
	Callers     *httpx.Callers
	KV          valkey.Client
	Log         *slog.Logger
}

// Relay is one tunnel endpoint. A Relay with no DSN answers 404.
type Relay struct {
	kind, origin, environment string
	dsn                       string
	envelopeURL, publicKey    string
	perAddress, overall       *ratelimit.Window
	callers                   *httpx.Callers
	client                    *http.Client
	log                       *slog.Logger
	inflight                  sync.WaitGroup
}

// NewRelay builds a relay; an unusable DSN is an error (config has already
// checked it with the same parser).
func NewRelay(o RelayOptions) (*Relay, error) {
	r := &Relay{kind: o.Kind, origin: o.Origin, environment: o.Environment, callers: o.Callers, log: o.Log,
		client: &http.Client{Timeout: forwardTimeout}}
	if o.DSN == "" {
		return r, nil
	}
	d, err := sentry.NewDsn(o.DSN)
	if err != nil {
		return nil, fmt.Errorf("the %s error-reports DSN is not a DSN", o.Kind)
	}
	r.dsn, r.envelopeURL, r.publicKey = o.DSN, d.GetAPIURL().String(), d.GetPublicKey()
	r.perAddress = ratelimit.New(o.KV, "errors-"+o.Kind+"-addr", perAddressLimit, perAddressPeriod)
	r.overall = ratelimit.New(o.KV, "errors-"+o.Kind+"-all", overallLimit, overallPeriod)
	return r, nil
}

// Enabled reports whether this relay forwards anything.
func (r *Relay) Enabled() bool { return r != nil && r.dsn != "" }

// Wait blocks until the forwards under way have finished (each is bounded
// by forwardTimeout): for shutdown and for tests.
func (r *Relay) Wait() {
	if r != nil {
		r.inflight.Wait()
	}
}

// ServeHTTP takes one envelope. The checks run cheapest first, and the
// body is read only once the sender has passed the limits. Nothing here
// touches the database, and the forward happens after the answer.
func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.Enabled() {
		apierr.Write(w, apierr.NotFound())
		return
	}
	switch r.kind {
	case Web:
		// A browser always sends Origin on a POST; only this site's own
		// pages may report. Not proof of a browser (anyone can set the
		// header), which is what the limits are for.
		if req.Header.Get("Origin") != r.origin {
			apierr.Write(w, apierr.New(http.StatusForbidden, "Reports are taken from this site's own pages only."))
			return
		}
	case App:
		// Phones send no Origin; the app sends this header on every call
		// (httpx requires it of every /api write too).
		if req.Header.Get(httpx.CSRFHeader) != httpx.CSRFValue {
			apierr.Write(w, apierr.New(http.StatusForbidden, "Missing X-Requested-With: movienight header"))
			return
		}
	}
	if req.ContentLength > MaxEnvelopeBytes {
		r.tooLarge(w)
		return
	}
	ctx := req.Context()
	if ok, retry := r.allowed(ctx, req); !ok {
		if retry > 0 {
			// The SDKs pause sending for this long instead of retrying.
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
			apierr.Write(w, apierr.New(http.StatusTooManyRequests, "Too many error reports; this one was dropped."))
		} else {
			apierr.Write(w, apierr.New(http.StatusServiceUnavailable, "Error reports can't be taken just now."))
		}
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, MaxEnvelopeBytes+1))
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, "The report could not be read."))
		return
	}
	if len(body) > MaxEnvelopeBytes {
		r.tooLarge(w)
		return
	}
	out, events, err := rewriteEnvelope(body, r.dsn, r.environment)
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, "Not a Sentry envelope."))
		return
	}
	if events > 0 {
		r.inflight.Add(1)
		go r.forward(out)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

func (r *Relay) tooLarge(w http.ResponseWriter) {
	apierr.Write(w, apierr.New(http.StatusRequestEntityTooLarge, fmt.Sprintf("The report is too large: at most %d bytes.", MaxEnvelopeBytes)))
}

// allowed takes one event from both windows. A refusal returns how long
// the sender should wait; a Valkey failure returns 0 and refuses: without
// its counts the relay cannot keep the collector safe, so it fails closed.
func (r *Relay) allowed(ctx context.Context, req *http.Request) (bool, time.Duration) {
	ok, err := r.perAddress.Take(ctx, r.callers.Bucket(req))
	if err == nil && !ok {
		return false, perAddressPeriod
	}
	if err == nil {
		ok, err = r.overall.Take(ctx, "all")
		if err == nil && !ok {
			return false, overallPeriod
		}
	}
	if err != nil {
		r.log.WarnContext(ctx, "error reports: the rate limit could not be checked", "kind", r.kind, "err", err)
		return false, 0
	}
	return true, 0
}

// forward sends one envelope to the collector, once, on its own context:
// the client has had its answer already. A failure is logged at WARN, not
// ERROR, so a collector that is down never reports itself, and without the
// envelope, which is the client's data.
func (r *Relay) forward(envelope []byte) {
	defer r.inflight.Done()
	ctx, cancel := context.WithTimeout(context.Background(), forwardTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.envelopeURL, bytes.NewReader(envelope))
	if err != nil {
		r.log.Warn("error reports: forward failed", "kind", r.kind, "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-sentry-envelope")
	req.Header.Set("X-Sentry-Auth", "Sentry sentry_version=7, sentry_client=movienight-relay/1, sentry_key="+r.publicKey)
	resp, err := r.client.Do(req)
	if err != nil {
		// The transport's error quotes the URL, which is the collector's
		// address and project only (the key travels in the header).
		r.log.Warn("error reports: forward failed", "kind", r.kind, "err", redactURL(err))
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		r.log.Warn("error reports: the collector refused a report", "kind", r.kind, "status", resp.StatusCode)
	}
}

// redactURL keeps a *url.Error's operation and cause but not its URL's
// query (there is none today; this keeps it that way in the log).
func redactURL(err error) error {
	if ue, ok := err.(*url.Error); ok {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// Relays is the pair the API serves.
type Relays struct{ Web, App *Relay }

// Wait waits for both relays' forwards under way.
func (rs *Relays) Wait() {
	if rs != nil {
		rs.Web.Wait()
		rs.App.Wait()
	}
}
