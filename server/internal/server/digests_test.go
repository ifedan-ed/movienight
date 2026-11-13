package server_test

// The digest emails' routes (tests/test_digest_send.py and
// test_digest_year.py, their API parts): settings on by default, a preview
// made on request, the email page under its own strict policy, and "Send it
// to me now" through a real SMTP conversation.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"movienight/server/internal/clients"
	"movienight/server/internal/mail/mailtest"
	"movienight/server/internal/testbase"
	"movienight/server/internal/testenv"
)

func digestServer(t *testing.T, smtpMode string) (*testenv.Server, *mailtest.SMTPServer) {
	env := testbase.BaseEnv(t)
	var smtp *mailtest.SMTPServer
	if smtpMode != "" {
		smtp = mailtest.Start(t, smtpMode)
		for k, v := range smtp.Env() {
			env[k] = v
		}
	}
	s := testenv.Start(t, env)
	// No title is looked up here, but nothing may reach the real TMDB.
	tmdb := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(tmdb.Close)
	s.App.Clients.TMDB = clients.NewTMDB(tmdb.Client(), tmdb.URL, "k", "en-US", "US", false)
	return s, smtp
}

type emailSettings struct {
	Weekly, Monthly, Yearly bool
	Address                 *string
}

func TestEmailSettingsAreOnByDefaultAndSwitchOneByOne(t *testing.T) {
	s, _ := digestServer(t, "")
	alice := s.Client("alice", true)
	var got emailSettings
	r := alice.Do(t, "GET", "/api/v1/me/emails", nil)
	if json.Unmarshal([]byte(r.Body), &got); r.StatusCode != 200 || !got.Weekly || !got.Monthly || !got.Yearly ||
		got.Address == nil || *got.Address != "alice@example.org" {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	r = alice.Do(t, "PUT", "/api/v1/me/emails", `{"yearly": false, "address": "  me@home.example  "}`)
	if json.Unmarshal([]byte(r.Body), &got); !got.Weekly || got.Yearly || *got.Address != "me@home.example" {
		t.Fatalf("%s", r.Body)
	}
	// An empty address goes back to the sign-in's.
	r = alice.Do(t, "PUT", "/api/v1/me/emails", `{"address": ""}`)
	if json.Unmarshal([]byte(r.Body), &got); *got.Address != "alice@example.org" || got.Yearly {
		t.Fatalf("%s", r.Body)
	}
	r = alice.Do(t, "PUT", "/api/v1/me/emails", `{"address": "not an address"}`)
	if r.StatusCode != 422 || !strings.Contains(r.Body, "doesn't look like an email address") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestAYearInReviewIsMadeOnRequestAndShownUnderItsOwnPolicy(t *testing.T) {
	s, _ := digestServer(t, "")
	alice := s.Client("alice", true)
	r := alice.Do(t, "POST", "/api/v1/me/digests/year", nil)
	var d struct {
		ID     int
		Kind   string
		SentAt *string `json:"sent_at"`
	}
	if json.Unmarshal([]byte(r.Body), &d); r.StatusCode != 200 || d.Kind != "year" || d.SentAt != nil {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// Made once: asking again gives the same one.
	again := alice.Do(t, "POST", "/api/v1/me/digests/year", nil)
	if !strings.Contains(again.Body, `"id":`+itoa(d.ID)) {
		t.Fatalf("%s", again.Body)
	}
	page := alice.Do(t, "GET", "/api/v1/me/digests/"+itoa(d.ID)+"/html", nil)
	if page.StatusCode != 200 || !strings.Contains(page.Body, "Your year in review") ||
		!strings.HasPrefix(page.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(page.Header.Get("Content-Security-Policy"), "img-src https://image.tmdb.org") {
		t.Fatalf("%d %v", page.StatusCode, page.Header)
	}
	if kind := digestField(t, s, d.ID, "kind"); kind != "year" {
		t.Fatal(kind)
	}
	// Nobody else's.
	bob := s.Client("bob", true)
	if r := bob.Do(t, "GET", "/api/v1/me/digests/"+itoa(d.ID)+"/html", nil); r.StatusCode != 404 {
		t.Fatalf("%d", r.StatusCode)
	}
	if r := bob.Do(t, "POST", "/api/v1/me/digests/"+itoa(d.ID)+"/send", nil); r.StatusCode != 404 {
		t.Fatalf("%d", r.StatusCode)
	}
	if r := alice.Do(t, "POST", "/api/v1/me/digests/fortnight", nil); r.StatusCode != 422 {
		t.Fatalf("%d", r.StatusCode)
	}
}

func makeWeek(t *testing.T, c *testenv.Client) int {
	t.Helper()
	r := c.Do(t, "POST", "/api/v1/me/digests/week", nil)
	var d struct{ ID int }
	if json.Unmarshal([]byte(r.Body), &d); r.StatusCode != 200 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	return d.ID
}

func TestSendItToMeNow(t *testing.T) {
	s, smtp := digestServer(t, "ok")
	alice := s.Client("alice", true)
	id := makeWeek(t, alice)
	r := alice.Do(t, "POST", "/api/v1/me/digests/"+itoa(id)+"/send", nil)
	if r.StatusCode != 200 || !strings.Contains(r.Body, `"sent_to":"alice@example.org"`) {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	got := smtp.Received()
	if len(got) != 1 || !strings.Contains(got[0], "List-Unsubscribe: <http://mn.test/settings/emails?off=week>") {
		t.Fatalf("%v", got)
	}
	if to := digestField(t, s, id, "sent_to"); to != "alice@example.org" {
		t.Fatal(to)
	}
}

func TestSendItToMeNowSaysWhenItMayHaveArrived(t *testing.T) {
	s, smtp := digestServer(t, "drop_after_data")
	alice := s.Client("alice", true)
	id := makeWeek(t, alice)
	r := alice.Do(t, "POST", "/api/v1/me/digests/"+itoa(id)+"/send", nil)
	if r.StatusCode != 502 || !strings.Contains(r.Body, "probably arrived") || len(smtp.Received()) != 1 {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
	// Recorded as sent, so the schedule never sends it again.
	if at := digestField(t, s, id, "sent_at"); at == "" {
		t.Fatal("not recorded as sent")
	}
	smtp.SetMode("refuse_data")
	r = alice.Do(t, "POST", "/api/v1/me/digests/"+itoa(id)+"/send", nil)
	if r.StatusCode != 502 || !strings.Contains(r.Body, "didn't take it") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

func TestSendingWithoutEmailSetUpSaysSo(t *testing.T) {
	s, _ := digestServer(t, "")
	alice := s.Client("alice", true)
	id := makeWeek(t, alice)
	r := alice.Do(t, "POST", "/api/v1/me/digests/"+itoa(id)+"/send", nil)
	if r.StatusCode != 409 || !strings.Contains(r.Body, "Email isn't set up") {
		t.Fatalf("%d %s", r.StatusCode, r.Body)
	}
}

// digestField reads one column of a digest as text ("" for NULL).
func digestField(t *testing.T, s *testenv.Server, id any, column string) string {
	t.Helper()
	var v *string
	if err := s.Pool.QueryRow(context.Background(), "SELECT "+column+"::text FROM digests WHERE id = $1", id).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return ""
	}
	return *v
}
