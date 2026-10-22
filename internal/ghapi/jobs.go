package ghapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Repo is a repository the orchestrator may poll.
type Repo struct {
	Name     string    `json:"name"`
	FullName string    `json:"full_name"`
	Archived bool      `json:"archived"`
	Disabled bool      `json:"disabled"`
	Fork     bool      `json:"fork"`
	PushedAt time.Time `json:"pushed_at"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// Job is a self-hosted workflow job that is waiting for a runner or running
// on one.
type Job struct {
	ID       int64     `json:"id"`
	RunID    int64     `json:"run_id"`
	Repo     string    `json:"repo"`
	Branch   string    `json:"branch"`
	Name     string    `json:"name"`
	Labels   []string  `json:"labels"`
	Status   string    `json:"status"` // queued | in_progress | completed
	Started  time.Time `json:"started_at"`
	QueuedAt time.Time `json:"queued_at"`
	// Runner is the name of the runner executing the job. Empty while queued.
	Runner string `json:"runner,omitempty"`
}

// Jobs is one poll's worth of self-hosted work across the watched repos.
type Jobs struct {
	// Queued jobs are waiting for a runner and drive scaling.
	Queued []Job
	// Running jobs are executing; each names its runner, which is how
	// `arc status` says what a busy runner is working on.
	Running []Job
}

// SelfHosted reports whether the job asks for a self-hosted runner.
//
// A job that only names GitHub-hosted labels (ubuntu-latest and friends) must
// not drive our scaling, or every hosted job in the org would spin up local
// runners that GitHub never routes work to.
func (j Job) SelfHosted() bool {
	if len(j.Labels) == 0 {
		return false
	}
	for _, l := range j.Labels {
		if strings.EqualFold(l, "self-hosted") {
			return true
		}
	}
	// Some workflows use a bare custom label without "self-hosted". Treat any
	// label that isn't a known GitHub-hosted image as self-hosted.
	for _, l := range j.Labels {
		if !githubHostedLabel(l) {
			return true
		}
	}
	return false
}

// githubHostedLabel reports whether a label names a GitHub-hosted image.
func githubHostedLabel(l string) bool {
	l = strings.ToLower(l)
	for _, prefix := range []string{
		"ubuntu-", "windows-", "macos-", "ubuntu_", "windows_", "macos_",
	} {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	switch l {
	case "ubuntu", "windows", "macos":
		return true
	}
	return false
}

// ListRepos returns the account's repositories, filtered by the config's rules.
func (c *Client) ListRepos(ctx context.Context, filter RepoFilterOpts) ([]Repo, error) {
	if filter.IncludeURL != "" {
		remote, err := c.fetchRepoInclude(ctx, filter.IncludeURL)
		if err != nil {
			return nil, err
		}
		filter.Include = append(filter.Include, remote...)
	}
	var all []Repo
	// sort=pushed puts recently active repos first, so ActiveWithin can stop
	// paginating early instead of walking the whole account.
	p := fmt.Sprintf("/orgs/%s/repos?per_page=100&type=all&sort=pushed&direction=desc", c.org)
	if c.owner != "" {
		// /users/{owner}/repos only returns public repos, so personal mode
		// lists the authenticated user's own repos and then filters to the
		// configured owner as a guard against a token for the wrong account.
		p = "/user/repos?per_page=100&affiliation=owner&sort=pushed&direction=desc"
	}

	cutoff := time.Time{}
	if filter.ActiveWithin > 0 {
		cutoff = time.Now().Add(-filter.ActiveWithin)
	}

	stop := false
	err := c.paginate(ctx, p, "", func(page []byte) error {
		if stop {
			return nil
		}
		var repos []Repo
		if err := json.Unmarshal(page, &repos); err != nil {
			return fmt.Errorf("decode repos: %w", err)
		}
		for _, r := range repos {
			if !cutoff.IsZero() && r.PushedAt.Before(cutoff) {
				// Sorted by push time, so everything after this is older too.
				stop = true
				break
			}
			if c.owner != "" && !strings.EqualFold(r.Owner.Login, c.owner) {
				continue
			}
			// Forks rarely run their own Actions; watching them multiplies
			// API cost for nothing. An explicit include list overrides this.
			if c.owner != "" && r.Fork && len(filter.Include) == 0 {
				continue
			}
			if r.Disabled {
				continue
			}
			if r.Archived && !filter.Archived {
				continue
			}
			if !filter.match(r.Name) {
				continue
			}
			all = append(all, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

// RepoFilterOpts is the resolved repo filter.
type RepoFilterOpts struct {
	Include      []string
	IncludeURL   string
	Exclude      []string
	ActiveWithin time.Duration
	Archived     bool
}

func (c *Client) fetchRepoInclude(ctx context.Context, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build repo allowlist request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// Current macOS releases can deny Local Network access to a background
		// LaunchAgent binary while allowing the system curl executable. Arc's
		// shared allowlist is commonly served by another LAN host, so use curl
		// as the narrow platform fallback instead of making the fleet silently
		// expand back to every repository.
		if runtime.GOOS == "darwin" {
			body, curlErr := exec.CommandContext(ctx, "/usr/bin/curl", "-fsS", "--max-time", "30", url).Output()
			if curlErr == nil {
				return parseRepoInclude(body, url)
			}
		}
		return nil, fmt.Errorf("fetch repo allowlist %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch repo allowlist %s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read repo allowlist %s: %w", url, err)
	}
	return parseRepoInclude(body, url)
}

func parseRepoInclude(body []byte, source string) ([]string, error) {
	var repos []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		repos = append(repos, line)
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("repo allowlist %s is empty", source)
	}
	return repos, nil
}

func (f RepoFilterOpts) match(name string) bool {
	if len(f.Include) > 0 && !matchAny(f.Include, name) {
		return false
	}
	return !matchAny(f.Exclude, name)
}

func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if strings.EqualFold(p, name) {
			return true
		}
		if ok, err := path.Match(strings.ToLower(p), strings.ToLower(name)); err == nil && ok {
			return true
		}
	}
	return false
}

// PollJobs returns every self-hosted job across the given repos that is
// waiting for a runner or running on one.
//
// There is no org-wide "list queued jobs" endpoint, so this walks each repo's
// active workflow runs and inspects their jobs. Every request is conditional on
// an ETag: repos with no new activity answer 304, which GitHub does not charge
// against the rate limit, so polling a large org stays affordable.
func (c *Client) PollJobs(ctx context.Context, repos []Repo, concurrency int) (Jobs, error) {
	if concurrency < 1 {
		concurrency = 8
	}

	type result struct {
		jobs []Job
		err  error
	}

	sem := make(chan struct{}, concurrency)
	results := make([]result, len(repos))
	var wg sync.WaitGroup

	for i, repo := range repos {
		wg.Add(1)
		go func(i int, repo Repo) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = result{err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			jobs, err := c.repoActiveJobs(ctx, repo)
			results[i] = result{jobs: jobs, err: err}
		}(i, repo)
	}
	wg.Wait()

	var (
		out  Jobs
		errs []string
	)
	for i, r := range results {
		if r.err != nil {
			// One broken repo (deleted, permissions changed) must not blind the
			// orchestrator to demand in every other repo.
			errs = append(errs, fmt.Sprintf("%s: %v", repos[i].Name, r.err))
			continue
		}
		for _, j := range r.jobs {
			if j.Status == "queued" {
				out.Queued = append(out.Queued, j)
			} else {
				out.Running = append(out.Running, j)
			}
		}
	}

	sort.Slice(out.Queued, func(i, j int) bool { return out.Queued[i].ID < out.Queued[j].ID })
	sort.Slice(out.Running, func(i, j int) bool { return out.Running[i].ID < out.Running[j].ID })

	if len(errs) > 0 && len(errs) == len(repos) {
		return out, fmt.Errorf("all repo polls failed: %s", strings.Join(errs, "; "))
	}
	if len(errs) > 0 {
		return out, &PartialError{Failures: errs}
	}
	return out, nil
}

// PartialError reports that some repos failed while others succeeded.
type PartialError struct{ Failures []string }

func (e *PartialError) Error() string {
	return fmt.Sprintf("%d repo poll(s) failed: %s", len(e.Failures), strings.Join(e.Failures, "; "))
}

// repoActiveJobs returns the repo's queued and in-progress self-hosted jobs.
func (c *Client) repoActiveJobs(ctx context.Context, repo Repo) ([]Job, error) {
	// Run id -> head branch. The branch lives on the run, not the job.
	runs := map[int64]string{}

	// A run that is "in_progress" can still hold queued jobs: matrix legs and
	// jobs gated on `needs:` enter the queue after the run has already started.
	// Polling only status=queued would miss all of them.
	for _, status := range []string{"queued", "in_progress"} {
		p := fmt.Sprintf("/repos/%s/%s/actions/runs?status=%s&per_page=100&exclude_pull_requests=true",
			c.entity(), repo.Name, status)
		key := fmt.Sprintf("runs:%s:%s", repo.Name, status)

		var out struct {
			WorkflowRuns []struct {
				ID         int64  `json:"id"`
				Status     string `json:"status"`
				HeadBranch string `json:"head_branch"`
			} `json:"workflow_runs"`
		}
		if _, err := c.getJSON(ctx, p, key, &out); err != nil {
			return nil, err
		}
		for _, r := range out.WorkflowRuns {
			runs[r.ID] = r.HeadBranch
		}
	}
	if len(runs) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var jobs []Job
	for _, runID := range ids {
		p := fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=100", c.entity(), repo.Name, runID)
		key := fmt.Sprintf("jobs:%s:%d", repo.Name, runID)

		var out struct {
			Jobs []struct {
				ID          int64     `json:"id"`
				RunID       int64     `json:"run_id"`
				Name        string    `json:"name"`
				Status      string    `json:"status"`
				Labels      []string  `json:"labels"`
				StartedAt   time.Time `json:"started_at"`
				CreatedAt   time.Time `json:"created_at"`
				RunnerName  string    `json:"runner_name"`
				RunnerGroup string    `json:"runner_group_name"`
			} `json:"jobs"`
		}
		if _, err := c.getJSON(ctx, p, key, &out); err != nil {
			if IsNotFound(err) {
				continue // run vanished between the two calls
			}
			return nil, err
		}

		for _, j := range out.Jobs {
			if j.Status != "queued" && j.Status != "in_progress" {
				continue
			}
			job := Job{
				ID:       j.ID,
				RunID:    j.RunID,
				Repo:     repo.Name,
				Branch:   runs[runID],
				Name:     j.Name,
				Labels:   j.Labels,
				Status:   j.Status,
				Started:  j.StartedAt,
				QueuedAt: j.CreatedAt,
				Runner:   j.RunnerName,
			}
			if !job.SelfHosted() {
				continue
			}
			jobs = append(jobs, job)
		}
	}
	return jobs, nil
}
