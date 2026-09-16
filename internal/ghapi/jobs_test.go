package ghapi

import (
	"context"
	"net/http"
	"testing"
)

func TestJobSelfHosted(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{"explicit self-hosted", []string{"self-hosted", "linux"}, true},
		{"custom label alone", []string{"my-big-mac"}, true},
		{"self-hosted with custom label", []string{"self-hosted", "gpu"}, true},

		// These must not drive scaling: GitHub runs them on its own hosted
		// machines and would never route them to our runners, so treating them
		// as demand would spin up runners that sit idle until culled.
		{"github hosted ubuntu", []string{"ubuntu-latest"}, false},
		{"github hosted macos", []string{"macos-14"}, false},
		{"github hosted windows", []string{"windows-2022"}, false},
		{"github hosted bare os", []string{"ubuntu"}, false},

		{"no labels", nil, false},

		// A self-hosted runner labelled to shadow a hosted image is unusual but
		// legal; the explicit self-hosted label settles it.
		{"self-hosted shadowing a hosted label", []string{"self-hosted", "ubuntu-latest"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Job{Labels: tt.labels}.SelfHosted()
			if got != tt.want {
				t.Errorf("SelfHosted(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

func TestRepoFilterMatch(t *testing.T) {
	tests := []struct {
		name   string
		filter RepoFilterOpts
		repo   string
		want   bool
	}{
		{"no filter allows all", RepoFilterOpts{}, "anything", true},
		{"include exact", RepoFilterOpts{Include: []string{"api"}}, "api", true},
		{"include excludes others", RepoFilterOpts{Include: []string{"api"}}, "web", false},
		{"include glob", RepoFilterOpts{Include: []string{"service-*"}}, "service-auth", true},
		{"include glob miss", RepoFilterOpts{Include: []string{"service-*"}}, "web", false},
		{"exclude wins", RepoFilterOpts{Exclude: []string{"archive-*"}}, "archive-old", false},
		{"exclude leaves others", RepoFilterOpts{Exclude: []string{"archive-*"}}, "api", true},
		{"case insensitive", RepoFilterOpts{Include: []string{"API"}}, "api", true},
		{
			"exclude overrides include",
			RepoFilterOpts{Include: []string{"*"}, Exclude: []string{"secret"}},
			"secret",
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.match(tt.repo); got != tt.want {
				t.Errorf("match(%q) = %v, want %v", tt.repo, got, tt.want)
			}
		})
	}
}

func TestNextLink(t *testing.T) {
	header := `<https://api.github.com/orgs/a/repos?page=2>; rel="next", ` +
		`<https://api.github.com/orgs/a/repos?page=9>; rel="last"`
	got := nextLink(header)
	want := "https://api.github.com/orgs/a/repos?page=2"
	if got != want {
		t.Errorf("nextLink = %q, want %q", got, want)
	}

	// The last page has no next link; returning something here would loop.
	if got := nextLink(`<https://api.github.com/orgs/a/repos?page=1>; rel="prev"`); got != "" {
		t.Errorf("nextLink on last page = %q, want empty", got)
	}
	if got := nextLink(""); got != "" {
		t.Errorf("nextLink of empty header = %q, want empty", got)
	}
}

func TestPollJobsSplitsQueuedAndRunning(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/chrisle/app/actions/runs":
			switch r.URL.Query().Get("status") {
			case "queued":
				_, _ = w.Write([]byte(`{"workflow_runs": [{"id": 1, "status": "queued", "head_branch": "main"}]}`))
			case "in_progress":
				_, _ = w.Write([]byte(`{"workflow_runs": [{"id": 2, "status": "in_progress", "head_branch": "feature/x"}]}`))
			}
		case "/repos/chrisle/app/actions/runs/1/jobs":
			_, _ = w.Write([]byte(`{"jobs": [
				{"id": 10, "run_id": 1, "name": "build", "status": "queued", "labels": ["self-hosted", "macos"]}
			]}`))
		case "/repos/chrisle/app/actions/runs/2/jobs":
			_, _ = w.Write([]byte(`{"jobs": [
				{"id": 20, "run_id": 2, "name": "test", "status": "in_progress", "labels": ["self-hosted", "macos"], "runner_name": "arc-macos-aabbccdd"},
				{"id": 21, "run_id": 2, "name": "lint", "status": "completed", "labels": ["self-hosted", "macos"], "runner_name": "arc-macos-11223344"},
				{"id": 22, "run_id": 2, "name": "hosted", "status": "in_progress", "labels": ["ubuntu-latest"], "runner_name": "GitHub Actions 3"}
			]}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	c, _ := ownerClient(t, handler)

	got, err := c.PollJobs(context.Background(), []Repo{{Name: "app"}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Queued) != 1 || got.Queued[0].ID != 10 || got.Queued[0].Branch != "main" {
		t.Errorf("queued = %+v, want job 10 on main", got.Queued)
	}
	if len(got.Running) != 1 {
		t.Fatalf("running = %+v, want exactly job 20", got.Running)
	}
	r := got.Running[0]
	if r.ID != 20 || r.Repo != "app" || r.Branch != "feature/x" || r.Name != "test" || r.Runner != "arc-macos-aabbccdd" {
		t.Errorf("running = %+v", r)
	}
}
