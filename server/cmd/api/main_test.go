package main

// -healthcheck: 0 only for a 200 from /healthz.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	gone.Close()
	for url, want := range map[string]int{ok.URL: 0, bad.URL: 1, gone.URL: 1} {
		if got := healthcheck(url); got != want {
			t.Errorf("%s: %d, want %d", url, got, want)
		}
	}
}
