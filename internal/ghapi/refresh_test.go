package ghapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/chrisle/action-runner-cluster/internal/config"
)

// fakeRefresher stands in for the vault-backed credential.
type fakeRefresher struct {
	mu       sync.Mutex
	token    string
	next     string // token the "vault" hands back; empty means no replacement
	err      error
	refreshN int
}

func (f *fakeRefresher) Token(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, nil
}

func (f *fakeRefresher) Describe() string { return "fake" }

func (f *fakeRefresher) Refresh(_ context.Context, stale string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshN++
	if f.err != nil {
		return false, f.err
	}
	if f.token != stale {
		return true, nil
	}
	if f.next == "" || f.next == stale {
		return false, nil
	}
	f.token = f.next
	return true, nil
}

func refresherClient(t *testing.T, auth Authenticator, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := &config.Config{}
	cfg.GitHub.Owner = "chrisle"
	cfg.GitHub.Token = "placeholder"
	cfg.GitHub.APIURL = srv.URL
	cfg.GitHub.WebURL = srv.URL
	c, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), WithAuthenticator(auth))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A PAT that expires while arc is running used to be terminal: the client held
// the dead token and every request 401'd until the service was restarted.
func TestRequestRetriesAfterCredentialRefresh(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	auth := &fakeRefresher{token: "expired", next: "new"}
	c := refresherClient(t, auth, handler)

	data, _, err := c.request(context.Background(), http.MethodGet, "/user", nil, "")
	if err != nil {
		t.Fatalf("request failed after refresh: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Errorf("body = %s, want the success payload", data)
	}
	if auth.refreshN != 1 {
		t.Errorf("refreshed %d times, want exactly 1", auth.refreshN)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"Bearer expired", "Bearer new"}
	if len(seen) != len(want) {
		t.Fatalf("authorization headers = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("header %d = %q, want %q", i, seen[i], want[i])
		}
	}
}

// When the vault still holds the rejected token there is nothing to retry with,
// so the 401 must surface instead of burning every attempt.
func TestRequestGivesUpWhenRefreshFindsNothingNew(t *testing.T) {
	var calls int
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	})

	auth := &fakeRefresher{token: "expired"} // vault has no replacement
	c := refresherClient(t, auth, handler)

	_, _, err := c.request(context.Background(), http.MethodGet, "/user", nil, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 APIError", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("made %d requests, want 1 — a refresh with no new token must not retry", calls)
	}
}

// A failing vault read must not mask the original rejection.
func TestRequestSurfacesAuthErrorWhenRefreshFails(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	})

	auth := &fakeRefresher{token: "expired", err: errors.New("op unavailable")}
	c := refresherClient(t, auth, handler)

	_, _, err := c.request(context.Background(), http.MethodGet, "/user", nil, "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want the underlying 401 APIError", err)
	}
}

// A plain StaticToken has no source to re-read; the 401 must pass straight
// through rather than looping.
func TestRequestDoesNotRetryStaticToken(t *testing.T) {
	var calls int
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
	})

	c := refresherClient(t, &StaticToken{Value: "dead"}, handler)
	if _, _, err := c.request(context.Background(), http.MethodGet, "/user", nil, ""); err == nil {
		t.Fatal("want an error")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("made %d requests, want 1", calls)
	}
}
