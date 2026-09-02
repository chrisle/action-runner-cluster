package orchestrator

import (
	"context"
	"errors"
	"fmt"

	"github.com/chrisle/action-runner-cluster/internal/ghapi"
)

// TeardownResult reports what a teardown removed.
type TeardownResult struct {
	// Destroyed counts local instances removed with their scratch state.
	Destroyed int
	// Deregistered counts runner registrations removed from GitHub.
	Deregistered int
	// Pruned counts leftover containers and directories the providers swept.
	Pruned int
	// Busy names the runners left alone because they were executing a job.
	Busy []string
}

// Teardown removes every runner this host owns: each provider's instances and
// their GitHub registrations, plus registrations this host minted that no
// instance is behind any more, then prunes whatever debris the providers find.
//
// It is what `arc uninstall` calls, so it does as much as it can rather than
// stopping at the first failure — a host whose credentials no longer work must
// still be able to clean up its own disk. The returned error collects
// everything that could not be removed; the result is what was.
//
// Runners executing a job are never touched, on the same principle as
// scale-down: their names come back in Busy so the caller can say which live
// builds it declined to kill.
func (o *Orchestrator) Teardown(ctx context.Context) (TeardownResult, error) {
	var res TeardownResult
	var errs []error

	// What GitHub still lists. Failing here is not fatal: without it the
	// registrations survive, but the local instances still go.
	byName := map[string]ghapi.Runner{}
	repos, err := o.pollableRepos(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("list repos: %w", err))
	} else {
		runners, err := o.gh.ListRunners(ctx, repos)
		if err != nil {
			errs = append(errs, fmt.Errorf("list runners: %w", err))
		}
		for _, r := range runners {
			byName[r.Name] = r
		}
	}

	for _, pool := range o.cfg.Pools {
		p := o.providers[pool.Name]

		instances, err := p.List(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("pool %s: list instances: %w", pool.Name, err))
		}

		have := make(map[string]bool, len(instances))
		for _, inst := range instances {
			have[inst.RunnerName] = true
		}

		for _, inst := range instances {
			if r, ok := byName[inst.RunnerName]; ok && r.Busy {
				res.Busy = append(res.Busy, inst.RunnerName)
				continue
			}
			_, registered := byName[inst.RunnerName]
			switch err := o.destroyInstance(ctx, pool, inst, byName, "uninstall"); {
			case err == nil:
				res.Destroyed++
				if registered {
					res.Deregistered++
				}
			case errors.Is(err, ghapi.ErrRunnerBusy):
				// It picked up a job between the listing and now.
				res.Busy = append(res.Busy, inst.RunnerName)
			default:
				errs = append(errs, fmt.Errorf("pool %s: %w", pool.Name, err))
			}
		}

		// Registrations this host minted with nothing behind them: a crash
		// between registering and creating, or an instance removed by hand.
		for name, r := range byName {
			if have[name] || !belongsToPool(name, pool.Name) {
				continue
			}
			if r.Busy {
				res.Busy = append(res.Busy, name)
				continue
			}
			if err := o.gh.RemoveRunner(ctx, r.Repo, r.ID); err != nil {
				if errors.Is(err, ghapi.ErrRunnerBusy) {
					res.Busy = append(res.Busy, name)
					continue
				}
				errs = append(errs, fmt.Errorf("deregister %s: %w", name, err))
				continue
			}
			res.Deregistered++
		}

		n, err := p.Prune(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("pool %s: prune: %w", pool.Name, err))
		}
		res.Pruned += n
	}

	return res, errors.Join(errs...)
}

// BusyRunners returns the names of this host's runners that GitHub reports as
// executing a job. `arc uninstall` asks before it touches anything, so it can
// stop with a live build named rather than half-removed.
func (o *Orchestrator) BusyRunners(ctx context.Context) ([]string, error) {
	repos, err := o.pollableRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	runners, err := o.gh.ListRunners(ctx, repos)
	if err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}

	// A busy runner counts whether or not a local instance is still behind it:
	// either way the job is running on this machine.
	var busy []string
	for _, r := range runners {
		if !r.Busy {
			continue
		}
		for _, pool := range o.cfg.Pools {
			if belongsToPool(r.Name, pool.Name) {
				busy = append(busy, r.Name)
				break
			}
		}
	}
	return busy, nil
}
