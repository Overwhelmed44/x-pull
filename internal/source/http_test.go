package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/x-pull/x-pull/internal/config"
)

func TestFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Accept") != "application/json" {
			t.Error("unexpected request")
		}
		fmt.Fprint(w, `{"users":[{"email":"a","password":"secret"}]}`)
	}))
	defer server.Close()
	c := New(time.Second, 1024)
	defer c.Close()
	users, err := c.Fetch(context.Background(), config.Source{URL: server.URL, Headers: map[string]string{"Authorization": "Bearer secret"}})
	if err != nil || len(users) != 1 || users[0].Email != "a" {
		t.Fatalf("fetch: %v", err)
	}
}

func TestRefusesIncompleteOrInvalidSnapshots(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"users":null}`, `{"users":{}}`, `{"users":[],"unknown":1}`, `{"users":[{"email":"a","extra":"secret"}]}`, `{"users":[]} {}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer s.Close()
			c := New(time.Second, 1024)
			defer c.Close()
			_, err := c.Fetch(context.Background(), config.Source{URL: s.URL})
			if err == nil {
				t.Fatal("accepted invalid snapshot")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("leaked body")
			}
		})
	}
}

func TestLimitsStatusRedirectAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		max     int64
		timeout time.Duration
	}{
		{"oversized", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"users":[]}`) }, 11, time.Second},
		{"server failure", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret", 500) }, 1024, time.Second},
		{"redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/secret", 302) }, 1024, time.Second},
		{"timeout", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, 1024, 20 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewServer(test.handler)
			defer s.Close()
			c := New(test.timeout, test.max)
			defer c.Close()
			if _, err := c.Fetch(context.Background(), config.Source{URL: s.URL}); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
	c := New(time.Second, 12)
	defer c.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"users":[]}`) }))
	defer s.Close()
	if _, err := c.Fetch(context.Background(), config.Source{URL: s.URL}); err != nil {
		t.Fatal("exact size rejected", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Fetch(ctx, config.Source{URL: s.URL + "?token=secret"}); err != context.Canceled {
		t.Fatal("cancellation not propagated")
	}
}
