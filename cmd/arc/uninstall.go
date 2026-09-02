package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chrisle/action-runner-cluster/internal/config"
	"github.com/chrisle/action-runner-cluster/internal/ghapi"
	"github.com/chrisle/action-runner-cluster/internal/hostid"
	"github.com/chrisle/action-runner-cluster/internal/opconfig"
	"github.com/chrisle/action-runner-cluster/internal/orchestrator"
	"github.com/chrisle/action-runner-cluster/internal/state"
	"github.com/chrisle/action-runner-cluster/internal/webhook"
)

// arc uninstall is the inverse of arc install: the service goes, this host's
// runners go — on GitHub as well as on disk — its webhooks go, and so do the
// files arc created.
//
// Two rules shape it. Nothing destructive happens before a runner executing a
// job has been looked for, because an uninstall must never kill a live build.
// And once past the confirmation it removes everything it can rather than
// stopping at the first failure: the host whose credentials expired or whose
// config was deleted is exactly the host someone is trying to remove.
func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	cfgPath := fs.String("config", "", "path to arc.yaml")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	force := fs.Bool("force", false, "uninstall even while runners are executing jobs")
	// No backticks in the description: the flag package reads them as the
	// argument's name.
	keepConfig := fs.Bool("keep-config", false,
		"keep the config, cached credentials and runner template, so arc install brings the host back")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: arc uninstall [flags]\n\n"+
			"Stops and removes the background service, destroys this host's runners\n"+
			"and their GitHub registrations, removes its webhooks, and deletes the\n"+
			"files arc created. Runners mid-job are left alone unless -force.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	adoptInvokingUserHome()

	// Fail before the prompt, not after answering it.
	if err := serviceNeedsRoot(); err != nil {
		return err
	}

	// Configuration is best-effort. Without it the GitHub side cannot be
	// cleaned up, but the service and the files still can.
	cfg, cfgErr := loadConfig(*cfgPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Warnings are worth seeing during an uninstall; the informational chatter
	// of a reconcile is not.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	var orch *orchestrator.Orchestrator
	var gh *ghapi.Client
	if cfgErr == nil {
		var err error
		gh, orch, err = teardownOrchestrator(cfg, log)
		if err != nil {
			// A provider that will not construct (no Docker daemon, say) does
			// not block removing everything else.
			fmt.Fprintf(os.Stderr, "warning: cannot reach this host's runners: %v\n", err)
		} else {
			defer orch.Close()
		}
	}

	if orch != nil && !*force {
		if err := checkNoBusyRunners(ctx, orch); err != nil {
			return err
		}
	}

	targets := uninstallTargets(homeDir(), cfg, *keepConfig)
	printUninstallPlan(cfgErr, orch != nil, targets, *keepConfig)
	if !*yes {
		if err := confirm(); err != nil {
			return err
		}
	}
	fmt.Println()

	// The service first: everything after it assumes nothing is creating
	// runners any more.
	if err := removeService(); err != nil {
		return fmt.Errorf("remove the service: %w\nNothing else was touched; "+
			"fix this and run `arc uninstall` again", err)
	}
	fmt.Printf("removed %s\n", serviceDescription())

	if cfgErr == nil && !*force {
		if err := checkNotRunning(ctx, cfg); err != nil {
			return err
		}
	}

	if orch != nil {
		res, err := orch.Teardown(ctx)
		fmt.Printf("destroyed %d runner instance(s), deregistered %d, pruned %d leftover(s)\n",
			res.Destroyed, res.Deregistered, res.Pruned)
		if len(res.Busy) > 0 {
			fmt.Printf("left %d runner(s) alone, still executing a job: %s\n",
				len(res.Busy), strings.Join(res.Busy, ", "))
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: some runners could not be removed: %v\n", err)
		}

		if n, err := webhook.NewManager(cfg, gh, func() {}, log).Unregister(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remove this host's webhooks "+
				"(host id %s): %v\n", hostid.ID(), err)
		} else {
			fmt.Printf("removed %d webhook(s) for host %s\n", n, hostid.ID())
		}
	}

	removeTargets(targets)
	pruneEmptyDirs()
	reportLeftovers()
	return nil
}

// teardownOrchestrator builds the GitHub client and orchestrator uninstall
// needs. It is `arc run`'s wiring minus the reconcile loop.
func teardownOrchestrator(cfg *config.Config, log *slog.Logger) (*ghapi.Client, *orchestrator.Orchestrator, error) {
	gh, err := ghapi.New(cfg, log)
	if err != nil {
		return nil, nil, err
	}
	overrides, err := state.LoadOverrides(cfg.Server.StateDir)
	if err != nil {
		return nil, nil, err
	}
	orch, err := orchestrator.New(cfg, gh, overrides, log)
	if err != nil {
		return nil, nil, err
	}
	return gh, orch, nil
}

// checkNoBusyRunners refuses to start while this host is executing a job. It
// runs before anything is removed, so the answer is either "safe to proceed"
// or "nothing has changed, come back later".
func checkNoBusyRunners(ctx context.Context, orch *orchestrator.Orchestrator) error {
	busy, err := orch.BusyRunners(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not check for running jobs: %v\n", err)
		return nil
	}
	if len(busy) == 0 {
		return nil
	}
	return fmt.Errorf("%d runner(s) are executing a job: %s\n"+
		"Wait for them to finish (`arc status -wide`), or pass -force to uninstall anyway "+
		"and fail those jobs", len(busy), strings.Join(busy, ", "))
}

// checkNotRunning catches an orchestrator started outside the service — a
// foreground `arc run` in another terminal. Left alone it would re-register
// runners and rewrite the files this command is about to delete.
//
// The just-stopped service takes a moment to let go of the port, so a single
// probe would accuse the user of running arc by hand when they are not.
func checkNotRunning(ctx context.Context, cfg *config.Config) error {
	client := newControlClient(cfg)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var health map[string]any
		if err := client.do(ctx, "GET", "/healthz", nil, &health); err != nil {
			return nil // unreachable is what we want
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("an orchestrator is still running outside the service "+
		"(the control API at %s answers). Stop that `arc run` and run "+
		"`arc uninstall` again — the service is already removed, so it is safe "+
		"to repeat — or pass -force", cfg.Server.Addr)
}

// target is a path uninstall deletes, with what it holds.
type target struct {
	path string
	what string
}

// uninstallTargets lists what to delete, most specific first. It only names
// paths arc itself creates: a template directory or state directory the config
// points somewhere else is left alone, because arc did not put it there.
func uninstallTargets(home string, cfg *config.Config, keepConfig bool) []target {
	var targets []target
	seen := map[string]bool{}
	add := func(path, what string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		targets = append(targets, target{path: path, what: what})
	}

	arcDir := filepath.Join(home, ".arc")

	// Runtime state goes in every case: it is meaningless without the service.
	if cfg != nil {
		add(cfg.Server.StateDir, "orchestrator state")
		for _, pool := range cfg.Pools {
			if pool.Process != nil {
				add(pool.Process.InstancesDir, "pool "+pool.Name+" runner instances")
			}
		}
	} else {
		add(config.DefaultStateDir(), "orchestrator state")
	}
	add(filepath.Join(arcDir, "instances"), "runner instances")
	add(filepath.Join(arcDir, "arc.log"), "service log")
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			add(filepath.Join(local, "arc", "arc.log"), "service log")
		}
	}

	// The rest is what a reinstall would otherwise have to be told again.
	if keepConfig {
		return targets
	}
	add(config.DefaultUserPath(), "config written by arc config")
	add(opconfig.CachePath(), "cached 1Password credentials")
	add(filepath.Join(arcDir, "runner-template"), "runner template")
	return targets
}

func printUninstallPlan(cfgErr error, haveOrch bool, targets []target, keepConfig bool) {
	fmt.Println("arc uninstall will:")
	fmt.Printf("  · stop and remove the %s\n", serviceDescription())
	switch {
	case haveOrch:
		fmt.Println("  · destroy this host's runners and deregister them from GitHub")
		fmt.Printf("  · remove this host's webhooks (host id %s)\n", hostid.ID())
	case cfgErr != nil:
		fmt.Printf("  · skip the GitHub cleanup — the config did not load: %v\n", cfgErr)
	default:
		fmt.Println("  · skip the GitHub cleanup — this host's runners are unreachable")
	}
	// Only what is actually there: a plan listing paths that do not exist
	// reads as more destructive than the command is.
	for _, t := range targets {
		if _, err := os.Stat(t.path); err == nil {
			fmt.Printf("  · delete %s (%s)\n", t.path, t.what)
		}
	}
	if keepConfig {
		fmt.Println("\nKeeping the config, cached credentials and runner template (-keep-config).")
	}
}

// confirm asks before anything is removed. A non-interactive run has no answer
// to give, so it is told to pass -yes rather than guessed at.
func confirm() error {
	fmt.Print("\nProceed? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return errors.New("could not read a confirmation; pass -yes to uninstall without prompting")
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	default:
		return errors.New("aborted; nothing was removed")
	}
}

func removeTargets(targets []target) {
	for _, t := range targets {
		if _, err := os.Stat(t.path); os.IsNotExist(err) {
			continue
		}
		if err := os.RemoveAll(t.path); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not delete %s: %v\n", t.path, err)
			continue
		}
		fmt.Printf("deleted %s (%s)\n", t.path, t.what)
	}
}

// pruneEmptyDirs removes arc's directories once they hold nothing. os.Remove
// refuses a non-empty directory, which is exactly the guard wanted here: a
// leftover env file or token keeps its directory.
func pruneEmptyDirs() {
	_ = os.Remove(filepath.Join(homeDir(), ".arc"))
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			_ = os.Remove(filepath.Join(local, "arc"))
		}
	}
}

// reportLeftovers names what was deliberately not deleted: credentials the
// operator put on the host by hand, and the binary itself, which cannot
// remove itself reliably on Windows and is managed by `arc update` anyway.
func reportLeftovers() {
	secrets := []string{
		filepath.Join(homeDir(), ".arc", "env"),
		"/etc/arc/env",
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		secrets = append(secrets, filepath.Join(local, "arc", "op-token.txt"))
	}

	var left []string
	for _, path := range secrets {
		if _, err := os.Stat(path); err == nil {
			left = append(left, path)
		}
	}

	fmt.Println("\narc is uninstalled from this host.")
	if len(left) > 0 {
		fmt.Println("\nLeft in place, because you put them there:")
		for _, path := range left {
			fmt.Printf("  %s\n", path)
		}
	}
	if exe, err := os.Executable(); err == nil {
		fmt.Printf("\nThe binary is still at %s.\n", exe)
	}
}

// homeDir is the home directory of the account that owns the install. Under
// `sudo arc uninstall` — Linux needs root to remove the systemd unit — that is
// the invoking user's home, not root's: the state, credentials and config all
// live there.
func homeDir() string {
	if name := os.Getenv("SUDO_USER"); name != "" && name != "root" {
		if acct, err := user.Lookup(name); err == nil && acct.HomeDir != "" {
			return acct.HomeDir
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// adoptInvokingUserHome points HOME at that account for the rest of the
// process, so config discovery, the credentials cache and the default state
// directory all resolve where the service actually kept them instead of under
// root's home. It does nothing when the command was not run through sudo.
func adoptInvokingUserHome() {
	if os.Getenv("SUDO_USER") == "" {
		return
	}
	if home := homeDir(); home != "" {
		_ = os.Setenv("HOME", home)
	}
}
