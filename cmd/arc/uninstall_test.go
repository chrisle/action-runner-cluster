package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisle/action-runner-cluster/internal/config"
	"github.com/chrisle/action-runner-cluster/internal/opconfig"
)

func targetPaths(targets []target) []string {
	paths := make([]string, 0, len(targets))
	for _, t := range targets {
		paths = append(paths, t.path)
	}
	return paths
}

func has(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

func testConfig(home string) *config.Config {
	cfg := &config.Config{Pools: []*config.Pool{{
		Name:    "macos",
		Process: &config.ProcessSpec{InstancesDir: filepath.Join(home, ".arc", "instances", "macos")},
	}}}
	cfg.Server.StateDir = filepath.Join(home, ".config", "action-runner-cluster")
	return cfg
}

func TestUninstallTargetsRemovesEverythingByDefault(t *testing.T) {
	home := t.TempDir()
	paths := targetPaths(uninstallTargets(home, testConfig(home), false))

	for _, want := range []string{
		filepath.Join(home, ".config", "action-runner-cluster"),
		filepath.Join(home, ".arc", "instances", "macos"),
		filepath.Join(home, ".arc", "instances"),
		filepath.Join(home, ".arc", "arc.log"),
		filepath.Join(home, ".arc", "runner-template"),
		config.DefaultUserPath(),
		opconfig.CachePath(),
	} {
		if !has(paths, want) {
			t.Errorf("targets missing %s\ngot: %s", want, strings.Join(paths, "\n     "))
		}
	}
}

func TestUninstallTargetsKeepConfigLeavesReinstallableState(t *testing.T) {
	home := t.TempDir()
	paths := targetPaths(uninstallTargets(home, testConfig(home), true))

	// Runtime state is meaningless without the service, so it always goes.
	if !has(paths, filepath.Join(home, ".arc", "instances")) {
		t.Error("keep-config kept the instances directory")
	}
	for _, unwanted := range []string{
		filepath.Join(home, ".arc", "runner-template"),
		config.DefaultUserPath(),
		opconfig.CachePath(),
	} {
		if has(paths, unwanted) {
			t.Errorf("keep-config still deletes %s", unwanted)
		}
	}
}

// Secrets the operator placed on the host by hand are never arc's to delete.
func TestUninstallTargetsNeverTouchEnvFiles(t *testing.T) {
	home := t.TempDir()
	for _, keepConfig := range []bool{false, true} {
		for _, path := range targetPaths(uninstallTargets(home, testConfig(home), keepConfig)) {
			if strings.HasSuffix(path, filepath.Join(".arc", "env")) || path == "/etc/arc/env" {
				t.Errorf("keepConfig=%v deletes the operator's env file %s", keepConfig, path)
			}
		}
	}
}

// A host whose config no longer loads still has to be removable.
func TestUninstallTargetsWithoutConfig(t *testing.T) {
	home := t.TempDir()
	paths := targetPaths(uninstallTargets(home, nil, false))

	if !has(paths, config.DefaultStateDir()) {
		t.Errorf("targets missing the default state dir: %v", paths)
	}
	if !has(paths, filepath.Join(home, ".arc", "instances")) {
		t.Errorf("targets missing the default instances dir: %v", paths)
	}
}

func TestUninstallTargetsAreUnique(t *testing.T) {
	home := t.TempDir()
	cfg := testConfig(home)
	// A config pointing at the default location must not queue it twice: the
	// second delete would report a path that is already gone.
	cfg.Pools[0].Process.InstancesDir = filepath.Join(home, ".arc", "instances")

	seen := map[string]bool{}
	for _, p := range targetPaths(uninstallTargets(home, cfg, false)) {
		if seen[p] {
			t.Errorf("duplicate target %s", p)
		}
		seen[p] = true
	}
}

func TestRemoveTargetsDeletesWhatExists(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.MkdirAll(filepath.Join(state, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "arc.log")
	if err := os.WriteFile(log, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	removeTargets([]target{
		{path: state, what: "orchestrator state"},
		{path: log, what: "service log"},
		// A path that was never created must be a no-op, not a failure.
		{path: filepath.Join(dir, "never-existed"), what: "nothing"},
	})

	for _, path := range []string{state, log} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists", path)
		}
	}
}
