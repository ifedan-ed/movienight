package errreports

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"movienight/server/internal/logging"
)

// A Sentry envelope (https://develop.sentry.dev/sdk/envelopes/) is a JSON
// header line, then items: each a JSON header line and a payload, either
// "length" bytes long or running to the end of its line.

// maxItems bounds the work one envelope can ask for. An SDK sends one event
// per envelope; a few more leave room for what a future SDK might add.
const maxItems = 10

var errBadEnvelope = errors.New("not a Sentry envelope")

// rewriteEnvelope checks an envelope from a browser or a phone and returns
// the one that is forwarded: its header names the configured DSN (never the
// sender's placeholder), only its error events are kept, each scrubbed, and
// every event is filed under environment. events is how many were kept;
// zero means there is nothing to forward.
func rewriteEnvelope(raw []byte, dsn, environment string) (out []byte, events int, err error) {
	headerLine, rest, _ := bytes.Cut(raw, []byte("\n"))
	header := map[string]json.RawMessage{}
	if err := json.Unmarshal(headerLine, &header); err != nil {
		return nil, 0, errBadEnvelope
	}
	// The DSN the sender named is a placeholder (clients never know the
	// real one); "trace" is tracing context, which is off, and it repeats
	// the sender's public key.
	delete(header, "trace")
	header["dsn"], _ = json.Marshal(dsn)

	var buf bytes.Buffer
	head, _ := json.Marshal(header)
	buf.Write(head)
	buf.WriteByte('\n')

	for items := 0; len(rest) > 0; items++ {
		var line []byte
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		if len(bytes.TrimSpace(line)) == 0 {
			continue // blank lines between items (and the final newline)
		}
		if items >= maxItems {
			return nil, 0, errBadEnvelope
		}
		var item struct {
			Type   string `json:"type"`
			Length *int   `json:"length"`
		}
		if err := json.Unmarshal(line, &item); err != nil || item.Type == "" {
			return nil, 0, errBadEnvelope
		}
		var payload []byte
		if item.Length != nil {
			n := *item.Length
			if n < 0 || n > len(rest) {
				return nil, 0, errBadEnvelope
			}
			payload, rest = rest[:n], rest[n:]
			rest = bytes.TrimPrefix(rest, []byte("\n"))
		} else {
			payload, rest, _ = bytes.Cut(rest, []byte("\n"))
		}
		// Only error events go on. Sessions, client reports, attachments
		// and the rest are off in the clients; one that turns up anyway is
		// dropped rather than forwarded unread.
		if item.Type != "event" {
			continue
		}
		clean, err := scrubClientEvent(payload, environment)
		if err != nil {
			return nil, 0, errBadEnvelope
		}
		itemHead, _ := json.Marshal(map[string]any{"type": "event", "length": len(clean)})
		buf.Write(itemHead)
		buf.WriteByte('\n')
		buf.Write(clean)
		buf.WriteByte('\n')
		events++
	}
	return buf.Bytes(), events, nil
}

// scrubClientEvent removes from one event whatever could name a person or
// carry a secret, whatever the client sent: the SDKs are set up to send
// none of it, and this holds even for a sender that is not our client.
func scrubClientEvent(payload []byte, environment string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber() // numbers go back exactly as they came
	var ev map[string]any
	if err := dec.Decode(&ev); err != nil || ev == nil {
		return nil, errBadEnvelope
	}
	delete(ev, "user")
	// server_name is the device's own name on some platforms ("Ann's
	// iPhone").
	delete(ev, "server_name")
	ev["environment"] = environment
	// The browser SDK asks the collector to take the sender's IP address
	// ("infer_ip": "auto"). The sender is this server, so it would only
	// store ours; "never" says so plainly.
	if sdk, ok := ev["sdk"].(map[string]any); ok {
		settings, _ := sdk["settings"].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		settings["infer_ip"] = "never"
		sdk["settings"] = settings
	}

	if req, ok := ev["request"].(map[string]any); ok {
		clean := map[string]any{}
		if u, ok := req["url"].(string); ok {
			clean["url"] = stripQuery(u)
		}
		// The browser and OS come from the User-Agent; every other header
		// (Cookie, Authorization, Referer with its query) stays behind.
		if h, ok := req["headers"].(map[string]any); ok {
			for k, v := range h {
				if strings.EqualFold(k, "User-Agent") {
					clean["headers"] = map[string]any{"User-Agent": v}
				}
			}
		}
		ev["request"] = clean
	}
	if ctxs, ok := ev["contexts"].(map[string]any); ok {
		delete(ctxs, "user")
		if dev, ok := ctxs["device"].(map[string]any); ok {
			delete(dev, "name") // the phone's own name
		}
	}
	if crumbs, ok := ev["breadcrumbs"].(map[string]any); ok {
		ev["breadcrumbs"] = crumbs["values"]
	}
	if list, ok := ev["breadcrumbs"].([]any); ok {
		for _, c := range list {
			if crumb, ok := c.(map[string]any); ok {
				scrubCrumbData(crumb)
			}
		}
	}

	out, err := json.Marshal(ev)
	if err != nil {
		return nil, err
	}
	// Last, the log's own net over every string: query strings, email
	// addresses and tokens anywhere (an error message, a frame's URL).
	return []byte(logging.Redact(string(out))), nil
}

// scrubCrumbData keeps a breadcrumb's facts (method, status, the address
// without its query) and drops anything else it carries, headers and
// bodies included.
func scrubCrumbData(crumb map[string]any) {
	data, ok := crumb["data"].(map[string]any)
	if !ok {
		delete(crumb, "data")
		return
	}
	clean := map[string]any{}
	for _, k := range []string{"method", "status_code", "url", "from", "to"} {
		v, ok := data[k]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok {
			v = stripQuery(s)
		}
		clean[k] = v
	}
	crumb["data"] = clean
}

// stripQuery cuts the query and fragment off an address or a path.
func stripQuery(s string) string {
	if u, err := url.Parse(s); err == nil {
		u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = "", false, "", ""
		u.User = nil
		return u.String()
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}
