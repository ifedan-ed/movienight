package stream

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testMAC(key string) func([]byte) []byte {
	return func(b []byte) []byte {
		m := hmac.New(sha256.New, []byte(key))
		m.Write(b)
		return m.Sum(nil)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func claims() Claims {
	return Claims{Member: 7, Item: strings.Repeat("a", 32), Source: "src1", PlaySession: "ps1", Device: "movienight-7",
		Play: "play1", Runtime: 6000, Credits: 5700}
}

func TestTicketRoundTrip(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := NewSigner(testMAC("k"), c.now)
	tk := s.Sign(claims())
	if strings.ContainsAny(tk, "/?#+= ") {
		t.Fatalf("ticket is not path-safe: %q", tk)
	}
	got, err := s.Verify(tk)
	if err != nil {
		t.Fatal(err)
	}
	want := claims()
	want.Expires = c.t.Add(TicketLife).Unix()
	if got != want {
		t.Fatalf("claims %+v, want %+v", got, want)
	}
}

func TestTicketExpiry(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s := NewSigner(testMAC("k"), c.now)
	tk := s.Sign(claims())
	c.t = c.t.Add(TicketLife - time.Second)
	if _, err := s.Verify(tk); err != nil {
		t.Fatalf("a second before expiry: %v", err)
	}
	c.t = c.t.Add(time.Second)
	if _, err := s.Verify(tk); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expiry: %v, want ErrExpired", err)
	}
	// The person's own reports still name their play for a while...
	if _, err := s.Owned(tk, 7); err != nil {
		t.Fatalf("Owned within the grace: %v", err)
	}
	// ...but not for ever.
	c.t = c.t.Add(ReportGrace)
	if _, err := s.Owned(tk, 7); !errors.Is(err, ErrExpired) {
		t.Fatalf("Owned after the grace: %v, want ErrExpired", err)
	}
}

func TestTicketTamper(t *testing.T) {
	s := NewSigner(testMAC("k"), nil)
	tk := s.Sign(claims())
	payload, sig, _ := strings.Cut(tk, ".")

	// Another item in the claims, the old signature.
	raw, _ := b64.DecodeString(payload)
	var c Claims
	_ = json.Unmarshal(raw, &c)
	c.Item = strings.Repeat("b", 32)
	forged, _ := json.Marshal(c)
	cases := map[string]string{
		"changed claims":  b64.EncodeToString(forged) + "." + sig,
		"flipped sig":     payload + "." + flip(sig),
		"no signature":    payload + ".",
		"no dot":          payload,
		"empty":           "",
		"other key":       NewSigner(testMAC("other"), nil).Sign(claims()),
		"garbage payload": "!!!." + sig,
	}
	for name, bad := range cases {
		if _, err := s.Verify(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func flip(s string) string {
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}

func TestTicketWrongMember(t *testing.T) {
	s := NewSigner(testMAC("k"), nil)
	tk := s.Sign(claims())
	if _, err := s.Owned(tk, 8); !errors.Is(err, ErrMember) {
		t.Fatalf("someone else's ticket: %v, want ErrMember", err)
	}
	if _, err := s.Owned(tk, 7); err != nil {
		t.Fatalf("own ticket: %v", err)
	}
}

func TestRewritePlaylist(t *testing.T) {
	item := strings.Repeat("a", 32)
	up, _ := url.Parse("http://jf:8096/videos/" + item + "/master.m3u8?ApiKey=SECRET&PlaySessionId=ps1")
	in := "#EXTM3U\r\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1000,CODECS=\"avc1.640028,mp4a.40.2\"\r\n" +
		"main.m3u8?DeviceId=d&MediaSourceId=src1&api_key=SECRET&AudioStreamIndex=2\r\n" +
		"#EXT-X-MAP:URI=\"hls1/main/-1.mp4?ApiKey=SECRET&runtimeTicks=0\"\n" +
		"hls1/main/0.ts?ApiKey=SECRET&runtimeTicks=0\n" +
		"/Videos/" + strings.ToUpper(item[:8]) + item[8:] + "/hls1/main/1.ts?api_key=SECRET\n" +
		"#EXT-X-ENDLIST\n"
	out, err := rewritePlaylist([]byte(in), up, "", item)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if strings.Contains(got, "SECRET") || strings.Contains(strings.ToLower(got), "api_key") || strings.Contains(strings.ToLower(got), "apikey") {
		t.Fatalf("a key is left in the playlist:\n%s", got)
	}
	for _, want := range []string{
		"main.m3u8?AudioStreamIndex=2&DeviceId=d&MediaSourceId=src1\r\n",
		`#EXT-X-MAP:URI="hls1/main/-1.mp4?runtimeTicks=0"` + "\n",
		"hls1/main/0.ts?runtimeTicks=0\n",
		"hls1/main/1.ts\n",
		"#EXT-X-STREAM-INF:BANDWIDTH=1000,CODECS=\"avc1.640028,mp4a.40.2\"\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// Behind a sub-path (JELLYFIN_BASE_URL http://jf/jellyfin).
	sub, _ := url.Parse("http://jf:8096/jellyfin/videos/" + item + "/master.m3u8")
	if out, err := rewritePlaylist([]byte("main.m3u8?ApiKey=x\n"), sub, "/jellyfin", item); err != nil || string(out) != "main.m3u8\n" {
		t.Fatalf("sub-path: %q %v", out, err)
	}
	for name, bad := range map[string]string{
		"another item":   "../" + strings.Repeat("b", 32) + "/main.m3u8\n",
		"another host":   "http://evil.example/videos/" + item + "/main.m3u8\n",
		"another path":   "/Items/" + item + "/Download\n",
		"in a tag":       "#EXT-X-MAP:URI=\"/System/Info\"\n",
		"absolute other": "http://jf:8096/Users\n",
	} {
		if _, err := rewritePlaylist([]byte("#EXTM3U\n"+bad), up, "", item); err == nil {
			t.Errorf("%s: rewritten, want refused", name)
		}
	}
}

// fakeJellyfin serves one file with Range support and a playlist, and
// records what it was asked.
type fakeJellyfin struct {
	*httptest.Server
	last *http.Request
}

func newFake(t *testing.T, item string, file []byte) *fakeJellyfin {
	f := &fakeJellyfin{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.last = r.Clone(r.Context())
		switch {
		case r.URL.Path == "/Videos/"+item+"/stream.mp4":
			http.ServeContent(w, r, "f.mp4", time.Unix(0, 0), strings.NewReader(string(file)))
		case r.URL.Path == "/videos/"+item+"/main.m3u8":
			w.Header().Set("Content-Type", "application/x-mpegURL")
			_, _ = io.WriteString(w, "#EXTM3U\nhls1/main/0.ts?ApiKey=SECRET&x=1\n")
		case r.URL.Path == "/videos/"+item+"/hls1/main/0.ts":
			_, _ = io.WriteString(w, "SEGMENT")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func proxyFor(t *testing.T, f *fakeJellyfin) (*Proxy, *Signer, *httptest.Server) {
	s := NewSigner(testMAC("k"), nil)
	p, err := NewProxy(s, NewClient(), f.URL, "SERVERKEY", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	p.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return p, s, srv
}

func get(t *testing.T, u string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestProxyRange(t *testing.T) {
	item := strings.Repeat("a", 32)
	file := []byte("0123456789abcdefghij")
	f := newFake(t, item, file)
	_, s, srv := proxyFor(t, f)
	tk := s.Sign(claims())

	resp, body := get(t, srv.URL+FileURL(tk, "mp4"), "Range", "bytes=5-9")
	if resp.StatusCode != http.StatusPartialContent || body != "56789" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}
	if cr := resp.Header.Get("Content-Range"); cr != "bytes 5-9/20" {
		t.Fatalf("Content-Range %q", cr)
	}
	if resp.Header.Get("Content-Length") != "5" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("headers %v", resp.Header)
	}
	// The install's key goes upstream in the header, the ticket pins the
	// source and the play session, and the browser's query is ignored.
	if a := f.last.Header.Get("Authorization"); !strings.Contains(a, `Token="SERVERKEY"`) || !strings.Contains(a, `DeviceId="movienight-7"`) {
		t.Fatalf("upstream Authorization %q", a)
	}
	q := f.last.URL.Query()
	if q.Get("static") != "true" || q.Get("MediaSourceId") != "src1" || q.Get("PlaySessionId") != "ps1" {
		t.Fatalf("upstream query %v", q)
	}

	resp, body = get(t, srv.URL+FileURL(tk, "mp4"))
	if resp.StatusCode != http.StatusOK || body != string(file) {
		t.Fatalf("whole file: %d %q", resp.StatusCode, body)
	}
}

func TestProxyPlaylistAndSegments(t *testing.T) {
	item := strings.Repeat("a", 32)
	f := newFake(t, item, nil)
	_, s, srv := proxyFor(t, f)
	tk := s.Sign(claims())

	resp, body := get(t, srv.URL+Prefix+tk+"/main.m3u8?MediaSourceId=other&api_key=mine&AudioStreamIndex=2")
	if resp.StatusCode != http.StatusOK || body != "#EXTM3U\nhls1/main/0.ts?x=1\n" {
		t.Fatalf("playlist: %d %q", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Fatalf("Content-Type %q", ct)
	}
	q := f.last.URL.Query()
	if q.Get("MediaSourceId") != "src1" || q.Get("api_key") != "" || q.Get("AudioStreamIndex") != "2" {
		t.Fatalf("the browser's query was not cleaned and pinned: %v", q)
	}
	resp, body = get(t, srv.URL+Prefix+tk+"/hls1/main/0.ts?x=1")
	if resp.StatusCode != http.StatusOK || body != "SEGMENT" {
		t.Fatalf("segment: %d %q", resp.StatusCode, body)
	}
}

func TestProxyRefuses(t *testing.T) {
	item := strings.Repeat("a", 32)
	f := newFake(t, item, []byte("x"))
	_, s, srv := proxyFor(t, f)
	tk := s.Sign(claims())
	for name, path := range map[string]string{
		"bad ticket":     Prefix + "nope.nope/stream.mp4",
		"other path":     Prefix + tk + "/Items",
		"deeper path":    Prefix + tk + "/hls1/main/x/0.ts",
		"subtitle shape": Prefix + tk + "/subtitles/abc.vtt",
	} {
		resp, _ := get(t, srv.URL+path)
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
		if name == "bad ticket" && resp.StatusCode != http.StatusForbidden {
			t.Errorf("bad ticket: %d, want 403", resp.StatusCode)
		}
	}
}

func TestHLSURL(t *testing.T) {
	item := strings.Repeat("a", 32)
	got, err := HLSURL("T", "/videos/"+item+"/master.m3u8?DeviceId=d&MediaSourceId=m&PlaySessionId=p&ApiKey=SECRET&AudioStreamIndex=2", item)
	if err != nil {
		t.Fatal(err)
	}
	if got != Prefix+"T/master.m3u8?AudioStreamIndex=2" {
		t.Fatalf("HLSURL %q", got)
	}
	if _, err := HLSURL("T", "/Items/"+item+"/PlaybackInfo", item); err == nil {
		t.Fatal("a non-playlist address was accepted")
	}
}
