package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chrisle/action-runner-cluster/internal/opconfig"
)

// newTestVaultAuth builds a vaultAuth whose vault reads and clock are scripted,
// with the credentials cache redirected into the test's temp dir.
func newTestVaultAuth(t *testing.T, current string, load func(context.Context) (*opconfig.Item, error)) *vaultAuth {
	t.Helper()
	dir := t.TempDir()
	orig := opconfig.CachePath
	opconfig.CachePath = func() string { return filepath.Join(dir, "credentials.json") }
	t.Cleanup(func() { opconfig.CachePath = orig })

	return &vaultAuth{
		item: &opconfig.Item{Token: current, Login: "chrisle", AccountType: "User"},
		load: load,
		now:  time.Now,
	}
}

func TestVaultAuthRefreshPicksUpRotatedToken(t *testing.T) {
	var reads int
	v := newTestVaultAuth(t, "expired", func(context.Context) (*opconfig.Item, error) {
		reads++
		return &opconfig.Item{Token: "rotated", Login: "chrisle"}, nil
	})

	changed, err := v.Refresh(context.Background(), "expired")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !changed {
		t.Error("changed = false, want true")
	}
	if tok, _ := v.Token(context.Background()); tok != "rotated" {
		t.Errorf("token = %q, want rotated", tok)
	}
	if reads != 1 {
		t.Errorf("vault reads = %d, want 1", reads)
	}

	// The login is not part of a rotation, so the account type carries over
	// rather than costing another GitHub probe on the next start.
	if v.item.AccountType != "User" {
		t.Errorf("account type = %q, want it preserved as User", v.item.AccountType)
	}

	// The working token must reach the cache, or a restart would rediscover
	// exactly the failure that triggered this refresh.
	cached := opconfig.ReadCache()
	if cached == nil || cached.Token != "rotated" {
		t.Errorf("cache = %+v, want the rotated token persisted", cached)
	}
}

// The reconcile loop retries often. When the vault still holds the dead token,
// the cooldown keeps a rejection storm from becoming an `op` storm.
func TestVaultAuthThrottlesRepeatedFailedRefreshes(t *testing.T) {
	var reads int
	v := newTestVaultAuth(t, "expired", func(context.Context) (*opconfig.Item, error) {
		reads++
		return &opconfig.Item{Token: "expired", Login: "chrisle"}, nil
	})
	now := time.Now()
	v.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		changed, err := v.Refresh(context.Background(), "expired")
		if err != nil || changed {
			t.Fatalf("attempt %d: changed=%v err=%v, want false/nil", i, changed, err)
		}
	}
	if reads != 1 {
		t.Errorf("vault reads = %d, want 1 — the cooldown should suppress the rest", reads)
	}

	// Past the cooldown it tries again, and picks up a token rotated meanwhile.
	now = now.Add(vaultRefreshCooldown + time.Second)
	v.load = func(context.Context) (*opconfig.Item, error) {
		reads++
		return &opconfig.Item{Token: "rotated", Login: "chrisle"}, nil
	}
	changed, err := v.Refresh(context.Background(), "expired")
	if err != nil || !changed {
		t.Fatalf("after cooldown: changed=%v err=%v, want true/nil", changed, err)
	}
	if reads != 2 {
		t.Errorf("vault reads = %d, want 2", reads)
	}
}

// A vault read that errors is reported, and throttled the same way.
func TestVaultAuthReportsAndThrottlesReadFailure(t *testing.T) {
	var reads int
	v := newTestVaultAuth(t, "expired", func(context.Context) (*opconfig.Item, error) {
		reads++
		return nil, errors.New("op: rate-limited")
	})
	now := time.Now()
	v.now = func() time.Time { return now }

	if _, err := v.Refresh(context.Background(), "expired"); err == nil {
		t.Fatal("want an error when the vault read fails")
	}
	if _, err := v.Refresh(context.Background(), "expired"); err != nil {
		t.Fatalf("second attempt inside the cooldown should be a quiet no-op, got %v", err)
	}
	if reads != 1 {
		t.Errorf("vault reads = %d, want 1", reads)
	}
}

// Concurrent callers that all raced into the same rejection must cost one
// vault read between them, not one each.
func TestVaultAuthRefreshIsSingleFlight(t *testing.T) {
	var mu sync.Mutex
	var reads int
	v := newTestVaultAuth(t, "expired", func(context.Context) (*opconfig.Item, error) {
		mu.Lock()
		reads++
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return &opconfig.Item{Token: "rotated", Login: "chrisle"}, nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if changed, err := v.Refresh(context.Background(), "expired"); err != nil || !changed {
				t.Errorf("changed=%v err=%v, want true/nil", changed, err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if reads != 1 {
		t.Errorf("vault reads = %d, want 1", reads)
	}
}
