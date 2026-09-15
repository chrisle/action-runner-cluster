package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/chrisle/action-runner-cluster/internal/opconfig"
)

// vaultRefreshCooldown throttles vault reads when the vault keeps handing back
// the token GitHub just refused — the PAT has expired and nobody has rotated it
// yet. Reconciles are frequent and `op` is slow and rate-limited, so a rejection
// storm must not become a vault-read storm.
const vaultRefreshCooldown = 5 * time.Minute

// vaultAuth is the credential arc runs on in 1Password mode: the PAT from
// op://arc/github, re-read when GitHub stops accepting it.
//
// Startup already recovers from a rotated token (see autoConfig). This covers
// the other half — a token that dies while arc is running — so a long-lived
// service is not pinned to a credential that expired under it.
type vaultAuth struct {
	mu   sync.Mutex
	item *opconfig.Item
	// lastFailed is when a refresh last came back with the same token GitHub
	// had already rejected, and gates the cooldown.
	lastFailed time.Time

	// load and now are swapped out in tests.
	load func(context.Context) (*opconfig.Item, error)
	now  func() time.Time
}

func newVaultAuth(item *opconfig.Item) *vaultAuth {
	return &vaultAuth{item: item, load: opconfig.Load, now: time.Now}
}

func (v *vaultAuth) Describe() string { return "personal access token (op://arc/github)" }

func (v *vaultAuth) Token(context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.item.Token, nil
}

// Refresh re-reads the vault, unless another caller already replaced stale or
// the last read was too recent to be worth repeating. The lock is held across
// the read so a burst of rejected requests costs one `op` invocation, not one
// per caller.
func (v *vaultAuth) Refresh(ctx context.Context, stale string) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.item.Token != stale {
		// Another caller refreshed while this one was in flight.
		return true, nil
	}
	if !v.lastFailed.IsZero() && v.now().Sub(v.lastFailed) < vaultRefreshCooldown {
		return false, nil
	}

	fresh, err := v.load(ctx)
	if err != nil {
		v.lastFailed = v.now()
		return false, fmt.Errorf("re-read 1Password item \"github\" in vault \"arc\": %w", err)
	}
	if fresh.Token == stale {
		// The vault holds the same dead token: it needs rotating, and no
		// amount of retrying here will help.
		v.lastFailed = v.now()
		return false, nil
	}

	// The account is keyed to the login, which a rotation does not change;
	// carrying it over keeps the cache complete without another GitHub probe.
	fresh.AccountType = v.item.AccountType
	v.item = fresh
	v.lastFailed = time.Time{}

	// Persist so a later restart starts from the working token rather than
	// rediscovering this same failure. A cache that cannot be written is not
	// worth failing the refresh over — the token in memory is good, and the
	// next start re-reads the vault — so this warns the way autoConfig does.
	if err := opconfig.WriteCache(fresh); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not cache refreshed credentials (the vault "+
			"will be read again next start): %v\n", err)
	}
	return true, nil
}
