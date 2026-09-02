package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/chrisle/action-runner-cluster/internal/config"
	"github.com/chrisle/action-runner-cluster/internal/ghapi"
	"github.com/chrisle/action-runner-cluster/internal/hostid"
	"github.com/chrisle/action-runner-cluster/internal/provider"
)

// fakeProvider is a provider whose instances live only in the test.
type fakeProvider struct {
	mu        sync.Mutex
	instances []provider.Instance
	pruned    int
	destroyed []string
}

func (p *fakeProvider) Kind() string                    { return "fake" }
func (p *fakeProvider) Preflight(context.Context) error { return nil }
func (p *fakeProvider) Close() error                    { return nil }
func (p *fakeProvider) Logs(context.Context, string, int) (string, error) {
	return "", nil
}

func (p *fakeProvider) Create(context.Context, provider.Spec) (*provider.Instance, error) {
	return nil, fmt.Errorf("not used")
}

func (p *fakeProvider) List(context.Context) ([]provider.Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.instances), nil
}

func (p *fakeProvider) Destroy(_ context.Context, id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.destroyed = append(p.destroyed, id)
	p.instances = slices.DeleteFunc(p.instances, func(i provider.Instance) bool { return i.ID == id })
	return nil
}

func (p *fakeProvider) Prune(context.Context) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pruned, nil
}

// teardownFixture wires an orchestrator against a fake GitHub serving the
// given runners for repo "app".
func teardownFixture(t *testing.T, runners []map[string]any) (*Orchestrator, *fakeProvider, *[]string) {
	t.Helper()

	var mu sync.Mutex
	var deleted []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user/repos":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "app", "owner": map[string]any{"login": "chrisle"}},
			})
		case r.URL.Path == "/repos/chrisle/app/actions/runners":
			_ = json.NewEncoder(w).Encode(map[string]any{"runners": runners})
		case r.Method == http.MethodDelete:
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg := &config.Config{Pools: []*config.Pool{{Name: "macos", Max: 4}}}
	cfg.GitHub.Owner = "chrisle"
	cfg.GitHub.Token = "t"
	cfg.GitHub.APIURL = srv.URL
	cfg.GitHub.WebURL = srv.URL

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gh, err := ghapi.New(cfg, log)
	if err != nil {
		t.Fatal(err)
	}

	p := &fakeProvider{}
	o := &Orchestrator{
		cfg:       cfg,
		gh:        gh,
		log:       log,
		providers: map[string]provider.Provider{"macos": p},
		idleSince: map[string]time.Time{},
	}
	return o, p, &deleted
}

func TestTeardownRemovesInstancesAndOrphans(t *testing.T) {
	mine := "arc-macos-" + hostid.ID() + "-a1b2c3d4"
	orphan := "arc-macos-" + hostid.ID() + "-99887766"
	foreign := "arc-macos-ffffff-11223344"

	o, p, deleted := teardownFixture(t, []map[string]any{
		{"id": 1, "name": mine, "status": "online", "busy": false},
		{"id": 2, "name": orphan, "status": "online", "busy": false},
		{"id": 3, "name": foreign, "status": "online", "busy": false},
	})
	p.instances = []provider.Instance{
		{ID: "i-1", Pool: "macos", RunnerName: mine, RunnerID: 1},
		// A local instance GitHub never registered still has to be destroyed.
		{ID: "i-2", Pool: "macos", RunnerName: "arc-macos-" + hostid.ID() + "-deadbeef"},
	}
	p.pruned = 2

	res, err := o.Teardown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Destroyed != 2 {
		t.Errorf("destroyed = %d, want 2", res.Destroyed)
	}
	// The registered instance plus the orphaned registration.
	if res.Deregistered != 2 {
		t.Errorf("deregistered = %d, want 2", res.Deregistered)
	}
	if res.Pruned != 2 {
		t.Errorf("pruned = %d, want 2", res.Pruned)
	}
	if len(p.instances) != 0 {
		t.Errorf("instances left behind: %+v", p.instances)
	}
	// Another host's runner is never ours to deregister.
	for _, path := range *deleted {
		if path == "/repos/chrisle/app/actions/runners/3" {
			t.Error("deregistered another host's runner")
		}
	}
}

func TestTeardownLeavesBusyRunnersAlone(t *testing.T) {
	busy := "arc-macos-" + hostid.ID() + "-a1b2c3d4"

	o, p, deleted := teardownFixture(t, []map[string]any{
		{"id": 1, "name": busy, "status": "online", "busy": true},
	})
	p.instances = []provider.Instance{{ID: "i-1", Pool: "macos", RunnerName: busy, RunnerID: 1}}

	res, err := o.Teardown(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Destroyed != 0 {
		t.Errorf("destroyed = %d, want 0: a runner mid-job must survive", res.Destroyed)
	}
	if len(res.Busy) != 1 || res.Busy[0] != busy {
		t.Errorf("busy = %v, want [%s]", res.Busy, busy)
	}
	if len(*deleted) != 0 {
		t.Errorf("deregistered a busy runner: %v", *deleted)
	}
	if len(p.instances) != 1 {
		t.Errorf("destroyed the busy instance: %+v", p.instances)
	}
}

func TestBusyRunnersOnlyReportsThisHost(t *testing.T) {
	mine := "arc-macos-" + hostid.ID() + "-a1b2c3d4"

	o, _, _ := teardownFixture(t, []map[string]any{
		{"id": 1, "name": mine, "status": "online", "busy": true},
		{"id": 2, "name": "arc-macos-ffffff-11223344", "status": "online", "busy": true},
		{"id": 3, "name": "hand-rolled-runner", "status": "online", "busy": true},
	})

	busy, err := o.BusyRunners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(busy) != 1 || busy[0] != mine {
		t.Errorf("busy = %v, want [%s]", busy, mine)
	}
}
